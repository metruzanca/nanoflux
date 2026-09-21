-- name: CreateFeed :one
INSERT INTO feeds (user_id, author_id, title, feed_url, home_url, description, poll_interval_sec)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING id, user_id, author_id, title, feed_url, home_url, description,
         etag, last_modified, last_polled_at, poll_interval_sec, enabled, created_at;

-- name: GetFeed :one
SELECT id, user_id, author_id, title, feed_url, home_url, description,
       etag, last_modified, last_polled_at, poll_interval_sec, enabled, created_at
FROM feeds
WHERE id = ? AND user_id = ?;

-- name: GetFeedAny :one
SELECT id, user_id, author_id, title, feed_url, home_url, description,
       etag, last_modified, last_polled_at, poll_interval_sec, enabled, created_at
FROM feeds
WHERE id = ?;

-- name: ListFeeds :many
SELECT id, user_id, author_id, title, feed_url, home_url, description,
       etag, last_modified, last_polled_at, poll_interval_sec, enabled, created_at
FROM feeds
WHERE user_id = ?
ORDER BY title;

-- name: ListFeedsByAuthor :many
SELECT id, user_id, author_id, title, feed_url, home_url, description,
       etag, last_modified, last_polled_at, poll_interval_sec, enabled, created_at
FROM feeds
WHERE user_id = ? AND author_id = ?
ORDER BY title;

-- name: ListFeedsWithUnread :many
SELECT f.id, f.user_id, f.author_id, f.title, f.feed_url, f.home_url, f.description,
       f.etag, f.last_modified, f.last_polled_at, f.poll_interval_sec, f.enabled, f.created_at,
       a.name AS author_name,
       (SELECT COUNT(*) FROM items i WHERE i.feed_id = f.id AND i.read = 0) AS unread
FROM feeds f
LEFT JOIN authors a ON a.id = f.author_id
WHERE f.user_id = ?
ORDER BY f.title;

-- name: ListFeedsByAuthorWithUnread :many
SELECT f.id, f.user_id, f.author_id, f.title, f.feed_url, f.home_url, f.description,
       f.etag, f.last_modified, f.last_polled_at, f.poll_interval_sec, f.enabled, f.created_at,
       a.name AS author_name,
       (SELECT COUNT(*) FROM items i WHERE i.feed_id = f.id AND i.read = 0) AS unread
FROM feeds f
LEFT JOIN authors a ON a.id = f.author_id
WHERE f.user_id = ? AND f.author_id = ?
ORDER BY f.title;

-- name: UpdateFeed :execresult
UPDATE feeds
SET author_id = ?, title = ?, feed_url = ?, home_url = ?, description = ?,
    poll_interval_sec = ?, enabled = ?
WHERE id = ? AND user_id = ?;

-- name: DeleteFeed :execresult
DELETE FROM feeds
WHERE id = ? AND user_id = ?;

-- name: SetFeedPollMeta :exec
UPDATE feeds
SET etag = ?, last_modified = ?, last_polled_at = ?
WHERE id = ?;

-- name: SetFeedEnabled :exec
UPDATE feeds
SET enabled = ?
WHERE id = ? AND user_id = ?;

-- name: ListFeedsDue :many
SELECT id, user_id, author_id, title, feed_url, home_url, description,
       etag, last_modified, last_polled_at, poll_interval_sec, enabled, created_at
FROM feeds
WHERE enabled = 1
  AND (last_polled_at IS NULL OR last_polled_at <= datetime(CAST(sqlc.arg('now') AS TEXT), '-' || poll_interval_sec || ' seconds'));
-- name: GetFeedByTitle :one
SELECT id, user_id, author_id, title, feed_url, home_url, description,
       etag, last_modified, last_polled_at, poll_interval_sec, enabled, created_at
FROM feeds
WHERE user_id = ? AND title = ? COLLATE NOCASE;
