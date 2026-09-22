-- name: CreateUser :one
INSERT INTO users (username, password_hash)
VALUES (?, ?)
RETURNING id, username, password_hash, is_admin, avatar_key, timezone, theme, accent_color, home_config, created_at;

-- name: GetUserByID :one
SELECT id, username, password_hash, is_admin, avatar_key, timezone, theme, accent_color, home_config, created_at
FROM users
WHERE id = ?;

-- name: GetUserByUsername :one
SELECT id, username, password_hash, is_admin, avatar_key, timezone, theme, accent_color, home_config, created_at
FROM users
WHERE username = ?;

-- name: ListUsers :many
SELECT id, username, password_hash, is_admin, avatar_key, timezone, theme, accent_color, home_config, created_at
FROM users
ORDER BY username;

-- name: CountUsers :one
SELECT COUNT(*) FROM users;

-- name: CountAdmins :one
SELECT COUNT(*) FROM users WHERE is_admin = 1;

-- name: GetUserSignupBannerDismissed :one
SELECT signup_banner_dismissed FROM users WHERE id = ?;

-- name: SetUserSignupBannerDismissed :exec
UPDATE users SET signup_banner_dismissed = ?
WHERE id = ?;

-- name: GetUserAvatarKey :one
SELECT avatar_key FROM users
WHERE id = ?;

-- name: SetUserAvatarKey :exec
UPDATE users SET avatar_key = ?
WHERE id = ?;

-- name: SetUserPassword :exec
UPDATE users SET password_hash = ?
WHERE id = ?;

-- name: SetUserAdmin :exec
UPDATE users SET is_admin = ?
WHERE id = ?;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = ?;

-- name: ListUserIconKeys :many
SELECT icon_key FROM source_icons WHERE user_id = ? AND icon_key IS NOT NULL;

-- name: SetUserTimezone :exec
UPDATE users SET timezone = ?
WHERE id = ?;

-- name: SetUserTheme :exec
UPDATE users SET theme = ?
WHERE id = ?;

-- name: SetUserAccentColor :exec
UPDATE users SET accent_color = ?
WHERE id = ?;

-- name: SetUserHomeConfig :exec
UPDATE users SET home_config = ?
WHERE id = ?;