// Package queryingest 接收 mosdns 的查询审计事件，并在后台批量持久化。
package queryingest

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	maxBatchEvents = 500
	queueCapacity  = 65_536
	batchInterval  = 500 * time.Millisecond
	batchSize      = 500
)

// Event 与 mosdns query_audit 的 wire schema 保持独立，避免 controller 链接 GPL 代码。
type Event struct {
	SchemaVersion int    `json:"schema_version"`
	EventID       string `json:"event_id"`
	TimestampMS   int64  `json:"timestamp_unix_ms"`
	ClientIP      string `json:"client_ip"`
	Protocol      string `json:"protocol"`
	QName         string `json:"qname"`
	QType         uint16 `json:"qtype"`
	QClass        uint16 `json:"qclass"`
	RCode         int    `json:"rcode"`
	Route         string `json:"route"`
	RouteSource   string `json:"route_source"`
	UpstreamGroup string `json:"upstream_group"`
	UpstreamTag   string `json:"upstream_tag"`
	CacheHit      bool   `json:"cache_hit"`
	Snapshot      uint64 `json:"snapshot_version"`
	AccessRuleID  int64  `json:"access_rule_id"`
	RouteRuleID   int64  `json:"route_rule_id"`
	AnswerCount   int    `json:"answer_count"`
	LatencyUS     int64  `json:"latency_us"`
	ErrorCode     string `json:"error_code"`
	ErrorText     string `json:"error_text"`
}

type Batch struct {
	SchemaVersion int     `json:"schema_version"`
	SenderID      string  `json:"sender_id"`
	SentAtMS      int64   `json:"sent_at_unix_ms"`
	Events        []Event `json:"events"`
}

