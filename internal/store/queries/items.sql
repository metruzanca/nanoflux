-- name: UpsertItem :execresult
-- dedup_key is the stable per-feed identity (the plugin's Identity, else the
-- GUID); guid is the display identity and may legitimately change shape.
-- cross_key is the per-user cross-feed identity ("reddit:t3_<id>" or "").
INSERT INTO items (feed_id, user_id, guid, dedup_key, cross_key, title, link, summary, categories, image_url, duration_sec, published_at, fetched_at, read, read_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (feed_id, dedup_key) DO NOTHING;

-- name: SetItemContent :exec
-- Store an Enricher's resolved body on an item. Written once, keyed by id, and
-- never touched by the poll snapshot refresh, so an enriched body survives
-- re-polling. Scoped to the user so an id cannot cross accounts.
UPDATE items
SET content = ?
WHERE id = ? AND user_id = ?;

-- name: UpdateItemSnapshotByID :exec
-- Refresh the content snapshot of an existing item (summary, categories,
-- thumbnail, duration) on poll. Identity, published_at and read state are left
-- untouched. Keyed by id so an item owned by another feed (a cross-feed
-- member) is refreshed correctly.
UPDATE items
SET summary = ?, categories = ?, image_url = ?, duration_sec = ?
WHERE id = ?;

-- name: FeedUserID :one
-- The owner of a feed, for denormalizing items.user_id on insert.
SELECT user_id FROM feeds WHERE id = ?;

-- name: GetItemByUserCrossKey :one
-- Look an item up by its per-user cross-feed identity, regardless of which feed
-- owns it.
SELECT id FROM items
WHERE user_id = ? AND cross_key = ?;

-- name: SetItemCrossKey :exec
-- Adopt a cross-feed identity on an already-stored item (e.g. a row that
-- predates the backfill), within its owning feed.
UPDATE items
SET cross_key = ?
WHERE feed_id = ? AND dedup_key = ?;

-- name: AddItemFeed :execresult
-- Add an item to a feed's membership set. Idempotent.
INSERT INTO item_feeds (item_id, feed_id)
VALUES (?, ?)
ON CONFLICT (item_id, feed_id) DO NOTHING;

-- name: ListItemSourcesForItems :many
-- Every feed membership (including the owner feed) for a set of items, so a
-- page can attach alternate sources without a query per item. The caller drops
-- the row whose feed_id equals the item's owner feed. i.cross_key rides along
-- so the view-time dedup guard can tell a cross-feed post (one row per post)
-- from two distinct rows that merely share a title.
SELECT mf.item_id, i.cross_key,
       f.id AS feed_id, f.title AS feed_title,
       a.id AS author_id, a.name AS author_name
FROM item_feeds mf
JOIN items i ON i.id = mf.item_id
JOIN feeds f ON f.id = mf.feed_id
LEFT JOIN authors a ON a.id = f.author_id
WHERE mf.item_id IN (sqlc.slice('itemIDs'))
ORDER BY mf.item_id, f.title;

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
-- The display feed is the item's owner (i.feed_id); feed/author/collection
-- filters match membership (item_feeds), so an item seen through two feeds
-- appears in both streams but as one row.
SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.categories, i.image_url, i.image_cache_key, i.duration_sec,
       i.published_at, i.fetched_at, i.read, i.favorite, i.bookmark, i.read_at,
       f.title AS feed_title, f.feed_url AS feed_url, f.home_url AS feed_home_url,
       f.is_system AS feed_is_system,
       a.id AS author_id, a.name AS author_name
FROM items i
JOIN feeds f ON f.id = i.feed_id
LEFT JOIN authors a ON a.id = f.author_id
WHERE i.user_id = sqlc.arg('userID')
  AND (CAST(sqlc.arg('feedID') AS INTEGER) = 0 OR EXISTS (
        SELECT 1 FROM item_feeds mf WHERE mf.item_id = i.id AND mf.feed_id = CAST(sqlc.arg('feedID') AS INTEGER)))
  AND (CAST(sqlc.arg('authorID') AS INTEGER) = 0 OR EXISTS (
        SELECT 1 FROM item_feeds mf JOIN feeds mf2 ON mf2.id = mf.feed_id
        WHERE mf.item_id = i.id AND mf2.author_id = CAST(sqlc.arg('authorID') AS INTEGER)))
  AND (CAST(sqlc.arg('collectionID') AS INTEGER) = 0 OR EXISTS (
        SELECT 1 FROM item_feeds mf WHERE mf.item_id = i.id AND mf.feed_id IN (
          SELECT feed_id FROM collection_feeds WHERE collection_id = CAST(sqlc.arg('collectionID') AS INTEGER))))
  AND (CAST(sqlc.arg('unread') AS INTEGER) = 0 OR i.read = 0)
  AND (CAST(sqlc.arg('read') AS INTEGER) = 0 OR i.read = 1)
  AND (CAST(sqlc.arg('favorites') AS INTEGER) = 0 OR i.favorite = 1)
  AND (CAST(sqlc.arg('bookmarks') AS INTEGER) = 0 OR i.bookmark = 1)
  -- Saved pages (system feed items) surface only in favorites, bookmarks and
  -- search, not in the unread/read/feed/author/collection streams.
  AND (f.is_system = 0 OR CAST(sqlc.arg('favorites') AS INTEGER) = 1 OR CAST(sqlc.arg('bookmarks') AS INTEGER) = 1)
  AND (CAST(sqlc.arg('beforeID') AS INTEGER) = 0 OR
       (COALESCE(i.published_at, i.fetched_at), i.id) <
       (SELECT COALESCE(published_at, fetched_at), id FROM items WHERE id = CAST(sqlc.arg('beforeID') AS INTEGER)))
ORDER BY COALESCE(i.published_at, i.fetched_at) DESC, i.id DESC
LIMIT sqlc.arg('limit');

-- name: ListItemsAsc :many
SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.categories, i.image_url, i.image_cache_key, i.duration_sec,
       i.published_at, i.fetched_at, i.read, i.favorite, i.bookmark, i.read_at,
       f.title AS feed_title, f.feed_url AS feed_url, f.home_url AS feed_home_url,
       f.is_system AS feed_is_system,
       a.id AS author_id, a.name AS author_name
FROM items i
JOIN feeds f ON f.id = i.feed_id
LEFT JOIN authors a ON a.id = f.author_id
WHERE i.user_id = sqlc.arg('userID')
  AND (CAST(sqlc.arg('feedID') AS INTEGER) = 0 OR EXISTS (
        SELECT 1 FROM item_feeds mf WHERE mf.item_id = i.id AND mf.feed_id = CAST(sqlc.arg('feedID') AS INTEGER)))
  AND (CAST(sqlc.arg('authorID') AS INTEGER) = 0 OR EXISTS (
        SELECT 1 FROM item_feeds mf JOIN feeds mf2 ON mf2.id = mf.feed_id
        WHERE mf.item_id = i.id AND mf2.author_id = CAST(sqlc.arg('authorID') AS INTEGER)))
  AND (CAST(sqlc.arg('collectionID') AS INTEGER) = 0 OR EXISTS (
        SELECT 1 FROM item_feeds mf WHERE mf.item_id = i.id AND mf.feed_id IN (
          SELECT feed_id FROM collection_feeds WHERE collection_id = CAST(sqlc.arg('collectionID') AS INTEGER))))
  AND (CAST(sqlc.arg('unread') AS INTEGER) = 0 OR i.read = 0)
  AND (CAST(sqlc.arg('read') AS INTEGER) = 0 OR i.read = 1)
  AND (CAST(sqlc.arg('favorites') AS INTEGER) = 0 OR i.favorite = 1)
  AND (CAST(sqlc.arg('bookmarks') AS INTEGER) = 0 OR i.bookmark = 1)
  -- Saved pages surface only in favorites, bookmarks and search.
  AND (f.is_system = 0 OR CAST(sqlc.arg('favorites') AS INTEGER) = 1 OR CAST(sqlc.arg('bookmarks') AS INTEGER) = 1)
  AND (CAST(sqlc.arg('afterID') AS INTEGER) = 0 OR
       (COALESCE(i.published_at, i.fetched_at), i.id) >
       (SELECT COALESCE(published_at, fetched_at), id FROM items WHERE id = CAST(sqlc.arg('afterID') AS INTEGER)))
ORDER BY COALESCE(i.published_at, i.fetched_at) ASC, i.id ASC
LIMIT sqlc.arg('limit');

-- name: ListItemsMagic :many
-- The magic sort: rank feeds by the user's taste instead of strict time. Manual
-- raise/lower dominates in hard tiers (raised before neutral before lowered);
-- within a tier, feeds with more favorites rank first; within a feed, newest
-- items first. The favorite count is the owner feed's (i.feed_id), matching the
-- feed a card names. Offset-paged (no keyset cursor).
SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.categories, i.image_url, i.image_cache_key, i.duration_sec,
       i.published_at, i.fetched_at, i.read, i.favorite, i.bookmark, i.read_at,
       f.title AS feed_title, f.feed_url AS feed_url, f.home_url AS feed_home_url,
       f.is_system AS feed_is_system,
       a.id AS author_id, a.name AS author_name
FROM items i
JOIN feeds f ON f.id = i.feed_id
LEFT JOIN authors a ON a.id = f.author_id
LEFT JOIN (
  SELECT feed_id, COUNT(*) AS fav_count
  FROM items
  WHERE items.user_id = sqlc.arg('userID') AND favorite = 1
  GROUP BY feed_id
) fc ON fc.feed_id = i.feed_id
WHERE i.user_id = sqlc.arg('userID')
  AND (CAST(sqlc.arg('feedID') AS INTEGER) = 0 OR EXISTS (
        SELECT 1 FROM item_feeds mf WHERE mf.item_id = i.id AND mf.feed_id = CAST(sqlc.arg('feedID') AS INTEGER)))
  AND (CAST(sqlc.arg('authorID') AS INTEGER) = 0 OR EXISTS (
        SELECT 1 FROM item_feeds mf JOIN feeds mf2 ON mf2.id = mf.feed_id
        WHERE mf.item_id = i.id AND mf2.author_id = CAST(sqlc.arg('authorID') AS INTEGER)))
  AND (CAST(sqlc.arg('collectionID') AS INTEGER) = 0 OR EXISTS (
        SELECT 1 FROM item_feeds mf WHERE mf.item_id = i.id AND mf.feed_id IN (
          SELECT feed_id FROM collection_feeds WHERE collection_id = CAST(sqlc.arg('collectionID') AS INTEGER))))
  AND (CAST(sqlc.arg('unread') AS INTEGER) = 0 OR i.read = 0)
  AND (CAST(sqlc.arg('read') AS INTEGER) = 0 OR i.read = 1)
  AND (CAST(sqlc.arg('favorites') AS INTEGER) = 0 OR i.favorite = 1)
  AND (CAST(sqlc.arg('bookmarks') AS INTEGER) = 0 OR i.bookmark = 1)
  AND (f.is_system = 0 OR CAST(sqlc.arg('favorites') AS INTEGER) = 1 OR CAST(sqlc.arg('bookmarks') AS INTEGER) = 1)
ORDER BY
  CASE WHEN f.rank > 0 THEN 0 WHEN f.rank < 0 THEN 2 ELSE 1 END ASC,
  COALESCE(fc.fav_count, 0) DESC,
  COALESCE(i.published_at, i.fetched_at) DESC, i.id DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: GetItem :one
SELECT i.id, i.feed_id, i.user_id, i.guid, i.dedup_key, i.cross_key, i.title, i.link, i.summary, i.content, i.categories, i.duration_sec, i.image_url, i.image_cache_key, i.image_cache_size,
       i.published_at, i.fetched_at, i.read, i.read_at, i.favorite, i.bookmark
FROM items i
WHERE i.id = ? AND i.user_id = ?;

-- name: GetItemWithFeed :one
SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.content, i.categories, i.image_url, i.image_cache_key, i.duration_sec,
       i.published_at, i.fetched_at, i.read, i.favorite, i.bookmark, i.read_at,
       f.title AS feed_title, f.feed_url AS feed_url, f.home_url AS feed_home_url,
       f.is_system AS feed_is_system,
       a.id AS author_id, a.name AS author_name
FROM items i
JOIN feeds f ON f.id = i.feed_id
LEFT JOIN authors a ON a.id = f.author_id
WHERE i.id = ? AND i.user_id = ?;

-- name: GetItemWithFeedAny :one
SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.content, i.categories, i.image_url, i.image_cache_key, i.duration_sec,
       i.published_at, i.fetched_at, i.read, i.favorite, i.bookmark, i.read_at,
       f.title AS feed_title, f.feed_url AS feed_url, f.home_url AS feed_home_url,
       f.is_system AS feed_is_system,
       a.id AS author_id, a.name AS author_name
FROM items i
JOIN feeds f ON f.id = i.feed_id
LEFT JOIN authors a ON a.id = f.author_id
WHERE i.id = ?;

-- name: SetItemRead :execresult
UPDATE items
SET read = ?, read_at = ?
WHERE items.id = ? AND items.user_id = ?;

-- name: SetItemFavorite :execresult
UPDATE items
SET favorite = ?
WHERE items.id = ? AND items.user_id = ?;

-- name: SetItemBookmark :execresult
UPDATE items
SET bookmark = ?
WHERE items.id = ? AND items.user_id = ?;

-- name: MarkAllItemsRead :exec
-- Scope by membership so a cross-feed item is marked even on a feed that does
-- not own it. Only the user's non-system feeds count (saved pages are never in
-- the read stream); each item is updated once.
UPDATE items
SET read = 1, read_at = sqlc.arg('readAt')
WHERE items.user_id = sqlc.arg('userID')
  AND items.id IN (
    SELECT mf.item_id FROM item_feeds mf JOIN feeds f ON f.id = mf.feed_id
    WHERE f.user_id = sqlc.arg('userID') AND f.is_system = 0
      AND (CAST(sqlc.arg('feedID') AS INTEGER) = 0 OR mf.feed_id = CAST(sqlc.arg('feedID') AS INTEGER)));

