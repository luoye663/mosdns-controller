package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Store 使用单个 SQLite 连接。WAL 仍允许读取，同时串行化控制面的写入，
// 避免不同驱动实现下写锁竞争造成不确定的 busy 错误。
type Store struct{ db *sql.DB }

func Open(ctx context.Context, path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	if err := s.configure(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) configure(ctx context.Context) error {
	for _, statement := range []string{"PRAGMA foreign_keys = ON", "PRAGMA journal_mode = WAL", "PRAGMA synchronous = NORMAL", "PRAGMA busy_timeout = 5000", "PRAGMA temp_store = MEMORY", "PRAGMA wal_autocheckpoint = 1000"} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("sqlite pragma %q: %w", statement, err)
		}
	}
	return nil
}
func (s *Store) DB() *sql.DB  { return s.db }
func (s *Store) Close() error { return s.db.Close() }

// Backup 使用 SQLite 的 VACUUM INTO 生成一致性副本，不复制正在变化的 WAL 文件。
// 调用方必须把输出放在持久化卷或已挂载的备份目录中。
func (s *Store) Backup(ctx context.Context, output string) error {
	if output == "" {
		return fmt.Errorf("backup output is required")
	}
	if _, err := os.Stat(output); err == nil {
		return fmt.Errorf("backup output already exists: %s", output)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat backup output: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o750); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, output); err != nil {
		return fmt.Errorf("create sqlite backup: %w", err)
	}
	return nil
}
