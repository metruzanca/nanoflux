-- name: CreateAuthor :one
INSERT INTO authors (user_id, name, avatar_url, description)
VALUES (?, ?, ?, ?)
RETURNING id, user_id, name, avatar_url, avatar_key, last_fetched_at, description, is_system, auto_read_after_days, auto_delete_after_days, is_aggregator, created_at;

-- name: GetAuthor :one
SELECT id, user_id, name, avatar_url, avatar_key, last_fetched_at, description, is_system, auto_read_after_days, auto_delete_after_days, is_aggregator, created_at
FROM authors
WHERE id = ? AND user_id = ?;

-- name: ListAuthors :many
SELECT id, user_id, name, avatar_url, avatar_key, last_fetched_at, description, is_system, auto_read_after_days, auto_delete_after_days, is_aggregator, created_at
FROM authors
WHERE user_id = ? AND is_system = 0
ORDER BY name;

-- name: GetSystemAuthor :one
-- The hidden per-user author that owns the system feed holding saved pages.
SELECT id, user_id, name, avatar_url, avatar_key, last_fetched_at, description, is_system, auto_read_after_days, auto_delete_after_days, is_aggregator, created_at
FROM authors
WHERE user_id = ? AND is_system = 1
LIMIT 1;

-- name: CreateSystemAuthor :one
INSERT INTO authors (user_id, name, description, is_system)
VALUES (?, ?, ?, 1)
RETURNING id, user_id, name, avatar_url, avatar_key, last_fetched_at, description, is_system, auto_read_after_days, auto_delete_after_days, is_aggregator, created_at;

-- name: UpdateAuthor :execresult
UPDATE authors
SET name = ?, avatar_url = ?, description = ?
WHERE id = ? AND user_id = ? AND is_system = 0;

-- name: SetAuthorAvatarKey :execresult
UPDATE authors
SET avatar_key = ?, last_fetched_at = ?
WHERE id = ? AND user_id = ?;

-- name: SetAuthorAutoReadAfterDays :execresult
-- NULL inherits the account window; 0 disables auto-read for this author.
UPDATE authors
SET auto_read_after_days = sqlc.narg('auto_read_after_days')
WHERE id = sqlc.arg('id') AND user_id = sqlc.arg('userID') AND is_system = 0;

-- name: SetAuthorAutoDeleteAfterDays :execresult
-- NULL inherits the account delete window; 0 disables the sweep for this author.
UPDATE authors
SET auto_delete_after_days = sqlc.narg('auto_delete_after_days')
WHERE id = sqlc.arg('id') AND user_id = sqlc.arg('userID') AND is_system = 0;

-- name: SetAuthorAggregator :execresult
-- An aggregator does not claim its items and hides items claimed by other
-- authors. Toggling this rebuilds the author's claim rows outside SQL.
UPDATE authors
SET is_aggregator = sqlc.arg('isAggregator')
WHERE id = sqlc.arg('id') AND user_id = sqlc.arg('userID') AND is_system = 0;

-- name: DeleteAuthor :execresult
DELETE FROM authors
WHERE id = ? AND user_id = ? AND is_system = 0;

-- name: ListAuthorsWithFeedCount :many
-- max_rank and fav_count carry the author's magic-sort taste signal: the highest
-- manual rank across the author's member feeds (raised/neutral/lowered tier) and
-- the total favorites their feeds own. Unread counts are a separate grouped
-- query (ItemStore.UnreadCountsByAuthor), so this list does not multiply rows
-- through a join to visible_items.
SELECT a.id, a.user_id, a.name, a.avatar_url, a.avatar_key, a.last_fetched_at, a.description, a.is_system, a.auto_read_after_days, a.auto_delete_after_days, a.is_aggregator, a.created_at,
       (SELECT COUNT(*) FROM author_feeds af JOIN feeds f ON f.id = af.feed_id
          WHERE af.author_id = a.id AND f.user_id = a.user_id AND f.is_system = 0) AS feed_count,
       (SELECT COALESCE(MAX(f2.rank), 0) FROM author_feeds af2 JOIN feeds f2 ON f2.id = af2.feed_id
          WHERE af2.author_id = a.id AND f2.user_id = a.user_id) AS max_rank,
       (SELECT COUNT(*) FROM author_feeds af3
          JOIN feeds f3 ON f3.id = af3.feed_id
          JOIN items i3 ON i3.feed_id = f3.id
          WHERE af3.author_id = a.id AND f3.user_id = a.user_id AND i3.favorite = 1) AS fav_count
FROM authors a
WHERE a.user_id = ? AND a.is_system = 0
ORDER BY a.name;
-- name: CountAllAuthors :one
SELECT COUNT(*) FROM authors WHERE is_system = 0;

-- name: ListAuthorsAvatarKeys :many
SELECT avatar_key FROM authors
WHERE user_id = ? AND avatar_key IS NOT NULL;
