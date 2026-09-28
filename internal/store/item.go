package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

type Item struct {
	ID     int64
	FeedID int64 // owner/display feed; memberships live in item_feeds
	UserID int64 // denormalized owner, for user-scoped lookups and the cross index
	GUID   string
	// Identity is the stable per-feed dedup key (a plugin's Item.Identity).
	// Empty means "use GUID". Stored as items.dedup_key.
	Identity string
	// SharedKey is the cross-feed identity a plugin's Enricher supplied for the
	// item (e.g. "reddit:t3_<id>"). It is the input Upsert turns into the stored
	// items.cross_key; on a read it is empty (CrossKey carries the stored value).
	SharedKey string
	// CrossKey is the per-user cross-feed identity ("reddit:t3_<id>" for a
	// reddit post); empty when the item cannot be shared between feeds. Stored
	// as items.cross_key and unique per user. Populated on read; Upsert derives
	// it from SharedKey.
	CrossKey   string
	Title      string
	Link       string
	Summary    string
	Categories []string
	ImageURL   string
	// DurationSec is a media item's runtime in seconds. 0 means unknown.
	DurationSec int
	PublishedAt string
	FetchedAt   string
	Read        bool
	ReadAt      string
	Favorite    bool
}

// joinCategories encodes an item's categories for the denormalized
// items.categories column: newline-joined, so a category containing a comma or
// space stays intact. Newlines inside a category are stripped so the encoding
// is unambiguous.
func joinCategories(cats []string) string {
	if len(cats) == 0 {
		return ""
	}
	clean := make([]string, 0, len(cats))
	for _, c := range cats {
		c = strings.ReplaceAll(strings.TrimSpace(c), "\n", " ")
		if c != "" {
			clean = append(clean, c)
		}
	}
	return strings.Join(clean, "\n")
}

