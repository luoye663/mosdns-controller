package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMigrateCreatesFinalBaselineAndIsIdempotent(t *testing.T) {
	store, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	for table, column := range map[string]string{"domain_rules": "upstream_group_id", "subscription_bindings": "upstream_group_id", "dns_stats_hourly_upstream_group": "upstream_group_id"} {
		var count int
		if err := store.DB().QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&count); err != nil || count != 1 {
			t.Fatalf("missing %s.%s: count=%d err=%v", table, column, count, err)
		}
	}
	var version int
	if err := store.DB().QueryRow(`SELECT version FROM schema_migrations`).Scan(&version); err != nil || version != baselineVersion {
		t.Fatalf("baseline version=%d err=%v", version, err)
	}
}

func TestMigrateV1PreservesRulesAndQueries(t *testing.T) {
	store, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, statement := range baselineSchema {
		statement = strings.Replace(statement, "CHECK(schema_version IN (4,5))", "CHECK(schema_version=4)", 1)
		statement = strings.Replace(statement, "category IN ('access','route','logging','answer')", "category IN ('access','route','logging')", 1)
		if strings.Contains(statement, "CREATE TABLE domain_rules") {
			statement = strings.Replace(statement, ", ipv4_addresses_json BLOB NOT NULL DEFAULT '[]', ipv6_addresses_json BLOB NOT NULL DEFAULT '[]', ttl INTEGER NOT NULL DEFAULT 0 CHECK(ttl BETWEEN 0 AND 86400)", "", 1)
			statement = strings.Replace(statement, " AND ttl=0", "", 3)
			statement = strings.Replace(statement, " OR (category='answer' AND action='static' AND upstream_group_id IS NULL AND ttl BETWEEN 1 AND 86400)", "", 1)
		}
		if strings.Contains(statement, "CREATE TABLE dns_queries") {
			statement = strings.Replace(statement, "'forward','block','local'", "'forward','block'", 1)
			statement = strings.Replace(statement, ", answer_rule_id INTEGER", "", 1)
		}
		if _, err := store.DB().Exec(statement); err != nil {
			t.Fatalf("create v1 schema: %v\n%s", err, statement)
		}
	}
	if _, err := store.DB().Exec(`INSERT INTO schema_migrations(version) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO rule_versions(version,schema_version,checksum,status,rule_count,regexp_rule_count,snapshot_json,rules_json,created_at_ms) VALUES(1,4,'v1','active',1,0,'{}','[]',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO domain_rules(id,version,category,action,match_type,pattern,normalized_pattern,priority,enabled,created_at_ms,updated_at_ms) VALUES(1,1,'access','block','full','old.example','old.example',100,1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO dns_queries(event_id,timestamp_unix_ms,client_ip,protocol,qname,qtype,qclass,rcode,route,route_source,cache_hit,snapshot_version,answer_count,latency_us,result_class,created_at_ms) VALUES('old-query',1,'192.0.2.1','udp','old.example',1,1,3,'block','dynamic_rule',0,1,0,1,'policy_block',1)`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	var ruleCount, queryCount, indexCount int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM domain_rules WHERE pattern='old.example' AND ttl=0`).Scan(&ruleCount); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM dns_queries WHERE event_id='old-query' AND answer_rule_id IS NULL`).Scan(&queryCount); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_dns_queries_time' AND tbl_name='dns_queries'`).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if ruleCount != 1 || queryCount != 1 || indexCount != 1 {
		t.Fatalf("rule=%d query=%d index=%d", ruleCount, queryCount, indexCount)
	}
}

func TestMigrateRejectsExistingLegacySchema(t *testing.T) {
	store, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB().Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY); INSERT INTO schema_migrations(version) VALUES(12); CREATE TABLE domain_rules(id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); !errors.Is(err, ErrLegacySchema) {
		t.Fatalf("migration error=%v, want ErrLegacySchema", err)
	}
}

func TestFinalRuleConstraintsRejectLegacyRouteActions(t *testing.T) {
	store, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO rule_versions(version,schema_version,checksum,status,rule_count,regexp_rule_count,snapshot_json,rules_json,created_at_ms) VALUES(1,4,'x','active',0,0,'{}','[]',1)`); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO domain_rules(id,version,category,action,upstream_group_id,match_type,pattern,normalized_pattern,priority,enabled,created_at_ms,updated_at_ms) VALUES(1,1,'route',?,?,'domain','example.com','example.com',0,1,1,1)`
	if _, err := store.DB().Exec(insert, "local", "default_dns"); err == nil {
		t.Fatal("legacy local route action accepted")
	}
	if _, err := store.DB().Exec(insert, "upstream", nil); err == nil {
		t.Fatal("route without upstream group accepted")
	}
	if _, err := store.DB().Exec(insert, "upstream", "default_dns"); err != nil {
		t.Fatalf("valid route rejected: %v", err)
	}
}

func TestBackupCreatesConsistentSQLiteFile(t *testing.T) {
	store, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO admins(username,password_hash,created_at_ms,updated_at_ms) VALUES('backup','hash',1,1)`); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "controller-backup.db")
	if err := store.Backup(context.Background(), output); err != nil {
		t.Fatal(err)
	}
	copy, err := Open(context.Background(), output)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	var count int
	if err := copy.DB().QueryRow(`SELECT COUNT(*) FROM admins WHERE username='backup'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("backup count=%d err=%v", count, err)
	}
}

func TestConcurrentWritesUseConfiguredSQLiteStrategy(t *testing.T) {
	store, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.DB().Exec(`INSERT INTO admins(username,password_hash,created_at_ms,updated_at_ms) VALUES(?,?,1,1)`, fmt.Sprintf("admin-%d", i), "hash")
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}
}
