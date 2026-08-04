package operations

import (
	"context"
	"encoding/json"
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
	cacheEnabled     bool
	cacheTTL         int
	negativeCache    map[string]mosdnsclient.NegativeCacheSettings
	negativeCalls    []string
	negativeErr      map[string]error
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
	return mosdnsclient.RegistrySnapshot{SchemaVersion: 1, Version: 1, DefaultGroupID: "default_dns", Groups: []mosdnsclient.UpstreamGroup{group("default_dns", "Default"), group("backup_dns", "Backup")}, Cache: mosdnsclient.RegistryCacheConfig{Enabled: true, Negative: mosdnsclient.NegativeCacheConfig{Enabled: true, TTL: 30}}}
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

func TestDeleteOrDisableUpstreamGroupRejectsDisabledSubscriptionReference(t *testing.T) {
	fake := &fakeMosdns{}
	s := testService(t, fake)
	registry, err := s.CreateUpstreamGroup(context.Background(), UpstreamGroupWrite{ExpectedCurrentVersion: 1, Group: mosdnsclient.UpstreamGroup{ID: "office_dns", Name: "Office", Enabled: true, Mode: "race", Concurrent: 1, Upstreams: []mosdnsclient.Upstream{{Tag: "office", Addr: "https://dns.example/dns-query"}}, ECS: mosdnsclient.ECSConfig{Mode: "off", Mask4: 24, Mask6: 48}, Cache: mosdnsclient.GroupCacheConfig{Enabled: true, Size: 1024}}}, 1, "create", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.db.Exec(`INSERT INTO rule_subscriptions(category,action,kind,name,refresh_interval_seconds,enabled,created_at_ms,updated_at_ms,domains_json) VALUES('route','upstream','upload','disabled',86400,0,1,1,'["example.com"]')`)
	if err != nil {
		t.Fatal(err)
	}
	subscriptionID, _ := result.LastInsertId()
	if _, err := s.db.Exec(`INSERT INTO subscription_bindings(subscription_id,upstream_group_id,priority,created_at_ms,updated_at_ms) VALUES(?,?,?,?,?)`, subscriptionID, "office_dns", 100, 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteUpstreamGroup(context.Background(), "office_dns", registry.Version, 1, "delete", ""); !errors.Is(err, ErrGroupReferenced) {
		t.Fatalf("delete err=%v, want ErrGroupReferenced", err)
	}
	group, ok := registryGroup(registry, "office_dns")
	if !ok {
		t.Fatal("office_dns missing")
	}
	group.Enabled = false
	if _, err := s.UpdateUpstreamGroup(context.Background(), group.ID, UpstreamGroupWrite{ExpectedCurrentVersion: registry.Version, Group: group}, 1, "disable", ""); !errors.Is(err, ErrGroupReferenced) {
		t.Fatalf("disable err=%v, want ErrGroupReferenced", err)
	}
}

func TestDeleteUpstreamGroupRejectsReferenceRetainedByCandidateSnapshot(t *testing.T) {
	for _, status := range []string{"active", "pending", "unknown"} {
		t.Run(status, func(t *testing.T) {
			fake := &fakeMosdns{}
			s := testService(t, fake)
			registry, err := s.CreateUpstreamGroup(context.Background(), UpstreamGroupWrite{ExpectedCurrentVersion: 1, Group: mosdnsclient.UpstreamGroup{ID: "office_dns", Name: "Office", Enabled: true, Mode: "race", Concurrent: 1, Upstreams: []mosdnsclient.Upstream{{Tag: "office", Addr: "https://dns.example/dns-query"}}, ECS: mosdnsclient.ECSConfig{Mode: "off", Mask4: 24, Mask6: 48}, Cache: mosdnsclient.GroupCacheConfig{Enabled: true, Size: 1024}}}, 1, "create", "")
			if err != nil {
				t.Fatal(err)
			}
			snapshot := mosdnsclient.Snapshot{SchemaVersion: 4, Version: 1, SubscriptionSets: []mosdnsclient.SubscriptionSet{{SourceID: 1, SourceName: "route", Category: "route", Action: "upstream", BindingID: 1, UpstreamGroupID: "office_dns", Priority: 100, Domains: []string{"example.com"}}}}
			raw, _ := json.Marshal(snapshot)
			if _, err := s.db.Exec(`INSERT INTO rule_versions(version,schema_version,checksum,status,rule_count,regexp_rule_count,snapshot_json,rules_json,created_at_ms) VALUES(1,4,'snapshot',?,1,0,?,'[]',1)`, status, raw); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DeleteUpstreamGroup(context.Background(), "office_dns", registry.Version, 1, "delete", ""); !errors.Is(err, ErrGroupReferenced) {
				t.Fatalf("delete err=%v, want ErrGroupReferenced", err)
			}
		})
	}
}

func TestDevicesUpdateAndAudit(t *testing.T) {
	s := testService(t, &fakeMosdns{})
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO devices(ip,note,source,first_seen_at_ms,last_seen_at_ms,updated_at_ms) VALUES(?,?,?,?,?,?)`, `192.0.2.5`, "", "observed", now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO dns_queries(event_id,timestamp_unix_ms,client_ip,qname,qtype,qclass,route,route_source,cache_hit,snapshot_version,answer_count,latency_us,result_class,created_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, `event-device`, now, `192.0.2.5`, `example.com`, 1, 1, "forward", "default", 0, 1, 0, 1, "negative_answer", now); err != nil {
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

func TestUpdateSettingsPersistsAndAppliesCache(t *testing.T) {
	fake := &fakeMosdns{}
	s := testService(t, fake)
	if err := s.UpdateSettings(context.Background(), Settings{CacheEnabled: false, CacheTTL: 60, NegativeCacheEnabled: false, NegativeCacheTTL: 45, QueryRetentionDays: 3, DatabaseMaxSizeGiB: 4, AddressFamilyMode: "ipv4_only", DefaultUpstreamGroupID: "default_dns", UpstreamRegistryVersion: 1}, 1, "req-settings", "192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	settings, err := s.Settings(context.Background())
	if err != nil || settings.CacheEnabled || settings.CacheTTL != 60 || settings.NegativeCacheEnabled || settings.NegativeCacheTTL != 45 || settings.QueryRetentionDays != 3 || settings.DatabaseMaxSizeGiB != 4 || settings.AddressFamilyMode != "ipv4_only" || settings.DefaultUpstreamGroupID != "default_dns" || fake.addressFamily.Mode != "ipv4_only" {
		t.Fatalf("settings=%+v cache=%t err=%v", settings, fake.cacheEnabled, err)
	}
	if fake.registry.Cache.Negative.Enabled || fake.registry.Cache.Negative.TTL != 45 {
		t.Fatalf("negative cache=%+v", fake.registry.Cache.Negative)
	}
	if len(fake.flushes) != 1 || fake.flushes[0] != "" {
		t.Fatalf("flushes=%v", fake.flushes)
	}
	if err := s.UpdateSettings(context.Background(), Settings{CacheEnabled: false, CacheTTL: 60, NegativeCacheTTL: 45, QueryRetentionDays: 3, DatabaseMaxSizeGiB: 0, AddressFamilyMode: "ipv4_only", DefaultUpstreamGroupID: "default_dns", UpstreamRegistryVersion: fake.registry.Version}, 1, "req-settings", "192.0.2.10"); err == nil {
		t.Fatal("invalid database size accepted")
	}
}

func TestUpdateSettingsPersistsAddressFamilyWhenCacheFlushFails(t *testing.T) {
	fake := &fakeMosdns{flushErr: map[string]error{"": errors.New("cache unavailable")}}
	s := testService(t, fake)
	settings := Settings{CacheEnabled: true, CacheTTL: 0, NegativeCacheEnabled: true, NegativeCacheTTL: 30, QueryRetentionDays: 7, DatabaseMaxSizeGiB: 2, AddressFamilyMode: "ipv6_only", DefaultUpstreamGroupID: "default_dns", UpstreamRegistryVersion: 1}
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

func TestUpstreamGroupCRUDProtectsDefaultID(t *testing.T) {
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
	if _, err := s.DeleteUpstreamGroup(context.Background(), "default_dns", 2, 1, "req-delete", "192.0.2.10"); !errors.Is(err, ErrProtectedGroup) {
		t.Fatalf("default group delete error=%v", err)
	}
	deleted, err := s.DeleteUpstreamGroup(context.Background(), "office_dns", 2, 1, "req-delete", "192.0.2.10")
	if err != nil || len(deleted.Groups) != 2 {
		t.Fatalf("deleted=%+v err=%v", deleted.Groups, err)
	}
}

func TestUpstreamGroupMutationsRequireCurrentVersionAndKeepDefaultEnabled(t *testing.T) {
	fake := &fakeMosdns{}
	s := testService(t, fake)
	group, _ := registryGroup(testRegistry(), "default_dns")
	group.Enabled = false
	if _, err := s.UpdateUpstreamGroup(context.Background(), group.ID, UpstreamGroupWrite{ExpectedCurrentVersion: 1, Group: group}, 1, "req-update", "192.0.2.10"); !errors.Is(err, ErrProtectedGroup) {
		t.Fatalf("disable default_dns error=%v", err)
	}
	group.Enabled = true
	if _, err := s.UpdateUpstreamGroup(context.Background(), group.ID, UpstreamGroupWrite{ExpectedCurrentVersion: 99, Group: group}, 1, "req-update", "192.0.2.10"); !errors.Is(err, mosdnsclient.ErrConflict) {
		t.Fatalf("stale update error=%v", err)
	}
	if err := s.FlushUpstreamGroup(context.Background(), "default_dns", 99, 1, "req-flush", "192.0.2.10"); !errors.Is(err, mosdnsclient.ErrConflict) {
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

func TestSyncSettingsPreservesEnabledRegistryDefault(t *testing.T) {
	fake := &fakeMosdns{}
	fake.registry = testRegistry()
	fake.registry.DefaultGroupID = "backup_dns"
	s := testService(t, fake)
	if err := s.SyncSettings(context.Background()); err != nil || fake.registry.DefaultGroupID != "backup_dns" {
		t.Fatalf("default group=%q err=%v", fake.registry.DefaultGroupID, err)
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
	if _, err := s.db.Exec(`INSERT INTO dns_queries(event_id,timestamp_unix_ms,client_ip,qname,qtype,qclass,route,route_source,cache_hit,snapshot_version,answer_count,latency_us,result_class,created_at_ms) VALUES('event-clear',?,'192.0.2.5','example.com',1,1,'forward','default',0,1,0,1,'negative_answer',?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO dns_stats_hourly_global(hour_start_ms,route,qtype,rcode,query_count,error_count,cache_hit_count,latency_sum_us,latency_max_us,success_count,negative_answer_count,policy_block_count,processing_error_count) VALUES(?, 'forward', 1, 0, 1, 0, 0, 1, 1, 0, 1, 0, 0)`, now); err != nil {
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
