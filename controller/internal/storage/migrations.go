package storage

import (
	"context"
	"fmt"
)

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		return err
	}
	for _, migration := range []struct {
		version    int
		statements []string
	}{{1, migrationV1}, {2, migrationV2}, {3, migrationV3}, {4, migrationV4}, {5, migrationV5}, {6, migrationV6}, {7, migrationV7}, {8, migrationV8}, {9, migrationV9}, {10, migrationV10}, {11, migrationV11}, {12, migrationV12}} {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, migration.version).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, statement := range migration.statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d: %w", migration.version, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES (?)`, migration.version); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

var migrationV1 = []string{
	`CREATE TABLE admins (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE COLLATE NOCASE, password_hash TEXT NOT NULL, disabled INTEGER NOT NULL DEFAULT 0 CHECK(disabled IN (0,1)), created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL)`, `CREATE TABLE sessions (token_hash BLOB PRIMARY KEY, admin_id INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE, csrf_hash BLOB NOT NULL, client_ip TEXT, user_agent TEXT, created_at_ms INTEGER NOT NULL, last_seen_at_ms INTEGER NOT NULL, expires_at_ms INTEGER NOT NULL)`, `CREATE INDEX idx_sessions_admin ON sessions(admin_id)`, `CREATE INDEX idx_sessions_expires ON sessions(expires_at_ms)`,
	`CREATE TABLE rule_versions (version INTEGER PRIMARY KEY, schema_version INTEGER NOT NULL, checksum TEXT NOT NULL UNIQUE, status TEXT NOT NULL CHECK(status IN ('pending','unknown','active','superseded','failed')), previous_version INTEGER, rollback_from_version INTEGER, rule_count INTEGER NOT NULL, regexp_rule_count INTEGER NOT NULL, snapshot_json BLOB NOT NULL, created_by INTEGER REFERENCES admins(id) ON DELETE SET NULL, error_code TEXT, error_message TEXT, created_at_ms INTEGER NOT NULL, activated_at_ms INTEGER)`, `CREATE UNIQUE INDEX uq_rule_versions_active ON rule_versions(status) WHERE status='active'`, `CREATE INDEX idx_rule_versions_created ON rule_versions(created_at_ms DESC)`,
	`CREATE TABLE domain_rules (id INTEGER PRIMARY KEY, version INTEGER NOT NULL REFERENCES rule_versions(version), category TEXT NOT NULL CHECK(category IN ('access','route','logging')), action TEXT NOT NULL CHECK(action IN ('allow','block','local','remote','no_log')), match_type TEXT NOT NULL CHECK(match_type IN ('full','domain','regexp')), pattern TEXT NOT NULL, normalized_pattern TEXT NOT NULL, priority INTEGER NOT NULL DEFAULT 100 CHECK(priority BETWEEN 0 AND 1000), source TEXT NOT NULL DEFAULT 'manual', comment TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)), created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL, UNIQUE(normalized_pattern,match_type,category))`, `CREATE INDEX idx_domain_rules_category ON domain_rules(category,action,enabled)`, `CREATE INDEX idx_domain_rules_pattern ON domain_rules(normalized_pattern)`,
	`CREATE TABLE admin_audit_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, admin_id INTEGER REFERENCES admins(id) ON DELETE SET NULL, action TEXT NOT NULL, resource_type TEXT NOT NULL, resource_id TEXT, request_id TEXT NOT NULL, client_ip TEXT, before_json TEXT, after_json TEXT, result TEXT NOT NULL, error_code TEXT, created_at_ms INTEGER NOT NULL)`, `CREATE INDEX idx_admin_audit_created ON admin_audit_logs(created_at_ms DESC)`,
	`CREATE TABLE devices (id INTEGER PRIMARY KEY AUTOINCREMENT, ip TEXT NOT NULL UNIQUE, mac TEXT, hostname TEXT, display_name TEXT, note TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT 'observed', first_seen_at_ms INTEGER NOT NULL, last_seen_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL)`, `CREATE INDEX idx_devices_last_seen ON devices(last_seen_at_ms DESC)`,
	`CREATE TABLE dns_queries (id INTEGER PRIMARY KEY AUTOINCREMENT, event_id TEXT NOT NULL UNIQUE, timestamp_unix_ms INTEGER NOT NULL, client_ip TEXT NOT NULL, protocol TEXT, qname TEXT NOT NULL, qtype INTEGER NOT NULL, qclass INTEGER NOT NULL, rcode INTEGER, route TEXT NOT NULL, route_source TEXT NOT NULL, upstream_group TEXT, cache_hit INTEGER NOT NULL CHECK(cache_hit IN (0,1)), snapshot_version INTEGER NOT NULL, access_rule_id INTEGER, route_rule_id INTEGER, answer_count INTEGER NOT NULL DEFAULT 0, latency_us INTEGER NOT NULL, error_code TEXT, error_text TEXT, created_at_ms INTEGER NOT NULL)`, `CREATE INDEX idx_dns_queries_time ON dns_queries(timestamp_unix_ms DESC,id DESC)`, `CREATE INDEX idx_dns_queries_client_time ON dns_queries(client_ip,timestamp_unix_ms DESC,id DESC)`, `CREATE INDEX idx_dns_queries_qname_time ON dns_queries(qname,timestamp_unix_ms DESC,id DESC)`, `CREATE INDEX idx_dns_queries_route_time ON dns_queries(route,timestamp_unix_ms DESC,id DESC)`,
	`CREATE TABLE dns_stats_hourly_global (hour_start_ms INTEGER NOT NULL, route TEXT NOT NULL, qtype INTEGER NOT NULL, rcode INTEGER NOT NULL, query_count INTEGER NOT NULL, error_count INTEGER NOT NULL, cache_hit_count INTEGER NOT NULL, latency_sum_us INTEGER NOT NULL, latency_max_us INTEGER NOT NULL, PRIMARY KEY(hour_start_ms,route,qtype,rcode)) WITHOUT ROWID`,
	`CREATE TABLE dns_stats_hourly_domain (hour_start_ms INTEGER NOT NULL, qname TEXT NOT NULL, route TEXT NOT NULL, query_count INTEGER NOT NULL, error_count INTEGER NOT NULL, cache_hit_count INTEGER NOT NULL, latency_sum_us INTEGER NOT NULL, PRIMARY KEY(hour_start_ms,qname,route)) WITHOUT ROWID`,
	`CREATE TABLE dns_stats_hourly_client (hour_start_ms INTEGER NOT NULL, client_ip TEXT NOT NULL, route TEXT NOT NULL, query_count INTEGER NOT NULL, error_count INTEGER NOT NULL, cache_hit_count INTEGER NOT NULL, latency_sum_us INTEGER NOT NULL, PRIMARY KEY(hour_start_ms,client_ip,route)) WITHOUT ROWID`,
	`CREATE TABLE dns_stats_hourly_client_domain (hour_start_ms INTEGER NOT NULL, client_ip TEXT NOT NULL, qname TEXT NOT NULL, route TEXT NOT NULL, query_count INTEGER NOT NULL, PRIMARY KEY(hour_start_ms,client_ip,qname,route)) WITHOUT ROWID`,
	`CREATE TABLE system_state (key TEXT PRIMARY KEY, value_json TEXT NOT NULL, updated_at_ms INTEGER NOT NULL)`,
}

