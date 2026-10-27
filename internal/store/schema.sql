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
    hide_unread_counts INTEGER NOT NULL DEFAULT 0,
    hide_unread_nav INTEGER NOT NULL DEFAULT 0,
    grid_max_columns INTEGER NOT NULL DEFAULT 2,
    is_ephemeral  INTEGER NOT NULL DEFAULT 0,
    expires_at    TEXT,
    favorites_share_token TEXT,
    bookmarks_share_token TEXT,
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE UNIQUE INDEX idx_users_favorites_share_token ON users(favorites_share_token);
CREATE UNIQUE INDEX idx_users_bookmarks_share_token ON users(bookmarks_share_token);
CREATE INDEX idx_users_ephemeral ON users(expires_at) WHERE is_ephemeral = 1;

CREATE TABLE sessions (
    id         INTEGER PRIMARY KEY,
    token      TEXT NOT NULL UNIQUE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at TEXT NOT NULL
);
CREATE INDEX idx_sessions_user ON sessions(user_id);
CREATE INDEX idx_sessions_token ON sessions(token);

CREATE TABLE user_view_prefs (
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope      TEXT NOT NULL,
    mode       TEXT NOT NULL DEFAULT 'list',
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (user_id, scope)
);

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
    is_system   INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_authors_user ON authors(user_id);
CREATE UNIQUE INDEX idx_authors_system ON authors(user_id) WHERE is_system = 1;

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
    next_poll_scheduled INTEGER NOT NULL DEFAULT 0,
    plugin_name       TEXT NOT NULL DEFAULT '',
    disabled_reason   TEXT,
    enabled           INTEGER NOT NULL DEFAULT 1,
    is_system         INTEGER NOT NULL DEFAULT 0,
    rank              INTEGER NOT NULL DEFAULT 0,
    cache_images      INTEGER NOT NULL DEFAULT 0,
    created_at        TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_feeds_user ON feeds(user_id);
CREATE INDEX idx_feeds_author ON feeds(author_id);
CREATE UNIQUE INDEX idx_feeds_system ON feeds(user_id) WHERE is_system = 1;

CREATE TABLE items (
    id           INTEGER PRIMARY KEY,
    feed_id      INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
    user_id      INTEGER NOT NULL DEFAULT 0,
    guid         TEXT NOT NULL,
    dedup_key    TEXT NOT NULL DEFAULT '',
    cross_key    TEXT NOT NULL DEFAULT '',
    title        TEXT NOT NULL DEFAULT '',
    link         TEXT NOT NULL DEFAULT '',
    summary      TEXT NOT NULL DEFAULT '',
    content      TEXT NOT NULL DEFAULT '',
    categories   TEXT NOT NULL DEFAULT '',
    duration_sec INTEGER,
    image_url    TEXT,
    image_cache_key TEXT,
    image_cache_size INTEGER NOT NULL DEFAULT 0,
    published_at TEXT,
    fetched_at   TEXT NOT NULL DEFAULT (datetime('now')),
    read         INTEGER NOT NULL DEFAULT 0,
    read_at      TEXT,
    favorite     INTEGER NOT NULL DEFAULT 0,
    bookmark     INTEGER NOT NULL DEFAULT 0,
    UNIQUE(feed_id, guid)
);
CREATE INDEX idx_items_feed ON items(feed_id);
CREATE INDEX idx_items_fetched ON items(feed_id, fetched_at);
CREATE UNIQUE INDEX idx_items_dedup ON items(feed_id, dedup_key);
-- Partial unique index over the per-user cross-feed identity. Created at
-- startup after MergeCrossFeedDuplicates has merged pre-existing duplicates, so
-- it is declared here for sqlc only (migrations create it, not schema.sql).
CREATE UNIQUE INDEX idx_items_cross ON items(user_id, cross_key) WHERE cross_key <> '';

