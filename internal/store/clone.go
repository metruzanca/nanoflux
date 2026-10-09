package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// CloneUser copies a seed account into a brand-new non-admin user, remapping
// every parent id (authors -> feeds -> items/memberships, collections, lists,
// filters, icons, links, view preferences). It is the engine behind demo mode:
// NF_DEMO_USER is curated once, then each visitor gets their own throwaway copy.
//
// The clone is deliberately not a general-purpose "fork account" tool:
//
//   - The new user is never an admin, has no share tokens, and gets the caller's
//     password hash (demo callers pass a random one that nobody knows).
//   - Every cloned feed is created disabled so nothing is fetched until the demo
//     user chooses to enable it. Feed poll bookkeeping (etag, last polled, next
//     page, rate-limit deadline) is reset so a copy starts from a clean slate.
//   - The seed's avatar/icon blobs are not shared: only the object key would be
//     copied, and deleting one account's objects must never reach the other's.
//     The clone re-derives its avatar/icon from the URL on demand instead.
//   - home_config is rewritten so pinned collection sections point at the cloned
//     collection ids rather than the seed's.
//
// expiresAt is a db-formatted UTC timestamp marking when the clone (and its
// session) expires; callers in demo mode read it back on every request.
func (s *Store) CloneUser(seedID int64, newUsername, passwordHash, expiresAt string) (User, error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, fmt.Errorf("clone user begin: %w", err)
	}
	defer tx.Rollback()

	var (
		theme, accent, homeConfig, timezone sql.NullString
		autoRead                            int64
		hideCounts, hideNav, gridCols       int64
	)
	err = tx.QueryRowContext(ctx, `
		SELECT theme, accent_color, home_config, timezone, auto_read_after_days,
		       hide_unread_counts, hide_unread_nav, grid_max_columns
		FROM users WHERE id = ?`, seedID,
	).Scan(&theme, &accent, &homeConfig, &timezone, &autoRead, &hideCounts, &hideNav, &gridCols)
	if err == sql.ErrNoRows {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("clone user read seed: %w", err)
	}

	res, err := tx.ExecContext(ctx, `
		INSERT INTO users (
			username, password_hash, is_admin, timezone, theme, accent_color, home_config,
			auto_read_after_days, hide_unread_counts, hide_unread_nav, grid_max_columns,
			is_ephemeral, expires_at
		) VALUES (?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
		newUsername, passwordHash, timezone, theme, accent, homeConfig,
		autoRead, hideCounts, hideNav, gridCols, ns(expiresAt))
	if err != nil {
		return User{}, fmt.Errorf("clone user insert: %w", err)
	}
	newID, err := res.LastInsertId()
	if err != nil {
		return User{}, fmt.Errorf("clone user id: %w", err)
	}

	if err := cloneAuthors(ctx, tx, seedID, newID); err != nil {
		return User{}, err
	}
	feedMap, err := cloneFeeds(ctx, tx, seedID, newID)
	if err != nil {
		return User{}, err
	}
	itemMap, err := cloneItems(ctx, tx, seedID, newID, feedMap)
	if err != nil {
		return User{}, err
	}
	if err := cloneItemCategories(ctx, tx, seedID, itemMap); err != nil {
		return User{}, err
	}
	if err := cloneItemFeeds(ctx, tx, seedID, itemMap, feedMap); err != nil {
		return User{}, err
	}
	if err := cloneEnclosures(ctx, tx, seedID, itemMap); err != nil {
		return User{}, err
	}
	collectionMap, err := cloneCollections(ctx, tx, seedID, newID, feedMap)
	if err != nil {
		return User{}, err
	}
	if err := cloneLists(ctx, tx, seedID, newID, itemMap); err != nil {
		return User{}, err
	}
	if err := cloneFilters(ctx, tx, seedID, newID, feedMap); err != nil {
		return User{}, err
	}
	if err := cloneAuthorLinks(ctx, tx, seedID, newID); err != nil {
		return User{}, err
	}
	if err := cloneSourceIcons(ctx, tx, seedID, newID); err != nil {
		return User{}, err
	}
	if err := cloneViewPrefs(ctx, tx, seedID, newID); err != nil {
		return User{}, err
	}
	if err := remapHomeConfig(ctx, tx, newID, homeConfig.String, collectionMap); err != nil {
		return User{}, err
	}

	if err := tx.Commit(); err != nil {
		return User{}, fmt.Errorf("clone user commit: %w", err)
	}

	u, err := s.Users.ByID(newID)
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// cloneAuthors copies the seed's real (non-system) authors and returns a map of
// old author id -> new author id.
func cloneAuthors(ctx context.Context, tx *sql.Tx, seedID, newID int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, name, avatar_url, description, last_fetched_at
		FROM authors WHERE user_id = ? AND is_system = 0`, seedID)
	if err != nil {
		return fmt.Errorf("clone authors select: %w", err)
	}
	defer rows.Close()
	type author struct {
		id          int64
		name        string
		avatarURL   sql.NullString
		description sql.NullString
		lastFetch   sql.NullString
	}
	var authors []author
	for rows.Next() {
		var a author
		if err := rows.Scan(&a.id, &a.name, &a.avatarURL, &a.description, &a.lastFetch); err != nil {
			return fmt.Errorf("clone authors scan: %w", err)
		}
		authors = append(authors, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, a := range authors {
		// avatar_key/blobs are intentionally not copied; see CloneUser.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO authors (user_id, name, avatar_url, description, last_fetched_at)
			VALUES (?, ?, ?, ?, ?)`,
			newID, a.name, a.avatarURL, a.description, a.lastFetch); err != nil {
			return fmt.Errorf("clone author insert: %w", err)
		}
	}
	return nil
}

// cloneFeeds copies the seed's non-system feeds (all disabled) and returns a map
// of old feed id -> new feed id.
func cloneFeeds(ctx context.Context, tx *sql.Tx, seedID, newID int64) (map[int64]int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, author_id, title, feed_url, home_url, description,
		       poll_interval_sec, poll_interval_auto, plugin_name, rank, filter_mode
		FROM feeds WHERE user_id = ? AND is_system = 0`, seedID)
	if err != nil {
		return nil, fmt.Errorf("clone feeds select: %w", err)
	}
	defer rows.Close()
	type feed struct {
		id               int64
		authorID         int64
		title, feedURL   string
		homeURL, desc    sql.NullString
		pollInterval     int64
		pollIntervalAuto int64
		pluginName       string
		rank             int64
		filterMode       string
	}
	var feeds []feed
	for rows.Next() {
		var f feed
		if err := rows.Scan(&f.id, &f.authorID, &f.title, &f.feedURL, &f.homeURL, &f.desc,
			&f.pollInterval, &f.pollIntervalAuto, &f.pluginName, &f.rank, &f.filterMode); err != nil {
			return nil, fmt.Errorf("clone feeds scan: %w", err)
		}
		feeds = append(feeds, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Map old author ids to new ones so a feed keeps its author.
	authorMap, err := cloneAuthorIDMap(ctx, tx, seedID, newID)
	if err != nil {
		return nil, err
	}
	feedMap := make(map[int64]int64, len(feeds))
	for _, f := range feeds {
		newAuthor, ok := authorMap[f.authorID]
		if !ok {
			// A feed whose author was somehow not cloned (e.g. it pointed at the
			// system author) is skipped rather than orphaned.
			continue
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO feeds (
				user_id, author_id, title, feed_url, home_url, description,
				poll_interval_sec, poll_interval_auto, plugin_name, rank, filter_mode,
				enabled, disabled_reason
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, NULL)`,
			newID, newAuthor, f.title, f.feedURL, f.homeURL, f.desc,
			f.pollInterval, f.pollIntervalAuto, f.pluginName, f.rank, f.filterMode)
		if err != nil {
			return nil, fmt.Errorf("clone feed insert: %w", err)
		}
		nid, err := res.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("clone feed id: %w", err)
		}
		feedMap[f.id] = nid
	}
	return feedMap, nil
}

// cloneAuthorIDMap returns a map of old author id -> new author id for the seed
// user, matching by name (names are only unique per user in practice; the seed
// is curated and small).
func cloneAuthorIDMap(ctx context.Context, tx *sql.Tx, seedID, newID int64) (map[int64]int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT a.id, b.id
		FROM authors a
		JOIN authors b ON b.user_id = ? AND b.is_system = 0 AND b.name = a.name
		WHERE a.user_id = ? AND a.is_system = 0`, newID, seedID)
	if err != nil {
		return nil, fmt.Errorf("clone author map: %w", err)
	}
	defer rows.Close()
	m := map[int64]int64{}
	for rows.Next() {
		var oldID, newID2 int64
		if err := rows.Scan(&oldID, &newID2); err != nil {
			return nil, err
		}
		m[oldID] = newID2
	}
	return m, rows.Err()
}

// cloneItems copies every item of the seed's feeds, remapping feed_id/user_id,
// and returns a map of old item id -> new item id.
func cloneItems(ctx context.Context, tx *sql.Tx, seedID, newID int64, feedMap map[int64]int64) (map[int64]int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT i.id, i.feed_id, i.guid, i.dedup_key, i.cross_key, i.title, i.link,
		       i.summary, i.content, i.categories, i.duration_sec, i.image_url,
		       i.published_at, i.fetched_at, i.read, i.read_at, i.favorite, i.bookmark
		FROM items i
		JOIN feeds f ON f.id = i.feed_id
		WHERE f.user_id = ? AND f.is_system = 0`, seedID)
	if err != nil {
		return nil, fmt.Errorf("clone items select: %w", err)
	}
	defer rows.Close()
	type item struct {
		id, feedID                            int64
		guid, dedupKey, crossKey, title, link string
		summary, content, categories          string
		durationSec                           sql.NullInt64
		imageURL, publishedAt, readAt         sql.NullString
		fetchedAt                             string
		read, favorite, bookmark              bool
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.feedID, &it.guid, &it.dedupKey, &it.crossKey, &it.title,
			&it.link, &it.summary, &it.content, &it.categories, &it.durationSec, &it.imageURL,
			&it.publishedAt, &it.fetchedAt, &it.read, &it.readAt, &it.favorite, &it.bookmark); err != nil {
			return nil, fmt.Errorf("clone items scan: %w", err)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	itemMap := make(map[int64]int64, len(items))
	for _, it := range items {
		newFeed, ok := feedMap[it.feedID]
		if !ok {
			continue
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO items (
				feed_id, user_id, guid, dedup_key, cross_key, title, link, summary,
				content, categories, duration_sec, image_url, published_at, fetched_at,
				read, read_at, favorite, bookmark
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			newFeed, newID, it.guid, it.dedupKey, it.crossKey, it.title, it.link, it.summary,
			it.content, it.categories, it.durationSec, it.imageURL, it.publishedAt, it.fetchedAt,
			it.read, it.readAt, it.favorite, it.bookmark)
		if err != nil {
			return nil, fmt.Errorf("clone item insert: %w", err)
		}
		nid, err := res.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("clone item id: %w", err)
		}
		itemMap[it.id] = nid
	}
	return itemMap, nil
}

// cloneItemCategories populates item_categories for cloned items, derived from
// the copied items.categories string (SQLite cannot split it in SQL). Uses the
// same newline codec as the store so the clone's tags match the seed's.
func cloneItemCategories(ctx context.Context, tx *sql.Tx, seedID int64, itemMap map[int64]int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT i.id, i.categories
		FROM items i
		JOIN feeds f ON f.id = i.feed_id
		WHERE f.user_id = ? AND f.is_system = 0 AND i.categories <> ''`, seedID)
	if err != nil {
		return fmt.Errorf("clone item_categories select: %w", err)
	}
	defer rows.Close()
	type row struct {
		itemID     int64
		categories string
	}
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.itemID, &r.categories); err != nil {
			return fmt.Errorf("clone item_categories scan: %w", err)
		}
		rs = append(rs, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range rs {
		newItem, ok := itemMap[r.itemID]
		if !ok {
			continue
		}
		for _, c := range splitCategories(r.categories) {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO item_categories (item_id, category) VALUES (?, ?)
				ON CONFLICT (item_id, category) DO NOTHING`, newItem, c); err != nil {
				return fmt.Errorf("clone item_category insert: %w", err)
			}
		}
	}
	return nil
}

// cloneItemFeeds copies feed memberships for cloned items (preserving cross-feed
// posts) with both ids remapped.
func cloneItemFeeds(ctx context.Context, tx *sql.Tx, seedID int64, itemMap, feedMap map[int64]int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT mf.item_id, mf.feed_id, mf.created_at
		FROM item_feeds mf
		JOIN feeds f ON f.id = mf.feed_id
		WHERE f.user_id = ? AND f.is_system = 0`, seedID)
	if err != nil {
		return fmt.Errorf("clone item_feeds select: %w", err)
	}
	defer rows.Close()
	type membership struct {
		itemID, feedID int64
		createdAt      string
	}
	var ms []membership
	for rows.Next() {
		var m membership
		if err := rows.Scan(&m.itemID, &m.feedID, &m.createdAt); err != nil {
			return fmt.Errorf("clone item_feeds scan: %w", err)
		}
		ms = append(ms, m)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, m := range ms {
		newItem, okItem := itemMap[m.itemID]
		newFeed, okFeed := feedMap[m.feedID]
		if !okItem || !okFeed {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO item_feeds (item_id, feed_id, created_at) VALUES (?, ?, ?)
			ON CONFLICT (item_id, feed_id) DO NOTHING`,
			newItem, newFeed, m.createdAt); err != nil {
			return fmt.Errorf("clone item_feeds insert: %w", err)
		}
	}
	return nil
}

// cloneEnclosures copies media enclosures for cloned items.
func cloneEnclosures(ctx context.Context, tx *sql.Tx, seedID int64, itemMap map[int64]int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT e.item_id, e.url, e.title, e.mime_type, e.size, e.sort
		FROM item_enclosures e
		JOIN items i ON i.id = e.item_id
		JOIN feeds f ON f.id = i.feed_id
		WHERE f.user_id = ? AND f.is_system = 0`, seedID)
	if err != nil {
		return fmt.Errorf("clone enclosures select: %w", err)
	}
	defer rows.Close()
	type enc struct {
		itemID     int64
		url, title string
		mime       sql.NullString
		size, sort int64
	}
	var encs []enc
	for rows.Next() {
		var e enc
		if err := rows.Scan(&e.itemID, &e.url, &e.title, &e.mime, &e.size, &e.sort); err != nil {
			return fmt.Errorf("clone enclosures scan: %w", err)
		}
		encs = append(encs, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range encs {
		newItem, ok := itemMap[e.itemID]
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO item_enclosures (item_id, url, title, mime_type, size, sort)
			VALUES (?, ?, ?, ?, ?, ?)`,
			newItem, e.url, e.title, e.mime, e.size, e.sort); err != nil {
			return fmt.Errorf("clone enclosure insert: %w", err)
		}
	}
	return nil
}

// cloneCollections copies collections and their feed membership, returning a map
// of old collection id -> new collection id.
func cloneCollections(ctx context.Context, tx *sql.Tx, seedID, newID int64, feedMap map[int64]int64) (map[int64]int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, name, is_auto FROM collections WHERE user_id = ?`, seedID)
	if err != nil {
		return nil, fmt.Errorf("clone collections select: %w", err)
	}
	defer rows.Close()
	type collection struct {
		id     int64
		name   string
		isAuto bool
	}
	var cols []collection
	for rows.Next() {
		var c collection
		if err := rows.Scan(&c.id, &c.name, &c.isAuto); err != nil {
			return nil, fmt.Errorf("clone collections scan: %w", err)
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	colMap := make(map[int64]int64, len(cols))
	for _, c := range cols {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO collections (user_id, name, is_auto) VALUES (?, ?, ?)`,
			newID, c.name, c.isAuto)
		if err != nil {
			return nil, fmt.Errorf("clone collection insert: %w", err)
		}
		nid, err := res.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("clone collection id: %w", err)
		}
		colMap[c.id] = nid
	}

	rows2, err := tx.QueryContext(ctx, `
		SELECT cf.collection_id, cf.feed_id
		FROM collection_feeds cf
		JOIN collections c ON c.id = cf.collection_id
		WHERE c.user_id = ?`, seedID)
	if err != nil {
		return nil, fmt.Errorf("clone collection_feeds select: %w", err)
	}
	defer rows2.Close()
	type cf struct{ collectionID, feedID int64 }
	var cfs []cf
	for rows2.Next() {
		var x cf
		if err := rows2.Scan(&x.collectionID, &x.feedID); err != nil {
			return nil, fmt.Errorf("clone collection_feeds scan: %w", err)
		}
		cfs = append(cfs, x)
	}
	if err := rows2.Err(); err != nil {
		return nil, err
	}
	for _, x := range cfs {
		newCol, ok := colMap[x.collectionID]
		newFeed, ok2 := feedMap[x.feedID]
		if !ok || !ok2 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO collection_feeds (collection_id, feed_id) VALUES (?, ?)
			ON CONFLICT (collection_id, feed_id) DO NOTHING`, newCol, newFeed); err != nil {
			return nil, fmt.Errorf("clone collection_feeds insert: %w", err)
		}
	}
	return colMap, nil
}

// cloneLists copies item lists (share tokens cleared, since a throwaway demo
// must not inherit a public link) and their item membership.
func cloneLists(ctx context.Context, tx *sql.Tx, seedID, newID int64, itemMap map[int64]int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM lists WHERE user_id = ?`, seedID)
	if err != nil {
		return fmt.Errorf("clone lists select: %w", err)
	}
	defer rows.Close()
	type list struct {
		id   int64
		name string
	}
	var lists []list
	for rows.Next() {
		var l list
		if err := rows.Scan(&l.id, &l.name); err != nil {
			return fmt.Errorf("clone lists scan: %w", err)
		}
		lists = append(lists, l)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	listMap := make(map[int64]int64, len(lists))
	for _, l := range lists {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO lists (user_id, name) VALUES (?, ?)`, newID, l.name)
		if err != nil {
			return fmt.Errorf("clone list insert: %w", err)
		}
		nid, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("clone list id: %w", err)
		}
		listMap[l.id] = nid
	}

	rows2, err := tx.QueryContext(ctx, `
		SELECT li.list_id, li.item_id, li.created_at
		FROM list_items li
		JOIN lists l ON l.id = li.list_id
		WHERE l.user_id = ?`, seedID)
	if err != nil {
		return fmt.Errorf("clone list_items select: %w", err)
	}
	defer rows2.Close()
	type li struct {
		listID, itemID int64
		createdAt      string
	}
	var lis []li
	for rows2.Next() {
		var x li
		if err := rows2.Scan(&x.listID, &x.itemID, &x.createdAt); err != nil {
			return fmt.Errorf("clone list_items scan: %w", err)
		}
		lis = append(lis, x)
	}
	if err := rows2.Err(); err != nil {
		return err
	}
	for _, x := range lis {
		newList, ok := listMap[x.listID]
		newItem, ok2 := itemMap[x.itemID]
		if !ok || !ok2 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO list_items (list_id, item_id, created_at) VALUES (?, ?, ?)
			ON CONFLICT (list_id, item_id) DO NOTHING`, newList, newItem, x.createdAt); err != nil {
			return fmt.Errorf("clone list_items insert: %w", err)
		}
	}
	return nil
}

// cloneFilters copies ingest-filter rules, remapping a feed-scoped rule's feed
// (an unclonable feed drops the rule to user-wide rather than pointing at the
// seed's feed).
func cloneFilters(ctx context.Context, tx *sql.Tx, seedID, newID int64, feedMap map[int64]int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT feed_id, action, field, pattern, is_regex
		FROM filters WHERE user_id = ?`, seedID)
	if err != nil {
		return fmt.Errorf("clone filters select: %w", err)
	}
	defer rows.Close()
	type filter struct {
		feedID        sql.NullInt64
		action, field string
		pattern       string
		isRegex       int64
	}
	var fs []filter
	for rows.Next() {
		var f filter
		if err := rows.Scan(&f.feedID, &f.action, &f.field, &f.pattern, &f.isRegex); err != nil {
			return fmt.Errorf("clone filters scan: %w", err)
		}
		fs = append(fs, f)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, f := range fs {
		var newFeed any
		if f.feedID.Valid {
			id, ok := feedMap[f.feedID.Int64]
			if !ok {
				continue
			}
			newFeed = id
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO filters (user_id, feed_id, action, field, pattern, is_regex)
			VALUES (?, ?, ?, ?, ?, ?)`,
			newID, newFeed, f.action, f.field, f.pattern, f.isRegex); err != nil {
			return fmt.Errorf("clone filter insert: %w", err)
		}
	}
	return nil
}

// cloneAuthorLinks copies external author bookmarks, remapping the author.
func cloneAuthorLinks(ctx context.Context, tx *sql.Tx, seedID, newID int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT al.author_id, al.label, al.url, al.created_at
		FROM author_links al
		JOIN authors a ON a.id = al.author_id
		WHERE a.user_id = ? AND a.is_system = 0`, seedID)
	if err != nil {
		return fmt.Errorf("clone author_links select: %w", err)
	}
	defer rows.Close()
	type link struct {
		authorID       int64
		label          sql.NullString
		url, createdAt string
	}
	var ls []link
	for rows.Next() {
		var l link
		if err := rows.Scan(&l.authorID, &l.label, &l.url, &l.createdAt); err != nil {
			return fmt.Errorf("clone author_links scan: %w", err)
		}
		ls = append(ls, l)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	authorMap, err := cloneAuthorIDMap(ctx, tx, seedID, newID)
	if err != nil {
		return err
	}
	for _, l := range ls {
		newAuthor, ok := authorMap[l.authorID]
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO author_links (user_id, author_id, label, url, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			newID, newAuthor, l.label, l.url, l.createdAt); err != nil {
			return fmt.Errorf("clone author_link insert: %w", err)
		}
	}
	return nil
}

// cloneSourceIcons copies cached source icons as URLs only (the binary/blob key
// is not shared; the clone re-fetches the icon when needed).
func cloneSourceIcons(ctx context.Context, tx *sql.Tx, seedID, newID int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT domain, icon_url FROM source_icons WHERE user_id = ?`, seedID)
	if err != nil {
		return fmt.Errorf("clone source_icons select: %w", err)
	}
	defer rows.Close()
	type icon struct{ domain, iconURL string }
	var icons []icon
	for rows.Next() {
		var i icon
		if err := rows.Scan(&i.domain, &i.iconURL); err != nil {
			return fmt.Errorf("clone source_icons scan: %w", err)
		}
		icons = append(icons, i)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, i := range icons {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO source_icons (user_id, domain, icon_url) VALUES (?, ?, ?)
			ON CONFLICT (user_id, domain) DO NOTHING`, newID, i.domain, i.iconURL); err != nil {
			return fmt.Errorf("clone source_icon insert: %w", err)
		}
	}
	return nil
}

// cloneViewPrefs copies per-scope display preferences (list/grid).
func cloneViewPrefs(ctx context.Context, tx *sql.Tx, seedID, newID int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT scope, mode FROM user_view_prefs WHERE user_id = ?`, seedID)
	if err != nil {
		return fmt.Errorf("clone view prefs select: %w", err)
	}
	defer rows.Close()
	type pref struct{ scope, mode string }
	var prefs []pref
	for rows.Next() {
		var p pref
		if err := rows.Scan(&p.scope, &p.mode); err != nil {
			return fmt.Errorf("clone view prefs scan: %w", err)
		}
		prefs = append(prefs, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range prefs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO user_view_prefs (user_id, scope, mode) VALUES (?, ?, ?)
			ON CONFLICT (user_id, scope) DO NOTHING`, newID, p.scope, p.mode); err != nil {
			return fmt.Errorf("clone view pref insert: %w", err)
		}
	}
	return nil
}

// remapHomeConfig rewrites the seed's home_config JSON so pinned collection
// sections reference the cloned collection ids. Sections whose collection could
// not be cloned (or that are not collections) are dropped.
func remapHomeConfig(ctx context.Context, tx *sql.Tx, newID int64, raw string, collectionMap map[int64]int64) error {
	if raw == "" {
		return nil
	}
	var sections []map[string]any
	if err := json.Unmarshal([]byte(raw), &sections); err != nil {
		// A corrupt seed config degrades to the default home rather than failing
		// the whole clone.
		return nil
	}
	out := make([]map[string]any, 0, len(sections))
	for _, s := range sections {
		kind, _ := s["kind"].(string)
		if kind != "collection" {
			continue
		}
		ref, _ := s["ref_id"].(float64)
		newRef, ok := collectionMap[int64(ref)]
		if !ok {
			continue
		}
		s["ref_id"] = newRef
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	buf, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET home_config = ? WHERE id = ?`, string(buf), newID); err != nil {
		return fmt.Errorf("clone remap home config: %w", err)
	}
	return nil
}
