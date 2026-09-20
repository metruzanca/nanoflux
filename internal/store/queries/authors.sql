-- name: CreateAuthor :one
INSERT INTO authors (user_id, name, url, avatar_url, description)
VALUES (?, ?, ?, ?, ?)
RETURNING id, user_id, name, url, avatar_url, description, created_at;

-- name: GetAuthor :one
SELECT id, user_id, name, url, avatar_url, description, created_at
FROM authors
WHERE id = ? AND user_id = ?;

-- name: ListAuthors :many
SELECT id, user_id, name, url, avatar_url, description, created_at
FROM authors
WHERE user_id = ?
ORDER BY name;

-- name: UpdateAuthor :execresult
UPDATE authors
SET name = ?, url = ?, avatar_url = ?, description = ?
WHERE id = ? AND user_id = ?;

-- name: DeleteAuthor :execresult
DELETE FROM authors
WHERE id = ? AND user_id = ?;

-- name: ListAuthorsWithFeedCount :many
SELECT a.id, a.user_id, a.name, a.url, a.avatar_url, a.description, a.created_at,
       COUNT(f.id) AS feed_count
FROM authors a
LEFT JOIN feeds f ON f.author_id = a.id AND f.user_id = a.user_id
WHERE a.user_id = ?
GROUP BY a.id
ORDER BY a.name;