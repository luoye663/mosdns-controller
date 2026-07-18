package rules

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/managed-dns/controller/internal/mosdnsclient"
	"github.com/managed-dns/controller/internal/storage"
)

type fakeMosdns struct {
	mu      sync.Mutex
	current mosdnsclient.Snapshot
	unknown bool
	flushes []string
}

func (f *fakeMosdns) Status(context.Context) (mosdnsclient.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return mosdnsclient.Status{SnapshotVersion: f.current.Version, Checksum: f.current.Checksum, State: "ready"}, nil
}
func (f *fakeMosdns) Validate(_ context.Context, s mosdnsclient.Snapshot) (mosdnsclient.ValidateResult, error) {
	return mosdnsclient.ValidateResult{Valid: true, Checksum: s.Checksum, RuleCount: len(s.Rules)}, nil
}
func (f *fakeMosdns) Apply(_ context.Context, s mosdnsclient.Snapshot) (mosdnsclient.ApplyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.current = s
	if f.unknown {
		return mosdnsclient.ApplyResult{}, mosdnsclient.ErrUnknown
	}
	return mosdnsclient.ApplyResult{Applied: true, Version: s.Version, Checksum: s.Checksum}, nil
}
func (f *fakeMosdns) Match(context.Context, string) (any, error) {
	return map[string]string{"route": "remote"}, nil
}
func (f *fakeMosdns) Flush(_ context.Context, tag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushes = append(f.flushes, tag)
	return nil
}
func (f *fakeMosdns) SetCacheEnabled(context.Context, bool) error { return nil }
func (f *fakeMosdns) SetCacheTTL(context.Context, int) error      { return nil }
func (f *fakeMosdns) UpstreamStatus(context.Context, string) (mosdnsclient.UpstreamSnapshot, error) {
	return mosdnsclient.UpstreamSnapshot{}, nil
}
func (f *fakeMosdns) ApplyUpstream(context.Context, string, mosdnsclient.UpstreamSnapshot) (mosdnsclient.UpstreamSnapshot, error) {
	return mosdnsclient.UpstreamSnapshot{}, nil
}
func (f *fakeMosdns) ECSStatus(context.Context, string) (mosdnsclient.ECSSnapshot, error) {
	return mosdnsclient.ECSSnapshot{}, nil
}
func (f *fakeMosdns) ApplyECS(context.Context, string, mosdnsclient.ECSSnapshot) (mosdnsclient.ECSSnapshot, error) {
	return mosdnsclient.ECSSnapshot{}, nil
}
func (f *fakeMosdns) AuditStatus(context.Context) (mosdnsclient.AuditStatus, error) {
	return mosdnsclient.AuditStatus{}, nil
}