// migrationV2 为已部署的 SQLite 数据库补充精确上游标签，历史记录保持为空。
var migrationV2 = []string{
	`ALTER TABLE dns_queries ADD COLUMN upstream_tag TEXT`,
}

// migrationV3 stores one bounded latency bucket per query. Dashboard percentiles
// are calculated from this aggregate rather than scanning retained query records.
var migrationV3 = []string{
	`CREATE TABLE dns_stats_hourly_latency_bucket (hour_start_ms INTEGER NOT NULL, upper_bound_us INTEGER NOT NULL, query_count INTEGER NOT NULL, PRIMARY KEY(hour_start_ms,upper_bound_us)) WITHOUT ROWID`,
}

// migrationV4 keeps common diagnostic filters bounded by their value and time.
var migrationV4 = []string{
	`CREATE INDEX idx_dns_queries_qtype_time ON dns_queries(qtype,timestamp_unix_ms DESC,id DESC)`,
	`CREATE INDEX idx_dns_queries_rcode_time ON dns_queries(rcode,timestamp_unix_ms DESC,id DESC)`,
	`CREATE INDEX idx_dns_queries_cache_time ON dns_queries(cache_hit,timestamp_unix_ms DESC,id DESC)`,
	`CREATE INDEX idx_dns_queries_upstream_tag_time ON dns_queries(upstream_tag,timestamp_unix_ms DESC,id DESC)`,
}

