package rules

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/managed-dns/controller/internal/mosdnsclient"
)

const (
	minRefreshInterval  = 15 * time.Minute
	maxRefreshInterval  = 30 * 24 * time.Hour
	maxSubscriptionSize = 20 << 20
)

type Subscription struct {
	ID                     int64                `json:"id"`
	Category               string               `json:"category"`
	Action                 string               `json:"action"`
	Kind                   string               `json:"kind"`
	Name                   string               `json:"name"`
	SourceURL              string               `json:"source_url"`
	RefreshIntervalSeconds int                  `json:"refresh_interval_seconds"`
	Enabled                bool                 `json:"enabled"`
	RuleCount              int                  `json:"rule_count"`
	LastCheckedAtMS        int64                `json:"last_checked_at_ms"`
	LastSuccessAtMS        int64                `json:"last_success_at_ms"`
	LastError              string               `json:"last_error"`
	CreatedAtMS            int64                `json:"created_at_ms"`
	UpdatedAtMS            int64                `json:"updated_at_ms"`
	Binding                *SubscriptionBinding `json:"binding,omitempty"`
}

type SubscriptionBinding struct {
	ID              int64  `json:"id"`
	UpstreamGroupID string `json:"upstream_group_id"`
	Priority        int    `json:"priority"`
	createdAtMS     int64
	updatedAtMS     int64
}

type SubscriptionInput struct {
	Category                        string  `json:"category"`
	Action                          string  `json:"action"`
	Name                            string  `json:"name"`
	SourceURL                       string  `json:"source_url"`
	RefreshIntervalSeconds          int     `json:"refresh_interval_seconds"`
	Enabled                         bool    `json:"enabled"`
	UpstreamGroupID                 string  `json:"upstream_group_id,omitempty"`
	Priority                        *int    `json:"priority,omitempty"`
	ExpectedUpstreamRegistryVersion *uint64 `json:"expected_upstream_registry_version,omitempty"`
}

type BindingInput struct {
	UpstreamGroupID                 string `json:"upstream_group_id"`
	Priority                        int    `json:"priority"`
	ExpectedUpstreamRegistryVersion uint64 `json:"expected_upstream_registry_version"`
}

