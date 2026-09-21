-- name: CreateAuthor :one
INSERT INTO authors (user_id, name, url, avatar_url, description)
VALUES (?, ?, ?, ?, ?)
RETURNING id, user_id, name, url, avatar_url, avatar_key, last_fetched_at, description, created_at;

-- name: GetAuthor :one
SELECT id, user_id, name, url, avatar_url, avatar_key, last_fetched_at, description, created_at
FROM authors
WHERE id = ? AND user_id = ?;

-- name: ListAuthors :many
SELECT id, user_id, name, url, avatar_url, avatar_key, last_fetched_at, description, created_at
FROM authors
WHERE user_id = ?
ORDER BY name;

-- name: UpdateAuthor :execresult
UPDATE authors
SET name = ?, url = ?, avatar_url = ?, description = ?
WHERE id = ? AND user_id = ?;

-- name: SetAuthorAvatarKey :execresult
UPDATE authors
SET avatar_key = ?, last_fetched_at = ?
WHERE id = ? AND user_id = ?;

-- name: DeleteAuthor :execresult
DELETE FROM authors
WHERE id = ? AND user_id = ?;

-- name: ListAuthorsWithFeedCount :many
SELECT a.id, a.user_id, a.name, a.url, a.avatar_url, a.avatar_key, a.last_fetched_at, a.description, a.created_at,
       COUNT(DISTINCT f.id) AS feed_count,
       COUNT(DISTINCT i.id) AS unread_count
FROM authors a
LEFT JOIN feeds f ON f.author_id = a.id AND f.user_id = a.user_id
LEFT JOIN items i ON i.feed_id = f.id AND i.read = 0
WHERE a.user_id = ?
GROUP BY a.id
ORDER BY a.name;
-- name: GetAuthorByName :one
SELECT id, user_id, name, url, avatar_url, avatar_key, last_fetched_at, description, created_at
FROM authors
WHERE user_id = ? AND name = ? COLLATE NOCASE;

-- name: CountAllAuthors :one
SELECT COUNT(*) FROM authors;

-- name: ListAuthorsAvatarKeys :many
SELECT avatar_key FROM authors
WHERE user_id = ? AND avatar_key IS NOT NULL;
