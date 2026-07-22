// Package operations 承载设备、系统和管理员操作等非规则控制面能力。
package operations

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/managed-dns/controller/internal/mosdnsclient"
	"github.com/managed-dns/controller/internal/queryingest"
)

var ErrInvalidCursor = errors.New("invalid cursor")

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
type Upstreams struct {
	Local     mosdnsclient.UpstreamSnapshot `json:"local"`
	Remote    mosdnsclient.UpstreamSnapshot `json:"remote"`
	LocalECS  mosdnsclient.ECSSnapshot      `json:"local_ecs"`
	RemoteECS mosdnsclient.ECSSnapshot      `json:"remote_ecs"`
}
type Settings struct {
	CacheEnabled       bool   `json:"cache_enabled"`
	CacheTTL           int    `json:"cache_ttl"`
	QueryRetentionDays int    `json:"query_retention_days"`
	DatabaseMaxSizeGiB int    `json:"database_max_size_gib"`
	AddressFamilyMode  string `json:"address_family_mode"`
}
type Service struct {
	db     *sql.DB
	dbPath string
	mosdns mosdnsclient.Client
	ingest *queryingest.Service
}

func New(db *sql.DB, dbPath string, mosdns mosdnsclient.Client, ingest *queryingest.Service) *Service {
	return &Service{db: db, dbPath: dbPath, mosdns: mosdns, ingest: ingest}
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
	// 两个独立 cache 都必须请求 flush；即使前一个失败，仍尝试另一个避免保留旧路由结果。
	localErr := s.mosdns.Flush(ctx, "cache_local")
	remoteErr := s.mosdns.Flush(ctx, "cache_remote")
	result, code := "success", ""
	if localErr != nil || remoteErr != nil {
		result = "failed"
		code = "CACHE_FLUSH_FAILED"
	}
	if err := s.Audit(ctx, adminID, "flush", "cache", "local,remote", requestID, clientIP, result, code); err != nil {
		return err
	}
	return errors.Join(localErr, remoteErr)
}
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	settings := Settings{CacheEnabled: true, QueryRetentionDays: 7, DatabaseMaxSizeGiB: 2, AddressFamilyMode: "dual_stack"}
	rows, err := s.db.QueryContext(ctx, `SELECT key,value_json FROM system_state WHERE key IN ('cache_enabled','cache_ttl','query_retention_days','database_max_size_gib','address_family_mode')`)
	if err != nil {
		return Settings{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return Settings{}, err
		}
		switch key {
		case "cache_enabled":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return Settings{}, err
			}
			settings.CacheEnabled = parsed
		case "query_retention_days":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return Settings{}, err
			}
			settings.QueryRetentionDays = parsed
		case "cache_ttl":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return Settings{}, err
			}
			settings.CacheTTL = parsed
		case "database_max_size_gib":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return Settings{}, err
			}
			settings.DatabaseMaxSizeGiB = parsed
		case "address_family_mode":
			settings.AddressFamilyMode = value
		}
	}
	if err := rows.Err(); err != nil {
		return Settings{}, err
	}
	if settings.QueryRetentionDays < 1 || settings.QueryRetentionDays > 365 {
		return Settings{}, errors.New("query retention days must be within 1..365")
	}
	if settings.CacheTTL < 0 || settings.CacheTTL > 604800 {
		return Settings{}, errors.New("cache TTL must be within 0..604800")
	}
	if settings.DatabaseMaxSizeGiB < 1 || settings.DatabaseMaxSizeGiB > 128 {
		return Settings{}, errors.New("database max size must be within 1..128 GiB")
	}
	if !validAddressFamilyMode(settings.AddressFamilyMode) {
		return Settings{}, errors.New("invalid address family mode")
	}
	return settings, nil
}
func (s *Service) SyncSettings(ctx context.Context) error {
	settings, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	if s.ingest != nil {
		if err := s.ingest.SetRetentionDays(settings.QueryRetentionDays); err != nil {
			return err
		}
		if err := s.ingest.SetDatabaseMaxGiB(settings.DatabaseMaxSizeGiB); err != nil {
			return err
		}
	}
	if err := s.mosdns.SetCacheEnabled(ctx, settings.CacheEnabled); err != nil {
		return err
	}
	if err := s.mosdns.SetCacheTTL(ctx, settings.CacheTTL); err != nil {
		return err
	}
	changed, err := s.applyAddressFamilyMode(ctx, settings.AddressFamilyMode)
	if err != nil {
		return err
	}
	if changed {
		return s.flushBothCaches(ctx)
	}
	return nil
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
	if settings.DatabaseMaxSizeGiB < 1 || settings.DatabaseMaxSizeGiB > 128 {
		return errors.New("database max size must be within 1..128 GiB")
	}
	if !validAddressFamilyMode(settings.AddressFamilyMode) {
		return errors.New("invalid address family mode")
	}
	current, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	if current.CacheEnabled != settings.CacheEnabled {
		if err := s.mosdns.SetCacheEnabled(ctx, settings.CacheEnabled); err != nil {
			return err
		}
	}
	if current.CacheTTL != settings.CacheTTL {
		if err := s.mosdns.SetCacheTTL(ctx, settings.CacheTTL); err != nil {
			return err
		}
	}
	if s.ingest != nil {
		if err := s.ingest.SetRetentionDays(settings.QueryRetentionDays); err != nil {
			return err
		}
		if err := s.ingest.SetDatabaseMaxGiB(settings.DatabaseMaxSizeGiB); err != nil {
			return err
		}
		if current.QueryRetentionDays != settings.QueryRetentionDays || current.DatabaseMaxSizeGiB != settings.DatabaseMaxSizeGiB {
			if err := s.ingest.RetainNow(); err != nil {
				return err
			}
		}
	}
	var addressFamilyErr error
	if current.AddressFamilyMode != settings.AddressFamilyMode {
		changed, err := s.applyAddressFamilyMode(ctx, settings.AddressFamilyMode)
		if err != nil {
			return err
		}
		if changed {
			if err := s.flushBothCaches(ctx); err != nil {
				// The policy snapshot is already durable in mosdns. Persist the
				// controller view before reporting the incomplete cache flush.
				addressFamilyErr = err
			}
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	for _, item := range []struct{ key, value string }{{"cache_enabled", strconv.FormatBool(settings.CacheEnabled)}, {"cache_ttl", strconv.Itoa(settings.CacheTTL)}, {"query_retention_days", strconv.Itoa(settings.QueryRetentionDays)}, {"database_max_size_gib", strconv.Itoa(settings.DatabaseMaxSizeGiB)}, {"address_family_mode", settings.AddressFamilyMode}} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO system_state(key,value_json,updated_at_ms) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json,updated_at_ms=excluded.updated_at_ms`, item.key, item.value, now); err != nil {
			return err
		}
	}
	result, code := "success", ""
	if addressFamilyErr != nil {
		result, code = "failed", "CACHE_FLUSH_FAILED"
	}
	if err := s.auditTx(ctx, tx, adminID, "update", "settings", "cache,query_retention,database_max_size,address_family", requestID, clientIP, result, code); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return addressFamilyErr
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

func (s *Service) flushBothCaches(ctx context.Context) error {
	return errors.Join(s.mosdns.Flush(ctx, "cache_local"), s.mosdns.Flush(ctx, "cache_remote"))
}
func (s *Service) ClearQueryHistory(ctx context.Context, adminID int64, requestID, clientIP string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"dns_queries", "dns_stats_hourly_global", "dns_stats_hourly_domain", "dns_stats_hourly_client", "dns_stats_hourly_client_domain", "dns_stats_hourly_latency_bucket"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return err
		}
	}
	if err := s.auditTx(ctx, tx, adminID, "clear", "query_history", "all", requestID, clientIP, "success", ""); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Service) Upstreams(ctx context.Context) (Upstreams, error) {
	local, err := s.mosdns.UpstreamStatus(ctx, "local_dns")
	if err != nil {
		return Upstreams{}, err
	}
	remote, err := s.mosdns.UpstreamStatus(ctx, "remote_dns")
	if err != nil {
		return Upstreams{}, err
	}
	localECS, err := s.mosdns.ECSStatus(ctx, "local_dns")
	if err != nil {
		return Upstreams{}, err
	}
	remoteECS, err := s.mosdns.ECSStatus(ctx, "remote_dns")
	if err != nil {
		return Upstreams{}, err
	}
	return Upstreams{Local: local, Remote: remote, LocalECS: localECS, RemoteECS: remoteECS}, nil
}
func (s *Service) UpdateECS(ctx context.Context, group string, snapshot mosdnsclient.ECSSnapshot, adminID int64, requestID, clientIP string) (mosdnsclient.ECSSnapshot, error) {
	if group != "local_dns" && group != "remote_dns" {
		return mosdnsclient.ECSSnapshot{}, errors.New("invalid upstream group")
	}
	updated, err := s.mosdns.ApplyECS(ctx, group, snapshot)
	if errors.Is(err, mosdnsclient.ErrUnknown) {
		current, statusErr := s.mosdns.ECSStatus(ctx, group)
		if statusErr == nil && current.Version == snapshot.Version {
			updated, err = current, nil
		}
	}
	result, code := "success", ""
	if err != nil {
		result, code = "failed", "ECS_APPLY_FAILED"
	} else {
		cache := "cache_remote"
		if group == "local_dns" {
			cache = "cache_local"
		}
		if flushErr := s.mosdns.Flush(ctx, cache); flushErr != nil {
			err, result, code = flushErr, "failed", "CACHE_FLUSH_FAILED"
		}
	}
	if auditErr := s.Audit(ctx, adminID, "update", "ecs", group, requestID, clientIP, result, code); auditErr != nil && err == nil {
		err = auditErr
	}
	return updated, err
}
func (s *Service) UpdateUpstream(ctx context.Context, group string, snapshot mosdnsclient.UpstreamSnapshot, adminID int64, requestID, clientIP string) (mosdnsclient.UpstreamSnapshot, error) {
	if group != "local_dns" && group != "remote_dns" {
		return mosdnsclient.UpstreamSnapshot{}, errors.New("invalid upstream group")
	}
	updated, err := s.mosdns.ApplyUpstream(ctx, group, snapshot)
	if errors.Is(err, mosdnsclient.ErrUnknown) {
		current, statusErr := s.mosdns.UpstreamStatus(ctx, group)
		if statusErr == nil && current.Version == snapshot.Version {
			updated, err = current, nil
		}
	}
	result, code := "success", ""
	if err != nil {
		result, code = "failed", "UPSTREAM_APPLY_FAILED"
	} else if flushErr := s.mosdns.Flush(ctx, map[string]string{"local_dns": "cache_local", "remote_dns": "cache_remote"}[group]); flushErr != nil {
		err, result, code = flushErr, "failed", "CACHE_FLUSH_FAILED"
	}
	if auditErr := s.Audit(ctx, adminID, "update", "upstream", group, requestID, clientIP, result, code); auditErr != nil && err == nil {
		err = auditErr
	}
	return updated, err
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
