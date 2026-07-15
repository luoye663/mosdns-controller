package rules

import (
	"context"
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
