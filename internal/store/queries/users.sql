-- name: CreateUser :one
INSERT INTO users (username, password_hash)
VALUES (?, ?)
RETURNING id, username, password_hash, avatar_key, timezone, theme, created_at;

-- name: GetUserByID :one
SELECT id, username, password_hash, avatar_key, timezone, theme, created_at
FROM users
WHERE id = ?;

-- name: GetUserByUsername :one
SELECT id, username, password_hash, avatar_key, timezone, theme, created_at
FROM users
WHERE username = ?;

-- name: ListUsers :many
SELECT id, username, password_hash, avatar_key, timezone, theme, created_at
FROM users
ORDER BY username;

-- name: CountUsers :one
SELECT COUNT(*) FROM users;

-- name: GetUserAvatarKey :one
SELECT avatar_key FROM users
WHERE id = ?;

-- name: SetUserAvatarKey :exec
UPDATE users SET avatar_key = ?
WHERE id = ?;

-- name: SetUserTimezone :exec
UPDATE users SET timezone = ?
WHERE id = ?;

-- name: SetUserTheme :exec
UPDATE users SET theme = ?
WHERE id = ?;