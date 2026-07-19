// Package queryingest 接收 mosdns 的查询审计事件，并在后台批量持久化。
package queryingest

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	maxBatchEvents        = 500
	queueCapacity         = 65_536
	batchInterval         = 500 * time.Millisecond
	batchSize             = 500
	maxAnswerIPs          = 16
	maxAnswerRecords      = 32
	maxAnswerRecordBytes  = 1024
	maxAnswerRecordsBytes = 16 * 1024
	maxAnswerDiagnostics  = 1024
	defaultDatabaseMaxGiB = 2
	sizeRetentionBatch    = 10_000
)

var latencyBucketBoundsUS = [...]int64{1_000, 5_000, 10_000, 25_000, 50_000, 100_000, 250_000, 500_000, 1_000_000, 5_000_000, 10_000_000}

// Event 与 mosdns query_audit 的 wire schema 保持独立，避免 controller 链接 GPL 代码。
type Event struct {
	SchemaVersion int    `json:"schema_version"`
	EventID       string `json:"event_id"`
	TimestampMS   int64  `json:"timestamp_unix_ms"`
	// ProcessStartedAtMS 由 mosdns 生成，用于跨进程追踪；当前 SQLite 查询表不需要单独索引该值。
	ProcessStartedAtMS     int64    `json:"process_started_at_unix_ms"`
	ClientIP               string   `json:"client_ip"`
	Protocol               string   `json:"protocol"`
	QName                  string   `json:"qname"`
	QType                  uint16   `json:"qtype"`
	QClass                 uint16   `json:"qclass"`
	RCode                  int      `json:"rcode"`
	Route                  string   `json:"route"`
	RouteSource            string   `json:"route_source"`
	UpstreamGroup          string   `json:"upstream_group"`
	UpstreamTag            string   `json:"upstream_tag"`
	CacheHit               bool     `json:"cache_hit"`
	Snapshot               uint64   `json:"snapshot_version"`
	AccessRuleID           int64    `json:"access_rule_id"`
	RouteRuleID            int64    `json:"route_rule_id"`
	SubscriptionSourceID   int64    `json:"subscription_source_id"`
	SubscriptionSourceName string   `json:"subscription_source_name"`
	SubscriptionCategories []string `json:"subscription_categories"`
	AnswerCount            int      `json:"answer_count"`
	AnswerMinTTLSeconds    *int     `json:"answer_min_ttl_seconds"`
	AnswerIPs              []string `json:"answer_ips"`
	AnswerRecords          []string `json:"answer_records"`
	LatencyUS              int64    `json:"latency_us"`
	ErrorCode              string   `json:"error_code"`
	ErrorText              string   `json:"error_text"`
}

type Batch struct {
	SchemaVersion int     `json:"schema_version"`
	SenderID      string  `json:"sender_id"`
	SentAtMS      int64   `json:"sent_at_unix_ms"`
	Events        []Event `json:"events"`
}

