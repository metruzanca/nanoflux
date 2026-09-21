-- name: CreateShare :one
INSERT INTO shared_items (item_id, token)
VALUES (?, ?)
RETURNING id, item_id, token, created_at;

-- name: GetShareByToken :one
SELECT id, item_id, token, created_at
FROM shared_items
WHERE token = ?;

-- name: GetShareByItem :one
SELECT sh.id, sh.item_id, sh.token, sh.created_at
FROM shared_items sh
JOIN items i ON i.id = sh.item_id
JOIN feeds f ON f.id = i.feed_id
WHERE f.user_id = ? AND i.id = ?;

-- name: DeleteShareByItem :exec
DELETE FROM shared_items
WHERE item_id = ? AND item_id IN (
  SELECT i.id FROM items i JOIN feeds f ON f.id = i.feed_id WHERE f.user_id = ?);