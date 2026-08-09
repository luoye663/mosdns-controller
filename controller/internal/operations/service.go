// Package operations 承载设备、系统和管理员操作等非规则控制面能力。
package operations

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/managed-dns/controller/internal/coordination"
	"github.com/managed-dns/controller/internal/mosdnsclient"
	"github.com/managed-dns/controller/internal/queryingest"
)

var ErrInvalidCursor = errors.New("invalid cursor")
var ErrNotFound = errors.New("upstream group not found")
var ErrProtectedGroup = errors.New("protected upstream group")
var ErrGroupReferenced = errors.New("upstream group is referenced")
var ErrValidation = errors.New("invalid upstream group request")

var groupIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type Device struct {
	ID            int64  `json:"id"`
	IP            string `json:"ip"`
	MAC           string `json:"mac"`
	Hostname      string `json:"hostname"`
	DisplayName   string `json:"display_name"`
	Note          string `json:"note"`
	Source        string `json:"source"`
	FirstSeenAtMS int64  `json:"first_seen_at_ms"`
	LastSeenAtMS  int64  `json:"last_seen_at_ms"`
	QueryCount24H int64  `json:"query_count_24h"`
}
type DevicePatch struct {
	DisplayName *string `json:"display_name"`
	Note        *string `json:"note"`
}
type AuditLog struct {
	ID            int64  `json:"id"`
	AdminID       *int64 `json:"admin_id,omitempty"`
	AdminUsername string `json:"admin_username"`
	Action        string `json:"action"`
	ResourceType  string `json:"resource_type"`
	ResourceID    string `json:"resource_id"`
	RequestID     string `json:"request_id"`
	ClientIP      string `json:"client_ip"`
	Result        string `json:"result"`
	ErrorCode     string `json:"error_code"`
	CreatedAtMS   int64  `json:"created_at_ms"`
}
type AuditLogQuery struct {
	Limit  int
	Cursor string
}
type AuditLogPage struct {
	Items      []AuditLog `json:"items"`
	NextCursor string     `json:"next_cursor,omitempty"`
}
type DatabaseStatus struct {
	Bytes    int64 `json:"bytes"`
	WALBytes int64 `json:"wal_bytes"`
}
type SystemStatus struct {
	Database                 DatabaseStatus            `json:"database"`
	ControllerMemoryRSSBytes int64                     `json:"controller_memory_rss_bytes"`
	Mosdns                   *mosdnsclient.Status      `json:"mosdns,omitempty"`
	MosdnsError              string                    `json:"mosdns_error,omitempty"`
	LastSuccessfulIngest     string                    `json:"last_successful_ingest_at,omitempty"`
	LastRetention            string                    `json:"last_retention_at,omitempty"`
	Audit                    *mosdnsclient.AuditStatus `json:"audit,omitempty"`
	AuditError               string                    `json:"audit_error,omitempty"`
}
type Settings struct {
	CacheEnabled               bool   `json:"cache_enabled"`
	CacheTTL                   int    `json:"cache_ttl"`
	NegativeCacheEnabled       bool   `json:"negative_cache_enabled"`
	NegativeCacheTTL           int    `json:"negative_cache_ttl"`
	QueryRetentionDays         int    `json:"query_retention_days"`
	DatabaseMaxSizeGiB         int    `json:"database_max_size_gib"`
	AddressFamilyMode          string `json:"address_family_mode"`
	DefaultUpstreamGroupID     string `json:"default_upstream_group_id"`
	GlobalMaxInFlight          int    `json:"global_max_in_flight"`
	DefaultGroupMaxInFlight    int    `json:"default_group_max_in_flight"`
	DefaultGroupQueryTimeoutMS int    `json:"default_group_query_timeout_ms"`
	OverloadAction             string `json:"overload_action"`
	UpstreamRegistryVersion    uint64 `json:"upstream_registry_version"`
}
type UpstreamGroupWrite struct {
	ExpectedCurrentVersion uint64                     `json:"expected_current_version"`
	Group                  mosdnsclient.UpstreamGroup `json:"group"`
}
type VersionPrecondition struct {
	ExpectedCurrentVersion uint64 `json:"expected_current_version"`
}
type Service struct {
	db       *sql.DB
	dbPath   string
	mosdns   mosdnsclient.Client
	ingest   *queryingest.Service
	bindings *coordination.UpstreamBindings
}