type Query struct {
	Limit                                          int
	FromMS, ToMS                                   int64
	QType                                          int
	RCode                                          *int
	CacheHit, HasError                             *bool
	Cursor, ClientIP, Route, QName                 string
	QNameMatch, RouteSource, UpstreamTag, Protocol string
}
type Page struct {
	Items      []StoredEvent `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}
type StoredEvent struct {
	ID                     int64    `json:"id"`
	EventID                string   `json:"event_id"`
	TimestampMS            int64    `json:"timestamp_unix_ms"`
	ClientIP               string   `json:"client_ip"`
	Protocol               string   `json:"protocol"`
	QName                  string   `json:"qname"`
	QType                  int      `json:"qtype"`
	QClass                 int      `json:"qclass"`
	RCode                  int      `json:"rcode"`
	Route                  string   `json:"route"`
	RouteSource            string   `json:"route_source"`
	UpstreamGroup          string   `json:"upstream_group"`
	UpstreamTag            string   `json:"upstream_tag"`
	CacheHit               bool     `json:"cache_hit"`
	Snapshot               uint64   `json:"snapshot_version"`
	AccessRuleID           int64    `json:"access_rule_id"`
	RouteRuleID            int64    `json:"route_rule_id"`
	SubscriptionSourceID   int64    `json:"subscription_source_id"`
	SubscriptionSourceName string   `json:"subscription_source_name"`
	SubscriptionCategories []string `json:"subscription_categories"`
	AnswerCount            int      `json:"answer_count"`
	AnswerMinTTLSeconds    *int     `json:"answer_min_ttl_seconds"`
	LatencyUS              int64    `json:"latency_us"`
	ErrorCode              string   `json:"error_code"`
	ErrorText              string   `json:"error_text"`
	DeviceName             string   `json:"device_name"`
}

type Subscriber struct {
	C     <-chan StoredEvent
	close func()
}

// AnswerDiagnostics is short-lived response data that is intentionally never persisted.
type AnswerDiagnostics struct {
	AnswerIPs     []string `json:"answer_ips"`
	AnswerRecords []string `json:"answer_records"`
}

type subscription struct {
	ch    chan StoredEvent
	query Query
}

func (s Subscriber) Close() {
	if s.close != nil {
		s.close()
	}
}

type metrics struct {
	received, rejected, queueFull, written, sseDropped prometheus.Counter
	queue                                              prometheus.Gauge
	subscribers                                        prometheus.Gauge
}
type Service struct {
	db                     *sql.DB
	token                  []byte
	queue                  chan Event
	enqueueMu              sync.Mutex
	done                   chan struct{}
	wg                     sync.WaitGroup
	mu                     sync.Mutex
	nextSubscriber         uint64
	subscribers            map[uint64]subscription
	answerDiagnostics      map[string]AnswerDiagnostics
	answerDiagnosticsOrder []string
	metrics                metrics
	dbPath                 string
	retentionDays          atomic.Int64
	databaseMaxBytes       atomic.Int64
	retentionMu            sync.Mutex
	maintenanceOnce        sync.Once
}

func New(db *sql.DB, token string, dbPath string) *Service {
	s := &Service{db: db, token: []byte(token), dbPath: dbPath, queue: make(chan Event, queueCapacity), done: make(chan struct{}), subscribers: make(map[uint64]subscription), answerDiagnostics: make(map[string]AnswerDiagnostics), answerDiagnosticsOrder: make([]string, 0, maxAnswerDiagnostics)}
	s.retentionDays.Store(7)
	s.databaseMaxBytes.Store(int64(defaultDatabaseMaxGiB) << 30)
	s.metrics = newMetrics()
	s.wg.Add(1)
	go s.writer()
	return s
}

// StartMaintenance begins retention after persisted settings have been loaded.
func (s *Service) StartMaintenance() {
	s.maintenanceOnce.Do(func() {
		s.wg.Add(1)
		go s.maintenance()
	})
}

func newMetrics() metrics {
	newCounter := func(name, help string) prometheus.Counter {
		c := prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
		_ = prometheus.Register(c)
		return c
	}
	newGauge := func(name, help string) prometheus.Gauge {
		g := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
		_ = prometheus.Register(g)
		return g
	}
	return metrics{received: newCounter("controller_query_ingest_received_total", "Accepted query events."), rejected: newCounter("controller_query_ingest_rejected_total", "Rejected query batches."), queueFull: newCounter("controller_query_ingest_queue_full_total", "Batches rejected because the queue was full."), written: newCounter("controller_query_ingest_written_total", "New query events committed."), sseDropped: newCounter("controller_sse_slow_clients_total", "SSE clients disconnected for being slow."), queue: newGauge("controller_query_ingest_queue_size", "Queued query events."), subscribers: newGauge("controller_sse_subscribers", "Connected query SSE subscribers.")}
}

func (s *Service) Authorized(header string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got := []byte(strings.TrimSpace(strings.TrimPrefix(header, prefix)))
	return len(got) == len(s.token) && subtle.ConstantTimeCompare(got, s.token) == 1
}

// Enqueue 只做内存操作；队列没有足够空间时整个 batch 被拒绝，发送端可重试。
func (s *Service) Enqueue(batch Batch) error {
	if err := validateBatch(batch); err != nil {
		s.metrics.rejected.Inc()
		return err
	}
	// 此锁只覆盖 channel 的容量检查和复制，不涉及 SQLite 或网络 I/O。
	// 一个 batch 因此要么完整接受，要么完整返回过载，避免重试出现半批次。
	s.enqueueMu.Lock()
	defer s.enqueueMu.Unlock()
	if len(s.queue)+len(batch.Events) > cap(s.queue) {
		s.metrics.queueFull.Inc()
		return ErrOverloaded
	}
	for _, event := range batch.Events {
		select {
		case s.queue <- event:
			s.metrics.received.Inc()
			s.metrics.queue.Inc()
		default:
			s.metrics.queueFull.Inc()
			return ErrOverloaded
		}
	}
	return nil
}

var ErrOverloaded = errors.New("ingestion queue is full")

func validateBatch(batch Batch) error {
	if batch.SchemaVersion != 1 || len(batch.SenderID) == 0 || len(batch.SenderID) > 128 || batch.SentAtMS <= 0 || len(batch.Events) == 0 || len(batch.Events) > maxBatchEvents {
		return errors.New("invalid event batch")
	}
	for _, e := range batch.Events {
		if err := validateEvent(e); err != nil {
			return err
		}
	}
	return nil
}
func validateEvent(e Event) error {
	if e.SchemaVersion != 1 || len(e.EventID) == 0 || len(e.EventID) > 128 || e.TimestampMS <= 0 {
		return errors.New("invalid event")
	}
	if _, err := netip.ParseAddr(e.ClientIP); err != nil || len(e.QName) == 0 || len(e.QName) > 253 || strings.ToLower(strings.TrimSuffix(e.QName, ".")) != e.QName {
		return errors.New("invalid event fields")
	}
	if e.QType == 0 || e.QClass == 0 || e.RCode < 0 || e.RCode > 23 || e.LatencyUS < 0 || e.AnswerCount < 0 || (e.AnswerMinTTLSeconds != nil && (*e.AnswerMinTTLSeconds < 0 || *e.AnswerMinTTLSeconds > 1<<32-1)) || e.Snapshot > uint64(^uint64(0)>>1) {
		return errors.New("invalid event values")
	}
	if e.Protocol != "udp" && e.Protocol != "tcp" || (e.Route != "local" && e.Route != "remote" && e.Route != "block") || (e.RouteSource != "default" && e.RouteSource != "dynamic_rule" && e.RouteSource != "subscription") {
		return errors.New("invalid event route")
	}
	for _, field := range []struct {
		v string
		n int
	}{{e.UpstreamGroup, 128}, {e.UpstreamTag, 128}, {e.ErrorCode, 128}, {e.ErrorText, 4096}} {
		if len(field.v) > field.n {
			return errors.New("event field exceeds limit")
		}
	}
	if len(e.AnswerIPs) > maxAnswerIPs {
		return errors.New("too many answer IPs")
	}
	for _, ip := range e.AnswerIPs {
		if _, err := netip.ParseAddr(ip); err != nil {
			return errors.New("invalid answer IP")
		}
	}
	if len(e.AnswerRecords) > maxAnswerRecords {
		return errors.New("too many answer records")
	}
	recordBytes := 0
	for _, record := range e.AnswerRecords {
		if len(record) == 0 || len(record) > maxAnswerRecordBytes {
			return errors.New("invalid answer record")
		}
		recordBytes += len(record)
		if recordBytes > maxAnswerRecordsBytes {
			return errors.New("answer records exceed size limit")
		}
	}
	return nil
}

func (s *Service) writer() {
	defer s.wg.Done()
	ticker := time.NewTicker(batchInterval)
	defer ticker.Stop()
	events := make([]Event, 0, batchSize)
	flush := func() {
		if len(events) == 0 {
			return
		}
		committed, err := s.persist(context.Background(), events)
		if err == nil {
			s.publish(committed)
		}
		events = events[:0]
	}
	for {
		select {
		case e := <-s.queue:
			s.metrics.queue.Dec()
			events = append(events, e)
			if len(events) == batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-s.done:
			for {
				select {
				case e := <-s.queue:
					s.metrics.queue.Dec()
					events = append(events, e)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (s *Service) persist(ctx context.Context, events []Event) ([]StoredEvent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	inserted := make([]Event, 0, len(events))
	stored := make([]StoredEvent, 0, len(events))
	now := time.Now().UnixMilli()
	for _, e := range events {
		categories, err := json.Marshal(e.SubscriptionCategories)
		if err != nil {
			return nil, err
		}
		result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO dns_queries(event_id,timestamp_unix_ms,client_ip,protocol,qname,qtype,qclass,rcode,route,route_source,upstream_group,upstream_tag,cache_hit,snapshot_version,access_rule_id,route_rule_id,subscription_source_id,subscription_source_name,subscription_categories_json,answer_count,answer_min_ttl_seconds,latency_us,error_code,error_text,created_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, e.EventID, e.TimestampMS, e.ClientIP, e.Protocol, e.QName, e.QType, e.QClass, e.RCode, e.Route, e.RouteSource, e.UpstreamGroup, e.UpstreamTag, boolInt(e.CacheHit), e.Snapshot, e.AccessRuleID, e.RouteRuleID, e.SubscriptionSourceID, e.SubscriptionSourceName, string(categories), e.AnswerCount, e.AnswerMinTTLSeconds, e.LatencyUS, e.ErrorCode, e.ErrorText, now)
		if err != nil {
			return nil, err
		}
		n, _ := result.RowsAffected()
		if n == 1 {
			inserted = append(inserted, e)
			item := fromEvent(e)
			item.ID, _ = result.LastInsertId()
			stored = append(stored, item)
		}
	}
	for _, e := range inserted {
		if err := s.aggregate(tx, e, now); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO system_state(key,value_json,updated_at_ms) VALUES('last_successful_ingest_at',?,?) ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json,updated_at_ms=excluded.updated_at_ms`, strconv.FormatInt(now, 10), now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.cacheAnswerDiagnostics(inserted)
	for i := range stored {
		// Live events use the same display name preference as paged query results.
		_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(display_name,''),hostname,'') FROM devices WHERE ip=?`, stored[i].ClientIP).Scan(&stored[i].DeviceName)
	}
	s.metrics.written.Add(float64(len(stored)))
	return stored, nil
}

// aggregate 与原始 INSERT 使用同一事务，因此重试的 event_id 绝不会重复计数。
func (s *Service) aggregate(tx *sql.Tx, e Event, now int64) error {
	if _, err := tx.Exec(`INSERT INTO devices(ip,first_seen_at_ms,last_seen_at_ms,updated_at_ms) VALUES(?,?,?,?) ON CONFLICT(ip) DO UPDATE SET last_seen_at_ms=MAX(last_seen_at_ms,excluded.last_seen_at_ms),updated_at_ms=excluded.updated_at_ms`, e.ClientIP, e.TimestampMS, e.TimestampMS, now); err != nil {
		return err
	}
	hour := e.TimestampMS / 3_600_000 * 3_600_000
	errCount := 0
	if e.ErrorCode != "" || e.RCode != 0 {
		errCount = 1
	}
	hit := boolInt(e.CacheHit)
	statements := []struct {
		q string
		a []any
	}{
		{`INSERT INTO dns_stats_hourly_global VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(hour_start_ms,route,qtype,rcode) DO UPDATE SET query_count=query_count+1,error_count=error_count+excluded.error_count,cache_hit_count=cache_hit_count+excluded.cache_hit_count,latency_sum_us=latency_sum_us+excluded.latency_sum_us,latency_max_us=MAX(latency_max_us,excluded.latency_max_us)`, []any{hour, e.Route, e.QType, e.RCode, 1, errCount, hit, e.LatencyUS, e.LatencyUS}},
		{`INSERT INTO dns_stats_hourly_domain VALUES(?,?,?,?,?,?,?) ON CONFLICT(hour_start_ms,qname,route) DO UPDATE SET query_count=query_count+1,error_count=error_count+excluded.error_count,cache_hit_count=cache_hit_count+excluded.cache_hit_count,latency_sum_us=latency_sum_us+excluded.latency_sum_us`, []any{hour, e.QName, e.Route, 1, errCount, hit, e.LatencyUS}},
		{`INSERT INTO dns_stats_hourly_client VALUES(?,?,?,?,?,?,?) ON CONFLICT(hour_start_ms,client_ip,route) DO UPDATE SET query_count=query_count+1,error_count=error_count+excluded.error_count,cache_hit_count=cache_hit_count+excluded.cache_hit_count,latency_sum_us=latency_sum_us+excluded.latency_sum_us`, []any{hour, e.ClientIP, e.Route, 1, errCount, hit, e.LatencyUS}},
		{`INSERT INTO dns_stats_hourly_client_domain VALUES(?,?,?,?,?) ON CONFLICT(hour_start_ms,client_ip,qname,route) DO UPDATE SET query_count=query_count+1`, []any{hour, e.ClientIP, e.QName, e.Route, 1}}}
	for _, st := range statements {
		if _, err := tx.Exec(st.q, st.a...); err != nil {
			return err
		}
	}
	upperBound := latencyBucketBoundsUS[len(latencyBucketBoundsUS)-1]
	for _, candidate := range latencyBucketBoundsUS {
		if e.LatencyUS <= candidate {
			upperBound = candidate
			break
		}
	}
	_, err := tx.Exec(`INSERT INTO dns_stats_hourly_latency_bucket(hour_start_ms,upper_bound_us,query_count) VALUES(?,?,1) ON CONFLICT(hour_start_ms,upper_bound_us) DO UPDATE SET query_count=query_count+1`, hour, upperBound)
	return err
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func fromEvent(e Event) StoredEvent {
	return StoredEvent{EventID: e.EventID, TimestampMS: e.TimestampMS, ClientIP: e.ClientIP, Protocol: e.Protocol, QName: e.QName, QType: int(e.QType), QClass: int(e.QClass), RCode: e.RCode, Route: e.Route, RouteSource: e.RouteSource, UpstreamGroup: e.UpstreamGroup, UpstreamTag: e.UpstreamTag, CacheHit: e.CacheHit, Snapshot: e.Snapshot, AccessRuleID: e.AccessRuleID, RouteRuleID: e.RouteRuleID, SubscriptionSourceID: e.SubscriptionSourceID, SubscriptionSourceName: e.SubscriptionSourceName, SubscriptionCategories: append([]string(nil), e.SubscriptionCategories...), AnswerCount: e.AnswerCount, AnswerMinTTLSeconds: e.AnswerMinTTLSeconds, LatencyUS: e.LatencyUS, ErrorCode: e.ErrorCode, ErrorText: e.ErrorText}
}

func (s *Service) Subscribe(q Query, _ string) (Subscriber, error) {
	if _, _, err := queryConditions(q); err != nil {
		return Subscriber{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextSubscriber
	s.nextSubscriber++
	ch := make(chan StoredEvent, 256)
	s.subscribers[id] = subscription{ch: ch, query: q}
	s.metrics.subscribers.Inc()
	return Subscriber{C: ch, close: func() {
		s.mu.Lock()
		if current, ok := s.subscribers[id]; ok {
			delete(s.subscribers, id)
			close(current.ch)
			s.metrics.subscribers.Dec()
		}
		s.mu.Unlock()
	}}, nil
}
func (s *Service) publish(events []StoredEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range events {
		for id, sub := range s.subscribers {
			if !queryMatches(e, sub.query) {
				continue
			}
			select {
			case sub.ch <- e:
			default:
				delete(s.subscribers, id)
				close(sub.ch)
				s.metrics.subscribers.Dec()
				s.metrics.sseDropped.Inc()
			}
		}
	}
}

func (s *Service) cacheAnswerDiagnostics(events []Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range events {
		if _, exists := s.answerDiagnostics[event.EventID]; exists {
			continue
		}
		s.answerDiagnostics[event.EventID] = AnswerDiagnostics{AnswerIPs: append([]string{}, event.AnswerIPs...), AnswerRecords: append([]string{}, event.AnswerRecords...)}
		s.answerDiagnosticsOrder = append(s.answerDiagnosticsOrder, event.EventID)
		if len(s.answerDiagnosticsOrder) > maxAnswerDiagnostics {
			oldest := s.answerDiagnosticsOrder[0]
			delete(s.answerDiagnostics, oldest)
			copy(s.answerDiagnosticsOrder, s.answerDiagnosticsOrder[1:])
			s.answerDiagnosticsOrder = s.answerDiagnosticsOrder[:len(s.answerDiagnosticsOrder)-1]
		}
	}
}

// AnswerDiagnostics returns transient response diagnostics. They are never written to SQLite.
func (s *Service) AnswerDiagnostics(eventID string) (AnswerDiagnostics, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	diagnostics, ok := s.answerDiagnostics[eventID]
	return AnswerDiagnostics{AnswerIPs: append([]string{}, diagnostics.AnswerIPs...), AnswerRecords: append([]string{}, diagnostics.AnswerRecords...)}, ok
}

func (s *Service) Queries(ctx context.Context, q Query) (Page, error) {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	where, args, err := queryConditions(q)
	if err != nil {
		return Page{}, err
	}
	if q.Cursor != "" {
		ts, id, err := decodeCursor(q.Cursor)
		if err != nil {
			return Page{}, errors.New("invalid cursor")
		}
		where = append(where, "(q.timestamp_unix_ms < ? OR (q.timestamp_unix_ms = ? AND q.id < ?))")
		args = append(args, ts, ts, id)
	}
	args = append(args, q.Limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT q.id,q.event_id,q.timestamp_unix_ms,q.client_ip,q.protocol,q.qname,q.qtype,q.qclass,COALESCE(q.rcode,0),q.route,q.route_source,COALESCE(q.upstream_group,''),COALESCE(q.upstream_tag,''),q.cache_hit,q.snapshot_version,COALESCE(q.access_rule_id,0),COALESCE(q.route_rule_id,0),COALESCE(q.subscription_source_id,0),COALESCE(q.subscription_source_name,''),q.subscription_categories_json,q.answer_count,q.answer_min_ttl_seconds,q.latency_us,COALESCE(q.error_code,''),COALESCE(q.error_text,''),COALESCE(NULLIF(d.display_name,''),d.hostname,'') FROM dns_queries q LEFT JOIN devices d ON d.ip=q.client_ip WHERE `+strings.Join(where, " AND ")+` ORDER BY q.timestamp_unix_ms DESC,q.id DESC LIMIT ?`, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	// Items 始终是数组，前端可直接使用 length 和 v-for 呈现空状态。
	page := Page{Items: make([]StoredEvent, 0)}
	for rows.Next() {
		var e StoredEvent
		var hit int
		var categories string
		if err := rows.Scan(&e.ID, &e.EventID, &e.TimestampMS, &e.ClientIP, &e.Protocol, &e.QName, &e.QType, &e.QClass, &e.RCode, &e.Route, &e.RouteSource, &e.UpstreamGroup, &e.UpstreamTag, &hit, &e.Snapshot, &e.AccessRuleID, &e.RouteRuleID, &e.SubscriptionSourceID, &e.SubscriptionSourceName, &categories, &e.AnswerCount, &e.AnswerMinTTLSeconds, &e.LatencyUS, &e.ErrorCode, &e.ErrorText, &e.DeviceName); err != nil {
			return Page{}, err
		}
		_ = json.Unmarshal([]byte(categories), &e.SubscriptionCategories)
		e.CacheHit = hit == 1
		page.Items = append(page.Items, e)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if len(page.Items) > q.Limit {
		last := page.Items[q.Limit-1]
		page.NextCursor = encodeCursor(last.TimestampMS, last.ID)
		page.Items = page.Items[:q.Limit]
	}
	return page, nil
}

func queryConditions(q Query) ([]string, []any, error) {
	if q.ClientIP != "" {
		if _, err := netip.ParseAddr(q.ClientIP); err != nil {
			return nil, nil, errors.New("invalid client_ip")
		}
	}
	if q.Route != "" && q.Route != "local" && q.Route != "remote" && q.Route != "block" {
		return nil, nil, errors.New("invalid route")
	}
	if q.QNameMatch != "" && q.QNameMatch != "exact" && q.QNameMatch != "contains" {
		return nil, nil, errors.New("invalid qname_match")
	}
	if q.QType < 0 || q.QType > 65535 {
		return nil, nil, errors.New("invalid qtype")
	}
	if q.RCode != nil && (*q.RCode < 0 || *q.RCode > 65535) {
		return nil, nil, errors.New("invalid rcode")
	}
	if q.FromMS < 0 || q.ToMS < 0 || (q.FromMS != 0 && q.ToMS != 0 && q.FromMS > q.ToMS) {
		return nil, nil, errors.New("invalid time range")
	}
	if q.QName != "" && q.QNameMatch == "contains" && (q.FromMS == 0 || q.ToMS == 0 || q.ToMS-q.FromMS > int64(7*24*time.Hour/time.Millisecond)) {
		return nil, nil, errors.New("qname contains requires a time range of at most 7 days")
	}
	where, args := []string{"1=1"}, []any{}
	add := func(condition string, value any) { where = append(where, condition); args = append(args, value) }
	if q.FromMS != 0 {
		add("q.timestamp_unix_ms>=?", q.FromMS)
	}
	if q.ToMS != 0 {
		add("q.timestamp_unix_ms<=?", q.ToMS)
	}
	if q.ClientIP != "" {
		add("q.client_ip=?", q.ClientIP)
	}
	if q.Route != "" {
		add("q.route=?", q.Route)
	}
	if q.RouteSource != "" {
		add("q.route_source=?", q.RouteSource)
	}
	if q.UpstreamTag != "" {
		add("q.upstream_tag=?", q.UpstreamTag)
	}
	if q.Protocol != "" {
		add("q.protocol=?", q.Protocol)
	}
	if q.QType != 0 {
		add("q.qtype=?", q.QType)
	}
	if q.RCode != nil {
		add("q.rcode=?", *q.RCode)
	}
	if q.CacheHit != nil {
		add("q.cache_hit=?", boolInt(*q.CacheHit))
	}
	if q.HasError != nil {
		if *q.HasError {
			where = append(where, "(COALESCE(q.error_code,'')<>'' OR COALESCE(q.rcode,0)<>0)")
		} else {
			where = append(where, "COALESCE(q.error_code,'')='' AND COALESCE(q.rcode,0)=0")
		}
	}
	if q.QName != "" {
		if q.QNameMatch == "contains" {
			add("q.qname LIKE ? ESCAPE '\\'", "%"+escapeLike(q.QName)+"%")
		} else {
			add("q.qname=?", q.QName)
		}
	}
	return where, args, nil
}

func escapeLike(value string) string {
	return strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(value)
}

func queryMatches(event StoredEvent, q Query) bool {
	if q.FromMS != 0 && event.TimestampMS < q.FromMS || q.ToMS != 0 && event.TimestampMS > q.ToMS || q.ClientIP != "" && event.ClientIP != q.ClientIP || q.Route != "" && event.Route != q.Route || q.RouteSource != "" && event.RouteSource != q.RouteSource || q.UpstreamTag != "" && event.UpstreamTag != q.UpstreamTag || q.Protocol != "" && event.Protocol != q.Protocol || q.QType != 0 && event.QType != q.QType || q.RCode != nil && event.RCode != *q.RCode || q.CacheHit != nil && event.CacheHit != *q.CacheHit {
		return false
	}
	if q.HasError != nil && (event.ErrorCode != "" || event.RCode != 0) != *q.HasError {
		return false
	}
	if q.QName == "" {
		return true
	}
	if q.QNameMatch == "contains" {
		return strings.Contains(event.QName, q.QName)
	}
	return event.QName == q.QName
}
func encodeCursor(ts, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(ts, 10) + ":" + strconv.FormatInt(id, 10)))
}
func decodeCursor(v string) (int64, int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return 0, 0, err
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 2 {
		return 0, 0, errors.New("invalid")
	}
	ts, e1 := strconv.ParseInt(parts[0], 10, 64)
	id, e2 := strconv.ParseInt(parts[1], 10, 64)
	if e1 != nil || e2 != nil || ts < 0 || id < 1 {
		return 0, 0, errors.New("invalid")
	}
	return ts, id, nil
}