// migrationV5 stores the smallest TTL in the final DNS Answer section.
var migrationV5 = []string{
	`ALTER TABLE dns_queries ADD COLUMN answer_min_ttl_seconds INTEGER`,
}

// migrationV6 adds controller-owned subscription sources. Their parsed rules
// remain in domain_rules so each successful change is part of the rule snapshot.
var migrationV6 = []string{
	`CREATE TABLE rule_subscriptions (id INTEGER PRIMARY KEY AUTOINCREMENT, category TEXT NOT NULL CHECK(category IN ('access','route')), action TEXT NOT NULL CHECK(action IN ('allow','block','local','remote')), kind TEXT NOT NULL CHECK(kind IN ('url','upload')), name TEXT NOT NULL, source_url TEXT NOT NULL DEFAULT '', refresh_interval_seconds INTEGER NOT NULL DEFAULT 86400 CHECK(refresh_interval_seconds BETWEEN 900 AND 2592000), enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)), content_checksum TEXT NOT NULL DEFAULT '', rule_count INTEGER NOT NULL DEFAULT 0, last_checked_at_ms INTEGER NOT NULL DEFAULT 0, last_success_at_ms INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL)`,
	`CREATE INDEX idx_rule_subscriptions_due ON rule_subscriptions(kind,enabled,last_checked_at_ms)`,
}

// migrationV7 stores a whole normalized source collection as one immutable
// JSON value instead of expanding every subscribed domain into domain_rules.
var migrationV7 = []string{
	`ALTER TABLE rule_subscriptions ADD COLUMN domains_json BLOB`,
}

var migrationV8 = []string{
	`ALTER TABLE dns_queries ADD COLUMN subscription_source_id INTEGER`,
	`ALTER TABLE dns_queries ADD COLUMN subscription_source_name TEXT`,
}
var migrationV9 = []string{
	`ALTER TABLE dns_queries ADD COLUMN subscription_categories_json TEXT NOT NULL DEFAULT '[]'`,
}

var migrationV10 = []string{
	`CREATE INDEX idx_admin_audit_created_id ON admin_audit_logs(created_at_ms DESC,id DESC)`,
}