-- name: MarkAuthorItemsRead :exec
-- Mark every item read across all of an author's feeds (by membership).
UPDATE items
SET read = 1, read_at = sqlc.arg('readAt')
WHERE items.user_id = sqlc.arg('userID')
  AND items.id IN (
    SELECT mf.item_id FROM item_feeds mf
    JOIN feeds f ON f.id = mf.feed_id
    WHERE f.user_id = sqlc.arg('userID') AND f.author_id = sqlc.arg('authorID')
  );

-- name: MarkItemsOlderThanRead :execresult
-- Mark unread items older than a cutoff read for a user, regardless of favorite
-- state. Used by the auto-read sweep. COALESCE(published_at, fetched_at) is the
-- canonical item time.
UPDATE items
SET read = 1, read_at = sqlc.arg('readAt')
WHERE read = 0
  AND COALESCE(items.published_at, items.fetched_at) < sqlc.arg('cutoff')
  AND items.user_id = sqlc.arg('userID');

-- name: MarkAllItemsUnread :exec
-- The mirror of MarkAllItemsRead: membership-scoped, non-system feeds only.
UPDATE items
SET read = 0, read_at = NULL
WHERE items.user_id = sqlc.arg('userID')
  AND items.id IN (
    SELECT mf.item_id FROM item_feeds mf JOIN feeds f ON f.id = mf.feed_id
    WHERE f.user_id = sqlc.arg('userID') AND f.is_system = 0
      AND (CAST(sqlc.arg('feedID') AS INTEGER) = 0 OR mf.feed_id = CAST(sqlc.arg('feedID') AS INTEGER)));

