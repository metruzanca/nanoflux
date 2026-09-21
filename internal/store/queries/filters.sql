-- name: ListFilters :many
SELECT id, user_id, feed_id, action, field, pattern, is_regex, created_at
FROM filters
WHERE user_id = ?
ORDER BY created_at DESC, id DESC;

-- name: GetFilter :one
SELECT id, user_id, feed_id, action, field, pattern, is_regex, created_at
FROM filters
WHERE id = ? AND user_id = ?;

-- name: ListFiltersByFeed :many
SELECT id, user_id, feed_id, action, field, pattern, is_regex, created_at
FROM filters
WHERE user_id = ? AND (feed_id IS NULL OR feed_id = ?)
ORDER BY created_at DESC, id DESC;

-- name: CreateFilter :one
INSERT INTO filters (user_id, feed_id, action, field, pattern, is_regex)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING id, user_id, feed_id, action, field, pattern, is_regex, created_at;

-- name: DeleteFilter :execresult
DELETE FROM filters
WHERE id = ? AND user_id = ?;