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