func (s *Service) Subscriptions(ctx context.Context, category, action string) ([]Subscription, error) {
	query := `SELECT s.id,s.category,s.action,s.kind,s.name,s.source_url,s.refresh_interval_seconds,s.enabled,s.rule_count,s.last_checked_at_ms,s.last_success_at_ms,s.last_error,s.created_at_ms,s.updated_at_ms,b.id,COALESCE(b.upstream_group_id,''),COALESCE(b.priority,0),COALESCE(b.created_at_ms,0),COALESCE(b.updated_at_ms,0) FROM rule_subscriptions s LEFT JOIN subscription_bindings b ON b.subscription_id=s.id`
	args := []any{}
	if category != "" || action != "" {
		if !validAction(category, action) {
			return nil, errors.New("invalid category/action")
		}
		query += ` WHERE s.category=? AND s.action=?`
		args = append(args, category, action)
	}
	query += ` ORDER BY s.created_at_ms,s.id`
	rows, err := s.store.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Subscription{}
	for rows.Next() {
		var item Subscription
		var bindingID sql.NullInt64
		var group string
		var priority int
		var bindingCreatedAtMS, bindingUpdatedAtMS int64
		if err := rows.Scan(&item.ID, &item.Category, &item.Action, &item.Kind, &item.Name, &item.SourceURL, &item.RefreshIntervalSeconds, &item.Enabled, &item.RuleCount, &item.LastCheckedAtMS, &item.LastSuccessAtMS, &item.LastError, &item.CreatedAtMS, &item.UpdatedAtMS, &bindingID, &group, &priority, &bindingCreatedAtMS, &bindingUpdatedAtMS); err != nil {
			return nil, err
		}
		if bindingID.Valid {
			item.Binding = &SubscriptionBinding{ID: bindingID.Int64, UpstreamGroupID: group, Priority: priority, createdAtMS: bindingCreatedAtMS, updatedAtMS: bindingUpdatedAtMS}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) CreateURLSubscription(ctx context.Context, input SubscriptionInput, adminID int64, requestID, ip string) (Subscription, Version, error) {
	if input.RefreshIntervalSeconds == 0 {
		input.RefreshIntervalSeconds = int((24 * time.Hour).Seconds())
	}
	if err := validateSubscriptionInput(input, "url"); err != nil {
		return Subscription{}, Version{}, err
	}
	body, normalizedURL, err := downloadSubscription(ctx, input.SourceURL)
	if err != nil {
		return Subscription{}, Version{}, err
	}
	input.SourceURL = normalizedURL
	return s.createSubscription(ctx, input, "url", body, adminID, requestID, ip)
}

func (s *Service) CreateUploadSubscription(ctx context.Context, input SubscriptionInput, filename string, body []byte, adminID int64, requestID, ip string) (Subscription, Version, error) {
	if input.RefreshIntervalSeconds == 0 {
		input.RefreshIntervalSeconds = int((24 * time.Hour).Seconds())
	}
	if !strings.HasSuffix(strings.ToLower(strings.TrimSpace(filename)), ".txt") {
		return Subscription{}, Version{}, errors.New("subscription upload must be a .txt file")
	}
	if input.Name == "" {
		input.Name = filename
	}
	if err := validateSubscriptionInput(input, "upload"); err != nil {
		return Subscription{}, Version{}, err
	}
	return s.createSubscription(ctx, input, "upload", body, adminID, requestID, ip)
}

func (s *Service) createSubscription(ctx context.Context, input SubscriptionInput, kind string, body []byte, adminID int64, requestID, ip string) (Subscription, Version, error) {
	patterns, err := parseSubscription(body)
	if err != nil {
		return Subscription{}, Version{}, err
	}
	s.bindings.Lock()
	defer s.bindings.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validateSubscriptionBinding(ctx, input); err != nil {
		return Subscription{}, Version{}, err
	}
	now := time.Now().UnixMilli()
	domains, err := json.Marshal(patterns)
	if err != nil {
		return Subscription{}, Version{}, err
	}
	result, err := s.store.DB().ExecContext(ctx, `INSERT INTO rule_subscriptions(category,action,kind,name,source_url,refresh_interval_seconds,enabled,content_checksum,rule_count,last_checked_at_ms,last_success_at_ms,last_error,created_at_ms,updated_at_ms,domains_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, input.Category, input.Action, kind, strings.TrimSpace(input.Name), input.SourceURL, input.RefreshIntervalSeconds, input.Enabled, checksum(body), len(patterns), now, now, "", now, now, domains)
	if err != nil {
		return Subscription{}, Version{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Subscription{}, Version{}, err
	}
	if input.Category == "route" {
		if _, err = s.store.DB().ExecContext(ctx, `INSERT INTO subscription_bindings(subscription_id,upstream_group_id,priority,created_at_ms,updated_at_ms) VALUES(?,?,?,?,?)`, id, strings.TrimSpace(input.UpstreamGroupID), *input.Priority, now, now); err != nil {
			_, _ = s.store.DB().ExecContext(ctx, `DELETE FROM rule_subscriptions WHERE id=?`, id)
			return Subscription{}, Version{}, err
		}
	}
	restore := func() error {
		_, restoreErr := s.store.DB().ExecContext(ctx, `DELETE FROM rule_subscriptions WHERE id=?`, id)
		return restoreErr
	}
	all, err := s.allRules(ctx)
	var version Version
	if err == nil {
		version, err = s.publish(ctx, all, adminID, requestID, ip, 0)
	}
	if err != nil {
		mutationErr := s.restoreSubscriptionMutation(ctx, version, err, restore)
		if errors.Is(mutationErr, mosdnsclient.ErrUnknown) {
			item, getErr := s.subscription(ctx, id)
			return item, version, errors.Join(mutationErr, getErr)
		}
		return Subscription{}, version, mutationErr
	}
	item, err := s.subscription(ctx, id)
	return item, version, err
}

func (s *Service) RefreshSubscription(ctx context.Context, id int64, adminID int64, requestID, ip string) (Subscription, *Version, error) {
	item, err := s.subscription(ctx, id)
	if err != nil {
		return Subscription{}, nil, err
	}
	if item.Kind != "url" {
		return Subscription{}, nil, errors.New("uploaded subscriptions cannot be refreshed")
	}
	body, normalizedURL, err := downloadSubscription(ctx, item.SourceURL)
	if err != nil {
		return Subscription{}, nil, s.subscriptionFailure(ctx, item, err)
	}
	patterns, err := parseSubscription(body)
	if err != nil {
		return Subscription{}, nil, s.subscriptionFailure(ctx, item, err)
	}
	domains, err := json.Marshal(patterns)
	if err != nil {
		return Subscription{}, nil, s.subscriptionFailure(ctx, item, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.subscription(ctx, id)
	if err != nil {
		return Subscription{}, nil, err
	}
	if current.UpdatedAtMS != item.UpdatedAtMS {
		return current, nil, mosdnsclient.ErrConflict
	}
	item = current
	now := time.Now().UnixMilli()
	if now <= item.UpdatedAtMS {
		now = item.UpdatedAtMS + 1
	}
	if checksum(body) == sourceChecksum(ctx, s, item.ID) {
		_, err = s.store.DB().ExecContext(ctx, `UPDATE rule_subscriptions SET source_url=?,last_checked_at_ms=?,last_error='',updated_at_ms=? WHERE id=?`, normalizedURL, now, now, item.ID)
		updated, getErr := s.subscription(ctx, item.ID)
		return updated, nil, errors.Join(err, getErr)
	}
	var previousDomains []byte
	if err = s.store.DB().QueryRowContext(ctx, `SELECT domains_json FROM rule_subscriptions WHERE id=?`, item.ID).Scan(&previousDomains); err != nil {
		return Subscription{}, nil, err
	}
	previousChecksum := sourceChecksum(ctx, s, item.ID)
	restore := func() error {
		_, restoreErr := s.store.DB().ExecContext(ctx, `UPDATE rule_subscriptions SET source_url=?,content_checksum=?,rule_count=?,last_checked_at_ms=?,last_success_at_ms=?,last_error=?,updated_at_ms=?,domains_json=? WHERE id=?`, item.SourceURL, previousChecksum, item.RuleCount, item.LastCheckedAtMS, item.LastSuccessAtMS, item.LastError, item.UpdatedAtMS, previousDomains, item.ID)
		return restoreErr
	}
	_, err = s.store.DB().ExecContext(ctx, `UPDATE rule_subscriptions SET source_url=?,content_checksum=?,rule_count=?,last_checked_at_ms=?,last_success_at_ms=?,last_error='',updated_at_ms=?,domains_json=? WHERE id=?`, normalizedURL, checksum(body), len(patterns), now, now, now, domains, item.ID)
	if err != nil {
		return Subscription{}, nil, err
	}
	all, err := s.allRules(ctx)
	if err != nil {
		return Subscription{}, nil, s.restoreSubscriptionMutation(ctx, Version{}, err, restore)
	}
	version, err := s.publish(ctx, all, adminID, requestID, ip, 0)
	if err != nil {
		mutationErr := s.restoreSubscriptionMutation(ctx, version, err, restore)
		if errors.Is(mutationErr, mosdnsclient.ErrUnknown) {
			updated, getErr := s.subscription(ctx, item.ID)
			return updated, &version, errors.Join(mutationErr, getErr)
		}
		return Subscription{}, &version, mutationErr
	}
	updated, getErr := s.subscription(ctx, item.ID)
	return updated, &version, getErr
}

func (s *Service) SetSubscriptionEnabled(ctx context.Context, id int64, enabled bool, adminID int64, requestID, ip string) (Subscription, Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.subscription(ctx, id)
	if err != nil {
		return Subscription{}, Version{}, err
	}
	if item.Enabled == enabled {
		return item, Version{}, nil
	}
	now := time.Now().UnixMilli()
	if now <= item.UpdatedAtMS {
		now = item.UpdatedAtMS + 1
	}
	_, err = s.store.DB().ExecContext(ctx, `UPDATE rule_subscriptions SET enabled=?,updated_at_ms=? WHERE id=?`, enabled, now, id)
	if err != nil {
		return Subscription{}, Version{}, err
	}
	restore := func() error {
		_, restoreErr := s.store.DB().ExecContext(ctx, `UPDATE rule_subscriptions SET enabled=?,updated_at_ms=? WHERE id=?`, item.Enabled, item.UpdatedAtMS, id)
		return restoreErr
	}
	all, err := s.allRules(ctx)
	if err != nil {
		return Subscription{}, Version{}, s.restoreSubscriptionMutation(ctx, Version{}, err, restore)
	}
	version, err := s.publish(ctx, all, adminID, requestID, ip, 0)
	if err != nil {
		mutationErr := s.restoreSubscriptionMutation(ctx, version, err, restore)
		if errors.Is(mutationErr, mosdnsclient.ErrUnknown) {
			updated, getErr := s.subscription(ctx, id)
			return updated, version, errors.Join(mutationErr, getErr)
		}
		return Subscription{}, version, mutationErr
	}
	updated, getErr := s.subscription(ctx, id)
	return updated, version, errors.Join(err, getErr)
}

func (s *Service) DeleteSubscription(ctx context.Context, id int64, adminID int64, requestID, ip string) (Version, error) {
	s.bindings.Lock()
	defer s.bindings.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.subscription(ctx, id)
	if err != nil {
		return Version{}, err
	}
	var domains []byte
	if err = s.store.DB().QueryRowContext(ctx, `SELECT domains_json FROM rule_subscriptions WHERE id=?`, id).Scan(&domains); err != nil {
		return Version{}, err
	}
	var contentChecksum string
	if err = s.store.DB().QueryRowContext(ctx, `SELECT content_checksum FROM rule_subscriptions WHERE id=?`, id).Scan(&contentChecksum); err != nil {
		return Version{}, err
	}
	restore := func() error {
		_, restoreErr := s.store.DB().ExecContext(ctx, `INSERT INTO rule_subscriptions(id,category,action,kind,name,source_url,refresh_interval_seconds,enabled,content_checksum,rule_count,last_checked_at_ms,last_success_at_ms,last_error,created_at_ms,updated_at_ms,domains_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.Category, item.Action, item.Kind, item.Name, item.SourceURL, item.RefreshIntervalSeconds, item.Enabled, contentChecksum, item.RuleCount, item.LastCheckedAtMS, item.LastSuccessAtMS, item.LastError, item.CreatedAtMS, item.UpdatedAtMS, domains)
		if restoreErr == nil && item.Binding != nil {
			_, restoreErr = s.store.DB().ExecContext(ctx, `INSERT INTO subscription_bindings(id,subscription_id,upstream_group_id,priority,created_at_ms,updated_at_ms) VALUES(?,?,?,?,?,?)`, item.Binding.ID, item.ID, item.Binding.UpstreamGroupID, item.Binding.Priority, item.Binding.createdAtMS, item.Binding.updatedAtMS)
		}
		return restoreErr
	}
	_, err = s.store.DB().ExecContext(ctx, `DELETE FROM rule_subscriptions WHERE id=?`, id)
	if err != nil {
		return Version{}, err
	}
	all, err := s.allRules(ctx)
	if err != nil {
		return Version{}, s.restoreSubscriptionMutation(ctx, Version{}, err, restore)
	}
	version, err := s.publish(ctx, all, adminID, requestID, ip, 0)
	if err != nil {
		return version, s.restoreSubscriptionMutation(ctx, version, err, restore)
	}
	return version, nil
}

func (s *Service) RefreshDue(ctx context.Context) {
	items, err := s.Subscriptions(ctx, "", "")
	if err != nil {
		return
	}
	now := time.Now().UnixMilli()
	for _, item := range items {
		if item.Kind == "url" && item.Enabled && now-item.LastCheckedAtMS >= int64(item.RefreshIntervalSeconds)*1000 {
			_, _, _ = s.RefreshSubscription(ctx, item.ID, 0, "subscription_refresh", "")
		}
	}
}

func (s *Service) subscriptionSets(ctx context.Context) ([]mosdnsclient.SubscriptionSet, error) {
	rows, err := s.store.DB().QueryContext(ctx, `SELECT s.id,s.name,s.category,s.action,s.enabled,s.domains_json,b.id,COALESCE(b.upstream_group_id,''),COALESCE(b.priority,100) FROM rule_subscriptions s LEFT JOIN subscription_bindings b ON b.subscription_id=s.id ORDER BY s.id`)
	if err != nil {
		return nil, err
	}
	type sourceRow struct {
		id                     int64
		name, category, action string
		enabled                bool
		raw                    []byte
		bindingID              sql.NullInt64
		upstreamGroupID        string
		priority               int
	}
	sources := make([]sourceRow, 0)
	for rows.Next() {
		var source sourceRow
		if err := rows.Scan(&source.id, &source.name, &source.category, &source.action, &source.enabled, &source.raw, &source.bindingID, &source.upstreamGroupID, &source.priority); err != nil {
			_ = rows.Close()
			return nil, err
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	sets := make([]mosdnsclient.SubscriptionSet, 0, len(sources))
	for _, source := range sources {
		if len(source.raw) == 0 {
			legacy, err := s.legacySubscriptionDomains(ctx, source.id)
			if err != nil {
				return nil, err
			}
			source.raw, err = json.Marshal(legacy)
			if err != nil {
				return nil, err
			}
			if _, err = s.store.DB().ExecContext(ctx, `UPDATE rule_subscriptions SET domains_json=?,rule_count=?,updated_at_ms=? WHERE id=?`, source.raw, len(legacy), time.Now().UnixMilli(), source.id); err != nil {
				return nil, err
			}
		}
		if !source.enabled {
			continue
		}
		if source.category == "route" && !source.bindingID.Valid {
			return nil, errors.New("route subscription has no binding")
		}
		var domains []string
		if err := json.Unmarshal(source.raw, &domains); err != nil {
			return nil, errors.New("subscription domains are invalid")
		}
		if len(domains) == 0 {
			continue
		}
		sort.Strings(domains)
		set := mosdnsclient.SubscriptionSet{SourceID: source.id, SourceName: source.name, Category: source.category, Action: source.action, Priority: 100, Domains: domains}
		if source.category == "route" {
			set.Action = "upstream"
			set.BindingID = source.bindingID.Int64
			set.UpstreamGroupID = source.upstreamGroupID
			set.Priority = source.priority
		}
		sets = append(sets, set)
	}
	return sets, nil
}

func (s *Service) legacySubscriptionDomains(ctx context.Context, id int64) ([]string, error) {
	rows, err := s.store.DB().QueryContext(ctx, `SELECT pattern FROM domain_rules WHERE source=? ORDER BY normalized_pattern`, subscriptionSource(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	domains := []string{}
	for rows.Next() {
		var domain string
		if err := rows.Scan(&domain); err != nil {
			return nil, err
		}
		domains = append(domains, domain)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return domains, nil
}

func (s *Service) subscription(ctx context.Context, id int64) (Subscription, error) {
	var item Subscription
	var bindingID sql.NullInt64
	var group string
	var priority int
	var bindingCreatedAtMS, bindingUpdatedAtMS int64
	err := s.store.DB().QueryRowContext(ctx, `SELECT s.id,s.category,s.action,s.kind,s.name,s.source_url,s.refresh_interval_seconds,s.enabled,s.rule_count,s.last_checked_at_ms,s.last_success_at_ms,s.last_error,s.created_at_ms,s.updated_at_ms,b.id,COALESCE(b.upstream_group_id,''),COALESCE(b.priority,0),COALESCE(b.created_at_ms,0),COALESCE(b.updated_at_ms,0) FROM rule_subscriptions s LEFT JOIN subscription_bindings b ON b.subscription_id=s.id WHERE s.id=?`, id).Scan(&item.ID, &item.Category, &item.Action, &item.Kind, &item.Name, &item.SourceURL, &item.RefreshIntervalSeconds, &item.Enabled, &item.RuleCount, &item.LastCheckedAtMS, &item.LastSuccessAtMS, &item.LastError, &item.CreatedAtMS, &item.UpdatedAtMS, &bindingID, &group, &priority, &bindingCreatedAtMS, &bindingUpdatedAtMS)
	if err == nil && bindingID.Valid {
		item.Binding = &SubscriptionBinding{ID: bindingID.Int64, UpstreamGroupID: group, Priority: priority, createdAtMS: bindingCreatedAtMS, updatedAtMS: bindingUpdatedAtMS}
	}
	return item, err
}

func (s *Service) UpdateSubscriptionBinding(ctx context.Context, id int64, input BindingInput, adminID int64, requestID, ip string) (Subscription, Version, error) {
	s.bindings.Lock()
	defer s.bindings.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.subscription(ctx, id)
	if err != nil {
		return Subscription{}, Version{}, err
	}
	if item.Category != "route" || item.Binding == nil {
		return Subscription{}, Version{}, errors.New("subscription is not a route subscription")
	}
	if err := s.validateBinding(ctx, input.UpstreamGroupID, input.Priority, input.ExpectedUpstreamRegistryVersion); err != nil {
		return Subscription{}, Version{}, err
	}
	if item.Binding.UpstreamGroupID == strings.TrimSpace(input.UpstreamGroupID) && item.Binding.Priority == input.Priority {
		return item, Version{}, nil
	}
	now := time.Now().UnixMilli()
	if _, err = s.store.DB().ExecContext(ctx, `UPDATE subscription_bindings SET upstream_group_id=?,priority=?,updated_at_ms=? WHERE subscription_id=?`, strings.TrimSpace(input.UpstreamGroupID), input.Priority, now, id); err != nil {
		return Subscription{}, Version{}, err
	}
	restore := func() error {
		_, restoreErr := s.store.DB().ExecContext(ctx, `UPDATE subscription_bindings SET upstream_group_id=?,priority=?,updated_at_ms=? WHERE subscription_id=?`, item.Binding.UpstreamGroupID, item.Binding.Priority, item.Binding.updatedAtMS, id)
		return restoreErr
	}
	all, err := s.allRules(ctx)
	var version Version
	if err == nil {
		version, err = s.publish(ctx, all, adminID, requestID, ip, 0)
	}
	if err != nil {
		mutationErr := s.restoreSubscriptionMutation(ctx, version, err, restore)
		if errors.Is(mutationErr, mosdnsclient.ErrUnknown) {
			updated, getErr := s.subscription(ctx, id)
			return updated, version, errors.Join(mutationErr, getErr)
		}
		return Subscription{}, version, mutationErr
	}
	updated, getErr := s.subscription(ctx, id)
	return updated, version, getErr
}

// restoreSubscriptionMutation compensates source-table writes only when the
// runtime is known not to have adopted the candidate. Ambiguous outcomes keep
// the desired source state so reconcile can converge forward without a split.
func (s *Service) restoreSubscriptionMutation(ctx context.Context, candidate Version, cause error, restore func() error) error {
	if candidate.Version == 0 {
		return errors.Join(cause, restore())
	}
	var checksum, status, errorCode string
	var previous sql.NullInt64
	rowErr := s.store.DB().QueryRowContext(ctx, `SELECT checksum,status,COALESCE(error_code,''),previous_version FROM rule_versions WHERE version=?`, candidate.Version).Scan(&checksum, &status, &errorCode, &previous)
	if rowErr != nil {
		return errors.Join(cause, mosdnsclient.ErrUnknown)
	}
	failedBeforeApply := status == statusFailed && (errorCode == "VALIDATION_FAILED" || errorCode == "CHECKSUM_MISMATCH" || errorCode == "APPLY_FAILED" || errorCode == "APPLY_TIMEOUT_NOT_APPLIED")
	runtime, statusErr := s.mosdns.Status(ctx)
	if statusErr == nil && runtime.SnapshotVersion == candidate.Version && runtime.Checksum == checksum {
		return errors.Join(cause, mosdnsclient.ErrUnknown)
	}
	if failedBeforeApply || (statusErr == nil && previous.Valid && runtime.SnapshotVersion == uint64(previous.Int64)) {
		return errors.Join(cause, restore())
	}
	return errors.Join(cause, mosdnsclient.ErrUnknown)
}

func (s *Service) validateSubscriptionBinding(ctx context.Context, input SubscriptionInput) error {
	if input.Category == "access" {
		if strings.TrimSpace(input.UpstreamGroupID) != "" || input.Priority != nil || input.ExpectedUpstreamRegistryVersion != nil {
			return errors.New("access subscriptions must not include a binding")
		}
		return nil
	}
	if input.Priority == nil || input.ExpectedUpstreamRegistryVersion == nil || strings.TrimSpace(input.UpstreamGroupID) == "" {
		return errors.New("route subscriptions require upstream_group_id, priority, and expected_upstream_registry_version")
	}
	return s.validateBinding(ctx, input.UpstreamGroupID, *input.Priority, *input.ExpectedUpstreamRegistryVersion)
}

func (s *Service) validateBinding(ctx context.Context, groupID string, priority int, expectedVersion uint64) error {
	if priority < 0 || priority > 1000 {
		return errors.New("priority must be between 0 and 1000")
	}
	registry, err := s.mosdns.RegistryStatus(ctx)
	if err != nil {
		return err
	}
	if registry.Version != expectedVersion {
		return mosdnsclient.ErrConflict
	}
	groupID = strings.TrimSpace(groupID)
	for _, group := range registry.Groups {
		if group.ID == groupID && group.Enabled {
			return nil
		}
	}
	return errors.New("upstream group must exist and be enabled")
}

func (s *Service) subscriptionFailure(ctx context.Context, item Subscription, cause error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.subscription(ctx, item.ID)
	if err != nil {
		return errors.Join(cause, err)
	}
	if current.UpdatedAtMS != item.UpdatedAtMS {
		return errors.Join(cause, mosdnsclient.ErrConflict)
	}
	now := time.Now().UnixMilli()
	if now <= item.UpdatedAtMS {
		now = item.UpdatedAtMS + 1
	}
	_, err = s.store.DB().ExecContext(ctx, `UPDATE rule_subscriptions SET last_checked_at_ms=?,last_error=?,updated_at_ms=? WHERE id=?`, now, truncateError(cause), now, item.ID)
	return errors.Join(cause, err)
}

func validateSubscriptionInput(input SubscriptionInput, kind string) error {
	if !validAction(input.Category, input.Action) || input.Category == "logging" {
		return errors.New("invalid subscription category/action")
	}
	if strings.TrimSpace(input.Name) == "" || len([]rune(input.Name)) > 128 {
		return errors.New("subscription name must contain 1..128 characters")
	}
	if kind == "url" && strings.TrimSpace(input.SourceURL) == "" {
		return errors.New("subscription source_url is required")
	}
	if time.Duration(input.RefreshIntervalSeconds)*time.Second < minRefreshInterval || time.Duration(input.RefreshIntervalSeconds)*time.Second > maxRefreshInterval {
		return errors.New("refresh_interval_seconds must be between 900 and 2592000")
	}
	return nil
}

func parseSubscription(body []byte) ([]string, error) {
	if len(body) == 0 || len(body) > maxSubscriptionSize {
		return nil, errors.New("subscription file must contain 1..20971520 bytes")
	}
	seen := map[string]bool{}
	patterns := []string{}
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pattern, err := normalize(line, "domain")
		if err != nil {
			return nil, fmt.Errorf("invalid subscription domain %q: %w", line, err)
		}
		if !seen[pattern] {
			seen[pattern] = true
			patterns = append(patterns, pattern)
		}
	}
	if len(patterns) == 0 {
		return nil, errors.New("subscription file contains no domains")
	}
	return patterns, nil
}

func downloadSubscription(ctx context.Context, sourceURL string) ([]byte, string, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(sourceURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, "", errors.New("subscription source_url must be an HTTP(S) URL")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "text/plain")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download subscription: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download subscription: HTTP %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSubscriptionSize+1))
	if err != nil {
		return nil, "", fmt.Errorf("read subscription: %w", err)
	}
	if len(body) == 0 || len(body) > maxSubscriptionSize {
		return nil, "", errors.New("subscription file must contain 1..20971520 bytes")
	}
	return body, resp.Request.URL.String(), nil
}

func checksum(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func sourceChecksum(ctx context.Context, s *Service, id int64) string {
	var value string
	_ = s.store.DB().QueryRowContext(ctx, `SELECT content_checksum FROM rule_subscriptions WHERE id=?`, id).Scan(&value)
	return value
}
func subscriptionSource(id int64) string { return fmt.Sprintf("subscription:%d", id) }
func truncateError(err error) string {
	value := err.Error()
	if len(value) > 500 {
		return value[:500]
	}
	return value
}
