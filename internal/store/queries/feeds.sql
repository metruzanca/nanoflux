-- name: CreateFeed :one
INSERT INTO feeds (user_id, author_id, title, feed_url, home_url, description, poll_interval_sec, plugin_name)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, user_id, author_id, title, feed_url, home_url, description,
         etag, last_modified, last_polled_at, last_error, next_page_url, poll_interval_sec, poll_interval_auto, last_item_at, next_poll_at, plugin_name, disabled_reason, enabled, created_at;

-- name: GetFeed :one
SELECT id, user_id, author_id, title, feed_url, home_url, description,
       etag, last_modified, last_polled_at, last_error, next_page_url, poll_interval_sec, poll_interval_auto, last_item_at, next_poll_at, plugin_name, disabled_reason, enabled, created_at
FROM feeds
WHERE id = ? AND user_id = ?;

-- name: GetFeedAny :one
SELECT id, user_id, author_id, title, feed_url, home_url, description,
       etag, last_modified, last_polled_at, last_error, next_page_url, poll_interval_sec, poll_interval_auto, last_item_at, next_poll_at, plugin_name, disabled_reason, enabled, created_at
FROM feeds
WHERE id = ?;

-- name: ListFeeds :many
SELECT id, user_id, author_id, title, feed_url, home_url, description,
       etag, last_modified, last_polled_at, last_error, next_page_url, poll_interval_sec, poll_interval_auto, last_item_at, next_poll_at, plugin_name, disabled_reason, enabled, created_at
FROM feeds
WHERE user_id = ?
ORDER BY title;

-- name: ListFeedsByAuthor :many
SELECT id, user_id, author_id, title, feed_url, home_url, description,
       etag, last_modified, last_polled_at, last_error, next_page_url, poll_interval_sec, poll_interval_auto, last_item_at, next_poll_at, plugin_name, disabled_reason, enabled, created_at
FROM feeds
WHERE user_id = ? AND author_id = ?
ORDER BY title;

-- name: ListFeedsWithUnread :many
SELECT f.id, f.user_id, f.author_id, f.title, f.feed_url, f.home_url, f.description,
       f.etag, f.last_modified, f.last_polled_at, f.last_error, f.next_page_url, f.poll_interval_sec, f.poll_interval_auto, f.last_item_at, f.next_poll_at, f.plugin_name, f.disabled_reason, f.enabled, f.created_at,
       a.name AS author_name,
       (SELECT COUNT(*) FROM items i WHERE i.feed_id = f.id AND i.read = 0) AS unread
FROM feeds f
LEFT JOIN authors a ON a.id = f.author_id
WHERE f.user_id = ?
ORDER BY f.title;

-- name: ListFeedsByAuthorWithUnread :many
SELECT f.id, f.user_id, f.author_id, f.title, f.feed_url, f.home_url, f.description,
       f.etag, f.last_modified, f.last_polled_at, f.last_error, f.next_page_url, f.poll_interval_sec, f.poll_interval_auto, f.last_item_at, f.next_poll_at, f.plugin_name, f.disabled_reason, f.enabled, f.created_at,
       a.name AS author_name,
       (SELECT COUNT(*) FROM items i WHERE i.feed_id = f.id AND i.read = 0) AS unread
FROM feeds f
LEFT JOIN authors a ON a.id = f.author_id
WHERE f.user_id = ? AND f.author_id = ?
ORDER BY f.title;

-- name: UpdateFeed :execresult
-- A user save clears any automatic disabled reason: the user took over the
-- feed's state, so it must not later be resumed by the plugin reconciler.
UPDATE feeds
SET author_id = sqlc.arg('authorID'), title = sqlc.arg('title'),
    feed_url = sqlc.arg('feedURL'), home_url = sqlc.arg('homeURL'),
    description = sqlc.arg('description'),
    poll_interval_sec = sqlc.arg('pollIntervalSec'),
    poll_interval_auto = sqlc.arg('pollIntervalAuto'),
    enabled = sqlc.arg('enabled'), disabled_reason = NULL
WHERE id = sqlc.arg('id') AND user_id = sqlc.arg('userID');

-- name: DeleteFeed :execresult
DELETE FROM feeds
WHERE id = ? AND user_id = ?;

