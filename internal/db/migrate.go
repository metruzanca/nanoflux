package db

import (
	"database/sql"
	"fmt"
)

type migration struct {
	version int
	sql     string
}

var migrations = []migration{
	{1, schemaV1},
	{2, schemaV2},
	{3, schemaV3},
	{4, schemaV4},
	{5, schemaV5},
	{6, schemaV6},
	{7, schemaV7},
	{8, schemaV8},
	{9, schemaV9},
	{10, schemaV10},
	{11, schemaV11},
	{12, schemaV12},
	{13, schemaV13},
	{14, schemaV14},
	{15, schemaV15},
	{16, schemaV16},
	{17, schemaV17},
	{18, schemaV18},
	{19, schemaV19},
	{20, schemaV20},
	{21, schemaV21},
	{22, schemaV22},
	{23, schemaV23},
	{24, schemaV24},
	{25, schemaV25},
	{26, schemaV26},
	{27, schemaV27},
}

// schemaV10 adds full-text search over item titles and summaries. items_fts is
// an external-content FTS5 table kept in sync by triggers; read/favorite
// toggles do not touch it because the update trigger only fires when the
// title or summary actually changes.
const schemaV10 = `
CREATE VIRTUAL TABLE items_fts USING fts5(title, summary, content='items', content_rowid='id');
CREATE TRIGGER items_ai AFTER INSERT ON items BEGIN
  INSERT INTO items_fts(rowid, title, summary) VALUES (new.id, new.title, new.summary);
END;
CREATE TRIGGER items_ad AFTER DELETE ON items BEGIN
  INSERT INTO items_fts(items_fts, rowid, title, summary) VALUES ('delete', old.id, old.title, old.summary);
END;
CREATE TRIGGER items_au AFTER UPDATE ON items
WHEN old.title IS NOT new.title OR old.summary IS NOT new.summary
BEGIN
  INSERT INTO items_fts(items_fts, rowid, title, summary) VALUES ('delete', old.id, old.title, old.summary);
  INSERT INTO items_fts(rowid, title, summary) VALUES (new.id, new.title, new.summary);
END;
INSERT INTO items_fts(rowid, title, summary) SELECT id, title, summary FROM items;
`

// schemaV11 stores feed enclosures (podcasts, media) per item.
const schemaV11 = `
CREATE TABLE item_enclosures (
    id        INTEGER PRIMARY KEY,
    item_id   INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    url       TEXT NOT NULL,
    title     TEXT NOT NULL DEFAULT '',
    mime_type TEXT,
    size      INTEGER NOT NULL DEFAULT 0,
    sort      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_enclosures_item ON item_enclosures(item_id);
`

// schemaV12 adds per-user filtering rules applied at ingest time. feed_id NULL
// means the rule applies to every feed.
const schemaV12 = `
CREATE TABLE filters (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    feed_id    INTEGER REFERENCES feeds(id) ON DELETE CASCADE,
    action     TEXT NOT NULL,
    field      TEXT NOT NULL,
    pattern    TEXT NOT NULL,
    is_regex   INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_filters_user ON filters(user_id);
CREATE INDEX idx_filters_feed ON filters(feed_id);
`

// schemaV13 adds public share links for individual items. Each share carries a
// random, unguessable token; tokens are never enumerated.
const schemaV13 = `
CREATE TABLE shared_items (
    id         INTEGER PRIMARY KEY,
    item_id    INTEGER NOT NULL UNIQUE REFERENCES items(id) ON DELETE CASCADE,
    token      TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
`

const schemaV9 = `
ALTER TABLE users ADD COLUMN theme TEXT NOT NULL DEFAULT 'dark';
`

// schemaV14 lets users override the app's accent color with a #rrggbb hex
// value. The default matches --accent in internal/web/static/app.css.
const schemaV14 = `
ALTER TABLE users ADD COLUMN accent_color TEXT NOT NULL DEFAULT '#5b8cff';
`

// schemaV15 marks auto-generated per-website collections so they can be
// rendered distinctly and never deleted by the user. Auto collections are
// created lazily when a feed whose hostname maps to their name is added.
const schemaV15 = `
ALTER TABLE collections ADD COLUMN is_auto INTEGER NOT NULL DEFAULT 0;
`

