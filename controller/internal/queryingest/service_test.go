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

func TestEmptyQueryPageReturnsItemsArray(t *testing.T) {
	s := testService(t)
	page, err := s.Queries(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("empty page items must be a non-nil array: %#v", page.Items)
	}
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

func TestSummaryUsesHourlyAggregatesAndLatencyBuckets(t *testing.T) {
	s := testService(t)
	now := time.Now().UnixMilli()
	events := []Event{
		{SchemaVersion: 1, EventID: "summary-fast", TimestampMS: now, ClientIP: "192.0.2.10", Protocol: "udp", QName: "fast.example", QType: 1, QClass: 1, RCode: 0, Route: "remote", RouteSource: "default", Snapshot: 1, CacheHit: true, LatencyUS: 4_000},
		{SchemaVersion: 1, EventID: "summary-slow", TimestampMS: now, ClientIP: "192.0.2.11", Protocol: "udp", QName: "slow.example", QType: 1, QClass: 1, RCode: 2, Route: "local", RouteSource: "default", Snapshot: 1, LatencyUS: 80_000},
	}
	if _, err := s.persist(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	summary, err := s.Summary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary["query_count"] != int64(2) || summary["cache_hit_count"] != int64(1) || summary["error_count"] != int64(1) {
		t.Fatalf("summary=%+v", summary)
	}
	if summary["p95_latency_us"] != int64(100_000) {
		t.Fatalf("p95=%v, want bucket upper bound 100000", summary["p95_latency_us"])
	}
	if summary["p95_sample_count"] != int64(2) {
		t.Fatalf("p95 samples=%v, want 2", summary["p95_sample_count"])
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

func TestPersistStoresMinimumAnswerTTL(t *testing.T) {
	s := testService(t)
	event := validEvent("answer-min-ttl")
	event.AnswerMinTTLSeconds = intPtr(60)
	stored, err := s.persist(context.Background(), []Event{event})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].AnswerMinTTLSeconds == nil || *stored[0].AnswerMinTTLSeconds != 60 {
		t.Fatalf("stored minimum answer TTL = %+v", stored)
	}
	page, err := s.Queries(context.Background(), Query{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].AnswerMinTTLSeconds == nil || *page.Items[0].AnswerMinTTLSeconds != 60 {
		t.Fatalf("queried minimum answer TTL = %+v err=%v", page.Items, err)
	}
}

func TestAnswerDiagnosticsRemainInBoundedMemoryOnly(t *testing.T) {
	s := testService(t)
	event := validEvent("answer-diagnostics")
	event.AnswerIPs = []string{"192.0.2.1", "2001:db8::1"}
	event.AnswerRecords = []string{"example.com. 60 IN A 192.0.2.1", "example.com. 60 IN AAAA 2001:db8::1"}
	if _, err := s.persist(context.Background(), []Event{event}); err != nil {
		t.Fatal(err)
	}
	diagnostics, ok := s.AnswerDiagnostics(event.EventID)
	if !ok || len(diagnostics.AnswerIPs) != 2 || diagnostics.AnswerIPs[0] != "192.0.2.1" || diagnostics.AnswerIPs[1] != "2001:db8::1" || len(diagnostics.AnswerRecords) != 2 {
		t.Fatalf("cached answer diagnostics = %#v, exists=%t", diagnostics, ok)
	}
	var ipColumns, recordColumns int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('dns_queries') WHERE name='answer_ips'`).Scan(&ipColumns); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('dns_queries') WHERE name='answer_records'`).Scan(&recordColumns); err != nil {
		t.Fatal(err)
	}
	if ipColumns != 0 || recordColumns != 0 {
		t.Fatal("answer diagnostics must not be persisted")
	}
}

func TestAnswerIPsReturnsEmptyArrayForAnswerWithoutAddress(t *testing.T) {
	s := testService(t)
	event := validEvent("empty-answer-ips")
	if _, err := s.persist(context.Background(), []Event{event}); err != nil {
		t.Fatal(err)
	}
	diagnostics, ok := s.AnswerDiagnostics(event.EventID)
	if !ok || diagnostics.AnswerIPs == nil || diagnostics.AnswerRecords == nil || len(diagnostics.AnswerIPs) != 0 || len(diagnostics.AnswerRecords) != 0 {
		t.Fatalf("cached answer diagnostics = %#v, exists=%t", diagnostics, ok)
	}
}

func TestValidateEventRejectsOversizedAnswerDiagnostics(t *testing.T) {
	event := validEvent("oversized-answer-diagnostics")
	event.AnswerRecords = make([]string, maxAnswerRecords+1)
	for i := range event.AnswerRecords {
		event.AnswerRecords[i] = "example.com. 60 IN A 192.0.2.1"
	}
	if err := validateEvent(event); err == nil {
		t.Fatal("oversized answer records were accepted")
	}
}

func TestQueriesApplyDiagnosticFiltersAndDeviceName(t *testing.T) {
	s := testService(t)
	first := validEvent("query-filter-first")
	first.TimestampMS = time.Now().Add(-2 * time.Second).UnixMilli()
	first.QName = "one.example"
	first.CacheHit = true
	first.UpstreamTag = "remote-a"
	second := validEvent("query-filter-second")
	second.TimestampMS = time.Now().Add(-time.Second).UnixMilli()
	second.QName = "two.example"
	second.RCode = 3
	second.ErrorCode = "NXDOMAIN"
	second.Route = "local"
	if _, err := s.persist(context.Background(), []Event{first, second}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE devices SET display_name='laptop' WHERE ip=?`, first.ClientIP); err != nil {
		t.Fatal(err)
	}
	page, err := s.Queries(context.Background(), Query{FromMS: first.TimestampMS - 100, ToMS: first.TimestampMS + 100, QName: "one.example", QNameMatch: "exact", CacheHit: boolPtr(true), UpstreamTag: "remote-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].EventID != first.EventID || page.Items[0].DeviceName != "laptop" {
		t.Fatalf("filtered page=%+v", page.Items)
	}
	page, err = s.Queries(context.Background(), Query{FromMS: first.TimestampMS - 100, ToMS: second.TimestampMS + 100, QName: "two", QNameMatch: "contains", HasError: boolPtr(true)})
	if err != nil || len(page.Items) != 1 || page.Items[0].EventID != second.EventID {
		t.Fatalf("contains error filter page=%+v err=%v", page.Items, err)
	}
	if _, err := s.Queries(context.Background(), Query{QName: "example", QNameMatch: "contains"}); err == nil {
		t.Fatal("contains query without time range was accepted")
	}
}

func TestSubscribeOnlyReceivesEventsPublishedAfterSubscription(t *testing.T) {
	s := testService(t)
	first := validEvent("replay-first")
	second := validEvent("replay-second")
	second.Route = "local"
	third := validEvent("replay-third")
	stored, err := s.persist(context.Background(), []Event{first, second, third})
	if err != nil {
		t.Fatal(err)
	}
	s.publish(stored)
	sub, err := s.Subscribe(Query{Route: "remote"}, first.EventID)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	fresh := validEvent("fresh-event")
	published := fromEvent(fresh)
	s.publish([]StoredEvent{published})
	select {
	case event := <-sub.C:
		if event.EventID != fresh.EventID {
			t.Fatalf("event ID = %q, want %q", event.EventID, fresh.EventID)
		}
	case <-time.After(time.Second):
		t.Fatal("new event was not delivered")
	}
}

func boolPtr(value bool) *bool { return &value }
func intPtr(value int) *int    { return &value }

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
	sub, err := s.Subscribe(Query{}, "")
	if err != nil {
		t.Fatal(err)
	}
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
