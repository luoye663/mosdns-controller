package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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
