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