-- name: MarkItemsBeforeRead :exec
-- Mark unread items newer than itemID (listed above it, newest first) in the
-- same feed page as read. feedID is the page's feed; 0 falls back to the item's
-- owner feed.
UPDATE items
SET read = 1, read_at = sqlc.arg('readAt')
WHERE read = 0
  AND items.id IN (
    SELECT mf.item_id FROM item_feeds mf
    WHERE mf.feed_id = (CASE WHEN CAST(sqlc.arg('feedID') AS INTEGER) = 0
                             THEN (SELECT i.feed_id FROM items i WHERE i.id = sqlc.arg('itemID'))
                             ELSE CAST(sqlc.arg('feedID') AS INTEGER) END))
  AND EXISTS (SELECT 1 FROM feeds f WHERE f.id = (CASE WHEN CAST(sqlc.arg('feedID') AS INTEGER) = 0
                             THEN (SELECT i.feed_id FROM items i WHERE i.id = sqlc.arg('itemID'))
                             ELSE CAST(sqlc.arg('feedID') AS INTEGER) END) AND f.user_id = sqlc.arg('userID'))
  AND (COALESCE(items.published_at, items.fetched_at), items.id) >
      (SELECT COALESCE(i.published_at, i.fetched_at), i.id FROM items i WHERE i.id = sqlc.arg('itemID'));