-- Owner-scoped access (schemaV51): the hot read paths filter items by user_id
-- (counts, favorites/bookmarks, the item list's user filter and time order), so
-- without these the queries full-scan the table.
CREATE INDEX idx_items_user_read ON items(user_id, read);
CREATE INDEX idx_items_user_time ON items(user_id, COALESCE(published_at, fetched_at) DESC, id DESC);
CREATE INDEX idx_items_user_favorite ON items(user_id) WHERE favorite = 1;
CREATE INDEX idx_items_user_bookmark ON items(user_id) WHERE bookmark = 1;

-- Membership: an item belongs to its owner feed (items.feed_id) plus any other
-- feeds it was also seen in. Feeds list items through this table.
CREATE TABLE item_feeds (
    item_id    INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    feed_id    INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (item_id, feed_id)
);
CREATE INDEX idx_item_feeds_feed ON item_feeds(feed_id);

-- Normalized item categories/tags (schemaV48): one row per tag. Lets a list be
-- filtered by tag with an exact, indexed match. Populated by ItemStore.Upsert
-- and by a one-time startup backfill from items.categories.
CREATE TABLE item_categories (
    item_id  INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    category TEXT    NOT NULL,
    PRIMARY KEY (item_id, category)
);
CREATE INDEX idx_item_categories_category ON item_categories(category);

-- Author-level always-on allow/block filters (schemaV55). Rules mirror the
-- transient list filter rows: kind (tag/feed), action (show/hide, labelled
-- allow/block in the UI), combine (any/all) and one value per row.
CREATE TABLE author_filter_rules (
    id         INTEGER PRIMARY KEY,
    author_id  INTEGER NOT NULL REFERENCES authors(id) ON DELETE CASCADE,
    action     TEXT NOT NULL,
    kind       TEXT NOT NULL,
    combine    TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_author_filter_rules_author ON author_filter_rules(author_id, action);

CREATE TABLE author_filter_values (
    rule_id INTEGER NOT NULL REFERENCES author_filter_rules(id) ON DELETE CASCADE,
    value   TEXT NOT NULL,
    PRIMARY KEY (rule_id, value)
);

-- visible_items is the read-side translation of author rules: an item is hidden
-- when any block rule of its owner author matches, or when any allow rule of
-- that author does not match. Item read paths select from this view so the
-- rules apply everywhere (lists, counts, search, badges) without per-caller
-- predicates. Keep the expression in sync with the migration in
-- internal/db/migrate.go (schemaV55).
CREATE VIEW visible_items AS
SELECT i.*
FROM items i
JOIN feeds f ON f.id = i.feed_id
WHERE NOT EXISTS (
    SELECT 1 FROM author_filter_rules r
    WHERE r.author_id = f.author_id AND r.action = 'hide'
      AND (
        CASE r.kind
          WHEN 'tag' THEN CASE r.combine
            WHEN 'all' THEN
              (SELECT COUNT(DISTINCT ic.category) FROM item_categories ic
                 JOIN author_filter_values v ON v.rule_id = r.id AND v.value = ic.category
                WHERE ic.item_id = i.id)
              = (SELECT COUNT(*) FROM author_filter_values v2 WHERE v2.rule_id = r.id)
            ELSE EXISTS (SELECT 1 FROM item_categories ic
                 JOIN author_filter_values v ON v.rule_id = r.id AND v.value = ic.category
                WHERE ic.item_id = i.id)
          END
          WHEN 'feed' THEN CASE r.combine
            WHEN 'all' THEN
              (SELECT COUNT(DISTINCT mf.feed_id) FROM item_feeds mf
                 JOIN feeds mf2 ON mf2.id = mf.feed_id AND mf2.author_id = r.author_id
                 JOIN author_filter_values v ON v.rule_id = r.id AND v.value = CAST(mf.feed_id AS TEXT)
                WHERE mf.item_id = i.id)
              = (SELECT COUNT(*) FROM author_filter_values v2 WHERE v2.rule_id = r.id)
            ELSE EXISTS (SELECT 1 FROM item_feeds mf
                 JOIN feeds mf2 ON mf2.id = mf.feed_id AND mf2.author_id = r.author_id
                 JOIN author_filter_values v ON v.rule_id = r.id AND v.value = CAST(mf.feed_id AS TEXT)
                WHERE mf.item_id = i.id)
          END
          ELSE 0
        END
      )
)
AND NOT EXISTS (
    SELECT 1 FROM author_filter_rules r
    WHERE r.author_id = f.author_id AND r.action = 'show'
      AND NOT (
        CASE r.kind
          WHEN 'tag' THEN CASE r.combine
            WHEN 'all' THEN
              (SELECT COUNT(DISTINCT ic.category) FROM item_categories ic
                 JOIN author_filter_values v ON v.rule_id = r.id AND v.value = ic.category
                WHERE ic.item_id = i.id)
              = (SELECT COUNT(*) FROM author_filter_values v2 WHERE v2.rule_id = r.id)
            ELSE EXISTS (SELECT 1 FROM item_categories ic
                 JOIN author_filter_values v ON v.rule_id = r.id AND v.value = ic.category
                WHERE ic.item_id = i.id)
          END
          WHEN 'feed' THEN CASE r.combine
            WHEN 'all' THEN
              (SELECT COUNT(DISTINCT mf.feed_id) FROM item_feeds mf
                 JOIN feeds mf2 ON mf2.id = mf.feed_id AND mf2.author_id = r.author_id
                 JOIN author_filter_values v ON v.rule_id = r.id AND v.value = CAST(mf.feed_id AS TEXT)
                WHERE mf.item_id = i.id)
              = (SELECT COUNT(*) FROM author_filter_values v2 WHERE v2.rule_id = r.id)
            ELSE EXISTS (SELECT 1 FROM item_feeds mf
                 JOIN feeds mf2 ON mf2.id = mf.feed_id AND mf2.author_id = r.author_id
                 JOIN author_filter_values v ON v.rule_id = r.id AND v.value = CAST(mf.feed_id AS TEXT)
                WHERE mf.item_id = i.id)
          END
          ELSE 0
        END
      )
);

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
    sort      INTEGER NOT NULL DEFAULT 0,
    kind      TEXT NOT NULL DEFAULT '',
    poster    TEXT NOT NULL DEFAULT '',
    cache_key TEXT NOT NULL DEFAULT '',
    cache_size INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_enclosures_item ON item_enclosures(item_id);

CREATE TABLE shared_items (
    id         INTEGER PRIMARY KEY,
    item_id    INTEGER NOT NULL UNIQUE REFERENCES items(id) ON DELETE CASCADE,
    token      TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- User-defined lists of items. Favorites (items.favorite) and bookmarks
-- (items.bookmark) are native special lists, not rows here; share_token is set
-- when a list is shared publicly.
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
