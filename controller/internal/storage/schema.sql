-- 此文件供 sqlc 生成查询类型使用；运行时建表仍由版本化 Go 迁移原子执行。
CREATE TABLE admins (
    id INTEGER PRIMARY KEY,
    username TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    disabled INTEGER NOT NULL
);
CREATE TABLE sessions (
    token_hash BLOB PRIMARY KEY,
    admin_id INTEGER NOT NULL,
    csrf_hash BLOB NOT NULL,
    expires_at_ms INTEGER NOT NULL
);
