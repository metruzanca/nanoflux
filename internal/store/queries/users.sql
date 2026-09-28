-- name: CreateUser :one
INSERT INTO users (username, password_hash)
VALUES (?, ?)
RETURNING id, username, password_hash, is_admin, avatar_key, timezone, theme, accent_color, home_config, auto_read_after_days, hide_unread_counts, hide_unread_nav, grid_max_columns, created_at;

-- name: GetUserByID :one
SELECT id, username, password_hash, is_admin, avatar_key, timezone, theme, accent_color, home_config, auto_read_after_days, hide_unread_counts, hide_unread_nav, grid_max_columns, created_at
FROM users
WHERE id = ?;

-- name: GetUserByUsername :one
SELECT id, username, password_hash, is_admin, avatar_key, timezone, theme, accent_color, home_config, auto_read_after_days, hide_unread_counts, hide_unread_nav, grid_max_columns, created_at
FROM users
WHERE username = ?;

-- name: ListUsers :many
SELECT id, username, password_hash, is_admin, avatar_key, timezone, theme, accent_color, home_config, auto_read_after_days, hide_unread_counts, hide_unread_nav, grid_max_columns, created_at
FROM users
WHERE is_ephemeral = 0
ORDER BY username;

-- name: CountUsers :one
SELECT COUNT(*) FROM users;

-- name: CountPersistentUsers :one
-- Real (non-ephemeral) users. Demo installs must not let throwaway demo accounts
-- satisfy the "first account" bootstrap, and the admin user list hides them.
SELECT COUNT(*) FROM users WHERE is_ephemeral = 0;

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

-- name: SetUserAutoReadAfterDays :exec
UPDATE users SET auto_read_after_days = ?
WHERE id = ?;

-- name: SetUserHideUnreadCounts :exec
UPDATE users SET hide_unread_counts = ?
WHERE id = ?;

-- name: SetUserHideUnreadNav :exec
UPDATE users SET hide_unread_nav = ?
WHERE id = ?;

-- name: SetUserGridMaxColumns :exec
UPDATE users SET grid_max_columns = ?
WHERE id = ?;

-- name: GetUserEphemeralStatus :one
-- Whether a user is an ephemeral demo account and when it expires (NULL when it
-- never does). Used on every session resolve in demo mode to reject an expired
-- demo, so it is a single indexed point lookup.
SELECT is_ephemeral, expires_at FROM users WHERE id = ?;

-- name: ListExpiredEphemeralUsers :many
-- Ephemeral demo accounts whose absolute expiry has passed. A NULL/empty
-- expires_at on an ephemeral row counts as expired.
SELECT id FROM users
WHERE is_ephemeral = 1
  AND (expires_at IS NULL OR expires_at <= CAST(sqlc.arg('now') AS TEXT))
ORDER BY id;