-- name: MarkItemsAfterRead :exec
-- Mark unread items older than itemID (listed below it, newest first) in the
-- same feed page as read. feedID is the page's feed; 0 falls back to the item's
-- owner feed.
UPDATE items
SET read = 1, read_at = sqlc.arg('readAt')
WHERE read = 0
  AND items.id IN (
    SELECT mf.item_id FROM item_feeds mf
    WHERE mf.feed_id = (CASE WHEN CAST(sqlc.arg('feedID') AS INTEGER) = 0
                             THEN (SELECT i.feed_id FROM items i WHERE i.id = sqlc.arg('itemID'))
                             ELSE CAST(sqlc.arg('feedID') AS INTEGER) END))
  AND EXISTS (SELECT 1 FROM feeds f WHERE f.id = (CASE WHEN CAST(sqlc.arg('feedID') AS INTEGER) = 0
                             THEN (SELECT i.feed_id FROM items i WHERE i.id = sqlc.arg('itemID'))
                             ELSE CAST(sqlc.arg('feedID') AS INTEGER) END) AND f.user_id = sqlc.arg('userID'))
  AND (COALESCE(items.published_at, items.fetched_at), items.id) <
      (SELECT COALESCE(i.published_at, i.fetched_at), i.id FROM items i WHERE i.id = sqlc.arg('itemID'));

-- name: MarkAuthorItemsBeforeRead :exec
-- Mark unread items newer than itemID (listed above it, newest first) across
-- every feed owned by the item's author as read. Used on an author page, where
-- the bulk action spans all of the author's feeds.
UPDATE items
SET read = 1, read_at = sqlc.arg('readAt')
WHERE read = 0
  AND items.id IN (
    SELECT mf.item_id FROM item_feeds mf
    JOIN feeds f ON f.id = mf.feed_id
    WHERE f.user_id = sqlc.arg('userID')
      AND f.author_id = (
        SELECT fa.author_id FROM feeds fa
        JOIN item_feeds mfx ON mfx.feed_id = fa.id
        WHERE mfx.item_id = sqlc.arg('itemID') AND fa.user_id = sqlc.arg('userID')
      ))
  AND (COALESCE(items.published_at, items.fetched_at), items.id) >
      (SELECT COALESCE(i.published_at, i.fetched_at), i.id FROM items i WHERE i.id = sqlc.arg('itemID'));