func New(db *sql.DB, dbPath string, mosdns mosdnsclient.Client, ingest *queryingest.Service, locks ...*coordination.UpstreamBindings) *Service {
	bindings := &coordination.UpstreamBindings{}
	if len(locks) > 0 && locks[0] != nil {
		bindings = locks[0]
	}
	return &Service{db: db, dbPath: dbPath, mosdns: mosdns, ingest: ingest, bindings: bindings}
}

func (s *Service) Devices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.id,d.ip,COALESCE(d.mac,''),COALESCE(d.hostname,''),COALESCE(d.display_name,''),d.note,d.source,d.first_seen_at_ms,d.last_seen_at_ms,COUNT(q.id) FROM devices d LEFT JOIN dns_queries q ON q.client_ip=d.ip AND q.timestamp_unix_ms>=? GROUP BY d.id ORDER BY d.last_seen_at_ms DESC`, time.Now().Add(-24*time.Hour).UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	devices := []Device{}
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.IP, &d.MAC, &d.Hostname, &d.DisplayName, &d.Note, &d.Source, &d.FirstSeenAtMS, &d.LastSeenAtMS, &d.QueryCount24H); err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

func (s *Service) UpdateDevice(ctx context.Context, id int64, patch DevicePatch, adminID int64, requestID, clientIP string) (Device, error) {
	if patch.DisplayName == nil && patch.Note == nil {
		return Device{}, errors.New("at least one device field is required")
	}
	if patch.DisplayName != nil && len([]rune(strings.TrimSpace(*patch.DisplayName))) > 128 {
		return Device{}, errors.New("display_name exceeds 128 characters")
	}
	if patch.Note != nil && len([]rune(*patch.Note)) > 500 {
		return Device{}, errors.New("note exceeds 500 characters")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Device{}, err
	}
	defer tx.Rollback()
	var current Device
	err = tx.QueryRowContext(ctx, `SELECT id,ip,COALESCE(mac,''),COALESCE(hostname,''),COALESCE(display_name,''),note,source,first_seen_at_ms,last_seen_at_ms FROM devices WHERE id=?`, id).Scan(&current.ID, &current.IP, &current.MAC, &current.Hostname, &current.DisplayName, &current.Note, &current.Source, &current.FirstSeenAtMS, &current.LastSeenAtMS)
	if err != nil {
		return Device{}, err
	}
	if patch.DisplayName != nil {
		current.DisplayName = strings.TrimSpace(*patch.DisplayName)
	}
	if patch.Note != nil {
		current.Note = *patch.Note
	}
	now := time.Now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `UPDATE devices SET display_name=?,note=?,updated_at_ms=? WHERE id=?`, current.DisplayName, current.Note, now, id); err != nil {
		return Device{}, err
	}
	if err = s.auditTx(ctx, tx, adminID, "update", "device", fmt.Sprint(id), requestID, clientIP, "success", ""); err != nil {
		return Device{}, err
	}
	if err = tx.Commit(); err != nil {
		return Device{}, err
	}
	// 返回值也携带列表页所需的 24 小时计数，避免客户端因 PATCH 后数据不完整而额外猜测。
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dns_queries WHERE client_ip=? AND timestamp_unix_ms>=?`, current.IP, time.Now().Add(-24*time.Hour).UnixMilli()).Scan(&current.QueryCount24H); err != nil {
		return Device{}, err
	}
	return current, nil
}

