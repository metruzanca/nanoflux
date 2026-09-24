-- name: UpsertItem :execresult
INSERT INTO items (feed_id, guid, title, link, summary, image_url, published_at, fetched_at, read, read_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (feed_id, guid) DO NOTHING;

-- name: UpdateItemSnapshot :exec
-- Refresh the content snapshot of an existing item (summary, thumbnail) on
-- poll. Identity, published_at and read state are left untouched.
UPDATE items
SET summary = ?, image_url = ?
WHERE feed_id = ? AND guid = ?;

-- name: CountItemsMissingYouTubeThumbnail :one
SELECT COUNT(*) FROM items
WHERE image_url IS NULL AND guid LIKE 'yt:video:%';

-- name: BackfillYouTubeThumbnails :execresult
-- Fill image_url for YouTube items stored before the media:thumbnail fix.
-- The thumbnail is deterministic from the video id in the GUID, so no fetch is
-- needed. Only rows with a NULL image_url are touched.
UPDATE items
SET image_url = 'https://i.ytimg.com/vi/' || substr(guid, length('yt:video:') + 1) || '/hqdefault.jpg'
WHERE image_url IS NULL AND guid LIKE 'yt:video:%';

-- name: ListItems :many
SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.image_url,
       i.published_at, i.fetched_at, i.read, i.favorite, i.read_at,
       f.title AS feed_title, f.feed_url AS feed_url, f.home_url AS feed_home_url,
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

-- name: ListItemsAsc :many
SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.image_url,
       i.published_at, i.fetched_at, i.read, i.favorite, i.read_at,
       f.title AS feed_title, f.feed_url AS feed_url, f.home_url AS feed_home_url,
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
  AND (CAST(sqlc.arg('afterID') AS INTEGER) = 0 OR
       (COALESCE(i.published_at, i.fetched_at), i.id) >
       (SELECT COALESCE(published_at, fetched_at), id FROM items WHERE id = CAST(sqlc.arg('afterID') AS INTEGER)))
ORDER BY COALESCE(i.published_at, i.fetched_at) ASC, i.id ASC
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
       f.title AS feed_title, f.feed_url AS feed_url, f.home_url AS feed_home_url,
       a.id AS author_id, a.name AS author_name
FROM items i
JOIN feeds f ON f.id = i.feed_id
LEFT JOIN authors a ON a.id = f.author_id
WHERE i.id = ? AND f.user_id = ?;

-- name: GetItemWithFeedAny :one
SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.image_url,
       i.published_at, i.fetched_at, i.read, i.favorite, i.read_at,
       f.title AS feed_title, f.feed_url AS feed_url, f.home_url AS feed_home_url,
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

-- name: MarkItemsBeforeRead :exec
-- Mark unread items newer than itemID (listed above it, newest first) in the
-- same feed as read.
UPDATE items
SET read = 1, read_at = sqlc.arg('readAt')
WHERE read = 0
  AND feed_id = (SELECT i.feed_id FROM items i WHERE i.id = sqlc.arg('itemID'))
  AND feed_id IN (SELECT f.id FROM feeds f WHERE f.user_id = sqlc.arg('userID'))
  AND (COALESCE(items.published_at, items.fetched_at), items.id) >
      (SELECT COALESCE(i.published_at, i.fetched_at), i.id FROM items i WHERE i.id = sqlc.arg('itemID'));

-- name: MarkItemsAfterRead :exec
-- Mark unread items older than itemID (listed below it, newest first) in the
-- same feed as read.
UPDATE items
SET read = 1, read_at = sqlc.arg('readAt')
WHERE read = 0
  AND feed_id = (SELECT i.feed_id FROM items i WHERE i.id = sqlc.arg('itemID'))
  AND feed_id IN (SELECT f.id FROM feeds f WHERE f.user_id = sqlc.arg('userID'))
  AND (COALESCE(items.published_at, items.fetched_at), items.id) <
      (SELECT COALESCE(i.published_at, i.fetched_at), i.id FROM items i WHERE i.id = sqlc.arg('itemID'));

-- name: MarkAuthorItemsBeforeRead :exec
-- Mark unread items newer than itemID (listed above it, newest first) across
-- every feed owned by the item's author as read. Used on an author page, where
-- the bulk action spans all of the author's feeds.
UPDATE items
SET read = 1, read_at = sqlc.arg('readAt')
WHERE read = 0
  AND feed_id IN (
    SELECT f.id FROM feeds f
    WHERE f.user_id = sqlc.arg('userID')
      AND f.author_id = (
        SELECT fa.author_id FROM feeds fa
        JOIN items ia ON ia.feed_id = fa.id
        WHERE ia.id = sqlc.arg('itemID') AND fa.user_id = sqlc.arg('userID')
      ))
  AND (COALESCE(items.published_at, items.fetched_at), items.id) >
      (SELECT COALESCE(i.published_at, i.fetched_at), i.id FROM items i WHERE i.id = sqlc.arg('itemID'));

-- name: MarkAuthorItemsAfterRead :exec
-- Mark unread items older than itemID (listed below it, newest first) across
-- every feed owned by the item's author as read.
UPDATE items
SET read = 1, read_at = sqlc.arg('readAt')
WHERE read = 0
  AND feed_id IN (
    SELECT f.id FROM feeds f
    WHERE f.user_id = sqlc.arg('userID')
      AND f.author_id = (
        SELECT fa.author_id FROM feeds fa
        JOIN items ia ON ia.feed_id = fa.id
        WHERE ia.id = sqlc.arg('itemID') AND fa.user_id = sqlc.arg('userID')
      ))
  AND (COALESCE(items.published_at, items.fetched_at), items.id) <
      (SELECT COALESCE(i.published_at, i.fetched_at), i.id FROM items i WHERE i.id = sqlc.arg('itemID'));

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

-- name: ListRecentItemTimes :many
SELECT COALESCE(published_at, fetched_at) AS t
FROM items
WHERE feed_id = ?
ORDER BY COALESCE(published_at, fetched_at) DESC, id DESC
LIMIT ?;

-- name: ListAuthorRecentItemTimes :many
-- The newest item times across all of an author's feeds, for estimating a
-- posting cadence (mirrors ListRecentItemTimes, author-scoped).
SELECT COALESCE(i.published_at, i.fetched_at) AS t
FROM items i
JOIN feeds f ON f.id = i.feed_id
WHERE f.user_id = sqlc.arg('userID') AND f.author_id = sqlc.arg('authorID')
ORDER BY COALESCE(i.published_at, i.fetched_at) DESC, i.id DESC
LIMIT sqlc.arg('limit');

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

-- name: GetAuthorItemStats :one
-- Aggregate stats for one author across all of their feeds: all-time post
-- counts (read/unread split), first/last post times, and posts within the last
-- 30 days (COALESCE(published_at, fetched_at) is the canonical item time).
-- COUNT(CASE ...) returns 0 (not NULL) on an empty set; MIN/MAX stay NULL.
SELECT
    COUNT(i.id) AS total_posts,
    COUNT(CASE WHEN i.read = 0 THEN 1 END) AS unread_posts,
    COUNT(CASE WHEN i.read = 1 THEN 1 END) AS read_posts,
    COUNT(CASE WHEN i.favorite = 1 THEN 1 END) AS favorite_posts,
    CAST(COALESCE(MIN(COALESCE(i.published_at, i.fetched_at)), '') AS TEXT) AS first_post_at,
    CAST(COALESCE(MAX(COALESCE(i.published_at, i.fetched_at)), '') AS TEXT) AS last_post_at,
    COUNT(CASE WHEN COALESCE(i.published_at, i.fetched_at) >= datetime('now', '-30 days') THEN 1 END) AS recent_posts
FROM items i
JOIN feeds f ON f.id = i.feed_id
WHERE f.user_id = sqlc.arg('userID') AND f.author_id = sqlc.arg('authorID');

-- name: CountAllItems :one
SELECT COUNT(*) FROM items;

-- name: CountAllUnreadItems :one
SELECT COUNT(*) FROM items WHERE read = 0;