-- name: SetFeedPollMeta :exec
UPDATE feeds
SET etag = ?, last_modified = ?, last_polled_at = ?, last_error = ?
WHERE id = ?;

-- name: SetFeedNextPageURL :exec
UPDATE feeds
SET next_page_url = ?
WHERE id = ?;

-- name: SetFeedEnabled :exec
UPDATE feeds
SET enabled = ?
WHERE id = ? AND user_id = ?;

-- name: SetFeedPollInterval :exec
UPDATE feeds
SET poll_interval_sec = ?
WHERE id = ?;

-- name: SetFeedLastItemAt :exec
UPDATE feeds
SET last_item_at = ?
WHERE id = ?;

-- name: SetFeedNextPollAt :exec
UPDATE feeds
SET next_poll_at = ?
WHERE id = ?;

-- name: SetFeedFeedURL :exec
UPDATE feeds
SET feed_url = ?
WHERE id = ?;

-- name: SetFeedPluginName :exec
UPDATE feeds
SET plugin_name = ?
WHERE id = ?;

-- name: SetFeedEnabledForPlugin :exec
-- Admin/poller-internal: disable or enable a feed and record why. Not
-- user-scoped because the plugin reconciler owns it.
UPDATE feeds
SET enabled = ?, disabled_reason = ?
WHERE id = ?;

-- name: ReenableAutoDisabledFeed :execrows
-- Auto-re-enable one feed that was disabled because its owning plugin was
-- missing. Keyed to the automatic reason so a user-paused feed (no reason) is
-- never silently resumed. The reconciler calls this for feeds it just adopted,
-- so a plugin that was renamed/replaced still resumes its feeds.
UPDATE feeds
SET enabled = 1, disabled_reason = NULL
WHERE id = ?
  AND enabled = 0
  AND disabled_reason LIKE 'plugin not loaded:%';

-- name: ResetFeedPlugin :exec
-- Clear a feed's plugin owner (back to the generic parser). If the feed was
-- auto-disabled because its plugin was missing, un-park it too; a user pause
-- (enabled = 0 with no automatic reason) is preserved.
UPDATE feeds
SET plugin_name = '',
    disabled_reason = CASE WHEN disabled_reason LIKE 'plugin not loaded:%' THEN NULL ELSE disabled_reason END,
    enabled = CASE WHEN disabled_reason LIKE 'plugin not loaded:%' THEN 1 ELSE enabled END
WHERE id = ?;

-- name: ListFeedsDue :many
SELECT id, user_id, author_id, title, feed_url, home_url, description,
       etag, last_modified, last_polled_at, last_error, next_page_url, poll_interval_sec, poll_interval_auto, last_item_at, next_poll_at, plugin_name, disabled_reason, enabled, created_at
FROM feeds
WHERE enabled = 1
  AND (
    -- A rate-limit (or other) backoff deadline gates due-ness on its own, so
    -- the host's own retry window is honored instead of being masked by the
    -- (possibly long) poll interval.
    (next_poll_at IS NOT NULL AND next_poll_at <= CAST(sqlc.arg('now') AS TEXT))
    OR (
      next_poll_at IS NULL
      AND (last_polled_at IS NULL OR last_polled_at <= datetime(CAST(sqlc.arg('now') AS TEXT), '-' || poll_interval_sec || ' seconds'))
    )
  );

-- name: GetFeedByTitle :one
SELECT id, user_id, author_id, title, feed_url, home_url, description,
       etag, last_modified, last_polled_at, last_error, next_page_url, poll_interval_sec, poll_interval_auto, last_item_at, next_poll_at, plugin_name, disabled_reason, enabled, created_at
FROM feeds
WHERE user_id = ? AND title = ? COLLATE NOCASE;

-- name: ListAllFeeds :many
SELECT f.id, f.user_id, f.author_id, f.title, f.feed_url, f.home_url, f.description,
       f.etag, f.last_modified, f.last_polled_at, f.last_error, f.next_page_url, f.poll_interval_sec, f.poll_interval_auto, f.last_item_at, f.next_poll_at, f.plugin_name, f.disabled_reason, f.enabled, f.created_at,
       u.username AS owner
FROM feeds f
JOIN users u ON u.id = f.user_id
ORDER BY u.username, f.title;

-- name: CountAllFeeds :one
SELECT COUNT(*) FROM feeds;