func (s *Service) AuditLogs(ctx context.Context, query AuditLogQuery) (AuditLogPage, error) {
	if query.Limit <= 0 || query.Limit > 500 {
		query.Limit = 100
	}
	args := []any{}
	where := ""
	if query.Cursor != "" {
		ts, id, err := decodeAuditCursor(query.Cursor)
		if err != nil {
			return AuditLogPage{}, ErrInvalidCursor
		}
		where = " WHERE (l.created_at_ms < ? OR (l.created_at_ms = ? AND l.id < ?))"
		args = append(args, ts, ts, id)
	}
	args = append(args, query.Limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT l.id,l.admin_id,COALESCE(a.username,''),l.action,l.resource_type,COALESCE(l.resource_id,''),l.request_id,COALESCE(l.client_ip,''),l.result,COALESCE(l.error_code,''),l.created_at_ms FROM admin_audit_logs l LEFT JOIN admins a ON a.id=l.admin_id`+where+` ORDER BY l.created_at_ms DESC,l.id DESC LIMIT ?`, args...)
	if err != nil {
		return AuditLogPage{}, err
	}
	defer rows.Close()
	page := AuditLogPage{Items: []AuditLog{}}
	for rows.Next() {
		var item AuditLog
		var admin sql.NullInt64
		if err := rows.Scan(&item.ID, &admin, &item.AdminUsername, &item.Action, &item.ResourceType, &item.ResourceID, &item.RequestID, &item.ClientIP, &item.Result, &item.ErrorCode, &item.CreatedAtMS); err != nil {
			return AuditLogPage{}, err
		}
		if admin.Valid {
			value := admin.Int64
			item.AdminID = &value
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return AuditLogPage{}, err
	}
	if len(page.Items) > query.Limit {
		last := page.Items[query.Limit-1]
		page.NextCursor = encodeAuditCursor(last.CreatedAtMS, last.ID)
		page.Items = page.Items[:query.Limit]
	}
	return page, nil
}

func encodeAuditCursor(timestampMS, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(timestampMS, 10) + ":" + strconv.FormatInt(id, 10)))
}

func decodeAuditCursor(value string) (int64, int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, 0, err
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 2 {
		return 0, 0, ErrInvalidCursor
	}
	timestampMS, timestampErr := strconv.ParseInt(parts[0], 10, 64)
	id, idErr := strconv.ParseInt(parts[1], 10, 64)
	if timestampErr != nil || idErr != nil || timestampMS < 0 || id < 1 {
		return 0, 0, ErrInvalidCursor
	}
	return timestampMS, id, nil
}

func (s *Service) FlushCaches(ctx context.Context, adminID int64, requestID, clientIP string) error {
	registry, flushErr := s.mosdns.RegistryStatus(ctx)
	if flushErr == nil {
		flushErr = s.mosdns.FlushRegistry(ctx, "", registry.Version)
	}
	result, code := "success", ""
	if flushErr != nil {
		result = "failed"
		code = "CACHE_FLUSH_FAILED"
	}
	if err := s.Audit(ctx, adminID, "flush", "cache", "all", requestID, clientIP, result, code); err != nil {
		return err
	}
	return flushErr
}
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	registry, err := s.mosdns.RegistryStatus(ctx)
	if err != nil {
		return Settings{}, err
	}
	settings, _, err := s.databaseSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	settings.CacheEnabled = registry.Cache.Enabled
	settings.CacheTTL = registry.Cache.LazyTTL
	settings.NegativeCacheEnabled = registry.Cache.Negative.Enabled
	settings.NegativeCacheTTL = int(registry.Cache.Negative.TTL)
	settings.DefaultUpstreamGroupID = registry.DefaultGroupID
	settings.GlobalMaxInFlight = registry.Protection.GlobalMaxInFlight
	settings.DefaultGroupMaxInFlight = registry.Protection.DefaultGroupMaxInFlight
	settings.DefaultGroupQueryTimeoutMS = registry.Protection.DefaultGroupQueryTimeoutMS
	settings.OverloadAction = registry.Protection.OverloadAction
	settings.UpstreamRegistryVersion = registry.Version
	return settings, nil
}

func (s *Service) databaseSettings(ctx context.Context) (Settings, bool, error) {
	settings := Settings{CacheEnabled: true, NegativeCacheEnabled: true, NegativeCacheTTL: 30, QueryRetentionDays: 7, DatabaseMaxSizeGiB: 2, AddressFamilyMode: "dual_stack"}
	rows, err := s.db.QueryContext(ctx, `SELECT key,value_json FROM system_state WHERE key IN ('cache_enabled','cache_ttl','negative_cache_enabled','negative_cache_ttl','query_retention_days','database_max_size_gib','address_family_mode','default_upstream_group_id')`)
	if err != nil {
		return Settings{}, false, err
	}
	defer rows.Close()
	hasDefault := false
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return Settings{}, false, err
		}
		switch key {
		case "cache_enabled":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return Settings{}, false, err
			}
			settings.CacheEnabled = parsed
		case "query_retention_days":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return Settings{}, false, err
			}
			settings.QueryRetentionDays = parsed
		case "cache_ttl":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return Settings{}, false, err
			}
			settings.CacheTTL = parsed
		case "negative_cache_enabled":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return Settings{}, false, err
			}
			settings.NegativeCacheEnabled = parsed
		case "negative_cache_ttl":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return Settings{}, false, err
			}
			settings.NegativeCacheTTL = parsed
		case "database_max_size_gib":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return Settings{}, false, err
			}
			settings.DatabaseMaxSizeGiB = parsed
		case "address_family_mode":
			settings.AddressFamilyMode = value
		case "default_upstream_group_id":
			settings.DefaultUpstreamGroupID, hasDefault = value, true
		}
	}
	if err := rows.Err(); err != nil {
		return Settings{}, false, err
	}
	if settings.QueryRetentionDays < 1 || settings.QueryRetentionDays > 365 {
		return Settings{}, false, errors.New("query retention days must be within 1..365")
	}
	if settings.CacheTTL < 0 || settings.CacheTTL > 604800 {
		return Settings{}, false, errors.New("cache TTL must be within 0..604800")
	}
	if settings.NegativeCacheTTL < 1 || settings.NegativeCacheTTL > 86400 {
		return Settings{}, false, errors.New("negative cache TTL must be within 1..86400")
	}
	if settings.DatabaseMaxSizeGiB < 1 || settings.DatabaseMaxSizeGiB > 128 {
		return Settings{}, false, errors.New("database max size must be within 1..128 GiB")
	}
	if !validAddressFamilyMode(settings.AddressFamilyMode) {
		return Settings{}, false, errors.New("invalid address family mode")
	}
	return settings, hasDefault, nil
}
func (s *Service) SyncSettings(ctx context.Context) error {
	settings, hasPersistedDefault, err := s.databaseSettings(ctx)
	if err != nil {
		return err
	}
	registry, err := s.mosdns.RegistryStatus(ctx)
	if err != nil {
		return err
	}
	desired := cloneRegistry(registry)
	desired.Cache = registryCache(settings)
	defaultGroupID := registry.DefaultGroupID
	if hasPersistedDefault {
		defaultGroupID = settings.DefaultUpstreamGroupID
	}
	if !enabledGroup(registry, defaultGroupID) {
		return errors.New("default upstream group must exist and be enabled")
	}
	desired.DefaultGroupID = defaultGroupID
	applied, err := s.applyRegistry(ctx, registry, desired)
	if err != nil {
		return err
	}
	settings.DefaultUpstreamGroupID = applied.DefaultGroupID
	settings.CacheEnabled = applied.Cache.Enabled
	settings.CacheTTL = applied.Cache.LazyTTL
	settings.NegativeCacheEnabled = applied.Cache.Negative.Enabled
	settings.NegativeCacheTTL = int(applied.Cache.Negative.TTL)
	if s.ingest != nil {
		if err := s.ingest.SetRetentionDays(settings.QueryRetentionDays); err != nil {
			return err
		}
		if err := s.ingest.SetDatabaseMaxGiB(settings.DatabaseMaxSizeGiB); err != nil {
			return err
		}
	}
	changed, err := s.applyAddressFamilyMode(ctx, settings.AddressFamilyMode)
	if err != nil {
		return err
	}
	if changed {
		if err := s.mosdns.FlushRegistry(ctx, "", applied.Version); err != nil {
			return err
		}
	}
	return s.persistSettings(ctx, settings, 0, "", "", "success", "", false)
}
func (s *Service) UpdateSettings(ctx context.Context, settings Settings, adminID int64, requestID, clientIP string) error {
	if settings.AddressFamilyMode == "" {
		settings.AddressFamilyMode = "dual_stack"
	}
	if settings.QueryRetentionDays < 1 || settings.QueryRetentionDays > 365 {
		return errors.New("query retention days must be within 1..365")
	}
	if settings.CacheTTL < 0 || settings.CacheTTL > 604800 {
		return errors.New("cache TTL must be within 0..604800")
	}
	if settings.NegativeCacheTTL < 1 || settings.NegativeCacheTTL > 86400 {
		return errors.New("negative cache TTL must be within 1..86400")
	}
	if settings.DatabaseMaxSizeGiB < 1 || settings.DatabaseMaxSizeGiB > 128 {
		return errors.New("database max size must be within 1..128 GiB")
	}
	if !validAddressFamilyMode(settings.AddressFamilyMode) {
		return errors.New("invalid address family mode")
	}
	currentRegistry, err := s.mosdns.RegistryStatus(ctx)
	if err != nil {
		return err
	}
	if settings.UpstreamRegistryVersion != currentRegistry.Version {
		return mosdnsclient.ErrConflict
	}
	currentSettings, _, err := s.databaseSettings(ctx)
	if err != nil {
		return err
	}
	if !enabledGroup(currentRegistry, settings.DefaultUpstreamGroupID) {
		return errors.New("default upstream group must exist and be enabled")
	}
	desired := cloneRegistry(currentRegistry)
	desired.DefaultGroupID = settings.DefaultUpstreamGroupID
	desired.Cache = registryCache(settings)
	desired.Protection = registryProtection(settings)
	applied, err := s.applyRegistry(ctx, currentRegistry, desired)
	if err != nil {
		return err
	}
	settings.DefaultUpstreamGroupID = applied.DefaultGroupID
	settings.CacheEnabled, settings.CacheTTL = applied.Cache.Enabled, applied.Cache.LazyTTL
	settings.NegativeCacheEnabled, settings.NegativeCacheTTL = applied.Cache.Negative.Enabled, int(applied.Cache.Negative.TTL)
	settings.GlobalMaxInFlight = applied.Protection.GlobalMaxInFlight
	settings.DefaultGroupMaxInFlight = applied.Protection.DefaultGroupMaxInFlight
	settings.DefaultGroupQueryTimeoutMS = applied.Protection.DefaultGroupQueryTimeoutMS
	settings.OverloadAction = applied.Protection.OverloadAction
	settings.UpstreamRegistryVersion = applied.Version
	var partialErr error
	if s.ingest != nil {
		if err := s.ingest.SetRetentionDays(settings.QueryRetentionDays); err != nil {
			partialErr = errors.Join(partialErr, err)
		}
		if err := s.ingest.SetDatabaseMaxGiB(settings.DatabaseMaxSizeGiB); err != nil {
			partialErr = errors.Join(partialErr, err)
		}
		if currentSettings.QueryRetentionDays != settings.QueryRetentionDays || currentSettings.DatabaseMaxSizeGiB != settings.DatabaseMaxSizeGiB {
			if err := s.ingest.RetainNow(); err != nil {
				partialErr = errors.Join(partialErr, err)
			}
		}
	}
	if currentSettings.AddressFamilyMode != settings.AddressFamilyMode {
		changed, addressErr := s.applyAddressFamilyMode(ctx, settings.AddressFamilyMode)
		partialErr = errors.Join(partialErr, addressErr)
		if addressErr != nil {
			settings.AddressFamilyMode = currentSettings.AddressFamilyMode
			if actual, statusErr := s.mosdns.AddressFamilyStatus(ctx); statusErr == nil && validAddressFamilyMode(actual.Mode) {
				settings.AddressFamilyMode = actual.Mode
			} else {
				partialErr = errors.Join(partialErr, statusErr)
			}
		}
		if changed {
			if err := s.mosdns.FlushRegistry(ctx, "", applied.Version); err != nil {
				partialErr = errors.Join(partialErr, err)
			}
		}
	}
	result, code := "success", ""
	if partialErr != nil {
		result, code = "failed", "SETTINGS_PARTIAL_FAILURE"
	}
	if err := s.persistSettings(ctx, settings, adminID, requestID, clientIP, result, code, true); err != nil {
		return errors.Join(partialErr, err)
	}
	return partialErr
}

func (s *Service) persistSettings(ctx context.Context, settings Settings, adminID int64, requestID, clientIP, result, code string, audit bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	for _, item := range []struct{ key, value string }{{"cache_enabled", strconv.FormatBool(settings.CacheEnabled)}, {"cache_ttl", strconv.Itoa(settings.CacheTTL)}, {"negative_cache_enabled", strconv.FormatBool(settings.NegativeCacheEnabled)}, {"negative_cache_ttl", strconv.Itoa(settings.NegativeCacheTTL)}, {"query_retention_days", strconv.Itoa(settings.QueryRetentionDays)}, {"database_max_size_gib", strconv.Itoa(settings.DatabaseMaxSizeGiB)}, {"address_family_mode", settings.AddressFamilyMode}, {"default_upstream_group_id", settings.DefaultUpstreamGroupID}} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO system_state(key,value_json,updated_at_ms) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json,updated_at_ms=excluded.updated_at_ms`, item.key, item.value, now); err != nil {
			return err
		}
	}
	if audit {
		if err := s.auditTx(ctx, tx, adminID, "update", "settings", "default_upstream_group,cache,negative_cache,query_retention,database_max_size,address_family", requestID, clientIP, result, code); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func validAddressFamilyMode(mode string) bool {
	switch mode {
	case "dual_stack", "prefer_ipv4", "prefer_ipv6", "ipv4_only", "ipv6_only":
		return true
	default:
		return false
	}
}

func (s *Service) applyAddressFamilyMode(ctx context.Context, mode string) (bool, error) {
	current, err := s.mosdns.AddressFamilyStatus(ctx)
	if err != nil {
		return false, err
	}
	if current.Mode == mode {
		return false, nil
	}
	requested := mosdnsclient.AddressFamilySnapshot{Version: current.Version + 1, ExpectedCurrentVersion: current.Version, Mode: mode}
	_, err = s.mosdns.ApplyAddressFamily(ctx, requested)
	if errors.Is(err, mosdnsclient.ErrUnknown) {
		current, statusErr := s.mosdns.AddressFamilyStatus(ctx)
		if statusErr == nil && current.Mode == mode {
			return true, nil
		}
	}
	return err == nil, err
}

func registryCache(settings Settings) mosdnsclient.RegistryCacheConfig {
	return mosdnsclient.RegistryCacheConfig{Enabled: settings.CacheEnabled, LazyTTL: settings.CacheTTL, Negative: mosdnsclient.NegativeCacheConfig{Enabled: settings.NegativeCacheEnabled, TTL: uint32(settings.NegativeCacheTTL)}}
}

func registryProtection(settings Settings) mosdnsclient.ProtectionConfig {
	return mosdnsclient.ProtectionConfig{
		GlobalMaxInFlight:          settings.GlobalMaxInFlight,
		DefaultGroupMaxInFlight:    settings.DefaultGroupMaxInFlight,
		DefaultGroupQueryTimeoutMS: settings.DefaultGroupQueryTimeoutMS,
		OverloadAction:             settings.OverloadAction,
	}
}

func cloneRegistry(snapshot mosdnsclient.RegistrySnapshot) mosdnsclient.RegistrySnapshot {
	clone := snapshot
	clone.Groups = append([]mosdnsclient.UpstreamGroup(nil), snapshot.Groups...)
	for i := range clone.Groups {
		clone.Groups[i].Upstreams = append([]mosdnsclient.Upstream(nil), snapshot.Groups[i].Upstreams...)
	}
	return clone
}

func registryEqual(a, b mosdnsclient.RegistrySnapshot) bool {
	a.ExpectedCurrentVersion, b.ExpectedCurrentVersion = 0, 0
	return reflect.DeepEqual(a, b)
}

func (s *Service) applyRegistry(ctx context.Context, current, desired mosdnsclient.RegistrySnapshot) (mosdnsclient.RegistrySnapshot, error) {
	if registryEqual(current, desired) {
		return current, nil
	}
	desired.Version = current.Version + 1
	desired.ExpectedCurrentVersion = current.Version
	applied, err := s.mosdns.ApplyRegistry(ctx, desired)
	if errors.Is(err, mosdnsclient.ErrUnknown) {
		actual, statusErr := s.mosdns.RegistryStatus(ctx)
		if statusErr == nil && validAppliedRegistry(actual, desired.Version) {
			return actual, nil
		}
		return mosdnsclient.RegistrySnapshot{}, fmt.Errorf("registry update outcome is unknown and could not be reconciled: %w", err)
	}
	return applied, err
}

func validAppliedRegistry(snapshot mosdnsclient.RegistrySnapshot, version uint64) bool {
	if (snapshot.SchemaVersion != 1 && snapshot.SchemaVersion != 2) || snapshot.Version != version || snapshot.ExpectedCurrentVersion != 0 || snapshot.DefaultGroupID == "" || len(snapshot.Groups) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(snapshot.Groups))
	for _, group := range snapshot.Groups {
		if group.ID == "" {
			return false
		}
		if _, exists := seen[group.ID]; exists {
			return false
		}
		seen[group.ID] = struct{}{}
		if group.ID == snapshot.DefaultGroupID {
			return group.Enabled
		}
	}
	return false
}