// schemaV16 flags admin users. The bootstrap account is marked admin by the
// server at creation; existing installs grant admin via `nanoflux user
// set-admin <username> true` (no automatic promotion here).
const schemaV16 = `
ALTER TABLE users ADD COLUMN is_admin INTEGER NOT NULL DEFAULT 0;
`

// schemaV17 adds a global key/value settings table (seeded with signup
// enabled) and a per-user flag for dismissing the admin signup banner.
const schemaV17 = `
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
INSERT INTO settings(key, value) VALUES ('allow_signup', '1');
ALTER TABLE users ADD COLUMN signup_banner_dismissed INTEGER NOT NULL DEFAULT 0;
`

// schemaV18 records the last poll failure on each feed so owners can see when
// a feed is broken. NULL/empty means the last poll succeeded.
const schemaV18 = `
ALTER TABLE feeds ADD COLUMN last_error TEXT;
`

// schemaV20 tightens the author/feed relationship: every feed belongs to an
// author. Authorless feeds (from before the rule) are first given an author
// named after the feed (falling back to its home url, then its feed url), then
// feeds is rebuilt with author_id NOT NULL. Feeds whose title collides with an
// existing author of the same user may attach to that author — acceptable for
// a one-time backfill. The rebuild also carries forward next_page_url, the
// "load older items" pagination cursor for feeds that expose paged history.
const schemaV20 = `
INSERT INTO authors (user_id, name, url, avatar_url, description, created_at)
SELECT f.user_id,
       COALESCE(NULLIF(f.title, ''), NULLIF(f.home_url, ''), f.feed_url),
       f.home_url, NULL, NULL, datetime('now')
FROM feeds f
WHERE f.author_id IS NULL;

UPDATE feeds
SET author_id = (
    SELECT a.id FROM authors a
    WHERE a.user_id = feeds.user_id
      AND a.name = COALESCE(NULLIF(feeds.title, ''), NULLIF(feeds.home_url, ''), feeds.feed_url)
    LIMIT 1
)
WHERE author_id IS NULL;

CREATE TABLE feeds_v3 (
    id                INTEGER PRIMARY KEY,
    user_id           INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    author_id         INTEGER NOT NULL REFERENCES authors(id) ON DELETE CASCADE,
    title             TEXT NOT NULL,
    feed_url          TEXT NOT NULL,
    home_url          TEXT,
    description       TEXT,
    etag              TEXT,
    last_modified     TEXT,
    last_polled_at    TEXT,
    last_error        TEXT,
    next_page_url     TEXT NOT NULL DEFAULT '',
    poll_interval_sec INTEGER NOT NULL DEFAULT 900,
    enabled           INTEGER NOT NULL DEFAULT 1,
    created_at        TEXT NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO feeds_v3 (id, user_id, author_id, title, feed_url, home_url, description,
                      etag, last_modified, last_polled_at, last_error, next_page_url, poll_interval_sec, enabled, created_at)
SELECT id, user_id, author_id, title, feed_url, home_url, description,
       etag, last_modified, last_polled_at, last_error, '', poll_interval_sec, enabled, created_at
FROM feeds;

DROP TABLE feeds;

ALTER TABLE feeds_v3 RENAME TO feeds;

CREATE INDEX idx_feeds_user ON feeds(user_id);
CREATE INDEX idx_feeds_author ON feeds(author_id);
`

// schemaV22 adds user-defined lists of items. Favorites remains the special
// list (items.favorite); lists are curated sets of posts users can also share
// publicly. share_token is NULL until the list is shared. users gains a
// favorites share token so the special list can be shared the same way.
const schemaV22 = `
CREATE TABLE lists (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    share_token TEXT UNIQUE,
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_lists_user ON lists(user_id);

CREATE TABLE list_items (
    list_id    INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
    item_id    INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (list_id, item_id)
);
CREATE INDEX idx_list_items_item ON list_items(item_id);

ALTER TABLE users ADD COLUMN favorites_share_token TEXT;
CREATE UNIQUE INDEX idx_users_favorites_share_token ON users(favorites_share_token);
`

