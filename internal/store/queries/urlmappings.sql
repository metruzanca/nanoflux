-- name: CreateUrlMapping :one
INSERT INTO url_mappings (user_id, pattern, template)
VALUES (?, ?, ?)
RETURNING id, user_id, pattern, template, created_at;

-- name: GetUrlMapping :one
SELECT id, user_id, pattern, template, created_at
FROM url_mappings
WHERE id = ? AND user_id = ?;

-- name: ListUrlMappings :many
SELECT id, user_id, pattern, template, created_at
FROM url_mappings
WHERE user_id = ?
ORDER BY id;

-- name: UpdateUrlMapping :exec
UPDATE url_mappings
SET pattern = ?, template = ?
WHERE id = ? AND user_id = ?;

-- name: DeleteUrlMapping :exec
DELETE FROM url_mappings
WHERE id = ? AND user_id = ?;