func enabledGroup(snapshot mosdnsclient.RegistrySnapshot, id string) bool {
	for _, group := range snapshot.Groups {
		if group.ID == id {
			return group.Enabled
		}
	}
	return false
}
func (s *Service) ClearQueryHistory(ctx context.Context, adminID int64, requestID, clientIP string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"dns_queries", "dns_stats_hourly_global", "dns_stats_hourly_domain", "dns_stats_hourly_client", "dns_stats_hourly_client_domain", "dns_stats_hourly_upstream_group", "dns_stats_hourly_latency_bucket"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return err
		}
	}
	if err := s.auditTx(ctx, tx, adminID, "clear", "query_history", "all", requestID, clientIP, "success", ""); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Service) UpstreamGroups(ctx context.Context) (mosdnsclient.RegistrySnapshot, error) {
	return s.mosdns.RegistryStatus(ctx)
}

func (s *Service) UpstreamRuntimeStatus(ctx context.Context) (mosdnsclient.RegistryRuntimeStatus, error) {
	return s.mosdns.RegistryRuntimeStatus(ctx)
}

func (s *Service) CreateUpstreamGroup(ctx context.Context, input UpstreamGroupWrite, adminID int64, requestID, clientIP string) (mosdnsclient.RegistrySnapshot, error) {
	current, err := s.mosdns.RegistryStatus(ctx)
	if err != nil {
		return mosdnsclient.RegistrySnapshot{}, err
	}
	if input.ExpectedCurrentVersion != current.Version {
		return mosdnsclient.RegistrySnapshot{}, mosdnsclient.ErrConflict
	}
	group := input.Group
	if len(current.Groups) >= 32 {
		return mosdnsclient.RegistrySnapshot{}, fmt.Errorf("%w: upstream group limit exceeded", ErrValidation)
	}
	if !groupIDPattern.MatchString(group.ID) {
		return mosdnsclient.RegistrySnapshot{}, fmt.Errorf("%w: invalid upstream group id", ErrValidation)
	}
	if _, exists := registryGroup(current, group.ID); exists {
		return mosdnsclient.RegistrySnapshot{}, fmt.Errorf("%w: upstream group id already exists", ErrValidation)
	}
	desired := cloneRegistry(current)
	desired.Groups = append(desired.Groups, group)
	return s.applyGroupChange(ctx, current, desired, "create", group.ID, adminID, requestID, clientIP)
}

