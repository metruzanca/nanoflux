-- name: CreateCollection :one
INSERT INTO collections (user_id, name, is_auto)
VALUES (?, ?, ?)
RETURNING id, user_id, name, is_auto, created_at;

-- name: GetCollection :one
SELECT id, user_id, name, is_auto, created_at
FROM collections
WHERE id = ? AND user_id = ?;

-- name: GetAutoCollection :one
SELECT id, user_id, name, is_auto, created_at
FROM collections
WHERE user_id = ? AND name = ? AND is_auto = 1;

-- name: ListCollections :many
SELECT id, user_id, name, is_auto, created_at
FROM collections
WHERE user_id = ?
ORDER BY name;

-- name: DeleteCollection :execresult
DELETE FROM collections
WHERE id = ? AND user_id = ?;

-- name: VerifyCollectionFeed :one
SELECT COUNT(*) FROM collections c
JOIN feeds f ON f.user_id = c.user_id
WHERE c.id = ? AND f.id = ? AND c.user_id = ?;

-- name: AddFeedToCollection :execresult
INSERT INTO collection_feeds (collection_id, feed_id)
VALUES (?, ?)
ON CONFLICT (collection_id, feed_id) DO NOTHING;

-- name: RemoveFeedFromCollection :execresult
DELETE FROM collection_feeds
WHERE collection_feeds.collection_id = ?
  AND collection_feeds.feed_id = ?
  AND collection_feeds.collection_id IN (SELECT c.id FROM collections c WHERE c.user_id = ?)
  AND collection_feeds.feed_id IN (SELECT f.id FROM feeds f WHERE f.user_id = ?);

-- name: ListFeedsInCollection :many
SELECT f.id, f.user_id, f.author_id, f.title, f.feed_url, f.home_url, f.description,
       f.etag, f.last_modified, f.last_polled_at, f.poll_interval_sec, f.enabled, f.created_at
FROM feeds f
JOIN collection_feeds cf ON cf.feed_id = f.id
WHERE cf.collection_id = ? AND f.user_id = ?
ORDER BY f.title;

-- name: CollectionIDsForFeed :many
SELECT cf.collection_id
FROM collection_feeds cf
JOIN collections c ON c.id = cf.collection_id
WHERE cf.feed_id = ? AND c.user_id = ?;