-- name: MarkAuthorItemsAfterRead :exec
-- Mark unread items older than itemID (listed below it, newest first) across
-- every feed owned by the item's author as read.
UPDATE items
SET read = 1, read_at = sqlc.arg('readAt')
WHERE read = 0
  AND items.id IN (
    SELECT mf.item_id FROM item_feeds mf
    JOIN feeds f ON f.id = mf.feed_id
    WHERE f.user_id = sqlc.arg('userID')
      AND f.author_id = (
        SELECT fa.author_id FROM feeds fa
        JOIN item_feeds mfx ON mfx.feed_id = fa.id
        WHERE mfx.item_id = sqlc.arg('itemID') AND fa.user_id = sqlc.arg('userID')
      ))
  AND (COALESCE(items.published_at, items.fetched_at), items.id) <
      (SELECT COALESCE(i.published_at, i.fetched_at), i.id FROM items i WHERE i.id = sqlc.arg('itemID'));

-- name: GetItemContent :one
-- An item's enriched body, to skip re-enriching at ingest. Empty means the
-- plugin has not enriched it yet.
SELECT content FROM items WHERE id = ?;

-- name: GetItemByDedupKey :one
-- Look an item up by its stable per-feed identity (dedup_key), not its display
-- GUID.
SELECT id FROM items
WHERE feed_id = ? AND dedup_key = ?;

-- name: SetItemImageCacheKey :exec
-- Record the object-storage key and byte size of an item's cached primary
-- image, or clear it. A size of 0 with a non-empty key means "reused an existing
-- blob; keep the size already recorded", so a re-poll never zeroes it.
-- Independent of UpdateItemSnapshotByID, which refreshes the remote image_url on
-- every poll but must not clobber the cached copy.
UPDATE items
SET image_cache_key = sqlc.arg('key'),
    image_cache_size = CASE
        WHEN IFNULL(sqlc.arg('key'), '') = '' THEN 0
        WHEN CAST(sqlc.arg('size') AS INTEGER) > 0 THEN CAST(sqlc.arg('size') AS INTEGER)
        ELSE image_cache_size
    END
WHERE id = sqlc.arg('id');

-- name: ListEnclosures :many
SELECT url, title, mime_type, size, sort, kind, poster, cache_key, cache_size
FROM item_enclosures
WHERE item_id = ?
ORDER BY sort;

-- name: DeleteEnclosures :exec
DELETE FROM item_enclosures
WHERE item_id = ?;

-- name: DeleteSavedItem :execresult
-- Hard-delete a saved page: an item under the user's hidden system feed. The
-- system-feed guard means a regular feed item can never be deleted this way.
DELETE FROM items
WHERE items.id = sqlc.arg('itemID')
  AND items.feed_id IN (
    SELECT f.id FROM feeds f
    WHERE f.user_id = sqlc.arg('userID') AND f.is_system = 1
  );

-- name: InsertEnclosure :exec
INSERT INTO item_enclosures (item_id, url, title, mime_type, size, sort, kind, poster, cache_key, cache_size)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: SetItemImageCacheSizeByKey :exec
-- Backfill the recorded size of a cached primary image by its storage key.
UPDATE items
SET image_cache_size = ?
WHERE image_cache_key = ?;

-- name: SetEnclosureCacheSizeByKey :exec
-- Backfill the recorded size of a cached enclosure by its storage key.
UPDATE item_enclosures
SET cache_size = ?
WHERE cache_key = ?;

-- name: AllCacheKeys :many
-- Every object-storage key referenced by an item's cached media. Used to find
-- orphaned cache blobs (present in the store but referenced by no row).
SELECT image_cache_key AS obj_key FROM items
WHERE image_cache_key IS NOT NULL AND image_cache_key <> ''
UNION
SELECT cache_key AS obj_key FROM item_enclosures
WHERE cache_key <> '';

-- name: ListUserCacheKeys :many
-- Cached-media keys owned by a user, for purging when the account is deleted.
SELECT i.image_cache_key AS obj_key FROM items i
WHERE i.user_id = sqlc.arg('userID') AND i.image_cache_key IS NOT NULL AND i.image_cache_key <> ''
UNION
SELECT e.cache_key AS obj_key FROM item_enclosures e
JOIN items i ON i.id = e.item_id
WHERE i.user_id = sqlc.arg('userID') AND e.cache_key <> '';

-- name: ListFeedCacheKeys :many
-- Cached-media keys for the items a feed deletion actually removes: items owned
-- by the feed that are not also members of another feed (those are re-homed and
-- keep their cached bytes).
SELECT i.image_cache_key AS obj_key FROM items i
WHERE i.feed_id = sqlc.arg('feedID')
  AND i.image_cache_key IS NOT NULL AND i.image_cache_key <> ''
  AND NOT EXISTS (
    SELECT 1 FROM item_feeds mf WHERE mf.item_id = i.id AND mf.feed_id <> sqlc.arg('feedID'))