func testService(t *testing.T) (*Service, *fakeMosdns) {
	t.Helper()
	store, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO admins(id,username,password_hash,created_at_ms,updated_at_ms) VALUES(1,'tester','hash',1,1)`); err != nil {
		t.Fatal(err)
	}
	fake := new(fakeMosdns)
	return New(store, fake), fake
}
func blockRule(pattern string) Rule {
	return Rule{Category: "access", Action: "block", MatchType: "domain", Pattern: pattern, Priority: 100, Enabled: true}
}
func routeRule() Rule {
	return Rule{Category: "route", Action: "remote", MatchType: "domain", Pattern: "route.example", Priority: 100, Enabled: true}
}

func TestEmptyListsReturnArrays(t *testing.T) {
	service, _ := testService(t)
	rules, err := service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	versions, err := service.Versions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rules == nil || versions == nil || len(rules) != 0 || len(versions) != 0 {
		t.Fatalf("empty lists must be non-nil arrays: rules=%#v versions=%#v", rules, versions)
	}
}

func TestPublishAndRouteCacheFlush(t *testing.T) {
	service, fake := testService(t)
	version, err := service.Create(context.Background(), blockRule("blocked.example"), 1, "r1", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if version.Status != statusActive || version.Version != 1 {
		t.Fatalf("version=%+v", version)
	}
	if len(fake.flushes) != 0 {
		t.Fatalf("access change flushed cache: %v", fake.flushes)
	}
	_, err = service.Create(context.Background(), routeRule(), 1, "r2", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.flushes) != 2 {
		t.Fatalf("route change flushes=%v", fake.flushes)
	}
	listed, err := service.List(context.Background())
	if err != nil || len(listed) != 2 {
		t.Fatalf("rules=%v err=%v", listed, err)
	}
}

func TestUploadSubscriptionPublishesRouteRulesAndCanBeDisabled(t *testing.T) {
	service, fake := testService(t)
	input := SubscriptionInput{Category: "route", Action: "local", Name: "domestic.txt", RefreshIntervalSeconds: 86400, Enabled: true}
	source, published, err := service.CreateUploadSubscription(context.Background(), input, "domestic.txt", []byte("# comment\nExample.CN.\napi.example.cn\nexample.cn\n"), 1, "sub-create", "127.0.0.1")
	if err != nil || source.RuleCount != 2 || published.Version != 1 {
		t.Fatalf("source=%+v version=%+v err=%v", source, published, err)
	}
	if len(fake.current.Rules) != 2 || fake.current.Rules[0].Action != "local" || len(fake.flushes) != 2 {
		t.Fatalf("snapshot=%+v flushes=%v", fake.current, fake.flushes)
	}
	manual, err := service.List(context.Background())
	if err != nil || len(manual) != 0 {
		t.Fatalf("subscription rules leaked into manual list: %+v err=%v", manual, err)
	}
	updated, version, err := service.SetSubscriptionEnabled(context.Background(), source.ID, false, 1, "sub-disable", "127.0.0.1")
	if err != nil || updated.Enabled || version.Version != 2 || len(fake.current.Rules) != 0 {
		t.Fatalf("updated=%+v version=%+v snapshot=%+v err=%v", updated, version, fake.current, err)
	}
}

func TestDeleteSubscriptionRemovesOnlyItsRules(t *testing.T) {
	service, _ := testService(t)
	input := SubscriptionInput{Category: "access", Action: "block", Name: "one.txt", RefreshIntervalSeconds: 86400, Enabled: true}
	first, _, err := service.CreateUploadSubscription(context.Background(), input, "one.txt", []byte("one.example\n"), 1, "sub-one", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	input.Name = "two.txt"
	if _, _, err = service.CreateUploadSubscription(context.Background(), input, "two.txt", []byte("two.example\n"), 1, "sub-two", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, err = service.DeleteSubscription(context.Background(), first.ID, 1, "sub-delete", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	all, err := service.allRules(context.Background())
	if err != nil || len(all) != 1 || all[0].Pattern != "two.example" {
		t.Fatalf("remaining rules=%+v err=%v", all, err)
	}
}

func TestSubscriptionRulesRejectGenericMutation(t *testing.T) {
	service, _ := testService(t)
	input := SubscriptionInput{Category: "access", Action: "block", Name: "source.txt", RefreshIntervalSeconds: 86400, Enabled: true}
	if _, _, err := service.CreateUploadSubscription(context.Background(), input, "source.txt", []byte("blocked.example\n"), 1, "sub", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	all, err := service.allRules(context.Background())
	if err != nil || len(all) != 1 {
		t.Fatalf("rules=%+v err=%v", all, err)
	}
	if _, err := service.Delete(context.Background(), all[0].ID, 1, "delete", "127.0.0.1"); err == nil {
		t.Fatal("generic delete accepted a subscription rule")
	}
}

func TestDownloadSubscriptionAcceptsHTTPSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("example.test\n")) }))
	defer server.Close()
	body, sourceURL, err := downloadSubscription(context.Background(), server.URL)
	if err != nil || string(body) != "example.test\n" || sourceURL != server.URL {
		t.Fatalf("body=%q source_url=%q err=%v", body, sourceURL, err)
	}
}
func TestDeleteLastRulePublishesEmptySnapshot(t *testing.T) {
	service, fake := testService(t)
	created, err := service.Create(context.Background(), blockRule("only.example"), 1, "r1", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	deleted, err := service.Delete(context.Background(), 1, 1, "r2", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Status != statusActive || deleted.Version <= created.Version || deleted.RuleCount != 0 {
		t.Fatalf("delete version=%+v, created=%+v", deleted, created)
	}
	if len(fake.current.Rules) != 0 {
		t.Fatalf("runtime rules=%+v, want empty snapshot", fake.current.Rules)
	}
	listed, err := service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("rules=%+v, want no rules", listed)
	}
}
func TestUnknownApplyIsReconciled(t *testing.T) {
	service, fake := testService(t)
	fake.unknown = true
	version, err := service.Create(context.Background(), blockRule("unknown.example"), 1, "r1", "127.0.0.1")
	if err != nil || version.Status != statusActive {
		t.Fatalf("version=%+v err=%v", version, err)
	}
	state, err := service.Reconcile(context.Background())
	if err != nil || state != "unchanged" {
		t.Fatalf("state=%q err=%v", state, err)
	}
	versions, err := service.Versions(context.Background())
	if err != nil || versions[0].Status != statusActive {
		t.Fatalf("versions=%+v err=%v", versions, err)
	}
}
func TestRollbackCreatesMonotonicVersion(t *testing.T) {
	service, _ := testService(t)
	first, err := service.Create(context.Background(), blockRule("one.example"), 1, "r1", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(context.Background(), blockRule("two.example"), 1, "r2", "")
	if err != nil {
		t.Fatal(err)
	}
	rolled, err := service.Rollback(context.Background(), first.Version, 1, "r3", "")
	if err != nil {
		t.Fatal(err)
	}
	if rolled.Version <= second.Version {
		t.Fatalf("rollback version %d is not newer than %d", rolled.Version, second.Version)
	}
	rules, err := service.List(context.Background())
	if err != nil || len(rules) != 1 || rules[0].Pattern != "one.example" {
		t.Fatalf("rules=%+v err=%v", rules, err)
	}
}
