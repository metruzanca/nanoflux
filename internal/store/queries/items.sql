-- name: UpsertItem :execresult
INSERT INTO items (feed_id, guid, title, link, summary, image_url, published_at, fetched_at, read, read_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (feed_id, guid) DO NOTHING;

-- name: ListItems :many
SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.image_url,
       i.published_at, i.fetched_at, i.read, i.favorite, i.read_at,
       f.title AS feed_title, f.feed_url AS feed_url,
       a.id AS author_id, a.name AS author_name
FROM items i
JOIN feeds f ON f.id = i.feed_id
LEFT JOIN authors a ON a.id = f.author_id
WHERE f.user_id = sqlc.arg('userID')
  AND (CAST(sqlc.arg('feedID') AS INTEGER) = 0 OR f.id = CAST(sqlc.arg('feedID') AS INTEGER))
  AND (CAST(sqlc.arg('authorID') AS INTEGER) = 0 OR f.author_id = CAST(sqlc.arg('authorID') AS INTEGER))
  AND (CAST(sqlc.arg('collectionID') AS INTEGER) = 0 OR i.feed_id IN (
        SELECT feed_id FROM collection_feeds WHERE collection_id = CAST(sqlc.arg('collectionID') AS INTEGER)))
  AND (CAST(sqlc.arg('unread') AS INTEGER) = 0 OR i.read = 0)
  AND (CAST(sqlc.arg('read') AS INTEGER) = 0 OR i.read = 1)
  AND (CAST(sqlc.arg('favorites') AS INTEGER) = 0 OR i.favorite = 1)
  AND (CAST(sqlc.arg('beforeID') AS INTEGER) = 0 OR
       (COALESCE(i.published_at, i.fetched_at), i.id) <
       (SELECT COALESCE(published_at, fetched_at), id FROM items WHERE id = CAST(sqlc.arg('beforeID') AS INTEGER)))
ORDER BY COALESCE(i.published_at, i.fetched_at) DESC, i.id DESC
LIMIT sqlc.arg('limit');

-- name: GetItem :one
SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.image_url,
       i.published_at, i.fetched_at, i.read, i.read_at, i.favorite
FROM items i
JOIN feeds f ON f.id = i.feed_id
WHERE i.id = ? AND f.user_id = ?;

-- name: GetItemWithFeed :one
SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.image_url,
       i.published_at, i.fetched_at, i.read, i.favorite, i.read_at,
       f.title AS feed_title, f.feed_url AS feed_url,
       a.id AS author_id, a.name AS author_name
FROM items i
JOIN feeds f ON f.id = i.feed_id
LEFT JOIN authors a ON a.id = f.author_id
WHERE i.id = ? AND f.user_id = ?;

-- name: GetItemWithFeedAny :one
SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.image_url,
       i.published_at, i.fetched_at, i.read, i.favorite, i.read_at,
       f.title AS feed_title, f.feed_url AS feed_url,
       a.id AS author_id, a.name AS author_name
FROM items i
JOIN feeds f ON f.id = i.feed_id
LEFT JOIN authors a ON a.id = f.author_id
WHERE i.id = ?;

-- name: SetItemRead :execresult
UPDATE items
SET read = ?, read_at = ?
WHERE items.id = ? AND items.feed_id IN (SELECT f.id FROM feeds f WHERE f.user_id = ?);

-- name: SetItemFavorite :execresult
UPDATE items
SET favorite = ?
WHERE items.id = ? AND items.feed_id IN (SELECT f.id FROM feeds f WHERE f.user_id = ?);

-- name: MarkAllItemsRead :exec
UPDATE items
SET read = 1, read_at = sqlc.arg('readAt')
WHERE items.feed_id IN (SELECT f.id FROM feeds f WHERE f.user_id = sqlc.arg('userID'))
  AND (CAST(sqlc.arg('feedID') AS INTEGER) = 0 OR items.feed_id = CAST(sqlc.arg('feedID') AS INTEGER));

-- name: MarkAllItemsUnread :exec
UPDATE items
SET read = 0, read_at = NULL
WHERE items.feed_id IN (SELECT f.id FROM feeds f WHERE f.user_id = sqlc.arg('userID'))
  AND (CAST(sqlc.arg('feedID') AS INTEGER) = 0 OR items.feed_id = CAST(sqlc.arg('feedID') AS INTEGER));

-- name: GetItemByFeedGuid :one
SELECT id FROM items
WHERE feed_id = ? AND guid = ?;

-- name: ListEnclosures :many
SELECT url, title, mime_type, size, sort
FROM item_enclosures
WHERE item_id = ?
ORDER BY sort;

-- name: DeleteEnclosures :exec
DELETE FROM item_enclosures
WHERE item_id = ?;

-- name: InsertEnclosure :exec
INSERT INTO item_enclosures (item_id, url, title, mime_type, size, sort)
VALUES (?, ?, ?, ?, ?, ?);

-- name: CountUnreadItems :one
SELECT COUNT(*) FROM items i JOIN feeds f ON f.id = i.feed_id
WHERE f.user_id = sqlc.arg('userID') AND i.read = 0
  AND (CAST(sqlc.arg('feedID') AS INTEGER) = 0 OR f.id = CAST(sqlc.arg('feedID') AS INTEGER));

-- name: CountReadItems :one
SELECT COUNT(*) FROM items i JOIN feeds f ON f.id = i.feed_id
WHERE f.user_id = sqlc.arg('userID') AND i.read = 1
  AND (CAST(sqlc.arg('feedID') AS INTEGER) = 0 OR f.id = CAST(sqlc.arg('feedID') AS INTEGER));

-- name: CountFavoriteItems :one
SELECT COUNT(*) FROM items i JOIN feeds f ON f.id = i.feed_id
WHERE f.user_id = sqlc.arg('userID') AND i.favorite = 1
  AND (CAST(sqlc.arg('feedID') AS INTEGER) = 0 OR f.id = CAST(sqlc.arg('feedID') AS INTEGER));

-- name: CountUnreadItemsByAuthor :one
SELECT COUNT(*) FROM items i JOIN feeds f ON f.id = i.feed_id
WHERE f.user_id = ? AND i.read = 0 AND f.author_id = ?;

-- name: CountReadItemsByAuthor :one
SELECT COUNT(*) FROM items i JOIN feeds f ON f.id = i.feed_id
WHERE f.user_id = ? AND i.read = 1 AND f.author_id = ?;

-- name: CountUnreadItemsByCollection :one
SELECT COUNT(*) FROM items i JOIN feeds f ON f.id = i.feed_id
WHERE f.user_id = ? AND i.read = 0
  AND i.feed_id IN (SELECT feed_id FROM collection_feeds WHERE collection_id = ?);

-- name: CountReadItemsByCollection :one
SELECT COUNT(*) FROM items i JOIN feeds f ON f.id = i.feed_id
WHERE f.user_id = ? AND i.read = 1
  AND i.feed_id IN (SELECT feed_id FROM collection_feeds WHERE collection_id = ?);