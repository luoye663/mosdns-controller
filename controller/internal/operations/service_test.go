package operations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/managed-dns/controller/internal/mosdnsclient"
	"github.com/managed-dns/controller/internal/storage"
)

type fakeMosdns struct {
	flushes      []string
	flushErr     map[string]error
	statusErr    error
	upstreams    map[string]mosdnsclient.UpstreamSnapshot
	cacheEnabled bool
	geosite      mosdnsclient.DomainSetStatus
}

func (f *fakeMosdns) Status(context.Context) (mosdnsclient.Status, error) {
	if f.statusErr != nil {
		return mosdnsclient.Status{}, f.statusErr
	}
	return mosdnsclient.Status{State: "ready", SnapshotVersion: 3, Checksum: "sha256:test"}, nil
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
func (f *fakeMosdns) GeositeStatus(context.Context) (mosdnsclient.DomainSetStatus, error) {
	return f.geosite, nil
}
func (f *fakeMosdns) ApplyGeosite(_ context.Context, snapshot mosdnsclient.DomainSetSnapshot) (mosdnsclient.DomainSetStatus, error) {
	f.geosite = mosdnsclient.DomainSetStatus{Version: snapshot.Version, RuleCount: 1}
	return f.geosite, nil
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
	logs, err := s.AuditLogs(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].Action != "update" || logs[0].ResourceType != "device" {
		t.Fatalf("audit=%+v", logs)
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
	logs, err := s.AuditLogs(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].Result != "failed" || logs[0].ErrorCode != "CACHE_FLUSH_FAILED" {
		t.Fatalf("audit=%+v", logs)
	}
}

func TestSystemStatusDegradesWhenMosdnsUnavailable(t *testing.T) {
	s := testService(t, &fakeMosdns{statusErr: errors.New("offline")})
	status := s.SystemStatus(context.Background())
	if status.Mosdns != nil || status.MosdnsError == "" {
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
	logs, err := s.AuditLogs(context.Background(), 10)
	if err != nil || len(logs) != 1 || logs[0].ResourceType != "upstream" {
		t.Fatalf("logs=%+v err=%v", logs, err)
	}
}

func TestUpdateSettingsPersistsAndAppliesCache(t *testing.T) {
	fake := &fakeMosdns{}
	s := testService(t, fake)
	if err := s.UpdateSettings(context.Background(), Settings{CacheEnabled: false, QueryRetentionDays: 3}, 1, "req-settings", "192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	settings, err := s.Settings(context.Background())
	if err != nil || settings.CacheEnabled || settings.QueryRetentionDays != 3 || fake.cacheEnabled {
		t.Fatalf("settings=%+v cache=%t err=%v", settings, fake.cacheEnabled, err)
	}
}

func TestUpdateGeositeDownloadsAndPublishes(t *testing.T) {
	s := testService(t, &fakeMosdns{})
	if _, err := s.UpdateGeosite(context.Background(), "http://example.com/geosite.txt", 1, "req-geosite", "192.0.2.10"); err == nil {
		t.Fatal("non-HTTPS source accepted")
	}
}

func TestUploadGeositePublishesAndClearsSubscriptionSource(t *testing.T) {
	fake := &fakeMosdns{}
	s := testService(t, fake)
	status, err := s.UploadGeosite(context.Background(), []byte("example.cn\n"), "china.txt", 1, "req-geosite-upload", "192.0.2.10")
	if err != nil || status.Version != 1 || status.SourceURL != "" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if _, err := s.UploadGeosite(context.Background(), []byte("example.cn\n"), "china.dat", 1, "req-geosite-upload", "192.0.2.10"); err == nil {
		t.Fatal("non-txt upload accepted")
	}
}
