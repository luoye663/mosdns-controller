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
	flushes          []string
	flushErr         map[string]error
	statusErr        error
	upstreams        map[string]mosdnsclient.UpstreamSnapshot
	cacheEnabled     bool
	cacheTTL         int
	negativeCache    map[string]mosdnsclient.NegativeCacheSettings
	negativeCalls    []string
	negativeErr      map[string]error
	ecs              map[string]mosdnsclient.ECSSnapshot
	addressFamily    mosdnsclient.AddressFamilySnapshot
	registry         mosdnsclient.RegistrySnapshot
	registryApplyErr error
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
func (f *fakeMosdns) NegativeCache(_ context.Context, tag string) (mosdnsclient.NegativeCacheSettings, error) {
	return f.negativeCache[tag], nil
}
func (f *fakeMosdns) SetNegativeCache(_ context.Context, tag string, settings mosdnsclient.NegativeCacheSettings) (mosdnsclient.NegativeCacheSettings, error) {
	if f.negativeCache == nil {
		f.negativeCache = map[string]mosdnsclient.NegativeCacheSettings{}
	}
	f.negativeCalls = append(f.negativeCalls, tag)
	if err := f.negativeErr[tag]; err != nil {
		return mosdnsclient.NegativeCacheSettings{}, err
	}
	f.negativeCache[tag] = settings
	return settings, nil
}
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
func (f *fakeMosdns) RegistryStatus(context.Context) (mosdnsclient.RegistrySnapshot, error) {
	if f.registry.Version == 0 {
		f.registry = testRegistry()
	}
	return cloneRegistry(f.registry), nil
}
func (f *fakeMosdns) ApplyRegistry(_ context.Context, snapshot mosdnsclient.RegistrySnapshot) (mosdnsclient.RegistrySnapshot, error) {
	if f.registry.Version == 0 {
		f.registry = testRegistry()
	}
	if snapshot.ExpectedCurrentVersion != f.registry.Version {
		return mosdnsclient.RegistrySnapshot{}, mosdnsclient.ErrConflict
	}
	snapshot.ExpectedCurrentVersion = 0
	f.registry = cloneRegistry(snapshot)
	return cloneRegistry(snapshot), f.registryApplyErr
}
func (f *fakeMosdns) FlushRegistry(_ context.Context, group string, _ uint64) error {
	f.flushes = append(f.flushes, group)
	return f.flushErr[group]
}

