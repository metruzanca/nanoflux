-- name: CreateAuthor :one
INSERT INTO authors (user_id, name, avatar_url, description)
VALUES (?, ?, ?, ?)
RETURNING id, user_id, name, avatar_url, avatar_key, last_fetched_at, description, is_system, created_at;

-- name: GetAuthor :one
SELECT id, user_id, name, avatar_url, avatar_key, last_fetched_at, description, is_system, created_at
FROM authors
WHERE id = ? AND user_id = ?;

-- name: ListAuthors :many
SELECT id, user_id, name, avatar_url, avatar_key, last_fetched_at, description, is_system, created_at
FROM authors
WHERE user_id = ? AND is_system = 0
ORDER BY name;

-- name: GetSystemAuthor :one
-- The hidden per-user author that owns the system feed holding saved pages.
SELECT id, user_id, name, avatar_url, avatar_key, last_fetched_at, description, is_system, created_at
FROM authors
WHERE user_id = ? AND is_system = 1
LIMIT 1;

-- name: CreateSystemAuthor :one
INSERT INTO authors (user_id, name, description, is_system)
VALUES (?, ?, ?, 1)
RETURNING id, user_id, name, avatar_url, avatar_key, last_fetched_at, description, is_system, created_at;

-- name: UpdateAuthor :execresult
UPDATE authors
SET name = ?, avatar_url = ?, description = ?
WHERE id = ? AND user_id = ? AND is_system = 0;

-- name: SetAuthorAvatarKey :execresult
UPDATE authors
SET avatar_key = ?, last_fetched_at = ?
WHERE id = ? AND user_id = ?;

-- name: DeleteAuthor :execresult
DELETE FROM authors
WHERE id = ? AND user_id = ? AND is_system = 0;

-- name: ListAuthorsWithFeedCount :many
SELECT a.id, a.user_id, a.name, a.avatar_url, a.avatar_key, a.last_fetched_at, a.description, a.is_system, a.created_at,
       COUNT(DISTINCT f.id) AS feed_count,
       COUNT(DISTINCT i.id) AS unread_count
FROM authors a
LEFT JOIN feeds f ON f.author_id = a.id AND f.user_id = a.user_id
LEFT JOIN items i ON i.feed_id = f.id AND i.read = 0
WHERE a.user_id = ? AND a.is_system = 0
GROUP BY a.id
ORDER BY a.name;
-- name: GetAuthorByName :one
SELECT id, user_id, name, avatar_url, avatar_key, last_fetched_at, description, is_system, created_at
FROM authors
WHERE user_id = ? AND name = ? COLLATE NOCASE;

-- name: CountAllAuthors :one
SELECT COUNT(*) FROM authors WHERE is_system = 0;

-- name: ListAuthorsAvatarKeys :many
SELECT avatar_key FROM authors
WHERE user_id = ? AND avatar_key IS NOT NULL;