func (s *Service) Summary(ctx context.Context) (map[string]any, error) {
	from := time.Now().Add(-24 * time.Hour).UnixMilli()
	var count, errors, cacheHits, latencySum, latencyMax, lastHourCount int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(query_count),0),COALESCE(SUM(error_count),0),COALESCE(SUM(cache_hit_count),0),COALESCE(SUM(latency_sum_us),0),COALESCE(MAX(latency_max_us),0) FROM dns_stats_hourly_global WHERE hour_start_ms>=?`, from).Scan(&count, &errors, &cacheHits, &latencySum, &latencyMax); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(query_count),0) FROM dns_stats_hourly_global WHERE hour_start_ms>=?`, time.Now().Add(-time.Hour).UnixMilli()).Scan(&lastHourCount); err != nil {
		return nil, err
	}
	p95, samples, err := s.latencyPercentile(ctx, from, 0.95)
	if err != nil {
		return nil, err
	}
	average := int64(0)
	if count > 0 {
		average = latencySum / count
	}
	return map[string]any{"query_count": count, "last_hour_query_count": lastHourCount, "average_latency_us": average, "p95_latency_us": p95, "p95_sample_count": samples, "max_latency_us": latencyMax, "error_count": errors, "cache_hit_count": cacheHits}, nil
}