func testRegistry() mosdnsclient.RegistrySnapshot {
	group := func(id, name string) mosdnsclient.UpstreamGroup {
		return mosdnsclient.UpstreamGroup{ID: id, Name: name, Enabled: true, Mode: "race", Concurrent: 1, Upstreams: []mosdnsclient.Upstream{{Tag: id, Addr: "https://dns.example/dns-query"}}, ECS: mosdnsclient.ECSConfig{Mode: "off", Mask4: 24, Mask6: 48}, Cache: mosdnsclient.GroupCacheConfig{Enabled: true, Size: 1024}}
	}
	return mosdnsclient.RegistrySnapshot{Version: 1, DefaultGroupID: "remote_dns", Groups: []mosdnsclient.UpstreamGroup{group("local_dns", "Local"), group("remote_dns", "Remote")}, Cache: mosdnsclient.RegistryCacheConfig{Enabled: true, Negative: mosdnsclient.NegativeCacheConfig{Enabled: true, TTL: 30}}}
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
	fake := &fakeMosdns{flushErr: map[string]error{"": errors.New("registry unavailable")}}
	s := testService(t, fake)
	if err := s.FlushCaches(context.Background(), 1, "req-flush", "192.0.2.10"); err == nil {
		t.Fatal("expected flush error")
	}
	if len(fake.flushes) != 1 || fake.flushes[0] != "" {
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
	if len(fake.flushes) != 1 || fake.flushes[0] != "local_dns" {
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
	if err := s.UpdateSettings(context.Background(), Settings{CacheEnabled: false, CacheTTL: 60, NegativeCacheEnabled: false, NegativeCacheTTL: 45, QueryRetentionDays: 3, DatabaseMaxSizeGiB: 4, AddressFamilyMode: "ipv4_only", DefaultUpstreamGroupID: "remote_dns", UpstreamRegistryVersion: 1}, 1, "req-settings", "192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	settings, err := s.Settings(context.Background())
	if err != nil || settings.CacheEnabled || settings.CacheTTL != 60 || settings.NegativeCacheEnabled || settings.NegativeCacheTTL != 45 || settings.QueryRetentionDays != 3 || settings.DatabaseMaxSizeGiB != 4 || settings.AddressFamilyMode != "ipv4_only" || settings.DefaultUpstreamGroupID != "remote_dns" || fake.addressFamily.Mode != "ipv4_only" {
		t.Fatalf("settings=%+v cache=%t err=%v", settings, fake.cacheEnabled, err)
	}
	if fake.registry.Cache.Negative.Enabled || fake.registry.Cache.Negative.TTL != 45 {
		t.Fatalf("negative cache=%+v", fake.registry.Cache.Negative)
	}
	if len(fake.flushes) != 1 || fake.flushes[0] != "" {
		t.Fatalf("flushes=%v", fake.flushes)
	}
	if err := s.UpdateSettings(context.Background(), Settings{CacheEnabled: false, CacheTTL: 60, NegativeCacheTTL: 45, QueryRetentionDays: 3, DatabaseMaxSizeGiB: 0, AddressFamilyMode: "ipv4_only", DefaultUpstreamGroupID: "remote_dns", UpstreamRegistryVersion: fake.registry.Version}, 1, "req-settings", "192.0.2.10"); err == nil {
		t.Fatal("invalid database size accepted")
	}
}

func TestUpdateSettingsPersistsAddressFamilyWhenCacheFlushFails(t *testing.T) {
	fake := &fakeMosdns{flushErr: map[string]error{"": errors.New("cache unavailable")}}
	s := testService(t, fake)
	settings := Settings{CacheEnabled: true, CacheTTL: 0, NegativeCacheEnabled: true, NegativeCacheTTL: 30, QueryRetentionDays: 7, DatabaseMaxSizeGiB: 2, AddressFamilyMode: "ipv6_only", DefaultUpstreamGroupID: "remote_dns", UpstreamRegistryVersion: 1}
	if err := s.UpdateSettings(context.Background(), settings, 1, "req-settings", "192.0.2.10"); err == nil {
		t.Fatal("expected cache flush error")
	}
	persisted, err := s.Settings(context.Background())
	if err != nil || persisted.AddressFamilyMode != "ipv6_only" || fake.addressFamily.Mode != "ipv6_only" {
		t.Fatalf("persisted=%+v runtime=%+v err=%v", persisted, fake.addressFamily, err)
	}
	page, err := s.AuditLogs(context.Background(), AuditLogQuery{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Result != "failed" || page.Items[0].ErrorCode != "SETTINGS_PARTIAL_FAILURE" {
		t.Fatalf("audit=%+v err=%v", page.Items, err)
	}
}

func TestSettingsNegativeCacheDefaultsAndValidation(t *testing.T) {
	s := testService(t, &fakeMosdns{})
	settings, err := s.Settings(context.Background())
	if err != nil || !settings.NegativeCacheEnabled || settings.NegativeCacheTTL != 30 {
		t.Fatalf("settings=%+v err=%v", settings, err)
	}
	settings.NegativeCacheTTL = 0
	if err := s.UpdateSettings(context.Background(), settings, 1, "req-settings", "192.0.2.10"); err == nil {
		t.Fatal("zero negative cache TTL accepted")
	}
	settings.NegativeCacheTTL = 86401
	if err := s.UpdateSettings(context.Background(), settings, 1, "req-settings", "192.0.2.10"); err == nil {
		t.Fatal("oversized negative cache TTL accepted")
	}
}

func TestUpstreamGroupCRUDProtectsStableAndBuiltinIDs(t *testing.T) {
	fake := &fakeMosdns{}
	s := testService(t, fake)
	group := mosdnsclient.UpstreamGroup{ID: "office_dns", Name: "Office", Enabled: true, Mode: "race", Concurrent: 1, Upstreams: []mosdnsclient.Upstream{{Tag: "office", Addr: "https://dns.example/dns-query", Priority: 100, Weight: 1}}, ECS: mosdnsclient.ECSConfig{Mode: "off", Mask4: 24, Mask6: 48}, Cache: mosdnsclient.GroupCacheConfig{Enabled: true, Size: 1024}}
	created, err := s.CreateUpstreamGroup(context.Background(), UpstreamGroupWrite{ExpectedCurrentVersion: 1, Group: group}, 1, "req-create", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Groups) != 3 || created.Groups[2].ID != group.ID {
		t.Fatalf("created=%+v", created.Groups)
	}
	group.ID = "renamed"
	if _, err := s.UpdateUpstreamGroup(context.Background(), "office_dns", UpstreamGroupWrite{ExpectedCurrentVersion: 2, Group: group}, 1, "req-update", "192.0.2.10"); err == nil {
		t.Fatal("mutable group id accepted")
	}
	if _, err := s.DeleteUpstreamGroup(context.Background(), "remote_dns", 2, 1, "req-delete", "192.0.2.10"); !errors.Is(err, ErrProtectedGroup) {
		t.Fatalf("default group delete error=%v", err)
	}
	if _, err := s.DeleteUpstreamGroup(context.Background(), "local_dns", 2, 1, "req-delete", "192.0.2.10"); !errors.Is(err, ErrProtectedGroup) {
		t.Fatalf("builtin group delete error=%v", err)
	}
	deleted, err := s.DeleteUpstreamGroup(context.Background(), "office_dns", 2, 1, "req-delete", "192.0.2.10")
	if err != nil || len(deleted.Groups) != 2 {
		t.Fatalf("deleted=%+v err=%v", deleted.Groups, err)
	}
}

func TestUpstreamGroupMutationsRequireCurrentVersionAndKeepBuiltinsEnabled(t *testing.T) {
	fake := &fakeMosdns{}
	s := testService(t, fake)
	group, _ := registryGroup(testRegistry(), "local_dns")
	group.Enabled = false
	if _, err := s.UpdateUpstreamGroup(context.Background(), group.ID, UpstreamGroupWrite{ExpectedCurrentVersion: 1, Group: group}, 1, "req-update", "192.0.2.10"); !errors.Is(err, ErrProtectedGroup) {
		t.Fatalf("disable local_dns error=%v", err)
	}
	group.Enabled = true
	if _, err := s.UpdateUpstreamGroup(context.Background(), group.ID, UpstreamGroupWrite{ExpectedCurrentVersion: 99, Group: group}, 1, "req-update", "192.0.2.10"); !errors.Is(err, mosdnsclient.ErrConflict) {
		t.Fatalf("stale update error=%v", err)
	}
	if err := s.FlushUpstreamGroup(context.Background(), "local_dns", 99, 1, "req-flush", "192.0.2.10"); !errors.Is(err, mosdnsclient.ErrConflict) {
		t.Fatalf("stale flush error=%v", err)
	}
	if len(fake.flushes) != 0 {
		t.Fatalf("stale flush reached runtime: %v", fake.flushes)
	}
}

func TestUpdateSettingsRequiresRegistryVersion(t *testing.T) {
	s := testService(t, &fakeMosdns{})
	settings, err := s.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.UpstreamRegistryVersion++
	if err := s.UpdateSettings(context.Background(), settings, 1, "req-settings", "192.0.2.10"); !errors.Is(err, mosdnsclient.ErrConflict) {
		t.Fatalf("stale settings error=%v", err)
	}
}

func TestSyncSettingsPreservesUnpersistedRegistryDefault(t *testing.T) {
	fake := &fakeMosdns{}
	fake.registry = testRegistry()
	fake.registry.DefaultGroupID = "local_dns"
	s := testService(t, fake)
	if err := s.SyncSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	settings, err := s.Settings(context.Background())
	if err != nil || settings.DefaultUpstreamGroupID != "local_dns" {
		t.Fatalf("settings=%+v err=%v", settings, err)
	}
	var persisted string
	if err := s.db.QueryRow(`SELECT value_json FROM system_state WHERE key='default_upstream_group_id'`).Scan(&persisted); err != nil || persisted != "local_dns" {
		t.Fatalf("persisted=%q err=%v", persisted, err)
	}
}

func TestRegistryUnknownOutcomeIsReconciled(t *testing.T) {
	fake := &fakeMosdns{registryApplyErr: mosdnsclient.ErrUnknown}
	s := testService(t, fake)
	group := mosdnsclient.UpstreamGroup{ID: "unknown_ok", Name: "Unknown", Enabled: true, Mode: "race", Concurrent: 1, Upstreams: []mosdnsclient.Upstream{{Tag: "test", Addr: "https://dns.example/dns-query", Priority: 100, Weight: 1}}, ECS: mosdnsclient.ECSConfig{Mode: "off", Mask4: 24, Mask6: 48}, Cache: mosdnsclient.GroupCacheConfig{Enabled: true, Size: 1024}}
	updated, err := s.CreateUpstreamGroup(context.Background(), UpstreamGroupWrite{ExpectedCurrentVersion: 1, Group: group}, 1, "req-unknown", "192.0.2.10")
	if err != nil || len(updated.Groups) != 3 {
		t.Fatalf("updated=%+v err=%v", updated, err)
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
