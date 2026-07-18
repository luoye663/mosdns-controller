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
	}{{1, migrationV1}, {2, migrationV2}, {3, migrationV3}, {4, migrationV4}, {5, migrationV5}} {
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
