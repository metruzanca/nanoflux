-- name: CreateCollection :one
INSERT INTO collections (user_id, name)
VALUES (?, ?)
RETURNING id, user_id, name, created_at;

-- name: GetCollection :one
SELECT id, user_id, name, created_at
FROM collections
WHERE id = ? AND user_id = ?;

-- name: ListCollections :many
SELECT id, user_id, name, created_at
FROM collections
WHERE user_id = ?
ORDER BY name;

-- name: ListCollectionsWithCounts :many
-- Author count is live. Item counts cover the member authors' owned feeds; a
-- collection that also inherits member views gets exact counts from the
-- collections index cache (CountsByScope).
SELECT c.id, c.user_id, c.name, c.created_at,
       COUNT(DISTINCT ca.author_id) AS author_count,
       (SELECT COUNT(DISTINCT i.id) FROM visible_items i
          JOIN feeds f2 ON f2.id = i.feed_id
          JOIN collection_authors ca2 ON ca2.author_id = f2.author_id
         WHERE ca2.collection_id = c.id AND i.read = 0) AS unread_count,
       (SELECT COUNT(DISTINCT i.id) FROM visible_items i
          JOIN feeds f2 ON f2.id = i.feed_id
          JOIN collection_authors ca2 ON ca2.author_id = f2.author_id
         WHERE ca2.collection_id = c.id AND i.read = 1) AS read_count
FROM collections c
LEFT JOIN collection_authors ca ON ca.collection_id = c.id
WHERE c.user_id = ?
GROUP BY c.id
ORDER BY unread_count DESC, c.name;

-- name: DeleteCollection :execresult
DELETE FROM collections
WHERE id = ? AND user_id = ?;

-- name: RenameCollection :execresult
UPDATE collections
SET name = ?
WHERE id = ? AND user_id = ?;

-- name: VerifyCollectionAuthor :one
SELECT COUNT(*) FROM collections c
JOIN authors a ON a.user_id = c.user_id
WHERE c.id = ? AND a.id = ? AND c.user_id = ?;

-- name: AddAuthorToCollection :execresult
INSERT INTO collection_authors (collection_id, author_id)
VALUES (?, ?)
ON CONFLICT (collection_id, author_id) DO NOTHING;

-- name: RemoveAuthorFromCollection :execresult
DELETE FROM collection_authors
WHERE collection_authors.collection_id = ?
  AND collection_authors.author_id = ?
  AND collection_authors.collection_id IN (SELECT c.id FROM collections c WHERE c.user_id = ?)
  AND collection_authors.author_id IN (SELECT a.id FROM authors a WHERE a.user_id = ?);

-- name: AuthorIDsInCollection :many
SELECT ca.author_id
FROM collection_authors ca
JOIN collections c ON c.id = ca.collection_id
WHERE ca.collection_id = ? AND c.user_id = ?
ORDER BY ca.author_id;

-- name: CollectionIDsForAuthor :many
SELECT ca.collection_id
FROM collection_authors ca
JOIN collections c ON c.id = ca.collection_id
WHERE ca.author_id = ? AND c.user_id = ?
ORDER BY ca.collection_id;

-- name: ListCollectionsForAuthor :many
SELECT c.id, c.user_id, c.name, c.created_at
FROM collections c
JOIN collection_authors ca ON ca.collection_id = c.id
WHERE c.user_id = ? AND ca.author_id = ?
ORDER BY c.name;

-- name: ListFeedsInCollection :many
SELECT f.id, f.user_id, f.author_id, f.title, f.feed_url, f.home_url, f.description,
       f.etag, f.last_modified, f.last_polled_at, f.last_error, f.next_page_url, f.poll_interval_sec, f.poll_interval_auto, f.last_item_at, f.next_poll_at, f.next_poll_scheduled, f.plugin_name, f.disabled_reason, f.enabled, f.is_system, f.rank, f.cache_images, f.created_at
FROM feeds f
JOIN collection_authors ca ON ca.author_id = f.author_id
WHERE ca.collection_id = ? AND f.user_id = ? AND f.is_system = 0
ORDER BY f.title;