// migrationV11 classifies DNS outcomes without changing the ingest wire format.
// Existing aggregate NOERROR rows are initially treated as successful, then
// corrected exactly where the retained raw rows cover the complete aggregate.
var migrationV11 = []string{
	`ALTER TABLE dns_queries ADD COLUMN result_class TEXT NOT NULL DEFAULT 'success' CHECK(result_class IN ('success','negative_answer','policy_block','processing_error'))`,
	`UPDATE dns_queries SET result_class=CASE WHEN route='block' THEN 'policy_block' WHEN COALESCE(error_code,'')<>'' OR COALESCE(error_text,'')<>'' OR COALESCE(rcode,0) NOT IN (0,3) THEN 'processing_error' WHEN COALESCE(rcode,0)=3 OR (COALESCE(rcode,0)=0 AND answer_count=0) THEN 'negative_answer' ELSE 'success' END`,
	`CREATE INDEX idx_dns_queries_result_class_time ON dns_queries(result_class,timestamp_unix_ms DESC,id DESC)`,
	`ALTER TABLE dns_stats_hourly_global ADD COLUMN success_count INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE dns_stats_hourly_global ADD COLUMN negative_answer_count INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE dns_stats_hourly_global ADD COLUMN policy_block_count INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE dns_stats_hourly_global ADD COLUMN processing_error_count INTEGER NOT NULL DEFAULT 0`,
	`UPDATE dns_stats_hourly_global SET success_count=CASE WHEN route<>'block' AND rcode=0 THEN query_count ELSE 0 END,negative_answer_count=CASE WHEN route<>'block' AND rcode=3 THEN query_count ELSE 0 END,policy_block_count=CASE WHEN route='block' THEN query_count ELSE 0 END,processing_error_count=CASE WHEN route<>'block' AND rcode NOT IN (0,3) THEN query_count ELSE 0 END,error_count=CASE WHEN route<>'block' AND rcode NOT IN (0,3) THEN query_count ELSE 0 END`,
	`CREATE TEMP TABLE migration_v11_result_counts (hour_start_ms INTEGER NOT NULL,route TEXT NOT NULL,qtype INTEGER NOT NULL,rcode INTEGER NOT NULL,query_count INTEGER NOT NULL,success_count INTEGER NOT NULL,negative_answer_count INTEGER NOT NULL,policy_block_count INTEGER NOT NULL,processing_error_count INTEGER NOT NULL,PRIMARY KEY(hour_start_ms,route,qtype,rcode)) WITHOUT ROWID`,
	`INSERT INTO migration_v11_result_counts SELECT timestamp_unix_ms/3600000*3600000,route,qtype,COALESCE(rcode,0),COUNT(*),SUM(result_class='success'),SUM(result_class='negative_answer'),SUM(result_class='policy_block'),SUM(result_class='processing_error') FROM dns_queries GROUP BY 1,2,3,4`,
	`UPDATE dns_stats_hourly_global AS g SET success_count=(SELECT success_count FROM migration_v11_result_counts m WHERE m.hour_start_ms=g.hour_start_ms AND m.route=g.route AND m.qtype=g.qtype AND m.rcode=g.rcode),negative_answer_count=(SELECT negative_answer_count FROM migration_v11_result_counts m WHERE m.hour_start_ms=g.hour_start_ms AND m.route=g.route AND m.qtype=g.qtype AND m.rcode=g.rcode),policy_block_count=(SELECT policy_block_count FROM migration_v11_result_counts m WHERE m.hour_start_ms=g.hour_start_ms AND m.route=g.route AND m.qtype=g.qtype AND m.rcode=g.rcode),processing_error_count=(SELECT processing_error_count FROM migration_v11_result_counts m WHERE m.hour_start_ms=g.hour_start_ms AND m.route=g.route AND m.qtype=g.qtype AND m.rcode=g.rcode),error_count=(SELECT processing_error_count FROM migration_v11_result_counts m WHERE m.hour_start_ms=g.hour_start_ms AND m.route=g.route AND m.qtype=g.qtype AND m.rcode=g.rcode) WHERE query_count=(SELECT query_count FROM migration_v11_result_counts m WHERE m.hour_start_ms=g.hour_start_ms AND m.route=g.route AND m.qtype=g.qtype AND m.rcode=g.rcode)`,
	`DROP TABLE migration_v11_result_counts`,
}

// migrationV12 binds an entire route subscription to one registry-owned
// upstream group. Explicit IDs keep migrated bindings stable across databases.
var migrationV12 = []string{
	`CREATE TABLE subscription_bindings (id INTEGER PRIMARY KEY AUTOINCREMENT, subscription_id INTEGER NOT NULL UNIQUE REFERENCES rule_subscriptions(id) ON DELETE CASCADE, upstream_group_id TEXT NOT NULL, priority INTEGER NOT NULL DEFAULT 100 CHECK(priority BETWEEN 0 AND 1000), created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL)`,
	`CREATE INDEX idx_subscription_bindings_group ON subscription_bindings(upstream_group_id)`,
	`INSERT INTO subscription_bindings(id,subscription_id,upstream_group_id,priority,created_at_ms,updated_at_ms) SELECT id,id,CASE action WHEN 'local' THEN 'local_dns' ELSE 'remote_dns' END,100,created_at_ms,updated_at_ms FROM rule_subscriptions WHERE category='route' AND action IN ('local','remote') ORDER BY id`,
	`ALTER TABLE dns_queries ADD COLUMN subscription_binding_id INTEGER NOT NULL DEFAULT 0`,
}