// splitCategories decodes the items.categories column back into a slice.
func splitCategories(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// dedupKey is the value items are deduplicated on: the item's Identity when
// set, else its GUID. It keeps the storage layer's identity rule in one place.
func dedupKey(it Item) string {
	if it.Identity != "" {
		return it.Identity
	}
	return it.GUID
}

// crossFeedKey normalizes an item's plugin-supplied SharedKey into the stored
// items.cross_key, or "" when the item is not cross-deduplicated. The plugin
// owns what a shared key looks like (e.g. reddit's "reddit:t3_<id>"); the store
// only trims it and treats an empty value as "not shared".
func crossFeedKey(sharedKey string) string {
	return strings.TrimSpace(sharedKey)
}

// CrossFeedKey is the exported crossFeedKey, for callers that resolve an item
// without going through Upsert (e.g. enclosure attachment in the poller).
func CrossFeedKey(sharedKey string) string { return crossFeedKey(sharedKey) }

// ItemWithFeed joins an item with its feed and author for display.
type ItemWithFeed struct {
	Item
	FeedTitle    string
	FeedURL      string
	FeedHomeURL  string // the feed's home page, for source-icon lookups
	FeedIsSystem bool   // true for a saved page (hidden per-user system feed)
	AuthorID     int64
	AuthorName   string
	Sources      []ItemSource // additional feeds this item appears in (view-time dedup)
	// Kind is the item's view-time display classification (from the plugin's
	// decorator). KindText is the default.
	Kind ItemKind
	// Attribution is the item's resolved source line ("r/cats by u/sam") from
	// the plugin's decorator, or nil to keep the author/feed source.
	Attribution []AttributionPart
	// ThumbURL is a decorator-supplied row thumbnail override ("" keeps the
	// stored ImageURL).
	ThumbURL string
	Timezone string // user's IANA timezone, for relative timestamps in templates
}

// ItemSource is one alternate feed an item is a member of, besides the owner
// feed whose title/author the row displays. Populated from item_feeds (and, for
// the legacy title-based view-time collapse, by httpapi's dedupItems).
type ItemSource struct {
	FeedID     int64
	FeedTitle  string
	AuthorID   int64  // the source feed's author, for reddit "r/x by u/y" links
	AuthorName string // "" when the source feed has no author
	Link       string
}

type ItemFilter struct {
	UnreadOnly    bool
	ReadOnly      bool
	FavoritesOnly bool
	FeedID        int64 // 0 = all
	AuthorID      int64 // 0 = all
	CollectionID  int64 // 0 = all
	BeforeID      int64 // keyset cursor (descending): only items ordered before this id
	AfterID       int64 // keyset cursor (ascending): only items ordered after this id
	Ascending     bool  // oldest first (default false = newest first)
	Limit         int
}

type ItemStore struct {
	q  *sqlcgen.Queries
	db *sql.DB // raw handle for the FTS5 search query sqlc cannot generate
	// policy is the host's per-URL site rules (from the plugin layer), used to
	// resolve an item's tokens to the user's subscribed feeds.
	policy URLPolicy
	// decorator supplies view-time item decoration (from the plugin layer).
	decorator ItemDecorator
}

// Upsert stores an item for feedID, deduplicating within the feed on
// (feed_id, dedup_key) — the item's stable Identity, or its GUID when none is
// set. An item whose SharedKey is set (a plugin's Enricher supplied it, e.g. a
// reddit post's fullname) additionally carries a per-user cross_key, so the same
// post arriving through two subscriptions (a subreddit feed and a user feed)
// resolves to one row with two memberships rather than two rows: read/favorite/
// list/share state is then shared automatically.
//
// It reports whether the item is new to feedID (a new row or a new membership),
// which the poller uses to detect an exhausted history. When the item already
// exists its content snapshot (summary, categories, thumbnail, duration) is
// refreshed without touching identity, published_at or read state.
func (s *ItemStore) Upsert(feedID int64, it Item) (inserted bool, err error) {
	key := dedupKey(it)
	crossKey := crossFeedKey(it.SharedKey)
	ctx := context.Background()

	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin upsert: %w", err)
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)

	userID, err := q.FeedUserID(ctx, feedID)
	if err != nil {
		return false, fmt.Errorf("upsert item feed owner: %w", err)
	}

	var itemID int64
	if crossKey != "" {
		itemID, err = q.GetItemByUserCrossKey(ctx, sqlcgen.GetItemByUserCrossKeyParams{
			UserID:   userID,
			CrossKey: crossKey,
		})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("upsert item cross lookup: %w", err)
		}
		if errors.Is(err, sql.ErrNoRows) {
			itemID = 0
		}
	}

	newRow := false
	if itemID == 0 {
		res, err := q.UpsertItem(ctx, sqlcgen.UpsertItemParams{
			FeedID:      feedID,
			UserID:      userID,
			Guid:        it.GUID,
			DedupKey:    key,
			CrossKey:    crossKey,
			Title:       it.Title,
			Link:        it.Link,
			Summary:     it.Summary,
			Categories:  joinCategories(it.Categories),
			ImageUrl:    ns(it.ImageURL),
			DurationSec: ni(int64(it.DurationSec)),
			PublishedAt: ns(it.PublishedAt),
			FetchedAt:   it.FetchedAt,
			Read:        it.Read,
			ReadAt:      ns(it.ReadAt),
		})
		if err != nil {
			return false, fmt.Errorf("upsert item: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("upsert item rows affected: %w", err)
		}
		newRow = n > 0
		// Resolve the id whether we inserted or collided on (feed_id, dedup_key).
		itemID, err = q.GetItemByDedupKey(ctx, sqlcgen.GetItemByDedupKeyParams{
			FeedID:   feedID,
			DedupKey: key,
		})
		if err != nil {
			return false, fmt.Errorf("upsert item resolve id: %w", err)
		}
	}

	// A row that predates the cross-key backfill (or was stored by a feed that
	// did not derive one) adopts it now, so subsequent polls resolve to it.
	if crossKey != "" && !newRow {
		if err := q.SetItemCrossKey(ctx, sqlcgen.SetItemCrossKeyParams{
			CrossKey: crossKey,
			FeedID:   feedID,
			DedupKey: key,
		}); err != nil {
			return false, fmt.Errorf("set item cross key: %w", err)
		}
	}

	// Refresh the content snapshot of an already-stored row (including one owned
	// by another feed), so a cross-feed member tracks the live feed too.
	if !newRow {
		if err := q.UpdateItemSnapshotByID(ctx, sqlcgen.UpdateItemSnapshotByIDParams{
			Summary:     it.Summary,
			Categories:  joinCategories(it.Categories),
			ImageUrl:    ns(it.ImageURL),
			DurationSec: ni(int64(it.DurationSec)),
			ID:          itemID,
		}); err != nil {
			return false, fmt.Errorf("refresh item snapshot: %w", err)
		}
	}

	// Membership makes the item visible in this feed's streams. A new membership
	// (or a new row) counts as new for this feed.
	mres, err := q.AddItemFeed(ctx, sqlcgen.AddItemFeedParams{ItemID: itemID, FeedID: feedID})
	if err != nil {
		return false, fmt.Errorf("add item feed: %w", err)
	}
	mn, err := mres.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("add item feed rows affected: %w", err)
	}
	inserted = newRow || mn > 0
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit upsert: %w", err)
	}
	return inserted, nil
}

func (s *ItemStore) List(userID int64, f ItemFilter) ([]ItemWithFeed, error) {
	items, _, err := s.ListPage(userID, f)
	return items, err
}

