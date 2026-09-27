-- name: CreateSourceIcon :one
INSERT INTO source_icons (user_id, domain, icon_url)
VALUES (?, ?, ?)
RETURNING id, user_id, domain, icon_url, icon_key, last_fetched_at, created_at;

-- name: GetSourceIconByDomain :one
SELECT id, user_id, domain, icon_url, icon_key, last_fetched_at, created_at
FROM source_icons
WHERE user_id = ? AND domain = ?;

-- name: SetSourceIconKey :exec
UPDATE source_icons
SET icon_key = ?, last_fetched_at = ?
WHERE id = ? AND user_id = ?;