func (s *Service) latencyPercentile(ctx context.Context, from int64, percentile float64) (int64, int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT upper_bound_us,SUM(query_count) FROM dns_stats_hourly_latency_bucket WHERE hour_start_ms>=? GROUP BY upper_bound_us ORDER BY upper_bound_us`, from)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	counts := make([]struct{ bound, count int64 }, 0, len(latencyBucketBoundsUS))
	var samples int64
	for rows.Next() {
		var item struct{ bound, count int64 }
		if err := rows.Scan(&item.bound, &item.count); err != nil {
			return 0, 0, err
		}
		counts = append(counts, item)
		samples += item.count
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	if samples == 0 {
		return 0, 0, nil
	}
	target := int64(float64(samples)*percentile + 0.999999)
	var cumulative int64
	for _, item := range counts {
		cumulative += item.count
		if cumulative >= target {
			return item.bound, samples, nil
		}
	}
	return 0, samples, nil
}
func (s *Service) Top(ctx context.Context, kind string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	table, column := "", ""
	switch kind {
	case "domains":
		table, column = "dns_stats_hourly_domain", "qname"
	case "clients":
		table, column = "dns_stats_hourly_client", "client_ip"
	case "routes":
		table, column = "dns_stats_hourly_global", "route"
	case "rcode":
		table, column = "dns_stats_hourly_global", "rcode"
	default:
		return nil, errors.New("invalid statistic")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("SELECT %s,SUM(query_count) FROM %s WHERE hour_start_ms>=? GROUP BY %s ORDER BY SUM(query_count) DESC LIMIT ?", column, table, column), time.Now().Add(-24*time.Hour).UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var key any
		var count int64
		if err := rows.Scan(&key, &count); err != nil {
			return nil, err
		}
		result = append(result, map[string]any{"value": key, "query_count": count})
	}
	return result, rows.Err()
}
func (s *Service) Latency(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT hour_start_ms,SUM(query_count),SUM(latency_sum_us),MAX(latency_max_us) FROM dns_stats_hourly_global WHERE hour_start_ms>=? GROUP BY hour_start_ms ORDER BY hour_start_ms`, time.Now().Add(-24*time.Hour).UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var hour, count, sum, max int64
		if err := rows.Scan(&hour, &count, &sum, &max); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"hour_start_ms": hour, "query_count": count, "average_latency_us": sum / count, "max_latency_us": max})
	}
	return out, rows.Err()
}