func (s *Service) UpdateUpstreamGroup(ctx context.Context, id string, input UpstreamGroupWrite, adminID int64, requestID, clientIP string) (mosdnsclient.RegistrySnapshot, error) {
	s.bindings.Lock()
	defer s.bindings.Unlock()
	group := input.Group
	if group.ID != id {
		return mosdnsclient.RegistrySnapshot{}, fmt.Errorf("%w: upstream group id cannot be modified", ErrValidation)
	}
	current, err := s.mosdns.RegistryStatus(ctx)
	if err != nil {
		return mosdnsclient.RegistrySnapshot{}, err
	}
	if input.ExpectedCurrentVersion != current.Version {
		return mosdnsclient.RegistrySnapshot{}, mosdnsclient.ErrConflict
	}
	desired := cloneRegistry(current)
	index := groupIndex(desired, id)
	if index < 0 {
		return mosdnsclient.RegistrySnapshot{}, ErrNotFound
	}
	if id == current.DefaultGroupID && !group.Enabled {
		return mosdnsclient.RegistrySnapshot{}, ErrProtectedGroup
	}
	if desired.Groups[index].Enabled && !group.Enabled {
		referenced, err := s.upstreamGroupReferencedBySubscriptions(ctx, id)
		if err != nil {
			return mosdnsclient.RegistrySnapshot{}, err
		}
		if referenced {
			return mosdnsclient.RegistrySnapshot{}, ErrGroupReferenced
		}
	}
	desired.Groups[index] = group
	return s.applyGroupChange(ctx, current, desired, "update", id, adminID, requestID, clientIP)
}

