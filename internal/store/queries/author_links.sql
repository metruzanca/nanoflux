-- name: CreateAuthorLink :one
INSERT INTO author_links (user_id, author_id, label, url)
VALUES (?, ?, ?, ?)
RETURNING id, user_id, author_id, label, url, created_at;

-- name: GetAuthorLink :one
SELECT id, user_id, author_id, label, url, created_at
FROM author_links
WHERE id = ? AND user_id = ?;

-- name: ListAuthorLinks :many
SELECT id, user_id, author_id, label, url, created_at
FROM author_links
WHERE user_id = ? AND author_id = ?
ORDER BY id;

-- name: DeleteAuthorLink :execresult
DELETE FROM author_links
WHERE id = ? AND user_id = ?;

-- name: UpdateAuthorLink :execresult
UPDATE author_links
SET label = ?, url = ?
WHERE id = ? AND user_id = ?;