// maintenance 周期执行保留策略和 WAL checkpoint，避免写入路径承担清理成本。
func (s *Service) maintenance() {
	defer s.wg.Done()
	_ = s.retain()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = s.retain()
		case <-s.done:
			return
		}
	}
}
func (s *Service) retain() error {
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now()
	for _, item := range []struct {
		table, column string
		before        time.Time
	}{{"dns_stats_hourly_client_domain", "hour_start_ms", now.AddDate(0, 0, -90)}, {"dns_stats_hourly_domain", "hour_start_ms", now.AddDate(-1, 0, 0)}, {"dns_stats_hourly_client", "hour_start_ms", now.AddDate(-1, 0, 0)}, {"dns_stats_hourly_global", "hour_start_ms", now.AddDate(-1, 0, 0)}, {"dns_stats_hourly_latency_bucket", "hour_start_ms", now.AddDate(-1, 0, 0)}, {"admin_audit_logs", "created_at_ms", now.AddDate(-1, 0, 0)}} {
		if _, err := s.db.ExecContext(ctx, "DELETE FROM "+item.table+" WHERE "+item.column+" < ?", item.before.UnixMilli()); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM dns_queries WHERE timestamp_unix_ms < ?", now.AddDate(0, 0, -int(s.retentionDays.Load())).UnixMilli()); err != nil {
		return err
	}
	if s.databaseBytes() >= s.databaseMaxBytes.Load()*9/10 {
		if _, err := s.db.ExecContext(ctx, "DELETE FROM dns_queries WHERE id IN (SELECT id FROM dns_queries ORDER BY timestamp_unix_ms ASC LIMIT ?)", sizeRetentionBatch); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO system_state(key,value_json,updated_at_ms) VALUES('last_retention_at',?,?) ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json,updated_at_ms=excluded.updated_at_ms`, strconv.FormatInt(now.UnixMilli(), 10), now.UnixMilli())
	return err
}

func (s *Service) databaseBytes() int64 {
	if s.dbPath == "" || s.dbPath == ":memory:" {
		return 0
	}
	var size int64
	for _, path := range []string{s.dbPath, s.dbPath + "-wal"} {
		if info, err := os.Stat(path); err == nil {
			size += info.Size()
		}
	}
	return size
}
func (s *Service) SetRetentionDays(days int) error {
	if days < 1 || days > 365 {
		return errors.New("query retention days must be within 1..365")
	}
	s.retentionDays.Store(int64(days))
	return nil
}
func (s *Service) SetDatabaseMaxGiB(gib int) error {
	if gib < 1 || gib > 128 {
		return errors.New("database max size must be within 1..128 GiB")
	}
	s.databaseMaxBytes.Store(int64(gib) << 30)
	return nil
}
func (s *Service) RetentionDays() int { return int(s.retentionDays.Load()) }
func (s *Service) RetainNow() error   { return s.retain() }
func (s *Service) Close() {
	close(s.done)
	s.wg.Wait()
	s.mu.Lock()
	for id, sub := range s.subscribers {
		delete(s.subscribers, id)
		close(sub.ch)
	}
	s.mu.Unlock()
}

// QueueDepth 仅供控制面状态页读取，不参与 DNS 请求路径。
func (s *Service) QueueDepth() int { return len(s.queue) }