// schemaV21 caches author avatars in object storage. avatar_url stays the
// source of truth for the remote image; avatar_key points at the cached bytes
// (fetched and overwritten in place on refetch), last_fetched_at records when
// they were cached.
const schemaV21 = `
ALTER TABLE authors ADD COLUMN avatar_key TEXT;
ALTER TABLE authors ADD COLUMN last_fetched_at TEXT;
`

// schemaV19 stores per-user "url pattern -> feed url" mappings used to
// pre-fill the add-feed form. Patterns are Go regexes with named groups; the
// template references captures with {name}. Only consulted when adding feeds;
// existing feeds keep the feed url they were created with.
const schemaV19 = `
CREATE TABLE url_mappings (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    pattern    TEXT NOT NULL,
    template   TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(user_id, pattern)
);
CREATE INDEX idx_url_mappings_user ON url_mappings(user_id);
`

// schemaV25 adds adaptive polling and stale-feed detection. poll_interval_auto
// lets a feed's interval be derived from its posting cadence (default on for
// new feeds; feeds with a custom interval keep manual control). last_item_at
// records the feed's newest item time so the UI can warn when a feed has gone
// quiet and the poller can back a stale feed off to once a day.
const schemaV25 = `
ALTER TABLE feeds ADD COLUMN poll_interval_auto INTEGER NOT NULL DEFAULT 1;
ALTER TABLE feeds ADD COLUMN last_item_at TEXT;
UPDATE feeds SET last_item_at = (
    SELECT MAX(COALESCE(published_at, fetched_at)) FROM items WHERE items.feed_id = feeds.id
);
UPDATE feeds SET poll_interval_auto = 0 WHERE poll_interval_sec <> 900;
`

const schemaV8 = `
ALTER TABLE users ADD COLUMN timezone TEXT;
`

const schemaV7 = `
ALTER TABLE items ADD COLUMN read_at TEXT;
`

const schemaV6 = `
ALTER TABLE items ADD COLUMN favorite INTEGER NOT NULL DEFAULT 0;
`

const schemaV5 = `
ALTER TABLE users ADD COLUMN avatar_key TEXT;
ALTER TABLE source_icons ADD COLUMN icon_key TEXT;
`

const schemaV4 = `
ALTER TABLE users ADD COLUMN avatar_data BLOB;
ALTER TABLE users ADD COLUMN avatar_content_type TEXT;
`

const schemaV3 = `
ALTER TABLE users ADD COLUMN avatar_url TEXT;

CREATE TABLE source_icons (
    id              INTEGER PRIMARY KEY,
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    domain          TEXT NOT NULL,
    icon_url        TEXT NOT NULL,
    content_type    TEXT,
    icon_data       BLOB,
    last_fetched_at TEXT,
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(user_id, domain)
);
CREATE INDEX idx_source_icons_user ON source_icons(user_id);
`

const schemaV2 = `
CREATE TABLE feeds_v2 (
    id                INTEGER PRIMARY KEY,
    user_id           INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    author_id         INTEGER REFERENCES authors(id) ON DELETE CASCADE,
    title             TEXT NOT NULL,
    feed_url          TEXT NOT NULL,
    home_url          TEXT,
    description       TEXT,
    etag              TEXT,
    last_modified     TEXT,
    last_polled_at    TEXT,
    poll_interval_sec INTEGER NOT NULL DEFAULT 900,
    enabled           INTEGER NOT NULL DEFAULT 1,
    created_at        TEXT NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO feeds_v2 (id, user_id, author_id, title, feed_url, home_url, description,
                      etag, last_modified, last_polled_at, poll_interval_sec, enabled, created_at)
SELECT id, user_id, author_id, title, feed_url, home_url, description,
       etag, last_modified, last_polled_at, poll_interval_sec, enabled, created_at
FROM feeds;

DROP TABLE feeds;

ALTER TABLE feeds_v2 RENAME TO feeds;

CREATE INDEX idx_feeds_user ON feeds(user_id);
CREATE INDEX idx_feeds_author ON feeds(author_id);
`

