package queryingest

import (
	"context"
	"testing"
	"time"

	"github.com/managed-dns/controller/internal/storage"
)

func testService(t *testing.T) *Service {
	t.Helper()
	store, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := New(store.DB(), "shared-token", ":memory:")
	t.Cleanup(func() { s.Close(); _ = store.Close() })
	return s
}
func validEvent(id string) Event {
	return Event{SchemaVersion: 1, EventID: id, TimestampMS: time.Now().UnixMilli(), ClientIP: "192.0.2.10", Protocol: "udp", QName: "example.com", QType: 1, QClass: 1, RCode: 0, Route: "remote", RouteSource: "default", Snapshot: 1, LatencyUS: 42}
}
func validBatch(events ...Event) Batch {
	return Batch{SchemaVersion: 1, SenderID: "mosdns-test", SentAtMS: time.Now().UnixMilli(), Events: events}
}

func TestDuplicateEventDoesNotAggregateTwice(t *testing.T) {
	s := testService(t)
	event := validEvent("event-1")
	if _, err := s.persist(context.Background(), []Event{event, event}); err != nil {
		t.Fatal(err)
	}
	var raw, aggregate int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM dns_queries`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT query_count FROM dns_stats_hourly_global`).Scan(&aggregate); err != nil {
		t.Fatal(err)
	}
	if raw != 1 || aggregate != 1 {
		t.Fatalf("raw=%d aggregate=%d, want 1,1", raw, aggregate)
	}
}

func TestPersistStoresSelectedUpstreamTag(t *testing.T) {
	s := testService(t)
	event := validEvent("upstream-tag")
	event.UpstreamGroup = "remote_dns"
	event.UpstreamTag = "remote-doh-a"
	stored, err := s.persist(context.Background(), []Event{event})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].UpstreamTag != event.UpstreamTag {
		t.Fatalf("stored upstream tag = %+v, want %q", stored, event.UpstreamTag)
	}
	page, err := s.Queries(context.Background(), Query{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].UpstreamTag != event.UpstreamTag {
		t.Fatalf("queried upstream tag = %+v, want %q", page.Items, event.UpstreamTag)
	}
}

func TestPersistRollbackDoesNotLeavePartialAggregation(t *testing.T) {
	s := testService(t)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_domain BEFORE INSERT ON dns_stats_hourly_domain BEGIN SELECT RAISE(ABORT, 'test rollback'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.persist(context.Background(), []Event{validEvent("rollback-1")}); err == nil {
		t.Fatal("expected transaction failure")
	}
	var raw, aggregate int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM dns_queries`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM dns_stats_hourly_global`).Scan(&aggregate); err != nil {
		t.Fatal(err)
	}
	if raw != 0 || aggregate != 0 {
		t.Fatalf("rollback left raw=%d aggregate=%d", raw, aggregate)
	}
}

func TestBatchValidationAndQueueOverload(t *testing.T) {
	s := testService(t)
	if err := s.Enqueue(Batch{SchemaVersion: 1, SenderID: "x", SentAtMS: 1}); err == nil {
		t.Fatal("empty batch was accepted")
	}
	tooLarge := make([]Event, maxBatchEvents+1)
	for i := range tooLarge {
		tooLarge[i] = validEvent("event-too-large")
	}
	if err := s.Enqueue(validBatch(tooLarge...)); err == nil {
		t.Fatal("large batch was accepted")
	}
	// 独立的小队列不启动 worker，稳定验证 API 的过载返回值。
	overloaded := &Service{queue: make(chan Event, 1), metrics: newMetrics()}
	overloaded.queue <- validEvent("queued")
	if err := overloaded.Enqueue(validBatch(validEvent("overloaded"))); err != ErrOverloaded {
		t.Fatalf("error=%v, want overload", err)
	}
}

func TestSlowSubscriberIsDisconnected(t *testing.T) {
	s := testService(t)
	sub := s.Subscribe()
	defer sub.Close()
	events := make([]StoredEvent, 257)
	for i := range events {
		events[i] = fromEvent(validEvent("slow-" + string(rune(i))))
	}
	s.publish(events)
	s.mu.Lock()
	count := len(s.subscribers)
	s.mu.Unlock()
	if count != 0 {
		t.Fatalf("slow subscriber retained: %d", count)
	}
}
