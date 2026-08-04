package rules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/managed-dns/controller/internal/coordination"
	"github.com/managed-dns/controller/internal/mosdnsclient"
	"github.com/managed-dns/controller/internal/operations"
	"github.com/managed-dns/controller/internal/storage"
)

type fakeMosdns struct {
	mu        sync.Mutex
	current   mosdnsclient.Snapshot
	unknown   bool
	statusErr error
	applyErr  error
	flushes   []string
	registry  mosdnsclient.RegistrySnapshot
}

func (f *fakeMosdns) Status(context.Context) (mosdnsclient.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statusErr != nil {
		return mosdnsclient.Status{}, f.statusErr
	}
	return mosdnsclient.Status{SnapshotVersion: f.current.Version, Checksum: f.current.Checksum, State: "ready"}, nil
}
func (f *fakeMosdns) Validate(_ context.Context, s mosdnsclient.Snapshot) (mosdnsclient.ValidateResult, error) {
	return mosdnsclient.ValidateResult{Valid: true, Checksum: s.Checksum, RuleCount: len(s.Rules)}, nil
}
func (f *fakeMosdns) Apply(_ context.Context, s mosdnsclient.Snapshot) (mosdnsclient.ApplyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.applyErr != nil {
		return mosdnsclient.ApplyResult{}, f.applyErr
	}
	f.current = s
	if f.unknown {
		return mosdnsclient.ApplyResult{}, mosdnsclient.ErrUnknown
	}
	return mosdnsclient.ApplyResult{Applied: true, Version: s.Version, Checksum: s.Checksum}, nil
}

