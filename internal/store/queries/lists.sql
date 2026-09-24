-- name: CreateList :one
INSERT INTO lists (user_id, name)
VALUES (sqlc.arg('userID'), sqlc.arg('name'))
RETURNING id, user_id, name, share_token, created_at;

-- name: GetList :one
SELECT id, user_id, name, share_token, created_at
FROM lists
WHERE id = sqlc.arg('id') AND user_id = sqlc.arg('userID');

-- name: GetListByToken :one
SELECT id, user_id, name, share_token, created_at
FROM lists
WHERE share_token = sqlc.arg('token');

-- name: ListLists :many
SELECT l.id, l.user_id, l.name, l.share_token, l.created_at,
       COUNT(li.item_id) AS item_count
FROM lists l
LEFT JOIN list_items li ON li.list_id = l.id
WHERE l.user_id = sqlc.arg('userID')
GROUP BY l.id
ORDER BY l.name;

-- name: DeleteList :execresult
DELETE FROM lists
WHERE id = sqlc.arg('id') AND user_id = sqlc.arg('userID');

-- name: RenameList :execresult
UPDATE lists
SET name = sqlc.arg('name')
WHERE id = sqlc.arg('id') AND user_id = sqlc.arg('userID');

-- name: SetListShareToken :exec
UPDATE lists
SET share_token = sqlc.arg('token')
WHERE id = sqlc.arg('id') AND user_id = sqlc.arg('userID');

-- name: ClearListShareToken :exec
UPDATE lists
SET share_token = NULL
WHERE id = sqlc.arg('id') AND user_id = sqlc.arg('userID');

-- name: VerifyListItem :one
SELECT COUNT(*) FROM lists l
JOIN items i ON i.id = sqlc.arg('itemID')
JOIN feeds f ON f.id = i.feed_id
WHERE l.id = sqlc.arg('listID') AND l.user_id = sqlc.arg('userID') AND f.user_id = sqlc.arg('userID');

-- name: AddItemToList :execresult
INSERT INTO list_items (list_id, item_id)
VALUES (sqlc.arg('listID'), sqlc.arg('itemID'))
ON CONFLICT (list_id, item_id) DO NOTHING;

-- name: RemoveItemFromList :execresult
DELETE FROM list_items
WHERE list_items.list_id = sqlc.arg('listID')
  AND list_items.item_id = sqlc.arg('itemID')
  AND list_items.list_id IN (SELECT l.id FROM lists l WHERE l.user_id = sqlc.arg('userID'))
  AND list_items.item_id IN (SELECT i.id FROM items i JOIN feeds f ON f.id = i.feed_id WHERE f.user_id = sqlc.arg('userID'));

-- name: ListIDsForItem :many
SELECT li.list_id
FROM list_items li
JOIN lists l ON l.id = li.list_id
WHERE li.item_id = sqlc.arg('itemID') AND l.user_id = sqlc.arg('userID');

-- name: ListItemsInList :many
SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.image_url,
       i.published_at, i.fetched_at, i.read, i.favorite, i.read_at,
       f.title AS feed_title, f.feed_url AS feed_url, f.home_url AS feed_home_url,
       a.id AS author_id, a.name AS author_name
FROM list_items li
JOIN items i ON i.id = li.item_id
JOIN feeds f ON f.id = i.feed_id
LEFT JOIN authors a ON a.id = f.author_id
WHERE li.list_id = sqlc.arg('listID') AND f.user_id = sqlc.arg('userID')
  AND (CAST(sqlc.arg('beforeItemID') AS INTEGER) = 0 OR
       (li.created_at, i.id) < (SELECT li2.created_at, li2.item_id FROM list_items li2 WHERE li2.list_id = sqlc.arg('listID') AND li2.item_id = CAST(sqlc.arg('beforeItemID') AS INTEGER)))
ORDER BY li.created_at DESC, i.id DESC
LIMIT sqlc.arg('limit');

-- name: ListItemsInListAsc :many
SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.image_url,
       i.published_at, i.fetched_at, i.read, i.favorite, i.read_at,
       f.title AS feed_title, f.feed_url AS feed_url, f.home_url AS feed_home_url,
       a.id AS author_id, a.name AS author_name
FROM list_items li
JOIN items i ON i.id = li.item_id
JOIN feeds f ON f.id = i.feed_id
LEFT JOIN authors a ON a.id = f.author_id
WHERE li.list_id = sqlc.arg('listID') AND f.user_id = sqlc.arg('userID')
  AND (CAST(sqlc.arg('afterItemID') AS INTEGER) = 0 OR
       (li.created_at, i.id) > (SELECT li2.created_at, li2.item_id FROM list_items li2 WHERE li2.list_id = sqlc.arg('listID') AND li2.item_id = CAST(sqlc.arg('afterItemID') AS INTEGER)))
ORDER BY li.created_at ASC, i.id ASC
LIMIT sqlc.arg('limit');

-- name: ListItemsInListPublic :many
SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.image_url,
       i.published_at, i.fetched_at, i.read, i.favorite, i.read_at,
       f.title AS feed_title, f.feed_url AS feed_url, f.home_url AS feed_home_url,
       a.id AS author_id, a.name AS author_name
FROM list_items li
JOIN items i ON i.id = li.item_id
JOIN feeds f ON f.id = i.feed_id
LEFT JOIN authors a ON a.id = f.author_id
WHERE li.list_id = sqlc.arg('listID')
  AND (CAST(sqlc.arg('beforeItemID') AS INTEGER) = 0 OR
       (li.created_at, i.id) < (SELECT li2.created_at, li2.item_id FROM list_items li2 WHERE li2.list_id = sqlc.arg('listID') AND li2.item_id = CAST(sqlc.arg('beforeItemID') AS INTEGER)))
ORDER BY li.created_at DESC, i.id DESC
LIMIT sqlc.arg('limit');

-- name: GetFavoritesShareToken :one
SELECT favorites_share_token FROM users WHERE id = sqlc.arg('userID');

-- name: SetFavoritesShareToken :exec
UPDATE users
SET favorites_share_token = sqlc.arg('token')
WHERE id = sqlc.arg('userID');

-- name: GetUserByFavoritesShareToken :one
SELECT id, username, password_hash, is_admin, avatar_key, timezone, theme, accent_color, auto_read_after_days, created_at
FROM users
WHERE favorites_share_token = sqlc.arg('token');