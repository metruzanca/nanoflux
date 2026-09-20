-- Canonical schema for sqlc. Mirrors the migrations in internal/db/migrate.go
-- (schemaV1..schemaV8). Keep this file in sync when migrations change.

CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    avatar_key    TEXT,
    avatar_data   BLOB,
    avatar_content_type TEXT,
    avatar_url    TEXT,
    timezone      TEXT,
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
    read_at      TEXT,
    favorite     INTEGER NOT NULL DEFAULT 0,
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

CREATE TABLE source_icons (
    id              INTEGER PRIMARY KEY,
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    domain          TEXT NOT NULL,
    icon_url        TEXT NOT NULL,
    content_type    TEXT,
    icon_data       BLOB,
    icon_key        TEXT,
    last_fetched_at TEXT,
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(user_id, domain)
);
CREATE INDEX idx_source_icons_user ON source_icons(user_id);