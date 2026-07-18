// Package operations 承载设备、系统和管理员操作等非规则控制面能力。
package operations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/managed-dns/controller/internal/mosdnsclient"
	"github.com/managed-dns/controller/internal/queryingest"
)

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
type DatabaseStatus struct {
	Bytes    int64 `json:"bytes"`
	WALBytes int64 `json:"wal_bytes"`
}
type SystemStatus struct {
	Database             DatabaseStatus            `json:"database"`
	Mosdns               *mosdnsclient.Status      `json:"mosdns,omitempty"`
	MosdnsError          string                    `json:"mosdns_error,omitempty"`
	LastSuccessfulIngest string                    `json:"last_successful_ingest_at,omitempty"`
	LastRetention        string                    `json:"last_retention_at,omitempty"`
	Audit                *mosdnsclient.AuditStatus `json:"audit,omitempty"`
	AuditError           string                    `json:"audit_error,omitempty"`
}
type Upstreams struct {
	Local     mosdnsclient.UpstreamSnapshot `json:"local"`
	Remote    mosdnsclient.UpstreamSnapshot `json:"remote"`
	LocalECS  mosdnsclient.ECSSnapshot      `json:"local_ecs"`
	RemoteECS mosdnsclient.ECSSnapshot      `json:"remote_ecs"`
}
type Settings struct {
	CacheEnabled       bool `json:"cache_enabled"`
	CacheTTL           int  `json:"cache_ttl"`
	QueryRetentionDays int  `json:"query_retention_days"`
}
type GeositeStatus struct {
	SourceURL string `json:"source_url"`
	mosdnsclient.DomainSetStatus
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

func (s *Service) AuditLogs(ctx context.Context, limit int) ([]AuditLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT l.id,l.admin_id,COALESCE(a.username,''),l.action,l.resource_type,COALESCE(l.resource_id,''),l.request_id,COALESCE(l.client_ip,''),l.result,COALESCE(l.error_code,''),l.created_at_ms FROM admin_audit_logs l LEFT JOIN admins a ON a.id=l.admin_id ORDER BY l.created_at_ms DESC,l.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditLog{}
	for rows.Next() {
		var item AuditLog
		var admin sql.NullInt64
		if err := rows.Scan(&item.ID, &admin, &item.AdminUsername, &item.Action, &item.ResourceType, &item.ResourceID, &item.RequestID, &item.ClientIP, &item.Result, &item.ErrorCode, &item.CreatedAtMS); err != nil {
			return nil, err
		}
		if admin.Valid {
			value := admin.Int64
			item.AdminID = &value
		}
		out = append(out, item)
	}
	return out, rows.Err()
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
	settings := Settings{CacheEnabled: true, QueryRetentionDays: 7}
	rows, err := s.db.QueryContext(ctx, `SELECT key,value_json FROM system_state WHERE key IN ('cache_enabled','cache_ttl','query_retention_days')`)
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
	}
	if err := s.mosdns.SetCacheEnabled(ctx, settings.CacheEnabled); err != nil {
		return err
	}
	return s.mosdns.SetCacheTTL(ctx, settings.CacheTTL)
}
func (s *Service) UpdateSettings(ctx context.Context, settings Settings, adminID int64, requestID, clientIP string) error {
	if settings.QueryRetentionDays < 1 || settings.QueryRetentionDays > 365 {
		return errors.New("query retention days must be within 1..365")
	}
	if settings.CacheTTL < 0 || settings.CacheTTL > 604800 {
		return errors.New("cache TTL must be within 0..604800")
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
		if current.QueryRetentionDays != settings.QueryRetentionDays {
			s.ingest.RetainNow()
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	for _, item := range []struct{ key, value string }{{"cache_enabled", strconv.FormatBool(settings.CacheEnabled)}, {"cache_ttl", strconv.Itoa(settings.CacheTTL)}, {"query_retention_days", strconv.Itoa(settings.QueryRetentionDays)}} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO system_state(key,value_json,updated_at_ms) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json,updated_at_ms=excluded.updated_at_ms`, item.key, item.value, now); err != nil {
			return err
		}
	}
	if err := s.auditTx(ctx, tx, adminID, "update", "settings", "cache,query_retention", requestID, clientIP, "success", ""); err != nil {
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

func (s *Service) GeositeStatus(ctx context.Context) (GeositeStatus, error) {
	status, err := s.mosdns.GeositeStatus(ctx)
	if err != nil {
		return GeositeStatus{}, err
	}
	result := GeositeStatus{DomainSetStatus: status}
	_ = s.db.QueryRowContext(ctx, `SELECT value_json FROM system_state WHERE key='geosite_cn_source_url'`).Scan(&result.SourceURL)
	return result, nil
}

func (s *Service) UpdateGeosite(ctx context.Context, sourceURL string, adminID int64, requestID, clientIP string) (GeositeStatus, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(sourceURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return GeositeStatus{}, errors.New("geosite source_url must be an HTTPS URL")
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return errors.New("redirect must use HTTPS")
		}
		return nil
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return GeositeStatus{}, err
	}
	request.Header.Set("Accept", "text/plain")
	response, err := client.Do(request)
	if err != nil {
		return GeositeStatus{}, fmt.Errorf("download geosite: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return GeositeStatus{}, fmt.Errorf("download geosite: HTTP %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (20<<20)+1))
	if err != nil {
		return GeositeStatus{}, fmt.Errorf("read geosite: %w", err)
	}
	if len(body) == 0 || len(body) > 20<<20 {
		return GeositeStatus{}, errors.New("geosite file must contain 1..20971520 bytes")
	}
	return s.applyGeosite(ctx, body, parsed.String(), adminID, requestID, clientIP)
}

func (s *Service) UploadGeosite(ctx context.Context, content []byte, filename string, adminID int64, requestID, clientIP string) (GeositeStatus, error) {
	if !strings.HasSuffix(strings.ToLower(strings.TrimSpace(filename)), ".txt") {
		return GeositeStatus{}, errors.New("geosite upload must be a .txt file")
	}
	if len(content) == 0 || len(content) > 20<<20 {
		return GeositeStatus{}, errors.New("geosite file must contain 1..20971520 bytes")
	}
	return s.applyGeosite(ctx, content, "", adminID, requestID, clientIP)
}

func (s *Service) applyGeosite(ctx context.Context, body []byte, sourceURL string, adminID int64, requestID, clientIP string) (GeositeStatus, error) {
	runtime, err := s.mosdns.GeositeStatus(ctx)
	if err != nil {
		return GeositeStatus{}, err
	}
	updated, err := s.mosdns.ApplyGeosite(ctx, mosdnsclient.DomainSetSnapshot{Version: runtime.Version + 1, ExpectedCurrentVersion: runtime.Version, Rules: string(body)})
	result, code := "success", ""
	if err != nil {
		result, code = "failed", "GEOSITE_APPLY_FAILED"
	} else {
		_, err = s.db.ExecContext(ctx, `INSERT INTO system_state(key,value_json,updated_at_ms) VALUES('geosite_cn_source_url',?,?) ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json,updated_at_ms=excluded.updated_at_ms`, sourceURL, time.Now().UnixMilli())
		if err != nil {
			result, code = "failed", "GEOSITE_SOURCE_PERSIST_FAILED"
		}
	}
	if auditErr := s.Audit(ctx, adminID, "update", "geosite", "cn", requestID, clientIP, result, code); auditErr != nil && err == nil {
		err = auditErr
	}
	if err != nil {
		return GeositeStatus{}, err
	}
	return GeositeStatus{SourceURL: sourceURL, DomainSetStatus: updated}, nil
}

func (s *Service) DatabaseStatus() DatabaseStatus {
	return DatabaseStatus{Bytes: fileSize(s.dbPath), WALBytes: fileSize(s.dbPath + "-wal")}
}
func (s *Service) SystemStatus(ctx context.Context) SystemStatus {
	result := SystemStatus{Database: s.DatabaseStatus()}
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