func (s *Service) DeleteUpstreamGroup(ctx context.Context, id string, expectedCurrentVersion uint64, adminID int64, requestID, clientIP string) (mosdnsclient.RegistrySnapshot, error) {
	s.bindings.Lock()
	defer s.bindings.Unlock()
	current, err := s.mosdns.RegistryStatus(ctx)
	if err != nil {
		return mosdnsclient.RegistrySnapshot{}, err
	}
	if expectedCurrentVersion != current.Version {
		return mosdnsclient.RegistrySnapshot{}, mosdnsclient.ErrConflict
	}
	if id == current.DefaultGroupID {
		return mosdnsclient.RegistrySnapshot{}, ErrProtectedGroup
	}
	if _, exists := registryGroup(current, id); !exists {
		return mosdnsclient.RegistrySnapshot{}, ErrNotFound
	}
	referenced, err := s.upstreamGroupReferencedBySubscriptions(ctx, id)
	if err != nil {
		return mosdnsclient.RegistrySnapshot{}, err
	}
	if referenced {
		return mosdnsclient.RegistrySnapshot{}, ErrGroupReferenced
	}
	desired := cloneRegistry(current)
	index := groupIndex(desired, id)
	desired.Groups = append(desired.Groups[:index], desired.Groups[index+1:]...)
	return s.applyGroupChange(ctx, current, desired, "delete", id, adminID, requestID, clientIP)
}

