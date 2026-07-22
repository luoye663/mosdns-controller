package operations

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/managed-dns/controller/internal/mosdnsclient"
	"github.com/managed-dns/controller/internal/storage"
)

type fakeMosdns struct {
	flushes       []string
	flushErr      map[string]error
	statusErr     error
	upstreams     map[string]mosdnsclient.UpstreamSnapshot
	cacheEnabled  bool
	cacheTTL      int
	ecs           map[string]mosdnsclient.ECSSnapshot
	addressFamily mosdnsclient.AddressFamilySnapshot
}

func (f *fakeMosdns) Status(context.Context) (mosdnsclient.Status, error) {
	if f.statusErr != nil {
		return mosdnsclient.Status{}, f.statusErr
	}
	return mosdnsclient.Status{State: "ready", SnapshotVersion: 3, Checksum: "sha256:test", MemoryRSSBytes: 12 * 1024 * 1024}, nil
}
func (f *fakeMosdns) Validate(context.Context, mosdnsclient.Snapshot) (mosdnsclient.ValidateResult, error) {
	return mosdnsclient.ValidateResult{}, nil
}
func (f *fakeMosdns) Apply(context.Context, mosdnsclient.Snapshot) (mosdnsclient.ApplyResult, error) {
	return mosdnsclient.ApplyResult{}, nil
}
func (f *fakeMosdns) Match(context.Context, string) (any, error) { return nil, nil }
func (f *fakeMosdns) Flush(_ context.Context, tag string) error {
	f.flushes = append(f.flushes, tag)
	return f.flushErr[tag]
}
func (f *fakeMosdns) SetCacheEnabled(_ context.Context, enabled bool) error {
	f.cacheEnabled = enabled
	return nil
}
func (f *fakeMosdns) SetCacheTTL(_ context.Context, ttl int) error { f.cacheTTL = ttl; return nil }
func (f *fakeMosdns) UpstreamStatus(_ context.Context, group string) (mosdnsclient.UpstreamSnapshot, error) {
	if f.upstreams == nil {
		f.upstreams = map[string]mosdnsclient.UpstreamSnapshot{}
	}
	return f.upstreams[group], nil
}
func (f *fakeMosdns) ApplyUpstream(_ context.Context, group string, snapshot mosdnsclient.UpstreamSnapshot) (mosdnsclient.UpstreamSnapshot, error) {
	if f.upstreams == nil {
		f.upstreams = map[string]mosdnsclient.UpstreamSnapshot{}
	}
	current := f.upstreams[group]
	if snapshot.ExpectedCurrentVersion != current.Version {
		return mosdnsclient.UpstreamSnapshot{}, mosdnsclient.ErrConflict
	}
	f.upstreams[group] = snapshot
	return snapshot, nil
}
func (f *fakeMosdns) ECSStatus(_ context.Context, group string) (mosdnsclient.ECSSnapshot, error) {
	if f.ecs == nil {
		f.ecs = map[string]mosdnsclient.ECSSnapshot{}
	}
	return f.ecs[group], nil
}
func (f *fakeMosdns) ApplyECS(_ context.Context, group string, snapshot mosdnsclient.ECSSnapshot) (mosdnsclient.ECSSnapshot, error) {
	if f.ecs == nil {
		f.ecs = map[string]mosdnsclient.ECSSnapshot{}
	}
	current := f.ecs[group]
	if snapshot.ExpectedCurrentVersion != current.Version {
		return mosdnsclient.ECSSnapshot{}, mosdnsclient.ErrConflict
	}
	f.ecs[group] = snapshot
	return snapshot, nil
}
func (f *fakeMosdns) AddressFamilyStatus(context.Context) (mosdnsclient.AddressFamilySnapshot, error) {
	if f.addressFamily.Version == 0 {
		f.addressFamily = mosdnsclient.AddressFamilySnapshot{Version: 1, Mode: "dual_stack"}
	}
	return f.addressFamily, nil
}
func (f *fakeMosdns) ApplyAddressFamily(_ context.Context, snapshot mosdnsclient.AddressFamilySnapshot) (mosdnsclient.AddressFamilySnapshot, error) {
	if snapshot.ExpectedCurrentVersion != f.addressFamily.Version {
		return mosdnsclient.AddressFamilySnapshot{}, mosdnsclient.ErrConflict
	}
	f.addressFamily = snapshot
	f.addressFamily.ExpectedCurrentVersion = 0
	return f.addressFamily, nil
}
func (f *fakeMosdns) AuditStatus(context.Context) (mosdnsclient.AuditStatus, error) {
	return mosdnsclient.AuditStatus{QueueCapacity: 65536}, nil
}
func (f *fakeMosdns) SubscriptionStatus(context.Context, string) (mosdnsclient.DomainSetStatus, error) {
	return mosdnsclient.DomainSetStatus{}, nil
}
func (f *fakeMosdns) ApplySubscription(_ context.Context, _ string, snapshot mosdnsclient.DomainSetSnapshot) (mosdnsclient.DomainSetStatus, error) {
	return mosdnsclient.DomainSetStatus{Version: snapshot.Version}, nil
}