UNION
SELECT e.cache_key AS obj_key FROM item_enclosures e
JOIN items i ON i.id = e.item_id
WHERE i.feed_id = sqlc.arg('feedID') AND e.cache_key <> ''
  AND NOT EXISTS (
    SELECT 1 FROM item_feeds mf WHERE mf.item_id = i.id AND mf.feed_id <> sqlc.arg('feedID'));

-- name: StorageByFeed :one
-- Total bytes of cached media for the items a feed lists (by membership, so a
-- cross-feed item counts). Each item and enclosure is counted once.
SELECT CAST(COALESCE(SUM(sz), 0) AS INTEGER) AS bytes FROM (
  SELECT i.image_cache_size AS sz FROM items i
  JOIN item_feeds mf ON mf.item_id = i.id
  WHERE mf.feed_id = sqlc.arg('feedID') AND i.image_cache_size > 0
  UNION ALL
  SELECT e.cache_size AS sz FROM item_enclosures e
  JOIN item_feeds mf ON mf.item_id = e.item_id
  WHERE mf.feed_id = sqlc.arg('feedID') AND e.cache_size > 0
);

-- name: StorageByAuthor :one
-- Total bytes of cached media across all of an author's feeds. Counts each item
-- (and enclosure) once even when it is a member of several of those feeds.
SELECT CAST(COALESCE(SUM(sz), 0) AS INTEGER) AS bytes FROM (
  SELECT i.image_cache_size AS sz FROM items i
  WHERE i.user_id = sqlc.arg('userID') AND i.image_cache_size > 0
    AND i.id IN (
      SELECT mf.item_id FROM item_feeds mf JOIN feeds f ON f.id = mf.feed_id
      WHERE f.user_id = sqlc.arg('userID') AND f.author_id = sqlc.arg('authorID'))
  UNION ALL
  SELECT e.cache_size AS sz FROM item_enclosures e
  WHERE e.cache_size > 0
    AND e.item_id IN (
      SELECT mf.item_id FROM item_feeds mf JOIN feeds f ON f.id = mf.feed_id
      WHERE f.user_id = sqlc.arg('userID') AND f.author_id = sqlc.arg('authorID'))
);

-- name: ListFeedStorage :many
-- Cached-media bytes per feed for a user, largest first, for the admin storage
-- view. System feeds (saved pages) are excluded.
SELECT f.id AS feed_id, f.title AS title,
  COALESCE(img.bytes, 0) + COALESCE(enc.bytes, 0) AS bytes
FROM feeds f
LEFT JOIN (
  SELECT mf.feed_id AS feed_id, SUM(i.image_cache_size) AS bytes
  FROM item_feeds mf JOIN items i ON i.id = mf.item_id
  WHERE i.image_cache_size > 0 GROUP BY mf.feed_id
) img ON img.feed_id = f.id
LEFT JOIN (
  SELECT mf.feed_id AS feed_id, SUM(e.cache_size) AS bytes
  FROM item_feeds mf JOIN item_enclosures e ON e.item_id = mf.item_id
  WHERE e.cache_size > 0 GROUP BY mf.feed_id
) enc ON enc.feed_id = f.id
WHERE f.user_id = sqlc.arg('userID') AND f.is_system = 0
  AND COALESCE(img.bytes, 0) + COALESCE(enc.bytes, 0) > 0
ORDER BY bytes DESC
LIMIT sqlc.arg('limit');

-- name: ListRecentItemTimes :many
-- A feed's most recent item times (by membership, so a cross-feed item counts).
SELECT COALESCE(i.published_at, i.fetched_at) AS t
FROM items i
JOIN item_feeds mf ON mf.item_id = i.id
WHERE mf.feed_id = sqlc.arg('feedID')
ORDER BY COALESCE(i.published_at, i.fetched_at) DESC, i.id DESC
LIMIT sqlc.arg('limit');

-- name: ListAuthorRecentItemTimes :many
-- The newest item times across all of an author's feeds, for estimating a
-- posting cadence (mirrors ListRecentItemTimes, author-scoped).
SELECT COALESCE(i.published_at, i.fetched_at) AS t
FROM items i
WHERE i.user_id = sqlc.arg('userID')
  AND i.id IN (
    SELECT mf.item_id FROM item_feeds mf JOIN feeds f ON f.id = mf.feed_id
    WHERE f.user_id = sqlc.arg('userID') AND f.author_id = sqlc.arg('authorID'))
ORDER BY COALESCE(i.published_at, i.fetched_at) DESC, i.id DESC
LIMIT sqlc.arg('limit');

-- name: CountUnreadItems :one
-- Excludes saved pages (the system feed), which never appear in the unread
-- stream or its count. Feed scope matches membership, and the item is counted
-- once however many member feeds it has.
SELECT COUNT(*) FROM items i
WHERE i.user_id = sqlc.arg('userID') AND i.read = 0
  AND i.feed_id NOT IN (SELECT f.id FROM feeds f WHERE f.is_system = 1)
  AND (CAST(sqlc.arg('feedID') AS INTEGER) = 0 OR i.id IN (
        SELECT mf.item_id FROM item_feeds mf WHERE mf.feed_id = CAST(sqlc.arg('feedID') AS INTEGER)));