func (s *Service) upstreamGroupReferencedBySubscriptions(ctx context.Context, id string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM subscription_bindings WHERE upstream_group_id=?)+(SELECT COUNT(*) FROM domain_rules WHERE upstream_group_id=?)`, id, id).Scan(&count)
	if err != nil || count > 0 {
		return count > 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT snapshot_json FROM rule_versions WHERE status IN ('active','pending','unknown') AND schema_version=4`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var snapshot mosdnsclient.Snapshot
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return false, err
		}
		for _, rule := range snapshot.Rules {
			if rule.UpstreamGroupID == id {
				return true, nil
			}
		}
		for _, set := range snapshot.SubscriptionSets {
			if set.UpstreamGroupID == id {
				return true, nil
			}
		}
	}
	return false, rows.Err()
}

func (s *Service) FlushUpstreamGroup(ctx context.Context, id string, expectedCurrentVersion uint64, adminID int64, requestID, clientIP string) error {
	current, err := s.mosdns.RegistryStatus(ctx)
	if err == nil {
		if expectedCurrentVersion != current.Version {
			err = mosdnsclient.ErrConflict
		} else if _, exists := registryGroup(current, id); !exists {
			err = ErrNotFound
		} else {
			err = s.mosdns.FlushRegistry(ctx, id, expectedCurrentVersion)
		}
	}
	result, code := "success", ""
	if err != nil {
		result, code = "failed", "CACHE_FLUSH_FAILED"
	}
	return errors.Join(err, s.Audit(ctx, adminID, "flush", "upstream_group_cache", id, requestID, clientIP, result, code))
}

