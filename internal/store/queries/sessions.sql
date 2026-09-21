-- name: CreateSession :exec
INSERT INTO sessions (token, user_id, expires_at)
VALUES (?, ?, ?);

-- name: GetUserByToken :one
SELECT u.id, u.username, u.password_hash, u.is_admin, u.avatar_key, u.timezone, u.theme, u.accent_color, u.created_at
FROM sessions se
JOIN users u ON u.id = se.user_id
WHERE se.token = ? AND se.expires_at > datetime('now');

-- name: DeleteSession :exec
DELETE FROM sessions
WHERE token = ?;

-- name: DeleteSessionsByUser :exec
DELETE FROM sessions
WHERE user_id = ?;

-- name: DeleteSessionsByUserExcept :exec
DELETE FROM sessions
WHERE user_id = ? AND token != ?;

-- name: ListSessionsByUser :many
SELECT token, created_at, expires_at
FROM sessions
WHERE user_id = ?
ORDER BY created_at DESC;

-- name: TouchSession :exec
UPDATE sessions SET expires_at = ?
WHERE token = ?;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions
WHERE expires_at <= datetime('now');