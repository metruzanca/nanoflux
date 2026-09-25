-- name: ListViewPrefs :many
SELECT scope, mode FROM user_view_prefs WHERE user_id = ?;

-- name: GetViewPref :one
SELECT mode FROM user_view_prefs WHERE user_id = ? AND scope = ?;

-- name: UpsertViewPref :exec
INSERT INTO user_view_prefs (user_id, scope, mode, updated_at)
VALUES (?, ?, ?, datetime('now'))
ON CONFLICT(user_id, scope) DO UPDATE SET
    mode = excluded.mode,
    updated_at = excluded.updated_at;
