package storage

import (
	"context"
	"errors"
	"fmt"
)

const baselineVersion = 1

var ErrLegacySchema = errors.New("existing controller database uses an unsupported schema; a new empty database is required")

func (s *Store) Migrate(ctx context.Context) error {
	var tableCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&tableCount); err != nil {
		return err
	}
	if tableCount != 0 {
		return s.verifyBaseline(ctx)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range baselineSchema {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create controller schema baseline: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES (?)`, baselineVersion); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) verifyBaseline(ctx context.Context) error {
	var version int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM schema_migrations`).Scan(&version)
	if err != nil || version != baselineVersion {
		return fmt.Errorf("%w (migration marker)", ErrLegacySchema)
	}
	var extraVersions int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version<>?`, baselineVersion).Scan(&extraVersions); err != nil || extraVersions != 0 {
		return fmt.Errorf("%w (migration history)", ErrLegacySchema)
	}
	for table, column := range map[string]string{
		"domain_rules":                    "upstream_group_id",
		"rule_versions":                   "rules_json",
		"subscription_bindings":           "upstream_group_id",
		"dns_queries":                     "subscription_binding_id",
		"dns_stats_hourly_upstream_group": "upstream_group_id",
	} {
		var count int
		err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&count)
		if err != nil || count != 1 {
			return fmt.Errorf("%w (missing %s.%s)", ErrLegacySchema, table, column)
		}
	}
	return nil
}

var baselineSchema = []string{
	`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`,
	`CREATE TABLE admins (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE COLLATE NOCASE, password_hash TEXT NOT NULL, disabled INTEGER NOT NULL DEFAULT 0 CHECK(disabled IN (0,1)), created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL)`,
	`CREATE TABLE sessions (token_hash BLOB PRIMARY KEY, admin_id INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE, csrf_hash BLOB NOT NULL, client_ip TEXT, user_agent TEXT, created_at_ms INTEGER NOT NULL, last_seen_at_ms INTEGER NOT NULL, expires_at_ms INTEGER NOT NULL)`,
	`CREATE INDEX idx_sessions_admin ON sessions(admin_id)`,
	`CREATE INDEX idx_sessions_expires ON sessions(expires_at_ms)`,
	`CREATE TABLE rule_versions (version INTEGER PRIMARY KEY, schema_version INTEGER NOT NULL CHECK(schema_version=4), checksum TEXT NOT NULL UNIQUE, status TEXT NOT NULL CHECK(status IN ('pending','unknown','active','superseded','failed')), previous_version INTEGER, rollback_from_version INTEGER, rule_count INTEGER NOT NULL, regexp_rule_count INTEGER NOT NULL, snapshot_json BLOB NOT NULL, rules_json BLOB NOT NULL, created_by INTEGER REFERENCES admins(id) ON DELETE SET NULL, error_code TEXT, error_message TEXT, created_at_ms INTEGER NOT NULL, activated_at_ms INTEGER)`,
	`CREATE UNIQUE INDEX uq_rule_versions_active ON rule_versions(status) WHERE status='active'`,
	`CREATE INDEX idx_rule_versions_created ON rule_versions(created_at_ms DESC)`,
	`CREATE TABLE domain_rules (id INTEGER PRIMARY KEY, version INTEGER NOT NULL REFERENCES rule_versions(version), category TEXT NOT NULL CHECK(category IN ('access','route','logging')), action TEXT NOT NULL, upstream_group_id TEXT, match_type TEXT NOT NULL CHECK(match_type IN ('full','domain','regexp')), pattern TEXT NOT NULL, normalized_pattern TEXT NOT NULL, priority INTEGER NOT NULL DEFAULT 100 CHECK(priority BETWEEN 0 AND 1000), source TEXT NOT NULL DEFAULT 'manual', comment TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)), created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL, CHECK((category='access' AND action IN ('allow','block') AND upstream_group_id IS NULL) OR (category='route' AND action='upstream' AND upstream_group_id IS NOT NULL AND length(trim(upstream_group_id))>0) OR (category='logging' AND action='no_log' AND upstream_group_id IS NULL)), UNIQUE(normalized_pattern,match_type,category))`,
	`CREATE INDEX idx_domain_rules_category ON domain_rules(category,action,enabled)`,
	`CREATE INDEX idx_domain_rules_pattern ON domain_rules(normalized_pattern)`,
	`CREATE INDEX idx_domain_rules_upstream_group ON domain_rules(upstream_group_id) WHERE upstream_group_id IS NOT NULL`,
	`CREATE TABLE rule_subscriptions (id INTEGER PRIMARY KEY AUTOINCREMENT, category TEXT NOT NULL CHECK(category IN ('access','route')), action TEXT NOT NULL CHECK((category='access' AND action IN ('allow','block')) OR (category='route' AND action='upstream')), kind TEXT NOT NULL CHECK(kind IN ('url','upload')), name TEXT NOT NULL, source_url TEXT NOT NULL DEFAULT '', refresh_interval_seconds INTEGER NOT NULL DEFAULT 86400 CHECK(refresh_interval_seconds BETWEEN 900 AND 2592000), enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)), content_checksum TEXT NOT NULL DEFAULT '', rule_count INTEGER NOT NULL DEFAULT 0, last_checked_at_ms INTEGER NOT NULL DEFAULT 0, last_success_at_ms INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL, domains_json BLOB NOT NULL)`,
	`CREATE INDEX idx_rule_subscriptions_due ON rule_subscriptions(kind,enabled,last_checked_at_ms)`,
	`CREATE TABLE subscription_bindings (id INTEGER PRIMARY KEY AUTOINCREMENT, subscription_id INTEGER NOT NULL UNIQUE REFERENCES rule_subscriptions(id) ON DELETE CASCADE, upstream_group_id TEXT NOT NULL CHECK(length(trim(upstream_group_id))>0), priority INTEGER NOT NULL DEFAULT 100 CHECK(priority BETWEEN 0 AND 1000), created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL)`,
	`CREATE INDEX idx_subscription_bindings_group ON subscription_bindings(upstream_group_id)`,
	`CREATE TABLE admin_audit_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, admin_id INTEGER REFERENCES admins(id) ON DELETE SET NULL, action TEXT NOT NULL, resource_type TEXT NOT NULL, resource_id TEXT, request_id TEXT NOT NULL, client_ip TEXT, before_json TEXT, after_json TEXT, result TEXT NOT NULL, error_code TEXT, created_at_ms INTEGER NOT NULL)`,
	`CREATE INDEX idx_admin_audit_created ON admin_audit_logs(created_at_ms DESC)`,
	`CREATE INDEX idx_admin_audit_created_id ON admin_audit_logs(created_at_ms DESC,id DESC)`,
	`CREATE TABLE devices (id INTEGER PRIMARY KEY AUTOINCREMENT, ip TEXT NOT NULL UNIQUE, mac TEXT, hostname TEXT, display_name TEXT, note TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT 'observed', first_seen_at_ms INTEGER NOT NULL, last_seen_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL)`,
	`CREATE INDEX idx_devices_last_seen ON devices(last_seen_at_ms DESC)`,
	`CREATE TABLE dns_queries (id INTEGER PRIMARY KEY AUTOINCREMENT, event_id TEXT NOT NULL UNIQUE, timestamp_unix_ms INTEGER NOT NULL, client_ip TEXT NOT NULL, protocol TEXT, qname TEXT NOT NULL, qtype INTEGER NOT NULL, qclass INTEGER NOT NULL, rcode INTEGER, route TEXT NOT NULL CHECK(route IN ('forward','block')), route_source TEXT NOT NULL, upstream_group TEXT, upstream_tag TEXT, cache_hit INTEGER NOT NULL CHECK(cache_hit IN (0,1)), snapshot_version INTEGER NOT NULL, access_rule_id INTEGER, route_rule_id INTEGER, subscription_source_id INTEGER, subscription_binding_id INTEGER NOT NULL DEFAULT 0, subscription_source_name TEXT, subscription_categories_json TEXT NOT NULL DEFAULT '[]', answer_count INTEGER NOT NULL DEFAULT 0, answer_min_ttl_seconds INTEGER, latency_us INTEGER NOT NULL, error_code TEXT, error_text TEXT, result_class TEXT NOT NULL CHECK(result_class IN ('success','negative_answer','policy_block','processing_error')), created_at_ms INTEGER NOT NULL)`,
	`CREATE INDEX idx_dns_queries_time ON dns_queries(timestamp_unix_ms DESC,id DESC)`,
	`CREATE INDEX idx_dns_queries_client_time ON dns_queries(client_ip,timestamp_unix_ms DESC,id DESC)`,
	`CREATE INDEX idx_dns_queries_qname_time ON dns_queries(qname,timestamp_unix_ms DESC,id DESC)`,
	`CREATE INDEX idx_dns_queries_route_time ON dns_queries(route,timestamp_unix_ms DESC,id DESC)`,
	`CREATE INDEX idx_dns_queries_qtype_time ON dns_queries(qtype,timestamp_unix_ms DESC,id DESC)`,
	`CREATE INDEX idx_dns_queries_rcode_time ON dns_queries(rcode,timestamp_unix_ms DESC,id DESC)`,
	`CREATE INDEX idx_dns_queries_cache_time ON dns_queries(cache_hit,timestamp_unix_ms DESC,id DESC)`,
	`CREATE INDEX idx_dns_queries_upstream_tag_time ON dns_queries(upstream_tag,timestamp_unix_ms DESC,id DESC)`,
	`CREATE INDEX idx_dns_queries_upstream_group_time ON dns_queries(upstream_group,timestamp_unix_ms DESC,id DESC)`,
	`CREATE INDEX idx_dns_queries_result_class_time ON dns_queries(result_class,timestamp_unix_ms DESC,id DESC)`,
	`CREATE TABLE dns_stats_hourly_global (hour_start_ms INTEGER NOT NULL, route TEXT NOT NULL, qtype INTEGER NOT NULL, rcode INTEGER NOT NULL, query_count INTEGER NOT NULL, error_count INTEGER NOT NULL, cache_hit_count INTEGER NOT NULL, latency_sum_us INTEGER NOT NULL, latency_max_us INTEGER NOT NULL, success_count INTEGER NOT NULL, negative_answer_count INTEGER NOT NULL, policy_block_count INTEGER NOT NULL, processing_error_count INTEGER NOT NULL, PRIMARY KEY(hour_start_ms,route,qtype,rcode)) WITHOUT ROWID`,
	`CREATE TABLE dns_stats_hourly_domain (hour_start_ms INTEGER NOT NULL, qname TEXT NOT NULL, route TEXT NOT NULL, query_count INTEGER NOT NULL, error_count INTEGER NOT NULL, cache_hit_count INTEGER NOT NULL, latency_sum_us INTEGER NOT NULL, PRIMARY KEY(hour_start_ms,qname,route)) WITHOUT ROWID`,
	`CREATE TABLE dns_stats_hourly_client (hour_start_ms INTEGER NOT NULL, client_ip TEXT NOT NULL, route TEXT NOT NULL, query_count INTEGER NOT NULL, error_count INTEGER NOT NULL, cache_hit_count INTEGER NOT NULL, latency_sum_us INTEGER NOT NULL, PRIMARY KEY(hour_start_ms,client_ip,route)) WITHOUT ROWID`,
	`CREATE TABLE dns_stats_hourly_client_domain (hour_start_ms INTEGER NOT NULL, client_ip TEXT NOT NULL, qname TEXT NOT NULL, route TEXT NOT NULL, query_count INTEGER NOT NULL, PRIMARY KEY(hour_start_ms,client_ip,qname,route)) WITHOUT ROWID`,
	`CREATE TABLE dns_stats_hourly_upstream_group (hour_start_ms INTEGER NOT NULL, upstream_group_id TEXT NOT NULL, query_count INTEGER NOT NULL, error_count INTEGER NOT NULL, cache_hit_count INTEGER NOT NULL, latency_sum_us INTEGER NOT NULL, latency_max_us INTEGER NOT NULL, PRIMARY KEY(hour_start_ms,upstream_group_id)) WITHOUT ROWID`,
	`CREATE TABLE dns_stats_hourly_latency_bucket (hour_start_ms INTEGER NOT NULL, upper_bound_us INTEGER NOT NULL, query_count INTEGER NOT NULL, PRIMARY KEY(hour_start_ms,upper_bound_us)) WITHOUT ROWID`,
	`CREATE TABLE system_state (key TEXT PRIMARY KEY, value_json TEXT NOT NULL, updated_at_ms INTEGER NOT NULL)`,
}
