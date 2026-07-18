package rules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	minRefreshInterval  = 15 * time.Minute
	maxRefreshInterval  = 30 * 24 * time.Hour
	maxSubscriptionSize = 20 << 20
)

type Subscription struct {
	ID                     int64  `json:"id"`
	Category               string `json:"category"`
	Action                 string `json:"action"`
	Kind                   string `json:"kind"`
	Name                   string `json:"name"`
	SourceURL              string `json:"source_url"`
	RefreshIntervalSeconds int    `json:"refresh_interval_seconds"`
	Enabled                bool   `json:"enabled"`
	RuleCount              int    `json:"rule_count"`
	LastCheckedAtMS        int64  `json:"last_checked_at_ms"`
	LastSuccessAtMS        int64  `json:"last_success_at_ms"`
	LastError              string `json:"last_error"`
	CreatedAtMS            int64  `json:"created_at_ms"`
	UpdatedAtMS            int64  `json:"updated_at_ms"`
}

type SubscriptionInput struct {
	Category               string `json:"category"`
	Action                 string `json:"action"`
	Name                   string `json:"name"`
	SourceURL              string `json:"source_url"`
	RefreshIntervalSeconds int    `json:"refresh_interval_seconds"`
	Enabled                bool   `json:"enabled"`
}

func (s *Service) Subscriptions(ctx context.Context, category, action string) ([]Subscription, error) {
	query := `SELECT id,category,action,kind,name,source_url,refresh_interval_seconds,enabled,rule_count,last_checked_at_ms,last_success_at_ms,last_error,created_at_ms,updated_at_ms FROM rule_subscriptions`
	args := []any{}
	if category != "" || action != "" {
		if !validAction(category, action) {
			return nil, errors.New("invalid category/action")
		}
		query += ` WHERE category=? AND action=?`
		args = append(args, category, action)
	}
	query += ` ORDER BY created_at_ms,id`
	rows, err := s.store.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Subscription{}
	for rows.Next() {
		var item Subscription
		if err := rows.Scan(&item.ID, &item.Category, &item.Action, &item.Kind, &item.Name, &item.SourceURL, &item.RefreshIntervalSeconds, &item.Enabled, &item.RuleCount, &item.LastCheckedAtMS, &item.LastSuccessAtMS, &item.LastError, &item.CreatedAtMS, &item.UpdatedAtMS); err != nil {
			return nil, err
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
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UnixMilli()
	result, err := s.store.DB().ExecContext(ctx, `INSERT INTO rule_subscriptions(category,action,kind,name,source_url,refresh_interval_seconds,enabled,content_checksum,rule_count,last_checked_at_ms,last_success_at_ms,last_error,created_at_ms,updated_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, input.Category, input.Action, kind, strings.TrimSpace(input.Name), input.SourceURL, input.RefreshIntervalSeconds, input.Enabled, checksum(body), len(patterns), now, now, "", now, now)
	if err != nil {
		return Subscription{}, Version{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Subscription{}, Version{}, err
	}
	all, err := s.allRules(ctx)
	if err == nil {
		all, err = s.rulesForSubscription(ctx, all, id, input, patterns)
	}
	var version Version
	if err == nil {
		version, err = s.publish(ctx, all, adminID, requestID, ip, 0)
	}
	if err != nil {
		_, _ = s.store.DB().ExecContext(ctx, `DELETE FROM rule_subscriptions WHERE id=?`, id)
		return Subscription{}, version, err
	}
	item, err := s.subscription(ctx, id)
	return item, version, err
}

func (s *Service) RefreshSubscription(ctx context.Context, id int64, adminID int64, requestID, ip string) (Subscription, *Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	now := time.Now().UnixMilli()
	if checksum(body) == sourceChecksum(ctx, s, item.ID) {
		_, err = s.store.DB().ExecContext(ctx, `UPDATE rule_subscriptions SET source_url=?,last_checked_at_ms=?,last_error='',updated_at_ms=? WHERE id=?`, normalizedURL, now, now, item.ID)
		updated, getErr := s.subscription(ctx, item.ID)
		return updated, nil, errors.Join(err, getErr)
	}
	patterns, err := parseSubscription(body)
	if err != nil {
		return Subscription{}, nil, s.subscriptionFailure(ctx, item, err)
	}
	all, err := s.allRules(ctx)
	if err == nil {
		all, err = s.rulesForSubscription(ctx, all, item.ID, SubscriptionInput{Category: item.Category, Action: item.Action, Enabled: item.Enabled}, patterns)
	}
	if err != nil {
		return Subscription{}, nil, s.subscriptionFailure(ctx, item, err)
	}
	version, err := s.publish(ctx, all, adminID, requestID, ip, 0)
	if err != nil {
		return Subscription{}, &version, s.subscriptionFailure(ctx, item, err)
	}
	_, err = s.store.DB().ExecContext(ctx, `UPDATE rule_subscriptions SET source_url=?,content_checksum=?,rule_count=?,last_checked_at_ms=?,last_success_at_ms=?,last_error='',updated_at_ms=? WHERE id=?`, normalizedURL, checksum(body), len(patterns), now, now, now, item.ID)
	updated, getErr := s.subscription(ctx, item.ID)
	return updated, &version, errors.Join(err, getErr)
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
	all, err := s.allRules(ctx)
	if err != nil {
		return Subscription{}, Version{}, err
	}
	for i := range all {
		if all[i].Source == subscriptionSource(id) {
			all[i].Enabled = enabled
			all[i].UpdatedAtMS = time.Now().UnixMilli()
		}
	}
	version, err := s.publish(ctx, all, adminID, requestID, ip, 0)
	if err != nil {
		return Subscription{}, version, err
	}
	_, err = s.store.DB().ExecContext(ctx, `UPDATE rule_subscriptions SET enabled=?,updated_at_ms=? WHERE id=?`, enabled, time.Now().UnixMilli(), id)
	updated, getErr := s.subscription(ctx, id)
	return updated, version, errors.Join(err, getErr)
}

func (s *Service) DeleteSubscription(ctx context.Context, id int64, adminID int64, requestID, ip string) (Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.subscription(ctx, id); err != nil {
		return Version{}, err
	}
	all, err := s.allRules(ctx)
	if err != nil {
		return Version{}, err
	}
	kept := all[:0]
	for _, rule := range all {
		if rule.Source != subscriptionSource(id) {
			kept = append(kept, rule)
		}
	}
	version, err := s.publish(ctx, kept, adminID, requestID, ip, 0)
	if err != nil {
		return version, err
	}
	_, err = s.store.DB().ExecContext(ctx, `DELETE FROM rule_subscriptions WHERE id=?`, id)
	return version, err
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

func (s *Service) rulesForSubscription(ctx context.Context, all []Rule, id int64, input SubscriptionInput, patterns []string) ([]Rule, error) {
	kept := all[:0]
	for _, rule := range all {
		if rule.Source != subscriptionSource(id) {
			kept = append(kept, rule)
		}
	}
	now := time.Now().UnixMilli()
	for _, pattern := range patterns {
		ruleID, err := s.nextRuleID(ctx)
		if err != nil {
			return nil, err
		}
		kept = append(kept, Rule{ID: ruleID, Category: input.Category, Action: input.Action, MatchType: "domain", Pattern: pattern, Priority: 100, Source: subscriptionSource(id), Enabled: input.Enabled, CreatedAtMS: now, UpdatedAtMS: now})
	}
	return kept, nil
}

func (s *Service) subscription(ctx context.Context, id int64) (Subscription, error) {
	var item Subscription
	err := s.store.DB().QueryRowContext(ctx, `SELECT id,category,action,kind,name,source_url,refresh_interval_seconds,enabled,rule_count,last_checked_at_ms,last_success_at_ms,last_error,created_at_ms,updated_at_ms FROM rule_subscriptions WHERE id=?`, id).Scan(&item.ID, &item.Category, &item.Action, &item.Kind, &item.Name, &item.SourceURL, &item.RefreshIntervalSeconds, &item.Enabled, &item.RuleCount, &item.LastCheckedAtMS, &item.LastSuccessAtMS, &item.LastError, &item.CreatedAtMS, &item.UpdatedAtMS)
	return item, err
}

func (s *Service) subscriptionFailure(ctx context.Context, item Subscription, cause error) error {
	_, err := s.store.DB().ExecContext(ctx, `UPDATE rule_subscriptions SET last_checked_at_ms=?,last_error=?,updated_at_ms=? WHERE id=?`, time.Now().UnixMilli(), truncateError(cause), time.Now().UnixMilli(), item.ID)
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
