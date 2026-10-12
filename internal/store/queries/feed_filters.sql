-- name: ListFeedFilters :many
-- A feed's content-filter rules, oldest first (stable order in the editor).
SELECT id, user_id, feed_id, field, op, value, created_at
FROM feed_filters
WHERE feed_id = ?
ORDER BY created_at ASC, id ASC;

-- name: CreateFeedFilter :one
INSERT INTO feed_filters (user_id, feed_id, field, op, value)
VALUES (?, ?, ?, ?, ?)
RETURNING id, user_id, feed_id, field, op, value, created_at;

-- name: DeleteFeedFilter :execresult
DELETE FROM feed_filters
WHERE id = ? AND user_id = ?;

-- name: SetFeedFilterConfig :execresult
-- Set a feed's filter mode ('off'/'block'/'allow') and combine ('any'/'all').
UPDATE feeds
SET filter_mode = ?, filter_combine = ?
WHERE id = ? AND user_id = ?;
