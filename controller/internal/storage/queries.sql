-- sqlc 的输入查询。Phase 7/8 将在这些稳定表契约上扩展类型安全 CRUD 与聚合查询。
-- name: GetAdminByUsername :one
SELECT id, username, password_hash, disabled
FROM admins
WHERE username = ? COLLATE NOCASE;

-- name: GetSessionAdmin :one
SELECT a.id, a.username, s.expires_at_ms
FROM sessions AS s
JOIN admins AS a ON a.id = s.admin_id
WHERE s.token_hash = ? AND a.disabled = 0;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at_ms <= ?;