-- name: CountReadItems :one
SELECT COUNT(*) FROM items i
WHERE i.user_id = sqlc.arg('userID') AND i.read = 1
  AND i.feed_id NOT IN (SELECT f.id FROM feeds f WHERE f.is_system = 1)
  AND (CAST(sqlc.arg('feedID') AS INTEGER) = 0 OR i.id IN (
        SELECT mf.item_id FROM item_feeds mf WHERE mf.feed_id = CAST(sqlc.arg('feedID') AS INTEGER)));

-- name: CountFavoriteItems :one
SELECT COUNT(*) FROM items i
WHERE i.user_id = sqlc.arg('userID') AND i.favorite = 1
  AND (CAST(sqlc.arg('feedID') AS INTEGER) = 0 OR i.id IN (
        SELECT mf.item_id FROM item_feeds mf WHERE mf.feed_id = CAST(sqlc.arg('feedID') AS INTEGER)));

-- name: CountBookmarkItems :one
SELECT COUNT(*) FROM items i
WHERE i.user_id = sqlc.arg('userID') AND i.bookmark = 1
  AND (CAST(sqlc.arg('feedID') AS INTEGER) = 0 OR i.id IN (
        SELECT mf.item_id FROM item_feeds mf WHERE mf.feed_id = CAST(sqlc.arg('feedID') AS INTEGER)));

-- name: CountUnreadItemsByAuthor :one
SELECT COUNT(*) FROM items i
WHERE i.user_id = sqlc.arg('userID') AND i.read = 0
  AND i.id IN (
    SELECT mf.item_id FROM item_feeds mf JOIN feeds f ON f.id = mf.feed_id
    WHERE f.user_id = sqlc.arg('userID') AND f.author_id = sqlc.arg('authorID'));

-- name: CountReadItemsByAuthor :one
SELECT COUNT(*) FROM items i
WHERE i.user_id = sqlc.arg('userID') AND i.read = 1
  AND i.id IN (
    SELECT mf.item_id FROM item_feeds mf JOIN feeds f ON f.id = mf.feed_id
    WHERE f.user_id = sqlc.arg('userID') AND f.author_id = sqlc.arg('authorID'));

-- name: CountFavoriteItemsByAuthor :one
SELECT COUNT(*) FROM items i
WHERE i.user_id = sqlc.arg('userID') AND i.favorite = 1
  AND i.id IN (
    SELECT mf.item_id FROM item_feeds mf JOIN feeds f ON f.id = mf.feed_id
    WHERE f.user_id = sqlc.arg('userID') AND f.author_id = sqlc.arg('authorID'));

-- name: CountBookmarkItemsByAuthor :one
SELECT COUNT(*) FROM items i
WHERE i.user_id = sqlc.arg('userID') AND i.bookmark = 1
  AND i.id IN (
    SELECT mf.item_id FROM item_feeds mf JOIN feeds f ON f.id = mf.feed_id
    WHERE f.user_id = sqlc.arg('userID') AND f.author_id = sqlc.arg('authorID'));

-- name: CountUnreadItemsByCollection :one
SELECT COUNT(*) FROM items i
WHERE i.user_id = sqlc.arg('userID') AND i.read = 0
  AND i.id IN (
    SELECT mf.item_id FROM item_feeds mf
    WHERE mf.feed_id IN (SELECT feed_id FROM collection_feeds WHERE collection_id = sqlc.arg('collectionID')));

-- name: CountReadItemsByCollection :one
SELECT COUNT(*) FROM items i
WHERE i.user_id = sqlc.arg('userID') AND i.read = 1
  AND i.id IN (
    SELECT mf.item_id FROM item_feeds mf
    WHERE mf.feed_id IN (SELECT feed_id FROM collection_feeds WHERE collection_id = sqlc.arg('collectionID')));

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
WHERE i.user_id = sqlc.arg('userID')
  AND i.id IN (
    SELECT mf.item_id FROM item_feeds mf JOIN feeds f ON f.id = mf.feed_id
    WHERE f.user_id = sqlc.arg('userID') AND f.author_id = sqlc.arg('authorID'));

-- name: CountAllItems :one
SELECT COUNT(*) FROM items;

-- name: CountAllUnreadItems :one
SELECT COUNT(*) FROM items WHERE read = 0;

-- name: ListFeedItemsForFilter :many
-- Every item that is a member of a feed, with the same shape as ListItems, for
-- retroactively applying an ingest filter rule to already-stored items. Unlike
-- ListItems it is not limited and does not exclude the system feed, but the
-- caller scopes it to a regular feed.
SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.categories, i.image_url, i.image_cache_key, i.duration_sec,
       i.published_at, i.fetched_at, i.read, i.favorite, i.bookmark, i.read_at,
       f.title AS feed_title, f.feed_url AS feed_url, f.home_url AS feed_home_url,
       f.is_system AS feed_is_system,
       a.id AS author_id, a.name AS author_name
