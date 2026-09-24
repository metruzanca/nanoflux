-- Canonical schema for sqlc. Mirrors the migrations in internal/db/migrate.go
-- (schemaV1..schemaV8). Keep this file in sync when migrations change.

CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    is_admin      INTEGER NOT NULL DEFAULT 0,
    signup_banner_dismissed INTEGER NOT NULL DEFAULT 0,
    avatar_key    TEXT,
    avatar_data   BLOB,
    avatar_content_type TEXT,
    avatar_url    TEXT,
    timezone      TEXT,
    theme         TEXT NOT NULL DEFAULT 'dark',
    accent_color  TEXT NOT NULL DEFAULT '#5b8cff',
    home_config   TEXT,
    auto_read_after_days INTEGER NOT NULL DEFAULT 30,
    favorites_share_token TEXT,
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE UNIQUE INDEX idx_users_favorites_share_token ON users(favorites_share_token);

CREATE TABLE sessions (
    id         INTEGER PRIMARY KEY,
    token      TEXT NOT NULL UNIQUE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at TEXT NOT NULL
);
CREATE INDEX idx_sessions_user ON sessions(user_id);
CREATE INDEX idx_sessions_token ON sessions(token);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE authors (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    avatar_url  TEXT,
    avatar_key  TEXT,
    last_fetched_at TEXT,
    description TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_authors_user ON authors(user_id);

CREATE TABLE feeds (
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
    poll_interval_auto INTEGER NOT NULL DEFAULT 1,
    last_item_at      TEXT,
    next_poll_at      TEXT,
    plugin_name       TEXT NOT NULL DEFAULT '',
    disabled_reason   TEXT,
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
    is_auto    INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_collections_user ON collections(user_id);

CREATE TABLE collection_feeds (
    collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
    feed_id       INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
    PRIMARY KEY (collection_id, feed_id)
);

-- Full-text search over items. In the real DB (internal/db/migrate.go,
-- schemaV10) this is an FTS5 virtual table kept in sync by triggers; sqlc
-- cannot introspect FTS5 virtual tables, so it is declared here as a plain
-- table with the same columns. Generated queries run unchanged against the
-- virtual table at runtime.
CREATE TABLE items_fts (
    rowid   INTEGER PRIMARY KEY,
    title   TEXT NOT NULL,
    summary TEXT NOT NULL
);

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

CREATE TABLE shared_items (
    id         INTEGER PRIMARY KEY,
    item_id    INTEGER NOT NULL UNIQUE REFERENCES items(id) ON DELETE CASCADE,
    token      TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- User-defined lists of items. Favorites is the special list (items.favorite),
-- not a row here; share_token is set when a list is shared publicly.
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

-- Plain external bookmarks attached to an author (e.g. a Twitch or Discord
-- page). Unlike feeds they are never polled and hold no items.
CREATE TABLE author_links (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    author_id  INTEGER NOT NULL REFERENCES authors(id) ON DELETE CASCADE,
    label      TEXT,
    url        TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_author_links_author ON author_links(author_id);

CREATE TABLE url_mappings (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    pattern    TEXT NOT NULL,
    template   TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(user_id, pattern)
);
CREATE INDEX idx_url_mappings_user ON url_mappings(user_id);