type Query struct {
	Limit                          int
	Cursor, ClientIP, Route, QName string
}
type Page struct {
	Items      []StoredEvent `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}
type StoredEvent struct {
	ID            int64  `json:"id"`
	EventID       string `json:"event_id"`
	TimestampMS   int64  `json:"timestamp_unix_ms"`
	ClientIP      string `json:"client_ip"`
	Protocol      string `json:"protocol"`
	QName         string `json:"qname"`
	QType         int    `json:"qtype"`
	QClass        int    `json:"qclass"`
	RCode         int    `json:"rcode"`
	Route         string `json:"route"`
	RouteSource   string `json:"route_source"`
	UpstreamGroup string `json:"upstream_group"`
	UpstreamTag   string `json:"upstream_tag"`
	CacheHit      bool   `json:"cache_hit"`
	Snapshot      uint64 `json:"snapshot_version"`
	AccessRuleID  int64  `json:"access_rule_id"`
	RouteRuleID   int64  `json:"route_rule_id"`
	AnswerCount   int    `json:"answer_count"`
	LatencyUS     int64  `json:"latency_us"`
	ErrorCode     string `json:"error_code"`
	ErrorText     string `json:"error_text"`
}

type Subscriber struct {
	C     <-chan StoredEvent
	close func()
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
	db             *sql.DB
	token          []byte
	queue          chan Event
	enqueueMu      sync.Mutex
	done           chan struct{}
	wg             sync.WaitGroup
	mu             sync.Mutex
	nextSubscriber uint64
	subscribers    map[uint64]chan StoredEvent
	metrics        metrics
	dbPath         string
}

func New(db *sql.DB, token string, dbPath string) *Service {
	s := &Service{db: db, token: []byte(token), dbPath: dbPath, queue: make(chan Event, queueCapacity), done: make(chan struct{}), subscribers: make(map[uint64]chan StoredEvent)}
	s.metrics = newMetrics()
	s.wg.Add(2)
	go s.writer()
	go s.maintenance()
	return s
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
	if e.QType == 0 || e.QClass == 0 || e.RCode < 0 || e.RCode > 23 || e.LatencyUS < 0 || e.AnswerCount < 0 || e.Snapshot > uint64(^uint64(0)>>1) {
		return errors.New("invalid event values")
	}
	if e.Protocol != "udp" && e.Protocol != "tcp" || (e.Route != "local" && e.Route != "remote" && e.Route != "block") || (e.RouteSource != "default" && e.RouteSource != "dynamic_rule") {
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
	now := time.Now().UnixMilli()
	for _, e := range events {
		result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO dns_queries(event_id,timestamp_unix_ms,client_ip,protocol,qname,qtype,qclass,rcode,route,route_source,upstream_group,upstream_tag,cache_hit,snapshot_version,access_rule_id,route_rule_id,answer_count,latency_us,error_code,error_text,created_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, e.EventID, e.TimestampMS, e.ClientIP, e.Protocol, e.QName, e.QType, e.QClass, e.RCode, e.Route, e.RouteSource, e.UpstreamGroup, e.UpstreamTag, boolInt(e.CacheHit), e.Snapshot, e.AccessRuleID, e.RouteRuleID, e.AnswerCount, e.LatencyUS, e.ErrorCode, e.ErrorText, now)
		if err != nil {
			return nil, err
		}
		n, _ := result.RowsAffected()
		if n == 1 {
			inserted = append(inserted, e)
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
	result := make([]StoredEvent, 0, len(inserted))
	for _, e := range inserted {
		result = append(result, fromEvent(e))
	}
	s.metrics.written.Add(float64(len(result)))
	return result, nil
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
	return nil
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func fromEvent(e Event) StoredEvent {
	return StoredEvent{EventID: e.EventID, TimestampMS: e.TimestampMS, ClientIP: e.ClientIP, Protocol: e.Protocol, QName: e.QName, QType: int(e.QType), QClass: int(e.QClass), RCode: e.RCode, Route: e.Route, RouteSource: e.RouteSource, UpstreamGroup: e.UpstreamGroup, UpstreamTag: e.UpstreamTag, CacheHit: e.CacheHit, Snapshot: e.Snapshot, AccessRuleID: e.AccessRuleID, RouteRuleID: e.RouteRuleID, AnswerCount: e.AnswerCount, LatencyUS: e.LatencyUS, ErrorCode: e.ErrorCode, ErrorText: e.ErrorText}
}

func (s *Service) Subscribe() Subscriber {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextSubscriber
	s.nextSubscriber++
	ch := make(chan StoredEvent, 256)
	s.subscribers[id] = ch
	s.metrics.subscribers.Inc()
	return Subscriber{C: ch, close: func() {
		s.mu.Lock()
		if current, ok := s.subscribers[id]; ok {
			delete(s.subscribers, id)
			close(current)
			s.metrics.subscribers.Dec()
		}
		s.mu.Unlock()
	}}
}
func (s *Service) publish(events []StoredEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range events {
		for id, ch := range s.subscribers {
			select {
			case ch <- e:
			default:
				delete(s.subscribers, id)
				close(ch)
				s.metrics.subscribers.Dec()
				s.metrics.sseDropped.Inc()
			}
		}
	}
}

func (s *Service) Queries(ctx context.Context, q Query) (Page, error) {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	if q.ClientIP != "" {
		if _, err := netip.ParseAddr(q.ClientIP); err != nil {
			return Page{}, errors.New("invalid client_ip")
		}
	}
	if q.Route != "" && q.Route != "local" && q.Route != "remote" && q.Route != "block" {
		return Page{}, errors.New("invalid route")
	}
	where := []string{"1=1"}
	args := []any{}
	if q.ClientIP != "" {
		where = append(where, "client_ip=?")
		args = append(args, q.ClientIP)
	}
	if q.Route != "" {
		where = append(where, "route=?")
		args = append(args, q.Route)
	}
	if q.QName != "" {
		where = append(where, "qname=?")
		args = append(args, q.QName)
	}
	if q.Cursor != "" {
		ts, id, err := decodeCursor(q.Cursor)
		if err != nil {
			return Page{}, errors.New("invalid cursor")
		}
		where = append(where, "(timestamp_unix_ms < ? OR (timestamp_unix_ms = ? AND id < ?))")
		args = append(args, ts, ts, id)
	}
	args = append(args, q.Limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT id,event_id,timestamp_unix_ms,client_ip,protocol,qname,qtype,qclass,COALESCE(rcode,0),route,route_source,COALESCE(upstream_group,''),COALESCE(upstream_tag,''),cache_hit,snapshot_version,COALESCE(access_rule_id,0),COALESCE(route_rule_id,0),answer_count,latency_us,COALESCE(error_code,''),COALESCE(error_text,'') FROM dns_queries WHERE `+strings.Join(where, " AND ")+` ORDER BY timestamp_unix_ms DESC,id DESC LIMIT ?`, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	// Items 始终是数组，前端可直接使用 length 和 v-for 呈现空状态。
	page := Page{Items: make([]StoredEvent, 0)}
	for rows.Next() {
		var e StoredEvent
		var hit int
		if err := rows.Scan(&e.ID, &e.EventID, &e.TimestampMS, &e.ClientIP, &e.Protocol, &e.QName, &e.QType, &e.QClass, &e.RCode, &e.Route, &e.RouteSource, &e.UpstreamGroup, &e.UpstreamTag, &hit, &e.Snapshot, &e.AccessRuleID, &e.RouteRuleID, &e.AnswerCount, &e.LatencyUS, &e.ErrorCode, &e.ErrorText); err != nil {
			return Page{}, err
		}
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
	var count int64
	var latency sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(query_count),0),COALESCE(SUM(latency_sum_us),0) FROM dns_stats_hourly_global WHERE hour_start_ms>=?`, time.Now().Add(-24*time.Hour).UnixMilli()).Scan(&count, &latency)
	return map[string]any{"query_count": count, "average_latency_us": func() int64 {
		if count == 0 {
			return 0
		}
		return latency.Int64 / count
	}()}, err
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
	rows, err := s.db.QueryContext(ctx, `SELECT hour_start_ms,SUM(query_count),SUM(latency_sum_us) FROM dns_stats_hourly_global WHERE hour_start_ms>=? GROUP BY hour_start_ms ORDER BY hour_start_ms`, time.Now().Add(-24*time.Hour).UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var hour, count, sum int64
		if err := rows.Scan(&hour, &count, &sum); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"hour_start_ms": hour, "average_latency_us": sum / count})
	}
	return out, rows.Err()
}

// maintenance 周期执行保留策略和 WAL checkpoint，避免写入路径承担清理成本。
func (s *Service) maintenance() {
	defer s.wg.Done()
	s.retain()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.retain()
		case <-s.done:
			return
		}
	}
}
func (s *Service) retain() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now()
	for _, item := range []struct {
		table  string
		before time.Time
	}{{"dns_stats_hourly_client_domain", now.AddDate(0, 0, -90)}, {"dns_stats_hourly_domain", now.AddDate(-1, 0, 0)}, {"dns_stats_hourly_client", now.AddDate(-1, 0, 0)}, {"dns_stats_hourly_global", now.AddDate(-1, 0, 0)}} {
		_, _ = s.db.ExecContext(ctx, "DELETE FROM "+item.table+" WHERE hour_start_ms < ?", item.before.UnixMilli())
	}
	_, _ = s.db.ExecContext(ctx, "DELETE FROM dns_queries WHERE timestamp_unix_ms < ?", now.AddDate(0, 0, -7).UnixMilli())
	if s.dbPath != "" && s.dbPath != ":memory:" {
		if info, err := os.Stat(s.dbPath); err == nil && info.Size() > int64(2<<30)*9/10 {
			_, _ = s.db.ExecContext(ctx, "DELETE FROM dns_queries WHERE id IN (SELECT id FROM dns_queries ORDER BY timestamp_unix_ms ASC LIMIT 10000)")
		}
	}
	_, _ = s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)")
	_, _ = s.db.ExecContext(ctx, `INSERT INTO system_state(key,value_json,updated_at_ms) VALUES('last_retention_at',?,?) ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json,updated_at_ms=excluded.updated_at_ms`, strconv.FormatInt(now.UnixMilli(), 10), now.UnixMilli())
}
func (s *Service) Close() {
	close(s.done)
	s.wg.Wait()
	s.mu.Lock()
	for id, ch := range s.subscribers {
		delete(s.subscribers, id)
		close(ch)
	}
	s.mu.Unlock()
}

// QueueDepth 仅供控制面状态页读取，不参与 DNS 请求路径。
func (s *Service) QueueDepth() int { return len(s.queue) }