func routeSubscriptionInput(group string, priority int) SubscriptionInput {
	version := uint64(1)
	return SubscriptionInput{Category: "route", Action: "upstream", Name: "route.txt", RefreshIntervalSeconds: 86400, Enabled: true, UpstreamGroupID: group, Priority: &priority, ExpectedUpstreamRegistryVersion: &version}
}
func (f *fakeMosdns) Match(context.Context, string) (any, error) {
	return map[string]string{"route": "upstream", "upstream_group_id": "default_dns"}, nil
}
func (f *fakeMosdns) Flush(_ context.Context, tag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushes = append(f.flushes, tag)
	return nil
}
func (f *fakeMosdns) SetCacheEnabled(context.Context, bool) error { return nil }
func (f *fakeMosdns) SetCacheTTL(context.Context, int) error      { return nil }
func (f *fakeMosdns) NegativeCache(context.Context, string) (mosdnsclient.NegativeCacheSettings, error) {
	return mosdnsclient.NegativeCacheSettings{}, nil
}
func (f *fakeMosdns) SetNegativeCache(_ context.Context, _ string, settings mosdnsclient.NegativeCacheSettings) (mosdnsclient.NegativeCacheSettings, error) {
	return settings, nil
}
func (f *fakeMosdns) RegistryStatus(context.Context) (mosdnsclient.RegistrySnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.registry.Version == 0 {
		f.registry = mosdnsclient.RegistrySnapshot{SchemaVersion: 1, Version: 1, DefaultGroupID: "default_dns", Groups: []mosdnsclient.UpstreamGroup{{ID: "default_dns", Enabled: true}, {ID: "office_dns", Enabled: true}}}
	}
	result := f.registry
	result.Groups = append([]mosdnsclient.UpstreamGroup(nil), f.registry.Groups...)
	return result, nil
}
func (f *fakeMosdns) ApplyRegistry(_ context.Context, snapshot mosdnsclient.RegistrySnapshot) (mosdnsclient.RegistrySnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if snapshot.ExpectedCurrentVersion != f.registry.Version {
		return mosdnsclient.RegistrySnapshot{}, mosdnsclient.ErrConflict
	}
	snapshot.ExpectedCurrentVersion = 0
	f.registry = snapshot
	return snapshot, nil
}
func (f *fakeMosdns) FlushRegistry(_ context.Context, group string, _ uint64) error {
	f.flushes = append(f.flushes, group)
	return nil
}
func (f *fakeMosdns) AddressFamilyStatus(context.Context) (mosdnsclient.AddressFamilySnapshot, error) {
	return mosdnsclient.AddressFamilySnapshot{Version: 1, Mode: "dual_stack"}, nil
}
func (f *fakeMosdns) ApplyAddressFamily(context.Context, mosdnsclient.AddressFamilySnapshot) (mosdnsclient.AddressFamilySnapshot, error) {
	return mosdnsclient.AddressFamilySnapshot{}, nil
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
	version := uint64(1)
	return Rule{Category: "route", Action: "upstream", UpstreamGroupID: "default_dns", MatchType: "domain", Pattern: "route.example", Priority: 0, Enabled: true, ExpectedUpstreamRegistryVersion: &version}
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

func TestRulePublishDoesNotFlushRegistryCache(t *testing.T) {
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
	if len(fake.flushes) != 0 {
		t.Fatalf("route change flushed isolated registry cache: %v", fake.flushes)
	}
	listed, err := service.List(context.Background())
	if err != nil || len(listed) != 2 {
		t.Fatalf("rules=%v err=%v", listed, err)
	}
}

func TestRouteRuleRequiresEnabledGroupAndPreservesPriorityZero(t *testing.T) {
	service, fake := testService(t)
	version := uint64(1)
	invalid := Rule{Category: "route", Action: "upstream", UpstreamGroupID: "missing_dns", MatchType: "domain", Pattern: "missing.example", Priority: 0, Enabled: true, ExpectedUpstreamRegistryVersion: &version}
	if _, err := service.Create(context.Background(), invalid, 1, "invalid", ""); err == nil {
		t.Fatal("missing upstream group was accepted")
	}
	first := blockRule("later.example")
	first.Priority = 100
	if _, err := service.Create(context.Background(), first, 1, "first", ""); err != nil {
		t.Fatal(err)
	}
	second := blockRule("first.example")
	second.Priority = 0
	if _, err := service.Create(context.Background(), second, 1, "second", ""); err != nil {
		t.Fatal(err)
	}
	if len(fake.current.Rules) != 2 || fake.current.Rules[0].Priority != 0 || fake.current.Rules[0].Pattern != "first.example" {
		t.Fatalf("snapshot rules=%+v", fake.current.Rules)
	}
	listed, err := service.List(context.Background())
	if err != nil || len(listed) != 2 || listed[0].Priority != 0 {
		t.Fatalf("listed rules=%+v err=%v", listed, err)
	}
}

func TestUploadSubscriptionPublishesRouteRulesAndCanBeDisabled(t *testing.T) {
	service, fake := testService(t)
	priority, registryVersion := 250, uint64(1)
	input := SubscriptionInput{Category: "route", Action: "upstream", Name: "domestic.txt", RefreshIntervalSeconds: 86400, Enabled: true, UpstreamGroupID: "default_dns", Priority: &priority, ExpectedUpstreamRegistryVersion: &registryVersion}
	source, published, err := service.CreateUploadSubscription(context.Background(), input, "domestic.txt", []byte("# comment\nExample.CN.\napi.example.cn\nexample.cn\n"), 1, "sub-create", "127.0.0.1")
	if err != nil || source.RuleCount != 2 || published.Version != 1 {
		t.Fatalf("source=%+v version=%+v err=%v", source, published, err)
	}
	if len(fake.current.SubscriptionSets) != 1 || fake.current.SubscriptionSets[0].Action != "upstream" || fake.current.SubscriptionSets[0].UpstreamGroupID != "default_dns" || fake.current.SubscriptionSets[0].Priority != priority || len(fake.flushes) != 0 {
		t.Fatalf("snapshot=%+v flushes=%v", fake.current, fake.flushes)
	}
	manual, err := service.List(context.Background())
	if err != nil || len(manual) != 0 {
		t.Fatalf("subscription rules leaked into manual list: %+v err=%v", manual, err)
	}
	updated, version, err := service.SetSubscriptionEnabled(context.Background(), source.ID, false, 1, "sub-disable", "127.0.0.1")
	if err != nil || updated.Enabled || version.Version != 2 || len(fake.current.SubscriptionSets) != 0 {
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
	sets, err := service.subscriptionSets(context.Background())
	if err != nil || len(sets) != 1 || len(sets[0].Domains) != 1 || sets[0].Domains[0] != "two.example" {
		t.Fatalf("remaining sets=%+v err=%v", sets, err)
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

func TestReserveRuleIDsAllocatesContiguousRange(t *testing.T) {
	service, _ := testService(t)
	ids, err := service.reserveRuleIDs(context.Background(), 3)
	if err != nil || len(ids) != 3 || ids[0] != 1 || ids[1] != 2 || ids[2] != 3 {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	ids, err = service.reserveRuleIDs(context.Background(), 2)
	if err != nil || ids[0] != 4 || ids[1] != 5 {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
}

func TestUpdateRejectsStaleRule(t *testing.T) {
	service, _ := testService(t)
	if _, err := service.Create(context.Background(), blockRule("stale.example"), 1, "create", ""); err != nil {
		t.Fatal(err)
	}
	listed, err := service.List(context.Background())
	if err != nil || len(listed) != 1 {
		t.Fatalf("rules=%+v err=%v", listed, err)
	}
	patch := listed[0]
	patch.Comment = "first update"
	if _, err := service.Update(context.Background(), patch.ID, patch, 1, "update", ""); err != nil {
		t.Fatal(err)
	}
	patch.Comment = "stale update"
	if _, err := service.Update(context.Background(), patch.ID, patch, 1, "stale", ""); !errors.Is(err, ErrRuleConflict) {
		t.Fatalf("stale update error=%v", err)
	}
}

func TestImportAlwaysAllocatesFreshRuleIDs(t *testing.T) {
	service, _ := testService(t)
	if _, err := service.Create(context.Background(), blockRule("first.example"), 1, "create", ""); err != nil {
		t.Fatal(err)
	}
	incoming := blockRule("imported.example")
	incoming.ID = 1
	if _, err := service.Import(context.Background(), []Rule{incoming}, 1, "import", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), blockRule("third.example"), 1, "create-third", ""); err != nil {
		t.Fatal(err)
	}
	listed, err := service.List(context.Background())
	if err != nil || len(listed) != 3 {
		t.Fatalf("rules=%+v err=%v", listed, err)
	}
	seen := map[int64]bool{}
	for _, rule := range listed {
		seen[rule.ID] = true
	}
	if !seen[1] || !seen[2] || !seen[3] {
		t.Fatalf("allocated IDs=%v", seen)
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

func TestReconcilePublishesInitialEmptySnapshot(t *testing.T) {
	service, fake := testService(t)
	state, err := service.Reconcile(context.Background())
	if err != nil || state != "republished" || fake.current.SchemaVersion != 4 || fake.current.Version != 1 {
		t.Fatalf("state=%q snapshot=%+v err=%v", state, fake.current, err)
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

func TestSnapshotV4CanonicalContract(t *testing.T) {
	generatedAt := time.Date(2026, time.August, 4, 1, 2, 3, 0, time.UTC)
	snapshot := mosdnsclient.Snapshot{SchemaVersion: 4, Version: 9, ExpectedCurrentVersion: 8, GeneratedAt: generatedAt, BlockRCode: 3, Rules: []mosdnsclient.Rule{}, SubscriptionSets: []mosdnsclient.SubscriptionSet{{SourceID: 12, SourceName: "route", Category: "route", Action: "upstream", BindingID: 34, UpstreamGroupID: "office_dns", Priority: 250, Domains: []string{"a.example", "b.example"}}}}
	const canonical = `{"schema_version":4,"version":9,"expected_current_version":8,"generated_at":"2026-08-04T01:02:03Z","block_rcode":3,"rules":[],"subscription_sets":[{"source_id":12,"source_name":"route","category":"route","action":"upstream","binding_id":34,"upstream_group_id":"office_dns","priority":250,"domains":["a.example","b.example"]}]}`
	const expectedChecksum = "sha256:bbecc751a87323a29d3e671208dbfddedeccbd014185943d0f36f8c6df6a6433"
	got := snapshotCanonicalJSON(snapshot)
	if string(got) != canonical {
		t.Fatalf("canonical JSON changed:\n%s", got)
	}
	sum := sha256.Sum256(got)
	if checksum := "sha256:" + hex.EncodeToString(sum[:]); checksum != expectedChecksum {
		t.Fatalf("checksum=%s want=%s", checksum, expectedChecksum)
	}
	empty, err := json.Marshal(buildSnapshot(1, 0, nil, nil))
	if err != nil || !strings.Contains(string(empty), `"subscription_sets":[]`) {
		t.Fatalf("empty subscription_sets missing: %s err=%v", empty, err)
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
	rolled, err := service.Rollback(context.Background(), first.Version, nil, 1, "r3", "")
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

func TestRollbackRestoresHistoricalDisabledRules(t *testing.T) {
	service, _ := testService(t)
	disabled := blockRule("disabled.example")
	disabled.Enabled = false
	first, err := service.Create(context.Background(), disabled, 1, "disabled", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), blockRule("later.example"), 1, "later", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Rollback(context.Background(), first.Version, nil, 1, "rollback", ""); err != nil {
		t.Fatal(err)
	}
	listed, err := service.List(context.Background())
	if err != nil || len(listed) != 1 {
		t.Fatalf("rules=%+v err=%v", listed, err)
	}
	if listed[0].Pattern != "disabled.example" || listed[0].Enabled {
		t.Fatalf("historical disabled rule=%+v", listed[0])
	}
}

func TestFinalizeUnknownCandidatePreservesDisabledRules(t *testing.T) {
	service, _ := testService(t)
	disabled := blockRule("disabled.example")
	disabled.Enabled = false
	version, err := service.Create(context.Background(), disabled, 1, "disabled", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.store.DB().Exec(`UPDATE rule_versions SET status='unknown' WHERE version=?`, version.Version); err != nil {
		t.Fatal(err)
	}
	if err := service.finalize(context.Background(), version.Version); err != nil {
		t.Fatal(err)
	}
	listed, err := service.List(context.Background())
	if err != nil || len(listed) != 1 || listed[0].Enabled {
		t.Fatalf("rules=%+v err=%v", listed, err)
	}
}

func TestSubscriptionMutationsRollbackDatabaseStateOnPublishFailure(t *testing.T) {
	service, fake := testService(t)
	input := routeSubscriptionInput("default_dns", 200)
	source, _, err := service.CreateUploadSubscription(context.Background(), input, "route.txt", []byte("route.example\n"), 1, "create", "")
	if err != nil {
		t.Fatal(err)
	}
	fake.applyErr = errors.New("apply failed")
	if _, _, err := service.UpdateSubscriptionBinding(context.Background(), source.ID, BindingInput{UpstreamGroupID: "office_dns", Priority: 300, ExpectedUpstreamRegistryVersion: 1}, 1, "binding", ""); err == nil {
		t.Fatal("binding update succeeded while publish failed")
	}
	current, err := service.subscription(context.Background(), source.ID)
	if err != nil || current.Binding == nil || current.Binding.UpstreamGroupID != "default_dns" || current.Binding.Priority != 200 {
		t.Fatalf("binding was not restored: source=%+v err=%v", current, err)
	}
	if _, _, err := service.SetSubscriptionEnabled(context.Background(), source.ID, false, 1, "disable", ""); err == nil {
		t.Fatal("disable succeeded while publish failed")
	}
	current, err = service.subscription(context.Background(), source.ID)
	if err != nil || !current.Enabled {
		t.Fatalf("enabled state was not restored: source=%+v err=%v", current, err)
	}
	if _, err := service.DeleteSubscription(context.Background(), source.ID, 1, "delete", ""); err == nil {
		t.Fatal("delete succeeded while publish failed")
	}
	current, err = service.subscription(context.Background(), source.ID)
	if err != nil || current.Binding == nil || current.Binding.ID != source.Binding.ID {
		t.Fatalf("deleted subscription was not restored: source=%+v err=%v", current, err)
	}
}

func TestFailedCreateRemovesSubscriptionAndBinding(t *testing.T) {
	service, fake := testService(t)
	fake.applyErr = errors.New("apply failed")
	if _, _, err := service.CreateUploadSubscription(context.Background(), routeSubscriptionInput("default_dns", 100), "route.txt", []byte("route.example\n"), 1, "create", ""); err == nil {
		t.Fatal("create succeeded while publish failed")
	}
	var subscriptions, bindings int
	if err := service.store.DB().QueryRow(`SELECT COUNT(*) FROM rule_subscriptions`).Scan(&subscriptions); err != nil {
		t.Fatal(err)
	}
	if err := service.store.DB().QueryRow(`SELECT COUNT(*) FROM subscription_bindings`).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if subscriptions != 0 || bindings != 0 {
		t.Fatalf("subscriptions=%d bindings=%d", subscriptions, bindings)
	}
}

func TestUnknownCreateKeepsSourceStateForForwardReconcile(t *testing.T) {
	service, fake := testService(t)
	fake.unknown = true
	fake.statusErr = errors.New("status unavailable")
	_, version, err := service.CreateUploadSubscription(context.Background(), routeSubscriptionInput("default_dns", 100), "route.txt", []byte("route.example\n"), 1, "create", "")
	if !errors.Is(err, mosdnsclient.ErrUnknown) || version.Status != statusUnknown {
		t.Fatalf("version=%+v err=%v", version, err)
	}
	var subscriptions, bindings int
	if err := service.store.DB().QueryRow(`SELECT COUNT(*) FROM rule_subscriptions`).Scan(&subscriptions); err != nil {
		t.Fatal(err)
	}
	if err := service.store.DB().QueryRow(`SELECT COUNT(*) FROM subscription_bindings`).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if subscriptions != 1 || bindings != 1 {
		t.Fatalf("unknown outcome rolled source backward: subscriptions=%d bindings=%d", subscriptions, bindings)
	}
	fake.statusErr = nil
	if state, err := service.Reconcile(context.Background()); err != nil || state != "matched_candidate" {
		t.Fatalf("reconcile state=%q err=%v", state, err)
	}
	items, err := service.Subscriptions(context.Background(), "", "")
	if err != nil || len(items) != 1 {
		t.Fatalf("subscriptions=%+v err=%v", items, err)
	}
	fake.statusErr = errors.New("status unavailable")
	_, bindingVersion, err := service.UpdateSubscriptionBinding(context.Background(), items[0].ID, BindingInput{UpstreamGroupID: "office_dns", Priority: 300, ExpectedUpstreamRegistryVersion: 1}, 1, "binding", "")
	if !errors.Is(err, mosdnsclient.ErrUnknown) || bindingVersion.Status != statusUnknown {
		t.Fatalf("binding version=%+v err=%v", bindingVersion, err)
	}
	updated, err := service.subscription(context.Background(), items[0].ID)
	if err != nil || updated.Binding == nil || updated.Binding.UpstreamGroupID != "office_dns" || updated.Binding.Priority != 300 {
		t.Fatalf("unknown binding was rolled backward: source=%+v err=%v", updated, err)
	}
}

func TestRollbackKeepsCurrentSubscriptionSets(t *testing.T) {
	service, fake := testService(t)
	first, err := service.Create(context.Background(), blockRule("first.example"), 1, "first", "")
	if err != nil {
		t.Fatal(err)
	}
	input := SubscriptionInput{Category: "access", Action: "block", Name: "block.txt", RefreshIntervalSeconds: 86400, Enabled: true}
	if _, _, err := service.CreateUploadSubscription(context.Background(), input, "block.txt", []byte("sub.example\n"), 1, "subscription", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Rollback(context.Background(), first.Version, nil, 1, "rollback", ""); err != nil {
		t.Fatal(err)
	}
	if len(fake.current.SubscriptionSets) != 1 || fake.current.SubscriptionSets[0].Domains[0] != "sub.example" || fake.current.SchemaVersion != 4 {
		t.Fatalf("rollback snapshot=%+v", fake.current)
	}
}

func TestRefreshFailureRestoresCompleteSubscriptionState(t *testing.T) {
	service, fake := testService(t)
	var bodyMu sync.Mutex
	body := "first.example\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		bodyMu.Lock()
		defer bodyMu.Unlock()
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	input := SubscriptionInput{Category: "access", Action: "block", Name: "url", SourceURL: server.URL, RefreshIntervalSeconds: 86400, Enabled: true}
	source, _, err := service.CreateURLSubscription(context.Background(), input, 1, "create", "")
	if err != nil {
		t.Fatal(err)
	}
	var beforeChecksum string
	var beforeDomains []byte
	if err := service.store.DB().QueryRow(`SELECT content_checksum,domains_json FROM rule_subscriptions WHERE id=?`, source.ID).Scan(&beforeChecksum, &beforeDomains); err != nil {
		t.Fatal(err)
	}
	bodyMu.Lock()
	body = "second.example\n"
	bodyMu.Unlock()
	fake.applyErr = errors.New("apply failed")
	if _, _, err := service.RefreshSubscription(context.Background(), source.ID, 1, "refresh", ""); err == nil {
		t.Fatal("refresh succeeded while publish failed")
	}
	after, err := service.subscription(context.Background(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	var afterChecksum string
	var afterDomains []byte
	if err := service.store.DB().QueryRow(`SELECT content_checksum,domains_json FROM rule_subscriptions WHERE id=?`, source.ID).Scan(&afterChecksum, &afterDomains); err != nil {
		t.Fatal(err)
	}
	if after != source || afterChecksum != beforeChecksum || string(afterDomains) != string(beforeDomains) {
		t.Fatalf("refresh state changed: before=%+v/%s/%s after=%+v/%s/%s", source, beforeChecksum, beforeDomains, after, afterChecksum, afterDomains)
	}
}

func TestRefreshDownloadDoesNotBlockRuleMutation(t *testing.T) {
	service, fake := testService(t)
	downloadStarted := make(chan struct{})
	releaseDownload := make(chan struct{})
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests == 1 {
			_, _ = w.Write([]byte("old.example\n"))
			return
		}
		close(downloadStarted)
		<-releaseDownload
		_, _ = w.Write([]byte("new.example\n"))
	}))
	defer server.Close()

	source, _, err := service.CreateURLSubscription(context.Background(), SubscriptionInput{Category: "access", Action: "block", Name: "url", SourceURL: server.URL, RefreshIntervalSeconds: 86400, Enabled: true}, 1, "create", "")
	if err != nil {
		t.Fatal(err)
	}
	refreshDone := make(chan error, 1)
	go func() {
		_, _, refreshErr := service.RefreshSubscription(context.Background(), source.ID, 1, "refresh", "")
		refreshDone <- refreshErr
	}()
	<-downloadStarted

	mutationDone := make(chan error, 1)
	go func() {
		_, mutationErr := service.Create(context.Background(), blockRule("manual.example"), 1, "manual", "")
		mutationDone <- mutationErr
	}()
	select {
	case err := <-mutationDone:
		if err != nil {
			close(releaseDownload)
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(releaseDownload)
		<-refreshDone
		t.Fatal("rule mutation was blocked by subscription download")
	}
	close(releaseDownload)
	if err := <-refreshDone; err != nil {
		t.Fatal(err)
	}
	if len(fake.current.Rules) != 1 || len(fake.current.SubscriptionSets) != 1 || fake.current.SubscriptionSets[0].Domains[0] != "new.example" {
		t.Fatalf("snapshot=%+v", fake.current)
	}
}

func TestRefreshDoesNotOverwriteConcurrentSubscriptionMutation(t *testing.T) {
	service, fake := testService(t)
	downloadStarted := make(chan struct{})
	releaseDownload := make(chan struct{})
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests == 1 {
			_, _ = w.Write([]byte("old.example\n"))
			return
		}
		close(downloadStarted)
		<-releaseDownload
		_, _ = w.Write([]byte("stale.example\n"))
	}))
	defer server.Close()

	source, _, err := service.CreateURLSubscription(context.Background(), SubscriptionInput{Category: "access", Action: "block", Name: "url", SourceURL: server.URL, RefreshIntervalSeconds: 86400, Enabled: true}, 1, "create", "")
	if err != nil {
		t.Fatal(err)
	}
	refreshDone := make(chan error, 1)
	go func() {
		_, _, refreshErr := service.RefreshSubscription(context.Background(), source.ID, 1, "refresh", "")
		refreshDone <- refreshErr
	}()
	<-downloadStarted
	updated, _, err := service.SetSubscriptionEnabled(context.Background(), source.ID, false, 1, "disable", "")
	if err != nil || updated.Enabled {
		close(releaseDownload)
		<-refreshDone
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	close(releaseDownload)
	if err := <-refreshDone; !errors.Is(err, mosdnsclient.ErrConflict) {
		t.Fatalf("refresh err=%v, want conflict", err)
	}
	sets, err := service.subscriptionSets(context.Background())
	if err != nil || len(sets) != 0 || len(fake.current.SubscriptionSets) != 0 {
		t.Fatalf("sets=%+v snapshot=%+v err=%v", sets, fake.current, err)
	}
	var domains string
	if err := service.store.DB().QueryRow(`SELECT domains_json FROM rule_subscriptions WHERE id=?`, source.ID).Scan(&domains); err != nil {
		t.Fatal(err)
	}
	if domains != `["old.example"]` {
		t.Fatalf("domains_json=%s", domains)
	}
}

func TestReconcileCandidateRepublishesWhenSourcesDiverged(t *testing.T) {
	service, fake := testService(t)
	input := SubscriptionInput{Category: "access", Action: "block", Name: "block.txt", RefreshIntervalSeconds: 86400, Enabled: true}
	source, active, err := service.CreateUploadSubscription(context.Background(), input, "block.txt", []byte("old.example\n"), 1, "create", "")
	if err != nil {
		t.Fatal(err)
	}
	sets, err := service.subscriptionSets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	candidate := buildSnapshot(active.Version+1, active.Version, nil, sets)
	raw, _ := json.Marshal(candidate)
	if _, err := service.store.DB().Exec(`INSERT INTO rule_versions(version,schema_version,checksum,status,previous_version,rule_count,regexp_rule_count,snapshot_json,rules_json,created_at_ms) VALUES(?,4,?,'unknown',?,1,0,?,'[]',?)`, candidate.Version, candidate.Checksum, active.Version, raw, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.store.DB().Exec(`UPDATE rule_subscriptions SET domains_json='["new.example"]' WHERE id=?`, source.ID); err != nil {
		t.Fatal(err)
	}
	fake.current = candidate
	state, err := service.Reconcile(context.Background())
	if err != nil || state != "republished" || fake.current.Version <= candidate.Version || len(fake.current.SubscriptionSets) != 1 || fake.current.SubscriptionSets[0].Domains[0] != "new.example" {
		t.Fatalf("state=%q snapshot=%+v err=%v", state, fake.current, err)
	}
}

func TestBindingCreateAndGroupDeleteAreSerialized(t *testing.T) {
	store, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO admins(id,username,password_hash,created_at_ms,updated_at_ms) VALUES(1,'tester','hash',1,1)`); err != nil {
		t.Fatal(err)
	}
	fake := &fakeMosdns{registry: mosdnsclient.RegistrySnapshot{SchemaVersion: 1, Version: 1, DefaultGroupID: "default_dns", Groups: []mosdnsclient.UpstreamGroup{{ID: "default_dns", Enabled: true}, {ID: "office_dns", Enabled: true}}}}
	lock := &coordination.UpstreamBindings{}
	ruleService := New(store, fake, lock)
	operationService := operations.New(store.DB(), ":memory:", fake, nil, lock)
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, _, err := ruleService.CreateUploadSubscription(context.Background(), routeSubscriptionInput("office_dns", 100), "route.txt", []byte("route.example\n"), 1, "create", "")
		results <- err
	}()
	go func() {
		<-start
		_, err := operationService.DeleteUpstreamGroup(context.Background(), "office_dns", 1, 1, "delete", "")
		results <- err
	}()
	close(start)
	firstErr, secondErr := <-results, <-results
	successes := 0
	for _, resultErr := range []error{firstErr, secondErr} {
		if resultErr == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("create/delete results: %v, %v", firstErr, secondErr)
	}
	registry, err := fake.RegistryStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	groupExists := false
	for _, group := range registry.Groups {
		groupExists = groupExists || group.ID == "office_dns"
	}
	var bindings int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM subscription_bindings WHERE upstream_group_id='office_dns'`).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if groupExists != (bindings == 1) {
		t.Fatalf("group exists=%t bindings=%d", groupExists, bindings)
	}
}

func TestBindingCreateAndGroupDisableAreSerialized(t *testing.T) {
	store, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO admins(id,username,password_hash,created_at_ms,updated_at_ms) VALUES(1,'tester','hash',1,1)`); err != nil {
		t.Fatal(err)
	}
	office := mosdnsclient.UpstreamGroup{ID: "office_dns", Name: "Office", Enabled: true, Mode: "race", Concurrent: 1}
	fake := &fakeMosdns{registry: mosdnsclient.RegistrySnapshot{SchemaVersion: 1, Version: 1, DefaultGroupID: "default_dns", Groups: []mosdnsclient.UpstreamGroup{{ID: "default_dns", Enabled: true}, office}}}
	lock := &coordination.UpstreamBindings{}
	ruleService := New(store, fake, lock)
	operationService := operations.New(store.DB(), ":memory:", fake, nil, lock)
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, _, err := ruleService.CreateUploadSubscription(context.Background(), routeSubscriptionInput("office_dns", 100), "route.txt", []byte("route.example\n"), 1, "create", "")
		results <- err
	}()
	go func() {
		<-start
		office.Enabled = false
		_, err := operationService.UpdateUpstreamGroup(context.Background(), office.ID, operations.UpstreamGroupWrite{ExpectedCurrentVersion: 1, Group: office}, 1, "disable", "")
		results <- err
	}()
	close(start)
	firstErr, secondErr := <-results, <-results
	successes := 0
	for _, resultErr := range []error{firstErr, secondErr} {
		if resultErr == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("create/disable results: %v, %v", firstErr, secondErr)
	}
	registry, err := fake.RegistryStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	groupEnabled := false
	for _, group := range registry.Groups {
		if group.ID == "office_dns" {
			groupEnabled = group.Enabled
		}
	}
	var bindings int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM subscription_bindings WHERE upstream_group_id='office_dns'`).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if groupEnabled != (bindings == 1) {
		t.Fatalf("group enabled=%t bindings=%d", groupEnabled, bindings)
	}
}