const schemaV1 = `
CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE sessions (
    id         INTEGER PRIMARY KEY,
    token      TEXT NOT NULL UNIQUE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at TEXT NOT NULL
);
CREATE INDEX idx_sessions_user ON sessions(user_id);
CREATE INDEX idx_sessions_token ON sessions(token);

CREATE TABLE authors (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    url         TEXT,
    avatar_url  TEXT,
    description TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_authors_user ON authors(user_id);

CREATE TABLE feeds (
    id                INTEGER PRIMARY KEY,
    user_id           INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    author_id         INTEGER NOT NULL REFERENCES authors(id) ON DELETE RESTRICT,
    title             TEXT NOT NULL,
    feed_url          TEXT NOT NULL,
    home_url          TEXT,
    description       TEXT,
    etag              TEXT,
    last_modified     TEXT,
    last_polled_at    TEXT,
    poll_interval_sec INTEGER NOT NULL DEFAULT 900,
    enabled           INTEGER NOT NULL DEFAULT 1,
    created_at        TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_feeds_user ON feeds(user_id);
CREATE INDEX idx_feeds_author ON feeds(author_id);

CREATE TABLE items (
    id           INTEGER PRIMARY KEY,
    feed_id      INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
    guid         TEXT NOT NULL,
    title        TEXT NOT NULL DEFAULT '',
    link         TEXT NOT NULL DEFAULT '',
    summary      TEXT NOT NULL DEFAULT '',
    image_url    TEXT,
    published_at TEXT,
    fetched_at   TEXT NOT NULL DEFAULT (datetime('now')),
    read         INTEGER NOT NULL DEFAULT 0,
    UNIQUE(feed_id, guid)
);
CREATE INDEX idx_items_feed ON items(feed_id);
CREATE INDEX idx_items_fetched ON items(feed_id, fetched_at);

CREATE TABLE collections (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_collections_user ON collections(user_id);

CREATE TABLE collection_feeds (
    collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
    feed_id       INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
    PRIMARY KEY (collection_id, feed_id)
);
`

// schemaV23 backfills a thumbnail for items that carried their image only as
// an enclosure (no media:thumbnail). Rendering now treats image enclosures as
// the item's image, so existing rows gain an image_url from their first
// image/* enclosure.
const schemaV23 = `
UPDATE items
SET image_url = (
    SELECT e.url
    FROM item_enclosures e
    WHERE e.item_id = items.id
      AND e.mime_type LIKE 'image/%'
    ORDER BY e.sort LIMIT 1
)
WHERE image_url IS NULL OR image_url = '';
`

// schemaV24 marks feeds built by scraping a page (kind 'scrape') and stores
// their CSS-selector config. Regular RSS/Atom feeds stay kind 'feed'.
const schemaV24 = `
ALTER TABLE feeds ADD COLUMN kind TEXT NOT NULL DEFAULT 'feed';
ALTER TABLE feeds ADD COLUMN scrape_config TEXT;
`

// schemaV26 adds plain external bookmarks attached to an author (e.g. a Twitch
// or Discord page). Unlike feeds they are never polled and hold no items.
const schemaV26 = `
CREATE TABLE author_links (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    author_id  INTEGER NOT NULL REFERENCES authors(id) ON DELETE CASCADE,
    label      TEXT,
    url        TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_author_links_author ON author_links(author_id);
`

// schemaV27 stores the user's home-screen configuration as JSON: an ordered
// list of pinned sections (currently collections), each with an item limit.
// The column is a raw string; the HTTP layer parses it (like scrape_config).
const schemaV27 = `
ALTER TABLE users ADD COLUMN home_config TEXT;
`

// Migrate applies any pending migrations in order, recording each in
// schema_migrations. Each migration runs in its own transaction.
//
// Foreign keys are disabled for the duration because rebuilding a parent table
// (DROP + recreate) would otherwise cascade-delete its children.
func Migrate(sqldb *sql.DB) error {
	if _, err := sqldb.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	if _, err := sqldb.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	defer sqldb.Exec("PRAGMA foreign_keys=ON")
	for _, m := range migrations {
		var applied int
		if err := sqldb.QueryRow(
			`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.version,
		).Scan(&applied); err != nil {
			return err
		}
		if applied > 0 {
			continue
		}
		tx, err := sqldb.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", m.version, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`,
			m.version, Now(),
		); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