// ListPage returns up to f.Limit items plus whether more exist beyond them,
// so callers can render a "load more" button. Items are ordered newest first
// (oldest first when f.Ascending); pass the last returned id as f.BeforeID (desc)
// or f.AfterID (asc) to page further. One extra row is fetched to detect the
// next page.
func (s *ItemStore) ListPage(userID int64, f ItemFilter) ([]ItemWithFeed, bool, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if f.Ascending {
		rows, err := s.q.ListItemsAsc(context.Background(), sqlcgen.ListItemsAscParams{
			UserID:       userID,
			FeedID:       f.FeedID,
			AuthorID:     f.AuthorID,
			CollectionID: f.CollectionID,
			Unread:       boolInt(f.UnreadOnly),
			Read:         boolInt(f.ReadOnly),
			Favorites:    boolInt(f.FavoritesOnly),
			AfterID:      f.AfterID,
			Limit:        int64(limit) + 1,
		})
		if err != nil {
			return nil, false, err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		out := make([]ItemWithFeed, 0, len(rows))
		for _, r := range rows {
			out = append(out, toItemWithFeed(r.ID, r.FeedID, r.Guid, r.Title, r.Link, r.Summary, r.Categories,
				r.ImageUrl, r.DurationSec, r.PublishedAt, r.FetchedAt, r.Read, r.Favorite, r.ReadAt,
				r.FeedTitle, r.FeedUrl, r.FeedHomeUrl, r.FeedIsSystem, r.AuthorID, r.AuthorName))
		}
		if err := s.attachSources(userID, out); err != nil {
			return nil, false, err
		}
		return out, hasMore, nil
	}
	rows, err := s.q.ListItems(context.Background(), sqlcgen.ListItemsParams{
		UserID:       userID,
		FeedID:       f.FeedID,
		AuthorID:     f.AuthorID,
		CollectionID: f.CollectionID,
		Unread:       boolInt(f.UnreadOnly),
		Read:         boolInt(f.ReadOnly),
		Favorites:    boolInt(f.FavoritesOnly),
		BeforeID:     f.BeforeID,
		Limit:        int64(limit) + 1,
	})
	if err != nil {
		return nil, false, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	out := make([]ItemWithFeed, 0, len(rows))
	for _, r := range rows {
		out = append(out, toItemWithFeed(r.ID, r.FeedID, r.Guid, r.Title, r.Link, r.Summary, r.Categories,
			r.ImageUrl, r.DurationSec, r.PublishedAt, r.FetchedAt, r.Read, r.Favorite, r.ReadAt,
			r.FeedTitle, r.FeedUrl, r.FeedHomeUrl, r.FeedIsSystem, r.AuthorID, r.AuthorName))
	}
	if err := s.attachSources(userID, out); err != nil {
		return nil, false, err
	}
	return out, hasMore, nil
}

func (s *ItemStore) ByID(userID, id int64) (Item, error) {
	it, err := s.q.GetItem(context.Background(), sqlcgen.GetItemParams{ID: id, UserID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	if err != nil {
		return Item{}, err
	}
	return toItem(it), nil
}

// ByFeedIdentity returns the id of the item stored for (feedID, identity), or 0
// when it does not exist. identity is the item's stable dedup key: its
// Identity when set, else its GUID.
func (s *ItemStore) ByFeedIdentity(feedID int64, identity string) (int64, error) {
	id, err := s.q.GetItemByDedupKey(context.Background(), sqlcgen.GetItemByDedupKeyParams{
		FeedID:   feedID,
		DedupKey: identity,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// ByUserCrossKey returns the id of the item stored for an identity shared
// across a user's feeds (crossKey), or 0 when it does not exist.
func (s *ItemStore) ByUserCrossKey(userID int64, crossKey string) (int64, error) {
	id, err := s.q.GetItemByUserCrossKey(context.Background(), sqlcgen.GetItemByUserCrossKeyParams{
		UserID:   userID,
		CrossKey: crossKey,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// IngestItemID resolves the stored row for a just-ingested entry: its stable
// per-feed identity first, then the user's cross-feed identity. Used to attach
// enclosures to the row however it was first stored.
func (s *ItemStore) IngestItemID(userID, feedID int64, identity, crossKey string) (int64, error) {
	id, err := s.ByFeedIdentity(feedID, identity)
	if err != nil || id != 0 {
		return id, err
	}
	if crossKey == "" {
		return 0, nil
	}
	return s.ByUserCrossKey(userID, crossKey)
}

// searchPageLimit is the default page size for search results.
const searchPageLimit = 25

// SearchPage runs an FTS5 query over item titles and summaries, scoped to the
// user, returning one page plus whether more results exist. query must be a
// valid FTS5 MATCH expression (see parseSearchQuery in httpapi). This query is
// hand-written because sqlc cannot introspect FTS5 virtual tables; the column
// set mirrors ListItems so rows reuse the generated scanner.
func (s *ItemStore) SearchPage(userID int64, query string, f ItemFilter) ([]ItemWithFeed, bool, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = searchPageLimit
	}
	const sql = `SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.categories, i.image_url, i.duration_sec,
       i.published_at, i.fetched_at, i.read, i.favorite, i.read_at,
       f.title AS feed_title, f.feed_url AS feed_url, f.home_url AS feed_home_url,
       f.is_system AS feed_is_system,
       a.id AS author_id, a.name AS author_name
FROM items i
JOIN feeds f ON f.id = i.feed_id
LEFT JOIN authors a ON a.id = f.author_id
JOIN items_fts fts ON fts.rowid = i.id
WHERE i.user_id = ?1
  AND items_fts MATCH ?2
  AND (CAST(?3 AS INTEGER) = 0 OR EXISTS (
        SELECT 1 FROM item_feeds mf WHERE mf.item_id = i.id AND mf.feed_id = CAST(?3 AS INTEGER)))
  AND (CAST(?4 AS INTEGER) = 0 OR EXISTS (
        SELECT 1 FROM item_feeds mf JOIN feeds mf2 ON mf2.id = mf.feed_id
        WHERE mf.item_id = i.id AND mf2.author_id = CAST(?4 AS INTEGER)))
  AND (CAST(?5 AS INTEGER) = 0 OR i.read = 0)
  AND (CAST(?6 AS INTEGER) = 0 OR
       (COALESCE(i.published_at, i.fetched_at), i.id) <
       (SELECT COALESCE(published_at, fetched_at), id FROM items WHERE id = CAST(?6 AS INTEGER)))
ORDER BY COALESCE(i.published_at, i.fetched_at) DESC, i.id DESC
LIMIT ?7`
	rows, err := s.db.QueryContext(context.Background(), sql, userID, query,
		f.FeedID, f.AuthorID, boolInt(f.UnreadOnly), f.BeforeID, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	out := make([]ItemWithFeed, 0, limit)
	for rows.Next() {
		var r sqlcgen.ListItemsRow
		if err := rows.Scan(&r.ID, &r.FeedID, &r.Guid, &r.Title, &r.Link, &r.Summary, &r.Categories,
			&r.ImageUrl, &r.DurationSec, &r.PublishedAt, &r.FetchedAt, &r.Read, &r.Favorite, &r.ReadAt,
			&r.FeedTitle, &r.FeedUrl, &r.FeedHomeUrl, &r.FeedIsSystem, &r.AuthorID, &r.AuthorName); err != nil {
			return nil, false, err
		}
		out = append(out, toItemWithFeed(r.ID, r.FeedID, r.Guid, r.Title, r.Link, r.Summary, r.Categories,
			r.ImageUrl, r.DurationSec, r.PublishedAt, r.FetchedAt, r.Read, r.Favorite, r.ReadAt,
			r.FeedTitle, r.FeedUrl, r.FeedHomeUrl, r.FeedIsSystem, r.AuthorID, r.AuthorName))
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	if err := s.attachSources(userID, out); err != nil {
		return nil, false, err
	}
	return out, hasMore, nil
}

// BackfillYouTubeThumbnails fills image_url for YouTube items stored before the
// plugin's media:thumbnail fix (their feed advertises thumbnails only through
// media:group, which the generic parser did not map). The URL is deterministic
// from the video id in the GUID, so no network is involved. It returns how many
// rows were updated.
func (s *ItemStore) BackfillYouTubeThumbnails() (int64, error) {
	res, err := s.q.BackfillYouTubeThumbnails(context.Background())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

// CountItemsMissingYouTubeThumbnail counts YouTube items with no image_url,
// i.e. the rows BackfillYouTubeThumbnails would fix.
func (s *ItemStore) CountItemsMissingYouTubeThumbnail() (int64, error) {
	return s.q.CountItemsMissingYouTubeThumbnail(context.Background())
}

// Enclosure is one media attachment (podcast, video) on an item.
type Enclosure struct {
	URL      string
	Title    string
	MIMEType string
	Size     int64
	Sort     int
}

// Enclosures returns an item's media attachments in order.
func (s *ItemStore) Enclosures(itemID int64) ([]Enclosure, error) {
	rows, err := s.q.ListEnclosures(context.Background(), itemID)
	if err != nil {
		return nil, err
	}
	out := make([]Enclosure, 0, len(rows))
	for _, r := range rows {
		out = append(out, Enclosure{URL: r.Url, Title: r.Title, MIMEType: r.MimeType.String, Size: r.Size, Sort: int(r.Sort)})
	}
	return out, nil
}

// RecentTimes returns a feed's most recent item timestamps
// (published_at, falling back to fetched_at), newest first, up to limit.
// Used by the poller to derive a feed's posting cadence and newest item.
func (s *ItemStore) RecentTimes(feedID int64, limit int) ([]string, error) {
	rows, err := s.q.ListRecentItemTimes(context.Background(), sqlcgen.ListRecentItemTimesParams{
		FeedID: feedID,
		Limit:  int64(limit),
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// AuthorRecentTimes returns the newest item times across an author's feeds,
// newest first, up to limit. Used to estimate the author's posting cadence.
func (s *ItemStore) AuthorRecentTimes(userID, authorID int64, limit int) ([]string, error) {
	return s.q.ListAuthorRecentItemTimes(context.Background(), sqlcgen.ListAuthorRecentItemTimesParams{
		UserID:   userID,
		AuthorID: authorID,
		Limit:    int64(limit),
	})
}

// AuthorItemStats aggregates one author's posts across all of their feeds.
type AuthorItemStats struct {
	Total     int    // all-time posts
	Unread    int    // unread posts
	Read      int    // read posts
	Favorites int    // favorited posts
	Recent    int    // posts within the recent window
	FirstAt   string // oldest post time, "" when the author has no posts
	LastAt    string // newest post time, "" when the author has no posts
}

// StatsAuthor returns all-time aggregate stats for an author across all of their
// feeds: total/read/unread/favorite counts, first and last post times, and how
// many posts fall in the last 30 days.
func (s *ItemStore) StatsAuthor(userID, authorID int64) (AuthorItemStats, error) {
	r, err := s.q.GetAuthorItemStats(context.Background(), sqlcgen.GetAuthorItemStatsParams{
		UserID:   userID,
		AuthorID: authorID,
	})
	if err != nil {
		return AuthorItemStats{}, err
	}
	return AuthorItemStats{
		Total:     int(r.TotalPosts),
		Unread:    int(r.UnreadPosts),
		Read:      int(r.ReadPosts),
		Favorites: int(r.FavoritePosts),
		Recent:    int(r.RecentPosts),
		FirstAt:   r.FirstPostAt,
		LastAt:    r.LastPostAt,
	}, nil
}

// ReplaceEnclosures deletes and re-inserts an item's enclosures.
func (s *ItemStore) ReplaceEnclosures(itemID int64, encs []Enclosure) error {
	if err := s.q.DeleteEnclosures(context.Background(), itemID); err != nil {
		return err
	}
	for i, e := range encs {
		if err := s.q.InsertEnclosure(context.Background(), sqlcgen.InsertEnclosureParams{
			ItemID:   itemID,
			Url:      e.URL,
			Title:    e.Title,
			MimeType: ns(e.MIMEType),
			Size:     e.Size,
			Sort:     int64(i),
		}); err != nil {
			return err
		}
	}
	return nil
}

// DeleteSaved hard-deletes a saved page: an item under the user's hidden
// system feed. Regular feed items are never touched (the query's system-feed
// guard). Child rows (enclosures, list memberships, shares) cascade, and the
// items_ad trigger keeps items_fts consistent. Returns ErrNotFound when the id
// is not one of the user's saved pages.
func (s *ItemStore) DeleteSaved(userID, itemID int64) error {
	res, err := s.q.DeleteSavedItem(context.Background(), sqlcgen.DeleteSavedItemParams{
		ItemID: itemID,
		UserID: userID,
	})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListFeedItemsForFilter returns every item that is a member of feedID, for
// retroactively evaluating an ingest filter rule over already-stored items.
// Unlike ListItems it is not limited and includes the feed's items however they
// became members (a cross-feed post shows up in every feed it belongs to).
func (s *ItemStore) ListFeedItemsForFilter(feedID int64) ([]ItemWithFeed, error) {
	rows, err := s.q.ListFeedItemsForFilter(context.Background(), feedID)
	if err != nil {
		return nil, err
	}
	out := make([]ItemWithFeed, 0, len(rows))
	for _, r := range rows {
		out = append(out, toItemWithFeed(r.ID, r.FeedID, r.Guid, r.Title, r.Link, r.Summary, r.Categories,
			r.ImageUrl, r.DurationSec, r.PublishedAt, r.FetchedAt, r.Read, r.Favorite, r.ReadAt,
			r.FeedTitle, r.FeedUrl, r.FeedHomeUrl, r.FeedIsSystem, r.AuthorID, r.AuthorName))
	}
	return out, nil
}

// RemoveFeedMemberships removes itemIDs from feedID's membership set. Items
// still reachable through another feed are re-homed to one of those; items whose
// last membership is removed are deleted (cascading enclosures, list items,
// shares and the FTS index). One transaction, scoped to the feed's owner. It
// returns how many memberships were removed.
func (s *ItemStore) RemoveFeedMemberships(userID, feedID int64, itemIDs []int64) (int, error) {
	if len(itemIDs) == 0 {
		return 0, nil
	}
	// The feed is user-scoped so a caller can only touch its own feed.
	if _, err := s.q.GetFeed(context.Background(), sqlcgen.GetFeedParams{ID: feedID, UserID: userID}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin remove memberships: %w", err)
	}
	defer tx.Rollback()
	ctx := context.Background()
	q := s.q.WithTx(tx)

	if err := q.RemoveItemFeedMemberships(ctx, sqlcgen.RemoveItemFeedMembershipsParams{
		FeedID:  feedID,
		ItemIDs: itemIDs,
	}); err != nil {
		return 0, fmt.Errorf("remove memberships: %w", err)
	}
	// Re-home rows still owned by this feed that another feed still holds, then
	// delete the rest (their last membership is gone).
	if err := q.RehomeOwnedItems(ctx, sqlcgen.RehomeOwnedItemsParams{
		FeedID:  feedID,
		ItemIDs: itemIDs,
	}); err != nil {
		return 0, fmt.Errorf("rehome items: %w", err)
	}
	if err := q.DeleteOrphanOwnedItems(ctx, sqlcgen.DeleteOrphanOwnedItemsParams{
		FeedID:  feedID,
		ItemIDs: itemIDs,
	}); err != nil {
		return 0, fmt.Errorf("delete orphan items: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(itemIDs), nil
}

// SetItemsReadBulk marks a set of the user's items read, recording the time.
func (s *ItemStore) SetItemsReadBulk(userID int64, itemIDs []int64) error {
	if len(itemIDs) == 0 {
		return nil
	}
	return s.q.SetItemsReadByIDs(context.Background(), sqlcgen.SetItemsReadByIDsParams{
		ReadAt:  ns(db.Now()),
		UserID:  userID,
		ItemIDs: itemIDs,
	})
}

// OneWithFeed returns a single item joined with its feed and author.
func (s *ItemStore) OneWithFeed(userID, itemID int64) (ItemWithFeed, error) {
	it, err := s.q.GetItemWithFeed(context.Background(), sqlcgen.GetItemWithFeedParams{ID: itemID, UserID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		return ItemWithFeed{}, ErrNotFound
	}
	if err != nil {
		return ItemWithFeed{}, err
	}
	slice := []ItemWithFeed{toItemWithFeed(it.ID, it.FeedID, it.Guid, it.Title, it.Link, it.Summary, it.Categories,
		it.ImageUrl, it.DurationSec, it.PublishedAt, it.FetchedAt, it.Read, it.Favorite, it.ReadAt,
		it.FeedTitle, it.FeedUrl, it.FeedHomeUrl, it.FeedIsSystem, it.AuthorID, it.AuthorName)}
	if err := s.attachSources(userID, slice); err != nil {
		return ItemWithFeed{}, err
	}
	return slice[0], nil
}

// OneWithFeedAny returns a single item regardless of user. Used by the public
// share page, which serves an item identified only by its share token.
func (s *ItemStore) OneWithFeedAny(itemID int64) (ItemWithFeed, error) {
	it, err := s.q.GetItemWithFeedAny(context.Background(), itemID)
	if errors.Is(err, sql.ErrNoRows) {
		return ItemWithFeed{}, ErrNotFound
	}
	if err != nil {
		return ItemWithFeed{}, err
	}
	return toItemWithFeed(it.ID, it.FeedID, it.Guid, it.Title, it.Link, it.Summary, it.Categories,
		it.ImageUrl, it.DurationSec, it.PublishedAt, it.FetchedAt, it.Read, it.Favorite, it.ReadAt,
		it.FeedTitle, it.FeedUrl, it.FeedHomeUrl, it.FeedIsSystem, it.AuthorID, it.AuthorName), nil
}

// attachSources populates each item's Sources from its item_feeds memberships
// other than the owner feed, in one query for the whole page, and resolves its
// category tokens against userID's subscribed feeds. This is the persisted
// cross-feed membership set, replacing the old title-based view-time collapse
// for items stored once. userID 0 (a public page) skips the token resolution.
func (s *ItemStore) attachSources(userID int64, items []ItemWithFeed) error {
	return attachSources(s.q, userID, s.policy, s.decorator, items)
}

// attachSources is the query-layer implementation, shared by ItemStore and
// ListStore (which has no ItemStore handle of its own).
func attachSources(q *sqlcgen.Queries, userID int64, policy URLPolicy, decorator ItemDecorator, items []ItemWithFeed) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(items))
	for i := range items {
		ids = append(ids, items[i].ID)
	}
	rows, err := q.ListItemSourcesForItems(context.Background(), ids)
	if err != nil {
		return err
	}
	byID := make(map[int64]int, len(items))
	for i := range items {
		byID[items[i].ID] = i
	}
	for _, r := range rows {
		idx, ok := byID[r.ItemID]
		if !ok {
			continue
		}
		// cross_key rides along on every membership row for the item; capture it
		// once (the dedup guard needs it without a per-item query).
		if items[idx].CrossKey == "" {
			items[idx].CrossKey = r.CrossKey
		}
		if r.FeedID == items[idx].FeedID {
			continue
		}
		items[idx].Sources = append(items[idx].Sources, ItemSource{
			FeedID:     r.FeedID,
			FeedTitle:  r.FeedTitle,
			AuthorID:   r.AuthorID.Int64,
			AuthorName: r.AuthorName.String,
		})
	}
	if err := decorate(q, userID, policy, decorator, items); err != nil {
		return err
	}
	return nil
}

// decorate runs the injected item decorator over a page of items and resolves
// its raw attribution tokens against the user's subscribed feeds. A token is
// matched to a feed by the URL policy's FeedToken (site rules owned by the
// plugin), not by the item's memberships, so a post seen through one feed links
// its other subjects internally even before those feeds have polled it.
func decorate(q *sqlcgen.Queries, userID int64, policy URLPolicy, decorator ItemDecorator, items []ItemWithFeed) error {
	if decorator == nil {
		return nil
	}
	raw, err := decorator.Decorate(items)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	// Resolve tokens to subscribed feeds only when there are any and the page is
	// user-scoped (a public page has no subscriptions to resolve against).
	byToken := map[string]TokenTarget{}
	if userID != 0 && policy != nil && anyTokens(raw) {
		feeds, err := q.ListUserFeeds(context.Background(), userID)
		if err != nil {
			return err
		}
		for _, f := range feeds {
			tok := policy.FeedToken(f.FeedUrl)
			if tok == "" {
				continue
			}
			if _, ok := byToken[tok]; !ok {
				byToken[tok] = TokenTarget{FeedID: f.FeedID, AuthorID: f.AuthorID}
			}
		}
	}
	byID := make(map[int64]int, len(items))
	for i := range items {
		byID[items[i].ID] = i
	}
	for id, d := range raw {
		i, ok := byID[id]
		if !ok {
			continue
		}
		items[i].Kind = d.Kind
		items[i].ThumbURL = d.ThumbURL
		items[i].Attribution = resolveAttribution(d.Attribution, byToken)
	}
	return nil
}

// TokenTarget is a subscribed feed a token resolved to.
type TokenTarget struct {
	FeedID   int64
	AuthorID int64 // 0 when the subscribed feed has no author
}

// resolveAttribution turns a decoration's raw parts into final parts: a token
// that resolves to a subscribed feed links internally (its author page, else
// its feed page), otherwise the part's external URL is used, and a part with
// neither is plain text.
func resolveAttribution(parts []RawAttributionPart, byToken map[string]TokenTarget) []AttributionPart {
	out := make([]AttributionPart, 0, len(parts))
	for _, p := range parts {
		ap := AttributionPart{Text: p.Text}
		if p.Token != "" {
			if t, ok := byToken[strings.ToLower(p.Token)]; ok {
				if t.AuthorID != 0 {
					ap.URL = "/authors/" + strconv.FormatInt(t.AuthorID, 10)
				} else {
					ap.URL = "/feeds/" + strconv.FormatInt(t.FeedID, 10)
				}
				out = append(out, ap)
				continue
			}
		}
		if p.URL != "" {
			ap.URL = p.URL
			ap.External = true
		}
		out = append(out, ap)
	}
	return out
}

// anyTokens reports whether any decoration carries an unresolvable-needing token.
func anyTokens(raw map[int64]RawDecoration) bool {
	for _, d := range raw {
		for _, p := range d.Attribution {
			if p.Token != "" {
				return true
			}
		}
	}
	return false
}

// SetRead marks an item read/unread, verifying it belongs to the user. When an
// item is marked read its read_at timestamp is recorded; unread clears it.
func (s *ItemStore) SetRead(userID, itemID int64, read bool) error {
	var readAt sql.NullString
	if read {
		readAt = ns(db.Now())
	}
	res, err := s.q.SetItemRead(context.Background(), sqlcgen.SetItemReadParams{
		Read:   read,
		ReadAt: readAt,
		ID:     itemID,
		UserID: userID,
	})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkBeforeRead marks every unread item in the same feed page as itemID that is
// newer than it (listed above it, newest first) as read. feedID is the page's
// feed; 0 falls back to the item's owner feed.
func (s *ItemStore) MarkBeforeRead(userID, feedID, itemID int64) error {
	return s.q.MarkItemsBeforeRead(context.Background(), sqlcgen.MarkItemsBeforeReadParams{
		ReadAt: ns(db.Now()),
		FeedID: feedID,
		ItemID: itemID,
		UserID: userID,
	})
}

// MarkAfterRead marks every unread item in the same feed page as itemID that is
// older than it (listed below it, newest first) as read. feedID is the page's
// feed; 0 falls back to the item's owner feed.
func (s *ItemStore) MarkAfterRead(userID, feedID, itemID int64) error {
	return s.q.MarkItemsAfterRead(context.Background(), sqlcgen.MarkItemsAfterReadParams{
		ReadAt: ns(db.Now()),
		FeedID: feedID,
		ItemID: itemID,
		UserID: userID,
	})
}

// MarkAuthorBeforeRead marks every unread item newer than itemID across all
// feeds owned by the item's author as read. Used on an author page, where the
// bulk action spans the author's whole feed set.
func (s *ItemStore) MarkAuthorBeforeRead(userID, itemID int64) error {
	return s.q.MarkAuthorItemsBeforeRead(context.Background(), sqlcgen.MarkAuthorItemsBeforeReadParams{
		ReadAt: ns(db.Now()),
		ItemID: itemID,
		UserID: userID,
	})
}

// MarkAuthorAfterRead marks every unread item older than itemID across all
// feeds owned by the item's author as read.
func (s *ItemStore) MarkAuthorAfterRead(userID, itemID int64) error {
	return s.q.MarkAuthorItemsAfterRead(context.Background(), sqlcgen.MarkAuthorItemsAfterReadParams{
		ReadAt: ns(db.Now()),
		ItemID: itemID,
		UserID: userID,
	})
}

// SetFavorite marks an item as a favorite or not, verifying it belongs to the user.
func (s *ItemStore) SetFavorite(userID, itemID int64, fav bool) error {
	res, err := s.q.SetItemFavorite(context.Background(), sqlcgen.SetItemFavoriteParams{
		Favorite: fav,
		ID:       itemID,
		UserID:   userID,
	})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkAllRead marks every item read for a user; pass feedID 0 for all feeds.
func (s *ItemStore) MarkAllRead(userID, feedID int64) error {
	return s.q.MarkAllItemsRead(context.Background(), sqlcgen.MarkAllItemsReadParams{
		ReadAt: ns(db.Now()),
		UserID: userID,
		FeedID: feedID,
	})
}

// MarkAuthorRead marks every item read across all of an author's feeds.
func (s *ItemStore) MarkAuthorRead(userID, authorID int64) error {
	return s.q.MarkAuthorItemsRead(context.Background(), sqlcgen.MarkAuthorItemsReadParams{
		ReadAt:   ns(db.Now()),
		UserID:   userID,
		AuthorID: authorID,
	})
}

// MarkOlderThanRead marks every unread item older than days (by
// COALESCE(published_at, fetched_at)) as read for a user, regardless of
// favorite state. It returns how many items were marked. days must be > 0;
// callers pass 0 to mean "disabled" and skip the call.
func (s *ItemStore) MarkOlderThanRead(userID int64, days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := db.FormatTime(time.Now().AddDate(0, 0, -days))
	res, err := s.q.MarkItemsOlderThanRead(context.Background(), sqlcgen.MarkItemsOlderThanReadParams{
		ReadAt: ns(db.Now()),
		Cutoff: ns(cutoff),
		UserID: userID,
	})
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// MarkAllUnread marks every item unread for a user; pass feedID 0 for all feeds.
func (s *ItemStore) MarkAllUnread(userID, feedID int64) error {
	return s.q.MarkAllItemsUnread(context.Background(), sqlcgen.MarkAllItemsUnreadParams{
		UserID: userID,
		FeedID: feedID,
	})
}

// CountUnread counts unread items for a user; feedID 0 means all feeds.
func (s *ItemStore) CountUnread(userID, feedID int64) (int, error) {
	n, err := s.q.CountUnreadItems(context.Background(), sqlcgen.CountUnreadItemsParams{
		UserID: userID,
		FeedID: feedID,
	})
	return int(n), err
}

// CountRead counts read items for a user; feedID 0 means all feeds.
func (s *ItemStore) CountRead(userID, feedID int64) (int, error) {
	n, err := s.q.CountReadItems(context.Background(), sqlcgen.CountReadItemsParams{
		UserID: userID,
		FeedID: feedID,
	})
	return int(n), err
}

// CountFavorites counts favorited items for a user; feedID 0 means all feeds.
func (s *ItemStore) CountFavorites(userID, feedID int64) (int, error) {
	n, err := s.q.CountFavoriteItems(context.Background(), sqlcgen.CountFavoriteItemsParams{
		UserID: userID,
		FeedID: feedID,
	})
	return int(n), err
}

// CountUnreadAuthor counts unread items across an author's feeds.
func (s *ItemStore) CountUnreadAuthor(userID, authorID int64) (int, error) {
	n, err := s.q.CountUnreadItemsByAuthor(context.Background(), sqlcgen.CountUnreadItemsByAuthorParams{
		UserID:   userID,
		AuthorID: authorID,
	})
	return int(n), err
}

// CountReadAuthor counts read items across an author's feeds.
func (s *ItemStore) CountReadAuthor(userID, authorID int64) (int, error) {
	n, err := s.q.CountReadItemsByAuthor(context.Background(), sqlcgen.CountReadItemsByAuthorParams{
		UserID:   userID,
		AuthorID: authorID,
	})
	return int(n), err
}

// CountFavoritesAuthor counts favorited items across an author's feeds.
func (s *ItemStore) CountFavoritesAuthor(userID, authorID int64) (int, error) {
	n, err := s.q.CountFavoriteItemsByAuthor(context.Background(), sqlcgen.CountFavoriteItemsByAuthorParams{
		UserID:   userID,
		AuthorID: authorID,
	})
	return int(n), err
}

// CountUnreadCollection counts unread items across a collection's feeds.
func (s *ItemStore) CountUnreadCollection(userID, collectionID int64) (int, error) {
	n, err := s.q.CountUnreadItemsByCollection(context.Background(), sqlcgen.CountUnreadItemsByCollectionParams{
		UserID:       userID,
		CollectionID: collectionID,
	})
	return int(n), err
}

// CountReadCollection counts read items across a collection's feeds.
func (s *ItemStore) CountReadCollection(userID, collectionID int64) (int, error) {
	n, err := s.q.CountReadItemsByCollection(context.Background(), sqlcgen.CountReadItemsByCollectionParams{
		UserID:       userID,
		CollectionID: collectionID,
	})
	return int(n), err
}

// Count returns the total number of items across all users.
func (s *ItemStore) Count() (int, error) {
	n, err := s.q.CountAllItems(context.Background())
	return int(n), err
}

// CountAllUnread returns the total number of unread items across all users.
func (s *ItemStore) CountAllUnread() (int, error) {
	n, err := s.q.CountAllUnreadItems(context.Background())
	return int(n), err
}