func testService(t *testing.T, fake *fakeMosdns) *Service {
	t.Helper()
	store, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = store.DB().Exec(`INSERT INTO admins(id,username,password_hash,created_at_ms,updated_at_ms) VALUES(1,'admin','hash',1,1)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return New(store.DB(), ":memory:", fake, nil)
}

func TestDevicesUpdateAndAudit(t *testing.T) {
	s := testService(t, &fakeMosdns{})
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO devices(ip,note,source,first_seen_at_ms,last_seen_at_ms,updated_at_ms) VALUES(?,?,?,?,?,?)`, `192.0.2.5`, "", "observed", now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO dns_queries(event_id,timestamp_unix_ms,client_ip,qname,qtype,qclass,route,route_source,cache_hit,snapshot_version,answer_count,latency_us,created_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, `event-device`, now, `192.0.2.5`, `example.com`, 1, 1, "remote", "default", 0, 1, 0, 1, now); err != nil {
		t.Fatal(err)
	}
	devices, err := s.Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].QueryCount24H != 1 {
		t.Fatalf("devices=%+v", devices)
	}
	name, note := "办公电脑", "手工备注"
	updated, err := s.UpdateDevice(context.Background(), devices[0].ID, DevicePatch{DisplayName: &name, Note: &note}, 1, "req-1", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName != name || updated.Note != note || updated.QueryCount24H != 1 {
		t.Fatalf("updated=%+v", updated)
	}
	page, err := s.AuditLogs(context.Background(), AuditLogQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Action != "update" || page.Items[0].ResourceType != "device" {
		t.Fatalf("audit=%+v", page.Items)
	}
}

func TestFlushAttemptsBothCachesAndAuditsFailure(t *testing.T) {
	fake := &fakeMosdns{flushErr: map[string]error{"cache_local": errors.New("local unavailable")}}
	s := testService(t, fake)
	if err := s.FlushCaches(context.Background(), 1, "req-flush", "192.0.2.10"); err == nil {
		t.Fatal("expected flush error")
	}
	if len(fake.flushes) != 2 || fake.flushes[0] != "cache_local" || fake.flushes[1] != "cache_remote" {
		t.Fatalf("flushes=%v", fake.flushes)
	}
	page, err := s.AuditLogs(context.Background(), AuditLogQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Result != "failed" || page.Items[0].ErrorCode != "CACHE_FLUSH_FAILED" {
		t.Fatalf("audit=%+v", page.Items)
	}
}

func TestSystemStatusDegradesWhenMosdnsUnavailable(t *testing.T) {
	s := testService(t, &fakeMosdns{statusErr: errors.New("offline")})
	status := s.SystemStatus(context.Background())
	if status.Mosdns != nil || status.MosdnsError == "" || status.ControllerMemoryRSSBytes <= 0 {
		t.Fatalf("status=%+v", status)
	}
}

func TestSystemStatusReportsMosdnsRSS(t *testing.T) {
	s := testService(t, &fakeMosdns{})
	status := s.SystemStatus(context.Background())
	if status.Mosdns == nil || status.Mosdns.MemoryRSSBytes <= 0 || status.ControllerMemoryRSSBytes <= 0 {
		t.Fatalf("status=%+v", status)
	}
}

func TestUpdateUpstreamFlushesTheAffectedCacheAndAudits(t *testing.T) {
	fake := &fakeMosdns{upstreams: map[string]mosdnsclient.UpstreamSnapshot{"local_dns": {Version: 1}}}
	s := testService(t, fake)
	updated, err := s.UpdateUpstream(context.Background(), "local_dns", mosdnsclient.UpstreamSnapshot{Version: 2, ExpectedCurrentVersion: 1, Concurrent: 1, Upstreams: []mosdnsclient.Upstream{{Tag: "test", Addr: "https://dns.example/dns-query"}}}, 1, "req-upstream", "192.0.2.10")
	if err != nil || updated.Version != 2 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if len(fake.flushes) != 1 || fake.flushes[0] != "cache_local" {
		t.Fatalf("flushes=%v", fake.flushes)
	}
	page, err := s.AuditLogs(context.Background(), AuditLogQuery{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ResourceType != "upstream" {
		t.Fatalf("logs=%+v err=%v", page.Items, err)
	}
}

func TestUpdateSettingsPersistsAndAppliesCache(t *testing.T) {
	fake := &fakeMosdns{}
	s := testService(t, fake)
	if err := s.UpdateSettings(context.Background(), Settings{CacheEnabled: false, CacheTTL: 60, QueryRetentionDays: 3, DatabaseMaxSizeGiB: 4, AddressFamilyMode: "ipv4_only"}, 1, "req-settings", "192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	settings, err := s.Settings(context.Background())
	if err != nil || settings.CacheEnabled || settings.CacheTTL != 60 || settings.QueryRetentionDays != 3 || settings.DatabaseMaxSizeGiB != 4 || settings.AddressFamilyMode != "ipv4_only" || fake.cacheEnabled || fake.cacheTTL != 60 || fake.addressFamily.Mode != "ipv4_only" {
		t.Fatalf("settings=%+v cache=%t err=%v", settings, fake.cacheEnabled, err)
	}
	if len(fake.flushes) != 2 || fake.flushes[0] != "cache_local" || fake.flushes[1] != "cache_remote" {
		t.Fatalf("flushes=%v", fake.flushes)
	}
	if err := s.UpdateSettings(context.Background(), Settings{CacheEnabled: false, CacheTTL: 60, QueryRetentionDays: 3, DatabaseMaxSizeGiB: 0, AddressFamilyMode: "ipv4_only"}, 1, "req-settings", "192.0.2.10"); err == nil {
		t.Fatal("invalid database size accepted")
	}
}

func TestUpdateSettingsPersistsAddressFamilyWhenCacheFlushFails(t *testing.T) {
	fake := &fakeMosdns{flushErr: map[string]error{"cache_local": errors.New("cache unavailable")}}
	s := testService(t, fake)
	settings := Settings{CacheEnabled: true, CacheTTL: 0, QueryRetentionDays: 7, DatabaseMaxSizeGiB: 2, AddressFamilyMode: "ipv6_only"}
	if err := s.UpdateSettings(context.Background(), settings, 1, "req-settings", "192.0.2.10"); err == nil {
		t.Fatal("expected cache flush error")
	}
	persisted, err := s.Settings(context.Background())
	if err != nil || persisted.AddressFamilyMode != "ipv6_only" || fake.addressFamily.Mode != "ipv6_only" {
		t.Fatalf("persisted=%+v runtime=%+v err=%v", persisted, fake.addressFamily, err)
	}
	page, err := s.AuditLogs(context.Background(), AuditLogQuery{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Result != "failed" || page.Items[0].ErrorCode != "CACHE_FLUSH_FAILED" {
		t.Fatalf("audit=%+v err=%v", page.Items, err)
	}
}

func TestAuditLogsCursorHasNoDuplicatesAtEqualTimestamps(t *testing.T) {
	s := testService(t, &fakeMosdns{})
	for i := 1; i <= 3; i++ {
		if err := s.Audit(context.Background(), 1, "test", "audit", strconv.Itoa(i), "request", "192.0.2.10", "success", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`UPDATE admin_audit_logs SET created_at_ms=1000`); err != nil {
		t.Fatal(err)
	}
	first, err := s.AuditLogs(context.Background(), AuditLogQuery{Limit: 2})
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := s.AuditLogs(context.Background(), AuditLogQuery{Limit: 2, Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID || second.Items[0].ID == first.Items[1].ID {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if _, err := s.AuditLogs(context.Background(), AuditLogQuery{Cursor: "bad"}); err == nil {
		t.Fatal("invalid cursor accepted")
	}
}

func TestClearQueryHistoryLeavesAdministrativeAudit(t *testing.T) {
	s := testService(t, &fakeMosdns{})
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO dns_queries(event_id,timestamp_unix_ms,client_ip,qname,qtype,qclass,route,route_source,cache_hit,snapshot_version,answer_count,latency_us,created_at_ms) VALUES('event-clear',?,'192.0.2.5','example.com',1,1,'remote','default',0,1,0,1,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO dns_stats_hourly_global(hour_start_ms,route,qtype,rcode,query_count,error_count,cache_hit_count,latency_sum_us,latency_max_us) VALUES(?, 'remote', 1, 0, 1, 0, 0, 1, 1)`, now); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearQueryHistory(context.Background(), 1, "request-clear", "192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	var raw, aggregate, audit int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM dns_queries`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM admin_audit_logs`).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM dns_stats_hourly_global`).Scan(&aggregate); err != nil {
		t.Fatal(err)
	}
	if raw != 0 || aggregate != 0 || audit != 1 {
		t.Fatalf("raw=%d aggregate=%d audit=%d", raw, aggregate, audit)
	}
}
