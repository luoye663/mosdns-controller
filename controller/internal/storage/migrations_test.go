package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestMigrateFromEmptyDatabase(t *testing.T) {
	store, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='admins'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("admins table count=%d err=%v", count, err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
}

func TestMigrateV1DatabaseAddsUpstreamTag(t *testing.T) {
	store, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB().Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range migrationV1 {
		if _, err := store.DB().Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.DB().Exec(`INSERT INTO schema_migrations(version) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	var columnCount, migrationCount int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM pragma_table_info('dns_queries') WHERE name='upstream_tag'`).Scan(&columnCount); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=2`).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if columnCount != 1 || migrationCount != 1 {
		t.Fatalf("upstream_tag column=%d migration 2=%d, want 1,1", columnCount, migrationCount)
	}
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM pragma_table_info('dns_queries') WHERE name='answer_min_ttl_seconds'`).Scan(&columnCount); err != nil || columnCount != 1 {
		t.Fatalf("answer_min_ttl_seconds column=%d err=%v, want 1,nil", columnCount, err)
	}
}

func TestMigrateV11BackfillsResultClassesAndErrorCounts(t *testing.T) {
	store, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB().Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	previous := [][]string{migrationV1, migrationV2, migrationV3, migrationV4, migrationV5, migrationV6, migrationV7, migrationV8, migrationV9, migrationV10}
	for index, statements := range previous {
		for _, statement := range statements {
			if _, err := store.DB().Exec(statement); err != nil {
				t.Fatalf("migration %d setup: %v", index+1, err)
			}
		}
		if _, err := store.DB().Exec(`INSERT INTO schema_migrations(version) VALUES (?)`, index+1); err != nil {
			t.Fatal(err)
		}
	}
	hour := int64(3_600_000)
	rows := []struct {
		id          string
		route       string
		rcode       int
		answerCount int
		errorCode   string
	}{
		{"success", "remote", 0, 1, ""},
		{"nodata", "remote", 0, 0, ""},
		{"nxdomain", "local", 3, 0, ""},
		{"blocked", "block", 3, 0, ""},
		{"failed", "remote", 2, 0, "DNS_PROCESSING_ERROR"},
		{"text-failed", "remote", 0, 0, ""},
	}
	for _, row := range rows {
		if _, err := store.DB().Exec(`INSERT INTO dns_queries(event_id,timestamp_unix_ms,client_ip,qname,qtype,qclass,rcode,route,route_source,cache_hit,snapshot_version,answer_count,latency_us,error_code,created_at_ms) VALUES(?,?,?,?,1,1,?,?, 'default',0,1,?,1,?,?)`, row.id, hour, "192.0.2.1", row.id+".example", row.rcode, row.route, row.answerCount, row.errorCode, hour); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.DB().Exec(`UPDATE dns_queries SET error_text='upstream transport failed' WHERE event_id='text-failed'`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		route string
		rcode int
		count int
		error int
	}{{"remote", 0, 3, 0}, {"local", 3, 1, 1}, {"block", 3, 1, 1}, {"remote", 2, 1, 1}} {
		if _, err := store.DB().Exec(`INSERT INTO dns_stats_hourly_global(hour_start_ms,route,qtype,rcode,query_count,error_count,cache_hit_count,latency_sum_us,latency_max_us) VALUES(?,?,?,?,?,?,0,1,1)`, hour, statement.route, 1, statement.rcode, statement.count, statement.error); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []struct{ id, class string }{{"success", "success"}, {"nodata", "negative_answer"}, {"nxdomain", "negative_answer"}, {"blocked", "policy_block"}, {"failed", "processing_error"}, {"text-failed", "processing_error"}} {
		var class string
		if err := store.DB().QueryRow(`SELECT result_class FROM dns_queries WHERE event_id=?`, expected.id).Scan(&class); err != nil || class != expected.class {
			t.Fatalf("event %s class=%q err=%v, want %q", expected.id, class, err, expected.class)
		}
	}
	var success, negative, blocked, failed, errors int
	if err := store.DB().QueryRow(`SELECT SUM(success_count),SUM(negative_answer_count),SUM(policy_block_count),SUM(processing_error_count),SUM(error_count) FROM dns_stats_hourly_global`).Scan(&success, &negative, &blocked, &failed, &errors); err != nil {
		t.Fatal(err)
	}
	if success != 1 || negative != 2 || blocked != 1 || failed != 2 || errors != 2 {
		t.Fatalf("aggregate classes=%d,%d,%d,%d errors=%d", success, negative, blocked, failed, errors)
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