FROM items i
JOIN feeds f ON f.id = i.feed_id
JOIN item_feeds mf ON mf.item_id = i.id
LEFT JOIN authors a ON a.id = f.author_id
WHERE mf.feed_id = sqlc.arg('feedID')
ORDER BY COALESCE(i.published_at, i.fetched_at) DESC, i.id DESC;

-- name: RemoveItemFeedMemberships :exec
-- Drop a set of items from one feed's membership set (retroactive delete). The
-- items themselves are re-homed or deleted separately.
DELETE FROM item_feeds
WHERE feed_id = sqlc.arg('feedID') AND item_id IN (sqlc.slice('itemIDs'));

-- name: RehomeOwnedItems :exec
-- Re-home affected items owned by a feed to one of their remaining memberships,
-- so a row still reachable through another feed survives the owner's removal.
-- Scoped to itemIDs so untouched items keep their owner feed.
UPDATE items
SET feed_id = (
    SELECT mf.feed_id FROM item_feeds mf
    WHERE mf.item_id = items.id
    ORDER BY mf.feed_id LIMIT 1
)
WHERE items.feed_id = sqlc.arg('feedID')
  AND items.id IN (sqlc.slice('itemIDs'))
  AND EXISTS (SELECT 1 FROM item_feeds mf WHERE mf.item_id = items.id);

-- name: DeleteOrphanOwnedItems :exec
-- Delete affected items owned by a feed that no longer belong to any feed
-- (their last membership was removed). Cascades enclosures, list items, shares
-- and the FTS trigger. Scoped to itemIDs.
DELETE FROM items
WHERE items.feed_id = sqlc.arg('feedID')
  AND items.id IN (sqlc.slice('itemIDs'))
  AND NOT EXISTS (SELECT 1 FROM item_feeds mf WHERE mf.item_id = items.id);

-- name: SetItemsReadByIDs :exec
-- Mark a set of a user's items read, recording the time (retroactive
-- mark_read). Scoped by user so ids cannot cross accounts.
UPDATE items
SET read = 1, read_at = sqlc.arg('readAt')
WHERE user_id = sqlc.arg('userID') AND id IN (sqlc.slice('itemIDs'));

-- name: DeleteItemCategories :exec
-- Clear an item's tag rows before re-inserting the current set, so a re-poll
-- replaces rather than accumulates (mirroring items.categories).
DELETE FROM item_categories WHERE item_id = ?;

-- name: AddItemCategory :exec
INSERT INTO item_categories (item_id, category)
VALUES (?, ?)
ON CONFLICT DO NOTHING;

-- name: ListCategoriesForFeed :many
-- Distinct tags across a feed's items with their counts, most-used first. Scoped
-- through the feed's owner so ids cannot cross accounts.
SELECT ic.category, COUNT(*) AS item_count
FROM item_categories ic
JOIN item_feeds mf ON mf.item_id = ic.item_id
JOIN feeds f ON f.id = mf.feed_id
WHERE mf.feed_id = sqlc.arg('feedID') AND f.user_id = sqlc.arg('userID')
GROUP BY ic.category
ORDER BY item_count DESC, ic.category ASC;

-- name: ListCategoriesForAuthor :many
-- Distinct tags across all of an author's feeds, most-used first.
SELECT ic.category, COUNT(*) AS item_count
FROM item_categories ic
JOIN item_feeds mf ON mf.item_id = ic.item_id
JOIN feeds f ON f.id = mf.feed_id
WHERE f.author_id = sqlc.arg('authorID') AND f.user_id = sqlc.arg('userID')
GROUP BY ic.category
ORDER BY item_count DESC, ic.category ASC;

-- name: ListCategoriesForCollection :many
-- Distinct tags across a collection's feeds, most-used first.
SELECT ic.category, COUNT(*) AS item_count
FROM item_categories ic
JOIN item_feeds mf ON mf.item_id = ic.item_id
WHERE mf.feed_id IN (SELECT feed_id FROM collection_feeds WHERE collection_id = sqlc.arg('collectionID'))
  AND EXISTS (SELECT 1 FROM collections c WHERE c.id = sqlc.arg('collectionID') AND c.user_id = sqlc.arg('userID'))
GROUP BY ic.category
ORDER BY item_count DESC, ic.category ASC;

-- name: ListItemsWithCategories :many
-- Items that carry a category string but have no item_categories rows yet, for
-- the one-time backfill (SQLite cannot split the newline-joined column in SQL).
-- The NOT EXISTS makes the backfill a cheap no-op once every item is populated.
SELECT id, categories FROM items
WHERE categories <> ''
  AND NOT EXISTS (SELECT 1 FROM item_categories ic WHERE ic.item_id = items.id);