func (s *Service) applyGroupChange(ctx context.Context, current, desired mosdnsclient.RegistrySnapshot, action, id string, adminID int64, requestID, clientIP string) (mosdnsclient.RegistrySnapshot, error) {
	updated, err := s.applyRegistry(ctx, current, desired)
	result, code := "success", ""
	if err != nil {
		result, code = "failed", "UPSTREAM_GROUP_APPLY_FAILED"
	}
	if auditErr := s.Audit(ctx, adminID, action, "upstream_group", id, requestID, clientIP, result, code); auditErr != nil && err == nil {
		err = auditErr
	}
	return updated, err
}

func registryGroup(snapshot mosdnsclient.RegistrySnapshot, id string) (mosdnsclient.UpstreamGroup, bool) {
	index := groupIndex(snapshot, id)
	if index < 0 {
		return mosdnsclient.UpstreamGroup{}, false
	}
	return snapshot.Groups[index], true
}

func groupIndex(snapshot mosdnsclient.RegistrySnapshot, id string) int {
	for i := range snapshot.Groups {
		if snapshot.Groups[i].ID == id {
			return i
		}
	}
	return -1
}

func (s *Service) DatabaseStatus() DatabaseStatus {
	return DatabaseStatus{Bytes: fileSize(s.dbPath), WALBytes: fileSize(s.dbPath + "-wal")}
}
func (s *Service) SystemStatus(ctx context.Context) SystemStatus {
	result := SystemStatus{Database: s.DatabaseStatus(), ControllerMemoryRSSBytes: processRSSBytes()}
	if status, err := s.mosdns.Status(ctx); err != nil {
		result.MosdnsError = "mosdns 运行时不可用"
	} else {
		result.Mosdns = &status
	}
	if status, err := s.mosdns.AuditStatus(ctx); err != nil {
		result.AuditError = "查询审计运行时不可用"
	} else {
		result.Audit = &status
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key,value_json FROM system_state WHERE key IN ('last_successful_ingest_at','last_retention_at')`)
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if rows.Scan(&key, &value) != nil {
			continue
		}
		if key == "last_successful_ingest_at" {
			result.LastSuccessfulIngest = value
		} else {
			result.LastRetention = value
		}
	}
	return result
}
func fileSize(path string) int64 {
	if path == "" || path == ":memory:" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
func (s *Service) Audit(ctx context.Context, adminID int64, action, resourceType, resourceID, requestID, clientIP, result, errorCode string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO admin_audit_logs(admin_id,action,resource_type,resource_id,request_id,client_ip,result,error_code,created_at_ms) VALUES(?,?,?,?,?,?,?,?,?)`, adminID, action, resourceType, resourceID, requestID, clientIP, result, errorCode, time.Now().UnixMilli())
	return err
}
func (s *Service) auditTx(ctx context.Context, tx *sql.Tx, adminID int64, action, resourceType, resourceID, requestID, clientIP, result, errorCode string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO admin_audit_logs(admin_id,action,resource_type,resource_id,request_id,client_ip,result,error_code,created_at_ms) VALUES(?,?,?,?,?,?,?,?,?)`, adminID, action, resourceType, resourceID, requestID, clientIP, result, errorCode, time.Now().UnixMilli())
	return err
}
