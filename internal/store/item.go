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
	"github.com/metruzanca/nanoflux/internal/sanitize"
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
	// SharedKey is the cross-feed identity a plugin's SharedKeyer supplied for the
	// item (e.g. "site:type_<id>"). It is the input Upsert turns into the stored
	// items.cross_key; on a read it is empty (CrossKey carries the stored value).
	SharedKey string
	// CrossKey is the per-user cross-feed identity ("site:type_<id>"); empty when
	// the item cannot be shared between feeds. Stored
	// as items.cross_key and unique per user. Populated on read; Upsert derives
	// it from SharedKey.
	CrossKey string
	Title    string
	Link     string
	Summary  string
	// Content is a plugin-enriched body (full text, translation, transcript),
	// separate from Summary so a re-poll's snapshot refresh does not clobber it.
	// Empty means "no enrichment; render Summary".
	Content    string
	Categories []string
	ImageURL   string
	// ImageCacheKey is the object-storage key of the item's cached primary
	// image, set by the image cache at poll time. Empty means "not cached;
	// render ImageURL". It survives a snapshot refresh, which only rewrites
	// ImageURL.
	ImageCacheKey string
	// DurationSec is a media item's runtime in seconds. 0 means unknown.
	DurationSec int
	PublishedAt string
	FetchedAt   string
	Read        bool
	ReadAt      string
	Favorite    bool
	// Bookmark is membership of the special bookmarks list (save-for-later),
	// native like Favorite but a distinct signal: favorites feed the magic sort.
	Bookmark bool
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
// owns what a shared key looks like (e.g. "site:type_<id>"); the store only
// trims it and treats an empty value as "not shared".
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
	// ViewSources are the saved views that put this item in the collection it
	// was loaded from when the item's own feeds are not collection members.
	ViewSources []ItemViewSource
	// CollectionID is the collection context this row was rendered in (0
	// elsewhere). It lets a row toggle preserve its view-source links.
	CollectionID int64
	// Kind is the item's view-time display classification (from the plugin's
	// decorator). KindText is the default.
	Kind ItemKind
	// Attribution is the item's resolved source line ("r/cats by u/sam") from
	// the plugin's decorator, or nil to keep the author/feed source.
	Attribution []AttributionPart
	// ThumbURL is a decorator-supplied row thumbnail override ("" keeps the
	// stored ImageURL).
	ThumbURL string
	// DedupeKey is the plugin decorator's view-time content identity, consumed by
	// httpapi.dedupItems to collapse the same content (a link post crossposted to
	// several places under different titles) into one row. Empty falls back to
	// the legacy title-based collapse.
	DedupeKey string
	Timezone  string // user's IANA timezone, for relative timestamps in templates
}

// ItemSource is one alternate feed an item is a member of, besides the owner
// feed whose title/author the row displays. Populated from item_feeds (and, for
// the legacy title-based view-time collapse, by httpapi's dedupItems).
type ItemSource struct {
	FeedID     int64
	FeedTitle  string
	AuthorID   int64  // the source feed's author, for the source line's internal link
	AuthorName string // "" when the source feed has no author
	Link       string
}

// ItemViewSource is one saved view that brings an item into a collection when
// none of the collection's member feeds do.
type ItemViewSource struct {
	ID    int64
	Title string
}

// TagGroup is one tag predicate from a filter row: its tags are combined with
// AND (All) or OR (!All), and Exclude hides items that satisfy the predicate.
type TagGroup struct {
	Tags    []string
	All     bool
	Exclude bool
}

// FeedGroup is one feed predicate from a filter row, shaped like TagGroup.
type FeedGroup struct {
	FeedIDs []int64
	All     bool
	Exclude bool
}

// TimeGroup is one time-window predicate from a filter row. After and Before
// are inclusive bounds in the canonical UTC TimeFormat ("" = open); Exclude
// negates the whole window, so a hide row removes items inside it.
type TimeGroup struct {
	After   string
	Before  string
	Exclude bool
}

// ViewFilter is one saved view as an item predicate. It carries the view's tag
// and feed groups and time windows; a view always matches all feeds.
type ViewFilter struct {
	// AuthorID is the author the view is assigned to. A view follows that
	// author's always-on rules, not the rules of the author who owns the feed
	// its items come from. 0 means the view is unassigned and unfiltered.
	AuthorID   int64
	TagGroups  []TagGroup
	FeedGroups []FeedGroup
	TimeGroups []TimeGroup
}

type ItemFilter struct {
	UnreadOnly    bool
	ReadOnly      bool
	FavoritesOnly bool
	BookmarksOnly bool
	FeedID        int64 // 0 = all
	AuthorID      int64 // 0 = all (matches any membership of the author's feeds)
	OwnerAuthorID int64 // 0 = all (matches items owned by the author's feeds)
	CollectionID  int64 // 0 = all
	BeforeID      int64 // keyset cursor (descending): only items ordered before this id
	AfterID       int64 // keyset cursor (ascending): only items ordered after this id
	Ascending     bool  // oldest first (default false = newest first)
	// Magic sorts by feed taste instead of time: raised feeds first, then
	// neutral, then lowered; within a tier, feeds with more favorites first,
	// then by item time. It uses Offset rather than the keyset cursors.
	Magic  bool
	Offset int // magic-sort page offset (ignored when Magic is false)
	// Tags filters the list to items carrying every listed category (AND). An
	// item matches when its tag set is a superset of Tags. Empty means no tag
	// filter. Matching is exact and case-sensitive (tags are stored verbatim).
	Tags []string
	// ExcludeTags hides items carrying any listed category (OR exclusion).
	ExcludeTags []string
	// ExcludeFeedID hides items seen through that feed. 0 means no exclusion.
	ExcludeFeedID int64
	// TagGroups/FeedGroups are per-row predicates with their own any/all and
	// include/exclude semantics. The flat Tags/ExcludeTags fields remain for
	// callers that only need the simple forms.
	TagGroups  []TagGroup
	FeedGroups []FeedGroup
	// TimeGroups are per-row time-window predicates over
	// COALESCE(published_at, fetched_at), in UTC TimeFormat strings.
	TimeGroups []TimeGroup
	// Views extend the scope with OR branches. With AuthorID set they are added
	// to the author's own feeds; without AuthorID they are the whole scope.
	Views []ViewFilter
	// CollectionViews extend a collection's scope with OR branches (the
	// collection's member feeds OR any matching view).
	CollectionViews []ViewFilter
	Limit           int
}

type ItemStore struct {
	q  *sqlcgen.Queries
	db *sql.DB // raw handle for hand-written queries sqlc cannot generate
	// policy is the host's per-URL site rules (from the plugin layer), used to
	// resolve an item's tokens to the user's subscribed feeds.
	policy URLPolicy
	// decorator supplies view-time item decoration (from the plugin layer).
	decorator ItemDecorator
}

// Upsert stores an item for feedID, deduplicating within the feed on
// (feed_id, dedup_key) — the item's stable Identity, or its GUID when none is
// set. An item whose SharedKey is set (a plugin's SharedKeyer supplied it)
// additionally carries a per-user cross_key, so the same post arriving through
// two subscriptions resolves to one row with two memberships rather than two
// rows: read/favorite/list/share state is then shared automatically.
//
// It reports whether the item is new to feedID (a new row or a new membership),
// which the poller uses to detect an exhausted history. When the item already
// exists its content snapshot (summary, categories, thumbnail, duration) is
// refreshed without touching identity, published_at or read state.
func (s *ItemStore) Upsert(feedID int64, it Item) (inserted bool, err error) {
	res, err := s.UpsertMany(feedID, []Item{it})
	if err != nil {
		return false, err
	}
	return res[0].Inserted, nil
}

// UpsertResult is one item's outcome from UpsertMany.
type UpsertResult struct {
	ItemID   int64
	Inserted bool
}

// UpsertMany stores a feed's freshly parsed items in one transaction and returns
// each item's stored id and whether it is new to the feed (a new row or a new
// membership). Batching turns a page's many small write transactions into one,
// which matters on a single-writer SQLite host, and the returned ids save the
// caller a lookup per item.
func (s *ItemStore) UpsertMany(feedID int64, items []Item) ([]UpsertResult, error) {
	out := make([]UpsertResult, len(items))
	if len(items) == 0 {
		return out, nil
	}
	ctx := context.Background()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin upsert: %w", err)
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)

	userID, err := q.FeedUserID(ctx, feedID)
	if err != nil {
		return nil, fmt.Errorf("upsert item feed owner: %w", err)
	}
	for i, it := range items {
		itemID, inserted, err := upsertTx(ctx, q, feedID, userID, it)
		if err != nil {
			return nil, err
		}
		out[i] = UpsertResult{ItemID: itemID, Inserted: inserted}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit upsert: %w", err)
	}
	return out, nil
}

// upsertTx is the per-item upsert, run inside a caller-provided transaction. It
// reports the stored row id and whether the item is new to feedID.
func upsertTx(ctx context.Context, q *sqlcgen.Queries, feedID, userID int64, it Item) (itemID int64, inserted bool, err error) {
	it.Summary = sanitize.HTML(it.Summary)
	key := dedupKey(it)
	crossKey := crossFeedKey(it.SharedKey)

	if crossKey != "" {
		itemID, err = q.GetItemByUserCrossKey(ctx, sqlcgen.GetItemByUserCrossKeyParams{
			UserID:   userID,
			CrossKey: crossKey,
		})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, false, fmt.Errorf("upsert item cross lookup: %w", err)
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
			return 0, false, fmt.Errorf("upsert item: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, false, fmt.Errorf("upsert item rows affected: %w", err)
		}
		newRow = n > 0
		// Resolve the id whether we inserted or collided on (feed_id, dedup_key).
		itemID, err = q.GetItemByDedupKey(ctx, sqlcgen.GetItemByDedupKeyParams{
			FeedID:   feedID,
			DedupKey: key,
		})
		if err != nil {
			return 0, false, fmt.Errorf("upsert item resolve id: %w", err)
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
			return 0, false, fmt.Errorf("set item cross key: %w", err)
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
			return 0, false, fmt.Errorf("refresh item snapshot: %w", err)
		}
	}

	// Normalize the item's tags into item_categories so the list can filter by
	// tag with an exact, indexed match. Replace rather than append, mirroring
	// the denormalized column refreshed above.
	if err := syncItemCategories(ctx, q, itemID, it.Categories); err != nil {
		return 0, false, fmt.Errorf("sync item categories: %w", err)
	}

	// Membership makes the item visible in this feed's streams. A new membership
	// (or a new row) counts as new for this feed.
	mres, err := q.AddItemFeed(ctx, sqlcgen.AddItemFeedParams{ItemID: itemID, FeedID: feedID})
	if err != nil {
		return 0, false, fmt.Errorf("add item feed: %w", err)
	}
	mn, err := mres.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("add item feed rows affected: %w", err)
	}
	return itemID, newRow || mn > 0, nil
}

func (s *ItemStore) List(userID int64, f ItemFilter) ([]ItemWithFeed, error) {
	items, _, err := s.ListPage(userID, f)
	return items, err
}

// ListPage returns up to f.Limit items plus whether more exist beyond them,
// so callers can render a "load more" button. Items are ordered newest first
// (oldest first when f.Ascending); pass the last returned id as f.BeforeID (desc)
// or f.AfterID (asc) to page further. One extra row is fetched to detect the
// next page. When f.Magic is set, items instead sort by feed taste (see
// ListItemsMagic) and page by f.Offset.
func (s *ItemStore) ListPage(userID int64, f ItemFilter) ([]ItemWithFeed, bool, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	// Tag exclusion and feed exclusion are variable-shape predicates the
	// generated queries (fixed placeholders) cannot express, so any active tag
	// filter or negative filter uses the hand-written dynamic query. An author
	// scope also uses it: the author's own rules must apply to items reached
	// through feed membership, which visible_items (keyed on the owner feed's
	// author) cannot express. sqlc owns the rest of the list paths.
	if f.AuthorID != 0 || len(f.Tags) > 0 || len(f.ExcludeTags) > 0 || f.ExcludeFeedID != 0 ||
		len(f.TagGroups) > 0 || len(f.FeedGroups) > 0 || len(f.TimeGroups) > 0 ||
		len(f.Views) > 0 || len(f.CollectionViews) > 0 {
		return s.listPageTagged(userID, f, limit)
	}
	if f.Magic {
		rows, err := s.q.ListItemsMagic(context.Background(), sqlcgen.ListItemsMagicParams{
			UserID:       userID,
			FeedID:       f.FeedID,
			AuthorID:     f.AuthorID,
			CollectionID: f.CollectionID,
			Unread:       boolInt(f.UnreadOnly),
			Read:         boolInt(f.ReadOnly),
			Favorites:    boolInt(f.FavoritesOnly),
			Bookmarks:    boolInt(f.BookmarksOnly),
			Limit:        int64(limit) + 1,
			Offset:       int64(f.Offset),
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
				r.ImageUrl, r.ImageCacheKey, r.DurationSec, r.PublishedAt, r.FetchedAt, r.Read, r.Favorite, r.Bookmark, r.ReadAt,
				r.FeedTitle, r.FeedUrl, r.FeedHomeUrl, r.FeedIsSystem, r.AuthorID, r.AuthorName))
		}
		if err := s.attachSources(userID, out); err != nil {
			return nil, false, err
		}
		return out, hasMore, nil
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
			Bookmarks:    boolInt(f.BookmarksOnly),
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
				r.ImageUrl, r.ImageCacheKey, r.DurationSec, r.PublishedAt, r.FetchedAt, r.Read, r.Favorite, r.Bookmark, r.ReadAt,
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
		Bookmarks:    boolInt(f.BookmarksOnly),
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
			r.ImageUrl, r.ImageCacheKey, r.DurationSec, r.PublishedAt, r.FetchedAt, r.Read, r.Favorite, r.Bookmark, r.ReadAt,
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

// ListReadHistory returns a page of the user's read items, most recently read
// first (read_at desc, id desc), for the history page. Offset-paged, since
// read_at is not unique.
func (s *ItemStore) ListReadHistory(userID int64, offset, limit int) ([]ItemWithFeed, bool, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.q.ListItemsReadHistory(context.Background(), sqlcgen.ListItemsReadHistoryParams{
		UserID: userID,
		Limit:  int64(limit) + 1,
		Offset: int64(offset),
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
			r.ImageUrl, r.ImageCacheKey, r.DurationSec, r.PublishedAt, r.FetchedAt, r.Read, r.Favorite, r.Bookmark, r.ReadAt,
			r.FeedTitle, r.FeedUrl, r.FeedHomeUrl, r.FeedIsSystem, r.AuthorID, r.AuthorName))
	}
	if err := s.attachSources(userID, out); err != nil {
		return nil, false, err
	}
	return out, hasMore, nil
}

// itemListColumns is the shared SELECT list for the item list queries (the
// generated ListItems* and the hand-written tagged variant), so both scan the
// same sqlcgen.ListItemsRow shape. from is "visible_items" for normal reads and
// "items" only for the author-filter preview, which must evaluate draft rules
// without the saved ones.
func itemListColumns(from string) string {
	return `SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.categories, i.image_url, i.image_cache_key, i.duration_sec,
       i.published_at, i.fetched_at, i.read, i.favorite, i.bookmark, i.read_at,
       f.title AS feed_title, f.feed_url AS feed_url, f.home_url AS feed_home_url,
       f.is_system AS feed_is_system,
       a.id AS author_id, a.name AS author_name
FROM ` + from + ` i
JOIN feeds f ON f.id = i.feed_id
LEFT JOIN authors a ON a.id = f.author_id`
}

// scanItemList reads rows selected with itemListColumns into ItemWithFeed values.
func scanItemList(rows *sql.Rows, limit int) ([]ItemWithFeed, bool, error) {
	defer rows.Close()
	out := make([]ItemWithFeed, 0, limit)
	for rows.Next() {
		var r sqlcgen.ListItemsRow
		if err := rows.Scan(&r.ID, &r.FeedID, &r.Guid, &r.Title, &r.Link, &r.Summary, &r.Categories,
			&r.ImageUrl, &r.ImageCacheKey, &r.DurationSec, &r.PublishedAt, &r.FetchedAt, &r.Read, &r.Favorite, &r.Bookmark, &r.ReadAt,
			&r.FeedTitle, &r.FeedUrl, &r.FeedHomeUrl, &r.FeedIsSystem, &r.AuthorID, &r.AuthorName); err != nil {
			return nil, false, err
		}
		out = append(out, toItemWithFeed(r.ID, r.FeedID, r.Guid, r.Title, r.Link, r.Summary, r.Categories,
			r.ImageUrl, r.ImageCacheKey, r.DurationSec, r.PublishedAt, r.FetchedAt, r.Read, r.Favorite, r.Bookmark, r.ReadAt,
			r.FeedTitle, r.FeedUrl, r.FeedHomeUrl, r.FeedIsSystem, r.AuthorID, r.AuthorName))
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

// listPageTagged is ListPage with a variable-shape tag/feed predicate. It builds
// the query dynamically because the group set is variable and the generated
// queries have a fixed placeholder shape. It mirrors ListItems' scope,
// read/favorite/bookmark, saved-page and cursor semantics, and supports all
// three sorts. limit is the caller's already-clamped page size.
func (s *ItemStore) listPageTagged(userID int64, f ItemFilter, limit int) ([]ItemWithFeed, bool, error) {
	return s.listPageTaggedFrom(userID, f, limit, f.itemSource())
}

// itemSource is the table a scoped read starts from. An author scope and a
// saved-view scope read raw items: an author page applies the scoped author's
// rules to items reached through the author's feeds (a cross-feed item can be
// owned by another author), and a view follows its assigned author. Every other
// read uses the author-filtered visible_items view.
func (f ItemFilter) itemSource() string {
	if f.AuthorID != 0 || len(f.Views) > 0 || len(f.CollectionViews) > 0 {
		return "items"
	}
	return "visible_items"
}

func (s *ItemStore) listPageTaggedFrom(userID int64, f ItemFilter, limit int, from string) ([]ItemWithFeed, bool, error) {
	var b strings.Builder
	b.WriteString(itemListColumns(from))
	var args []any
	if f.Magic {
		b.WriteString(` LEFT JOIN (
			SELECT feed_id, COUNT(*) AS fav_count FROM items
			WHERE user_id = ? AND favorite = 1 GROUP BY feed_id
		) fc ON fc.feed_id = i.feed_id`)
		args = append(args, userID)
	}
	b.WriteString(" WHERE i.user_id = ?")
	args = append(args, userID)
	appendItemFilterPredicates(&b, &args, f, from == "items")
	switch {
	case f.Magic:
		b.WriteString(" ORDER BY CASE WHEN f.rank > 0 THEN 0 WHEN f.rank < 0 THEN 2 ELSE 1 END ASC, COALESCE(fc.fav_count, 0) DESC, COALESCE(i.published_at, i.fetched_at) DESC, i.id DESC LIMIT ? OFFSET ?")
		args = append(args, limit+1, f.Offset)
	case f.Ascending:
		if f.AfterID != 0 {
			b.WriteString(" AND (COALESCE(i.published_at, i.fetched_at), i.id) > (SELECT COALESCE(published_at, fetched_at), id FROM items WHERE id = ?)")
			args = append(args, f.AfterID)
		}
		b.WriteString(" ORDER BY COALESCE(i.published_at, i.fetched_at) ASC, i.id ASC LIMIT ?")
		args = append(args, limit+1)
	default:
		if f.BeforeID != 0 {
			b.WriteString(" AND (COALESCE(i.published_at, i.fetched_at), i.id) < (SELECT COALESCE(published_at, fetched_at), id FROM items WHERE id = ?)")
			args = append(args, f.BeforeID)
		}
		b.WriteString(" ORDER BY COALESCE(i.published_at, i.fetched_at) DESC, i.id DESC LIMIT ?")
		args = append(args, limit+1)
	}

	rows, err := s.db.QueryContext(context.Background(), b.String(), args...)
	if err != nil {
		return nil, false, err
	}
	out, hasMore, err := scanItemList(rows, limit)
	if err != nil {
		return nil, false, err
	}
	if err := s.attachSources(userID, out); err != nil {
		return nil, false, err
	}
	return out, hasMore, nil
}

// appendItemFilterPredicates adds the scope, view state, saved-page and
// tag/feed predicates shared by the dynamic item list and its count. The query
// must join feeds as f and items as i. rawBase reports that the caller selected
// from raw items rather than visible_items, so the author/collection feed
// branches have to carry their own visibility check.
func appendItemFilterPredicates(b *strings.Builder, args *[]any, f ItemFilter, rawBase bool) {
	if f.FeedID != 0 {
		b.WriteString(" AND EXISTS (SELECT 1 FROM item_feeds mf WHERE mf.item_id = i.id AND mf.feed_id = ?)")
		*args = append(*args, f.FeedID)
	}
	if f.AuthorID != 0 || len(f.Views) > 0 {
		clause, scopeArgs := authorScopeClause("i.id", "COALESCE(i.published_at, i.fetched_at)", f.AuthorID, f.Views)
		if clause != "" {
			b.WriteString(" AND ")
			b.WriteString(clause)
			*args = append(*args, scopeArgs...)
		}
	}
	if f.OwnerAuthorID != 0 {
		b.WriteString(" AND i.feed_id IN (SELECT oa.id FROM feeds oa WHERE oa.author_id = ?)")
		*args = append(*args, f.OwnerAuthorID)
	}
	if f.CollectionID != 0 || len(f.CollectionViews) > 0 {
		clause, scopeArgs := collectionScopeClause("i.id", "COALESCE(i.published_at, i.fetched_at)", f.CollectionID, f.CollectionViews, rawBase)
		if clause != "" {
			b.WriteString(" AND ")
			b.WriteString(clause)
			*args = append(*args, scopeArgs...)
		}
	}
	if f.UnreadOnly {
		b.WriteString(" AND i.read = 0")
	}
	if f.ReadOnly {
		b.WriteString(" AND i.read = 1")
	}
	if f.FavoritesOnly {
		b.WriteString(" AND i.favorite = 1")
	}
	if f.BookmarksOnly {
		b.WriteString(" AND i.bookmark = 1")
	}
	// Saved pages (system feed items) surface only in favorites and bookmarks;
	// keep them out of the unread/read/feed/author/collection streams.
	if !f.FavoritesOnly && !f.BookmarksOnly {
		b.WriteString(" AND f.is_system = 0")
	}
	for _, tag := range f.Tags {
		b.WriteString(" AND EXISTS (SELECT 1 FROM item_categories ic WHERE ic.item_id = i.id AND ic.category = ?)")
		*args = append(*args, tag)
	}
	for _, tag := range f.ExcludeTags {
		b.WriteString(" AND NOT EXISTS (SELECT 1 FROM item_categories ic WHERE ic.item_id = i.id AND ic.category = ?)")
		*args = append(*args, tag)
	}
	if f.ExcludeFeedID != 0 {
		b.WriteString(" AND NOT EXISTS (SELECT 1 FROM item_feeds mfx WHERE mfx.item_id = i.id AND mfx.feed_id = ?)")
		*args = append(*args, f.ExcludeFeedID)
	}
	appendFilterGroups(b, args, "i.id", f.TagGroups, f.FeedGroups)
	appendTimeGroups(b, args, "COALESCE(i.published_at, i.fetched_at)", f.TimeGroups)
}

// authorScopeClause builds the OR of an author's owned feeds and any saved views
// assigned to that author. itemID is the item-id expression of the caller's
// query; timeExpr is its item-time expression. The owned-feed branch applies the
// scoped author's rules to items reached through feed membership, not the rules
// of the author who owns the item's feed: a cross-feed post can be owned by
// another author while still being part of this author's feed. A view branch
// applies the rules of the author the view is assigned to (see viewFilterClause).
func authorScopeClause(itemID, timeExpr string, authorID int64, views []ViewFilter) (string, []any) {
	var branches []string
	var args []any
	if authorID != 0 {
		branch := "EXISTS (SELECT 1 FROM item_feeds mf JOIN feeds sf ON sf.id = mf.feed_id WHERE mf.item_id = " + itemID + " AND sf.author_id = ? AND (" + authorRulesVisibleSQL(itemID, "sf.author_id") + "))"
		branches = append(branches, branch)
		args = append(args, authorID)
	}
	for _, v := range views {
		clause, viewArgs := viewFilterClause(itemID, timeExpr, v)
		if clause == "" {
			clause = "1=1"
		}
		branches = append(branches, "("+clause+")")
		args = append(args, viewArgs...)
	}
	if len(branches) == 0 {
		return "", nil
	}
	return "(" + strings.Join(branches, " OR ") + ")", args
}

// collectionScopeClause builds the OR of a collection's member feeds and its
// member views. itemID is the item-id expression of the caller's query; timeExpr
// is its item-time expression. When rawBase is true the caller reads raw items,
// so the member-feed branch carries its own visible_items check.
func collectionScopeClause(itemID, timeExpr string, collectionID int64, views []ViewFilter, rawBase bool) (string, []any) {
	var branches []string
	var args []any
	if collectionID != 0 {
		branch := "EXISTS (SELECT 1 FROM item_feeds mf WHERE mf.item_id = " + itemID + " AND mf.feed_id IN (SELECT feed_id FROM collection_feeds WHERE collection_id = ?))"
		if rawBase {
			branch = "(" + branch + " AND " + visibleItemExists(itemID) + ")"
		}
		branches = append(branches, branch)
		args = append(args, collectionID)
	}
	for _, v := range views {
		clause, viewArgs := viewFilterClause(itemID, timeExpr, v)
		if clause == "" {
			clause = "1=1"
		}
		branches = append(branches, "("+clause+")")
		args = append(args, viewArgs...)
	}
	if len(branches) == 0 {
		return "", nil
	}
	return "(" + strings.Join(branches, " OR ") + ")", args
}

// viewFilterClause builds one saved view's predicate over an item alias. The
// view always matches all feeds; its tag/feed groups and time windows are the
// only content restrictions. System-feed (saved page) items never match a view.
// When the view is assigned to an author, that author's always-on rules also
// apply: a view is filtered by its assigned author, not by the author who owns
// the feed its items come from.
func viewFilterClause(itemID, timeExpr string, v ViewFilter) (string, []any) {
	parts := []string{
		itemID + " NOT IN (SELECT si.id FROM items si JOIN feeds sisf ON sisf.id = si.feed_id WHERE sisf.is_system = 1)",
	}
	var args []any
	if clause, groupArgs := filterGroupsClause(itemID, v.TagGroups, v.FeedGroups); clause != "" {
		parts = append(parts, "("+clause+")")
		args = append(args, groupArgs...)
	}
	if clause, timeArgs := timeGroupsClause(timeExpr, v.TimeGroups); clause != "" {
		parts = append(parts, "("+clause+")")
		args = append(args, timeArgs...)
	}
	if v.AuthorID != 0 {
		parts = append(parts, "("+authorRulesVisibleSQL(itemID, "?")+")")
		// authorRulesVisibleSQL binds the author twice (hide and show rules).
		args = append(args, v.AuthorID, v.AuthorID)
	}
	return strings.Join(parts, " AND "), args
}

// visibleItemExists reports whether itemID is visible under the rules of the
// author who owns its feed. It is the guard the owned-feed branches need when
// the query reads raw items so a view branch can bypass those rules.
func visibleItemExists(itemID string) string {
	return "EXISTS (SELECT 1 FROM visible_items vi WHERE vi.id = " + itemID + ")"
}

// authorRulesVisibleSQL is the per-author form of the visible_items predicate:
// the item is visible under the rules of the author identified by authorExpr. An
// author page passes the membership feed's author (`sf.author_id`); a saved view
// passes `?` and binds its assigned author twice (the hide subquery and the show
// subquery). visible_items applies the same expression to the item's owner
// author. Keep the three in sync (TestAuthorRulesVisibleSQLMatchesView guards
// the view/duplication).
func authorRulesVisibleSQL(itemID, authorExpr string) string {
	match := authorRuleMatchSQL(itemID)
	return `NOT EXISTS (
		SELECT 1 FROM author_filter_rules r
		WHERE r.author_id = ` + authorExpr + ` AND r.action = 'hide' AND (` + match + `)
	)
	AND NOT EXISTS (
		SELECT 1 FROM author_filter_rules r
		WHERE r.author_id = ` + authorExpr + ` AND r.action = 'show' AND NOT (` + match + `)
	)`
}

// authorRuleMatchSQL is one rule's match expression over an item alias, the same
// shape visible_items uses. r is the author_filter_rules row.
func authorRuleMatchSQL(itemID string) string {
	return `CASE r.kind
		WHEN 'tag' THEN CASE r.combine
			WHEN 'all' THEN
				(SELECT COUNT(DISTINCT ic.category) FROM item_categories ic
				   JOIN author_filter_values v ON v.rule_id = r.id AND v.value = ic.category
				  WHERE ic.item_id = ` + itemID + `)
				= (SELECT COUNT(*) FROM author_filter_values v2 WHERE v2.rule_id = r.id)
			ELSE EXISTS (SELECT 1 FROM item_categories ic
				   JOIN author_filter_values v ON v.rule_id = r.id AND v.value = ic.category
				  WHERE ic.item_id = ` + itemID + `)
		END
		WHEN 'feed' THEN CASE r.combine
			WHEN 'all' THEN
				(SELECT COUNT(DISTINCT mf.feed_id) FROM item_feeds mf
				   JOIN feeds mf2 ON mf2.id = mf.feed_id AND mf2.author_id = r.author_id
				   JOIN author_filter_values v ON v.rule_id = r.id AND v.value = CAST(mf.feed_id AS TEXT)
				  WHERE mf.item_id = ` + itemID + `)
				= (SELECT COUNT(*) FROM author_filter_values v2 WHERE v2.rule_id = r.id)
			ELSE EXISTS (SELECT 1 FROM item_feeds mf
				   JOIN feeds mf2 ON mf2.id = mf.feed_id AND mf2.author_id = r.author_id
				   JOIN author_filter_values v ON v.rule_id = r.id AND v.value = CAST(mf.feed_id AS TEXT)
				  WHERE mf.item_id = ` + itemID + `)
		END
		ELSE 0
	END`
}

// CountFiltered counts the items ListPage would return for f, ignoring its
// cursor and limit. It backs the "N matching" badge on a filtered item list.
func (s *ItemStore) CountFiltered(userID int64, f ItemFilter) (int, error) {
	return s.countFilteredFrom(userID, f, f.itemSource())
}

func (s *ItemStore) countFilteredFrom(userID int64, f ItemFilter, from string) (int, error) {
	var b strings.Builder
	b.WriteString(`SELECT COUNT(*) FROM ` + from + ` i JOIN feeds f ON f.id = i.feed_id WHERE i.user_id = ?`)
	args := []any{userID}
	appendItemFilterPredicates(&b, &args, f, from == "items")
	var n int
	if err := s.db.QueryRowContext(context.Background(), b.String(), args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// MatchingViews reports which of the supplied view predicates each item
// matches. Values are positions into views. It reuses the same clause the
// collection scope uses, so view-source attribution cannot drift from the list
// the item appears in.
func (s *ItemStore) MatchingViews(userID int64, itemIDs []int64, views []ViewFilter) (map[int64][]int, error) {
	out := make(map[int64][]int)
	if userID == 0 || len(itemIDs) == 0 || len(views) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(itemIDs)), ",")
	var b strings.Builder
	args := make([]any, 0, len(views)*(len(itemIDs)+4))
	for i, v := range views {
		if i > 0 {
			b.WriteString(" UNION ALL ")
		}
		clause, viewArgs := viewFilterClause("i.id", "COALESCE(i.published_at, i.fetched_at)", v)
		b.WriteString("SELECT ? AS view_index, i.id FROM items i WHERE i.user_id = ? AND i.id IN (")
		b.WriteString(placeholders)
		b.WriteString(") AND (")
		b.WriteString(clause)
		b.WriteString(")")
		args = append(args, i, userID)
		for _, id := range itemIDs {
			args = append(args, id)
		}
		args = append(args, viewArgs...)
	}
	rows, err := s.db.QueryContext(context.Background(), b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var index int
		var id int64
		if err := rows.Scan(&index, &id); err != nil {
			return nil, err
		}
		out[id] = append(out[id], index)
	}
	return out, rows.Err()
}

// AuthorFilterPreview is the draft-rule preview on the author settings page.
// Total and the allowed/blocked counts cover every item owned by the author;
// Allowed/Blocked are the newest samples.
type AuthorFilterPreview struct {
	Total        int
	AllowedCount int
	BlockedCount int
	Allowed      []ItemWithFeed
	Blocked      []ItemWithFeed
}

// PreviewAuthorFilters evaluates draft author rules against the author's owned
// items. It reads raw items, not visible_items, so saved rules are not
// double-applied. The blocked sample is the complement of the same group
// predicate the normal list uses.
func (s *ItemStore) PreviewAuthorFilters(userID, authorID int64, tagGroups []TagGroup, feedGroups []FeedGroup, limit int) (AuthorFilterPreview, error) {
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	total, err := s.countFilteredFrom(userID, ItemFilter{OwnerAuthorID: authorID}, "items")
	if err != nil {
		return AuthorFilterPreview{}, err
	}
	f := ItemFilter{
		OwnerAuthorID: authorID,
		TagGroups:     tagGroups,
		FeedGroups:    feedGroups,
	}
	allowedCount, err := s.countFilteredFrom(userID, f, "items")
	if err != nil {
		return AuthorFilterPreview{}, err
	}
	allowed, _, err := s.listPageTaggedFrom(userID, f, limit, "items")
	if err != nil {
		return AuthorFilterPreview{}, err
	}
	p := AuthorFilterPreview{
		Total:        total,
		AllowedCount: allowedCount,
		BlockedCount: total - allowedCount,
		Allowed:      allowed,
	}
	if p.BlockedCount <= 0 {
		return p, nil
	}
	clause, groupArgs := filterGroupsClause("i.id", tagGroups, feedGroups)
	if clause == "" {
		return p, nil
	}
	var b strings.Builder
	b.WriteString(itemListColumns("items"))
	b.WriteString(" WHERE i.user_id = ? AND i.feed_id IN (SELECT oa.id FROM feeds oa WHERE oa.author_id = ?)")
	b.WriteString(" AND NOT (")
	b.WriteString(clause)
	b.WriteString(") ORDER BY COALESCE(i.published_at, i.fetched_at) DESC, i.id DESC LIMIT ?")
	args := []any{userID, authorID}
	args = append(args, groupArgs...)
	args = append(args, limit)
	rows, err := s.db.QueryContext(context.Background(), b.String(), args...)
	if err != nil {
		return AuthorFilterPreview{}, err
	}
	blocked, _, err := scanItemList(rows, limit)
	if err != nil {
		return AuthorFilterPreview{}, err
	}
	if err := s.attachSources(userID, blocked); err != nil {
		return AuthorFilterPreview{}, err
	}
	p.Blocked = blocked
	return p, nil
}

// Enclosure is one media attachment (podcast, video) on an item.
type Enclosure struct {
	URL      string
	Title    string
	MIMEType string
	Size     int64
	Sort     int
	// Kind is the plugin-declared render kind (feedparse.EnclosureKind*); empty
	// means the host infers it from the MIME type/extension.
	Kind string
	// Poster is a video/audio enclosure's thumbnail; empty falls back to the
	// item's ImageURL.
	Poster string
	// CacheKey is the object-storage key of the enclosure's cached bytes when it
	// is an image and host-side image caching is enabled; empty otherwise.
	CacheKey string
	// CacheSize is the byte size of the cached enclosure (0 when not cached).
	CacheSize int64
}

// Enclosures returns an item's media attachments in order.
func (s *ItemStore) Enclosures(itemID int64) ([]Enclosure, error) {
	rows, err := s.q.ListEnclosures(context.Background(), itemID)
	if err != nil {
		return nil, err
	}
	out := make([]Enclosure, 0, len(rows))
	for _, r := range rows {
		out = append(out, Enclosure{
			URL: r.Url, Title: r.Title, MIMEType: r.MimeType.String, Size: r.Size, Sort: int(r.Sort),
			Kind: r.Kind, Poster: r.Poster, CacheKey: r.CacheKey, CacheSize: r.CacheSize,
		})
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
	Storage   int64  // bytes of cached media across the author's feeds
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
	storage, _ := s.StorageByAuthor(userID, authorID)
	return AuthorItemStats{
		Total:     int(r.TotalPosts),
		Unread:    int(r.UnreadPosts),
		Read:      int(r.ReadPosts),
		Favorites: int(r.FavoritePosts),
		Recent:    int(r.RecentPosts),
		FirstAt:   r.FirstPostAt,
		LastAt:    r.LastPostAt,
		Storage:   storage,
	}, nil
}

// StatsFilter returns author-style aggregate stats for an arbitrary item scope
// (used by author pages whose views extend the scope beyond owned feeds). The
// storage figure remains the author's own feed media, since view items belong to
// other authors' feeds.
func (s *ItemStore) StatsFilter(userID int64, f ItemFilter) (AuthorItemStats, error) {
	source := f.itemSource()
	var b strings.Builder
	b.WriteString(`SELECT COUNT(i.id),
		COUNT(CASE WHEN i.read = 0 THEN 1 END),
		COUNT(CASE WHEN i.read = 1 THEN 1 END),
		COUNT(CASE WHEN i.favorite = 1 THEN 1 END),
		COUNT(CASE WHEN COALESCE(i.published_at, i.fetched_at) >= ? THEN 1 END),
		CAST(COALESCE(MIN(COALESCE(i.published_at, i.fetched_at)), '') AS TEXT),
		CAST(COALESCE(MAX(COALESCE(i.published_at, i.fetched_at)), '') AS TEXT)
		FROM ` + source + ` i JOIN feeds f ON f.id = i.feed_id WHERE i.user_id = ?`)
	args := []any{db.FormatTime(time.Now().AddDate(0, 0, -30)), userID}
	appendItemFilterPredicates(&b, &args, f, source == "items")
	var (
		total, unread, read, favorites, recent int
		firstAt, lastAt                        string
	)
	if err := s.db.QueryRowContext(context.Background(), b.String(), args...).Scan(
		&total, &unread, &read, &favorites, &recent, &firstAt, &lastAt,
	); err != nil {
		return AuthorItemStats{}, err
	}
	return AuthorItemStats{
		Total: total, Unread: unread, Read: read, Favorites: favorites, Recent: recent,
		FirstAt: firstAt, LastAt: lastAt,
	}, nil
}

// RecentTimesFilter returns the newest item times in an arbitrary scope, newest
// first. It backs the posting-frequency estimate when an author's scope includes
// saved views.
func (s *ItemStore) RecentTimesFilter(userID int64, f ItemFilter, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 30
	}
	source := f.itemSource()
	var b strings.Builder
	b.WriteString(`SELECT COALESCE(i.published_at, i.fetched_at)
		FROM ` + source + ` i JOIN feeds f ON f.id = i.feed_id WHERE i.user_id = ?`)
	args := []any{userID}
	appendItemFilterPredicates(&b, &args, f, source == "items")
	b.WriteString(" ORDER BY COALESCE(i.published_at, i.fetched_at) DESC, i.id DESC LIMIT ?")
	args = append(args, limit)
	rows, err := s.db.QueryContext(context.Background(), b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListCategoriesFilter returns the distinct tags and item counts for an
// arbitrary item scope, most-used first. It backs the filter modal when an
// author's items include view matches from other feeds.
func (s *ItemStore) ListCategoriesFilter(userID int64, f ItemFilter) ([]CategoryCount, error) {
	source := f.itemSource()
	var b strings.Builder
	b.WriteString(`SELECT ic.category, COUNT(DISTINCT ic.item_id)
		FROM item_categories ic WHERE ic.item_id IN (
			SELECT i.id FROM ` + source + ` i JOIN feeds f ON f.id = i.feed_id WHERE i.user_id = ?`)
	args := []any{userID}
	appendItemFilterPredicates(&b, &args, f, source == "items")
	b.WriteString(`) GROUP BY ic.category ORDER BY COUNT(DISTINCT ic.item_id) DESC, ic.category`)
	rows, err := s.db.QueryContext(context.Background(), b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CategoryCount
	for rows.Next() {
		var c CategoryCount
		if err := rows.Scan(&c.Category, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// FeedItemStats aggregates one feed's posts (by item membership).
type FeedItemStats struct {
	Total   int    // all-time posts in the feed
	Unread  int    // unread posts
	Read    int    // read posts
	FirstAt string // oldest post time, "" when the feed has no posts
	LastAt  string // newest post time, "" when the feed has no posts
}

// StatsFeed returns all-time aggregate stats for one feed: total/read/unread
// counts and the first and last post times, for the settings history card.
func (s *ItemStore) StatsFeed(userID, feedID int64) (FeedItemStats, error) {
	r, err := s.q.GetFeedItemStats(context.Background(), sqlcgen.GetFeedItemStatsParams{
		UserID: userID,
		FeedID: feedID,
	})
	if err != nil {
		return FeedItemStats{}, err
	}
	return FeedItemStats{
		Total:   int(r.TotalPosts),
		Unread:  int(r.UnreadPosts),
		Read:    int(r.ReadPosts),
		FirstAt: r.FirstPostAt,
		LastAt:  r.LastPostAt,
	}, nil
}

// ReplaceEnclosures deletes and re-inserts an item's enclosures. A previous
// slot's cache_key is carried forward when the incoming enclosure has none, so
// a re-poll does not drop an already-cached image (which is what keeps a
// cached copy showing after the user turns caching off).
func (s *ItemStore) ReplaceEnclosures(itemID int64, encs []Enclosure) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)
	prev := map[int]Enclosure{}
	if old, err := q.ListEnclosures(ctx, itemID); err == nil {
		for _, r := range old {
			prev[int(r.Sort)] = Enclosure{
				URL: r.Url, Title: r.Title, MIMEType: r.MimeType.String, Size: r.Size, Sort: int(r.Sort),
				Kind: r.Kind, Poster: r.Poster, CacheKey: r.CacheKey, CacheSize: r.CacheSize,
			}
		}
	}
	if err := q.DeleteEnclosures(ctx, itemID); err != nil {
		return err
	}
	for i, e := range encs {
		cacheKey := e.CacheKey
		cacheSize := e.CacheSize
		if cacheKey == "" {
			cacheKey = prev[i].CacheKey
			cacheSize = prev[i].CacheSize
		}
		if err := q.InsertEnclosure(ctx, sqlcgen.InsertEnclosureParams{
			ItemID:    itemID,
			Url:       e.URL,
			Title:     e.Title,
			MimeType:  ns(e.MIMEType),
			Size:      e.Size,
			Sort:      int64(i),
			Kind:      e.Kind,
			Poster:    e.Poster,
			CacheKey:  cacheKey,
			CacheSize: cacheSize,
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetItemImageCacheKey records (or clears, with "", size 0) the object-storage
// key and byte size of an item's cached primary image. It is deliberately not
// part of the poll snapshot refresh, so a cached copy is never clobbered by a
// fresh remote URL.
func (s *ItemStore) SetItemImageCacheKey(itemID int64, key string, size int64) error {
	return s.q.SetItemImageCacheKey(context.Background(), sqlcgen.SetItemImageCacheKeyParams{
		Key:  ns(key),
		Size: size,
		ID:   itemID,
	})
}

// DeleteSaved hard-deletes a saved page: an item under the user's hidden
// system feed. Regular feed items are never touched (the query's system-feed
// guard). Child rows (enclosures, list memberships, shares) cascade. Returns
// ErrNotFound when the id is not one of the user's saved pages.
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

// AllCacheKeys returns every object-storage key referenced by cached item
// media. The storage audit diffs it against the filestore to find orphans.
func (s *ItemStore) AllCacheKeys() ([]string, error) {
	rows, err := s.q.AllCacheKeys(context.Background())
	if err != nil {
		return nil, err
	}
	return nonEmptyStrings(rows), nil
}

// ListUserCacheKeys returns the cached-media keys owned by a user, for purging
// when the account is deleted.
func (s *ItemStore) ListUserCacheKeys(userID int64) ([]string, error) {
	rows, err := s.q.ListUserCacheKeys(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	return nonEmptyStrings(rows), nil
}

// ListFeedCacheKeys returns the cached-media keys of the items a feed deletion
// removes (items owned solely by the feed; re-homed cross-feed items keep
// theirs), for purging those blobs.
func (s *ItemStore) ListFeedCacheKeys(feedID int64) ([]string, error) {
	rows, err := s.q.ListFeedCacheKeys(context.Background(), feedID)
	if err != nil {
		return nil, err
	}
	return nonEmptyStrings(rows), nil
}

// SetItemImageCacheSizeByKey records the byte size of a cached primary image by
// its storage key. Used by the storage backfill.
func (s *ItemStore) SetItemImageCacheSizeByKey(key string, size int64) error {
	return s.q.SetItemImageCacheSizeByKey(context.Background(), sqlcgen.SetItemImageCacheSizeByKeyParams{
		ImageCacheSize: size,
		ImageCacheKey:  ns(key),
	})
}

// SetEnclosureCacheSizeByKey records the byte size of a cached enclosure by its
// storage key. Used by the storage backfill.
func (s *ItemStore) SetEnclosureCacheSizeByKey(key string, size int64) error {
	return s.q.SetEnclosureCacheSizeByKey(context.Background(), sqlcgen.SetEnclosureCacheSizeByKeyParams{
		CacheSize: size,
		CacheKey:  key,
	})
}

// StorageByFeed returns the cached-media bytes a feed lists (by membership).
func (s *ItemStore) StorageByFeed(feedID int64) (int64, error) {
	return s.q.StorageByFeed(context.Background(), feedID)
}

// StorageByAuthor returns the cached-media bytes across all of an author's feeds.
func (s *ItemStore) StorageByAuthor(userID, authorID int64) (int64, error) {
	return s.q.StorageByAuthor(context.Background(), sqlcgen.StorageByAuthorParams{
		UserID:   userID,
		AuthorID: authorID,
	})
}

// FeedStorage is one feed's cached-media footprint.
type FeedStorage struct {
	FeedID int64
	Title  string
	Bytes  int64
}

// ListFeedStorage returns a user's feeds that hold cached media, largest first.
func (s *ItemStore) ListFeedStorage(userID int64, limit int) ([]FeedStorage, error) {
	rows, err := s.q.ListFeedStorage(context.Background(), sqlcgen.ListFeedStorageParams{
		UserID: userID,
		Limit:  int64(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]FeedStorage, 0, len(rows))
	for _, r := range rows {
		out = append(out, FeedStorage{FeedID: r.FeedID, Title: r.Title, Bytes: r.Bytes})
	}
	return out, nil
}

// nonEmptyStrings drops NULL/empty entries from a nullable-string column.
func nonEmptyStrings(rows []sql.NullString) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Valid && r.String != "" {
			out = append(out, r.String)
		}
	}
	return out
}

// CategoryCount is one tag and how many items in a scope carry it.
type CategoryCount struct {
	Category string
	Count    int
}

// syncItemCategories replaces itemID's tag rows with cats (normalized through
// the same newline codec as items.categories), inside the caller's transaction.
func syncItemCategories(ctx context.Context, q *sqlcgen.Queries, itemID int64, cats []string) error {
	if err := q.DeleteItemCategories(ctx, itemID); err != nil {
		return err
	}
	for _, c := range splitCategories(joinCategories(cats)) {
		if err := q.AddItemCategory(ctx, sqlcgen.AddItemCategoryParams{ItemID: itemID, Category: c}); err != nil {
			return err
		}
	}
	return nil
}

// ListCategories returns the distinct tags in a scope with their item counts,
// most-used first. It dispatches on the same FeedID/AuthorID/CollectionID
// fields as ListPage; all zero returns the user's tags across every feed.
func (s *ItemStore) ListCategories(userID int64, f ItemFilter) ([]CategoryCount, error) {
	ctx := context.Background()
	switch {
	case f.FeedID != 0:
		rows, err := s.q.ListCategoriesForFeed(ctx, sqlcgen.ListCategoriesForFeedParams{FeedID: f.FeedID, UserID: userID})
		if err != nil {
			return nil, err
		}
		return categoryCounts(len(rows), func(i int) (string, int64) { return rows[i].Category, rows[i].ItemCount }), nil
	case f.AuthorID != 0:
		rows, err := s.q.ListCategoriesForAuthor(ctx, sqlcgen.ListCategoriesForAuthorParams{AuthorID: f.AuthorID, UserID: userID})
		if err != nil {
			return nil, err
		}
		return categoryCounts(len(rows), func(i int) (string, int64) { return rows[i].Category, rows[i].ItemCount }), nil
	case f.CollectionID != 0:
		rows, err := s.q.ListCategoriesForCollection(ctx, sqlcgen.ListCategoriesForCollectionParams{CollectionID: f.CollectionID, UserID: userID})
		if err != nil {
			return nil, err
		}
		return categoryCounts(len(rows), func(i int) (string, int64) { return rows[i].Category, rows[i].ItemCount }), nil
	default:
		// No scope means every feed, the shape a saved view with all-feeds scope
		// uses. ListUserCategories is the same read as the global tag picker.
		return s.ListUserCategories(userID, false)
	}
}

// ListUserCategories returns the user's distinct tags with their item counts
// across every feed, most-used first. unreadOnly restricts the counts to unread
// items, which is what the global /unread filter offers. It reads visible_items
// so an author block rule also hides that author's tags from the picker.
func (s *ItemStore) ListUserCategories(userID int64, unreadOnly bool) ([]CategoryCount, error) {
	rows, err := s.q.ListCategoriesForUser(context.Background(), sqlcgen.ListCategoriesForUserParams{
		UserID: userID,
		Unread: boolInt(unreadOnly),
	})
	if err != nil {
		return nil, err
	}
	return categoryCounts(len(rows), func(i int) (string, int64) { return rows[i].Category, rows[i].ItemCount }), nil
}

// categoryCounts maps sqlc rows to CategoryCount via the caller's accessors.
func categoryCounts(n int, at func(i int) (string, int64)) []CategoryCount {
	out := make([]CategoryCount, 0, n)
	for i := 0; i < n; i++ {
		c, count := at(i)
		out = append(out, CategoryCount{Category: c, Count: int(count)})
	}
	return out
}

// BackfillItemCategories populates item_categories for items stored before
// schemaV48, deriving the rows from the denormalized items.categories column
// (SQLite cannot split it). Idempotent: items that already have tag rows are
// skipped, so it is a cheap no-op after the first run. Returns how many items
// were populated.
func (s *ItemStore) BackfillItemCategories() (int, error) {
	rows, err := s.q.ListItemsWithCategories(context.Background())
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin category backfill: %w", err)
	}
	defer tx.Rollback()
	ctx := context.Background()
	q := s.q.WithTx(tx)
	n := 0
	for _, r := range rows {
		cats := splitCategories(r.Categories)
		if len(cats) == 0 {
			continue
		}
		if err := syncItemCategories(ctx, q, r.ID, cats); err != nil {
			return n, fmt.Errorf("backfill item %d categories: %w", r.ID, err)
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return n, err
	}
	return n, nil
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
		it.ImageUrl, it.ImageCacheKey, it.DurationSec, it.PublishedAt, it.FetchedAt, it.Read, it.Favorite, it.Bookmark, it.ReadAt,
		it.FeedTitle, it.FeedUrl, it.FeedHomeUrl, it.FeedIsSystem, it.AuthorID, it.AuthorName)}
	slice[0].Content = it.Content
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
	itw := toItemWithFeed(it.ID, it.FeedID, it.Guid, it.Title, it.Link, it.Summary, it.Categories,
		it.ImageUrl, it.ImageCacheKey, it.DurationSec, it.PublishedAt, it.FetchedAt, it.Read, it.Favorite, it.Bookmark, it.ReadAt,
		it.FeedTitle, it.FeedUrl, it.FeedHomeUrl, it.FeedIsSystem, it.AuthorID, it.AuthorName)
	itw.Content = it.Content
	return itw, nil
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
		items[i].DedupeKey = d.DedupeKey
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

// SetContent stores a plugin-enriched body on an item, scoped to its owner. It
// is written once (the poll snapshot refresh never touches items.content), so an
// enriched body survives re-polling.
func (s *ItemStore) SetContent(userID, itemID int64, content string) error {
	return s.q.SetItemContent(context.Background(), sqlcgen.SetItemContentParams{
		Content: sanitize.HTML(content),
		ID:      itemID,
		UserID:  userID,
	})
}

// Content returns an item's enriched body, or "" when it has not been enriched.
// Used to skip re-enriching an item the plugin already resolved.
func (s *ItemStore) Content(itemID int64) (string, error) {
	return s.q.GetItemContent(context.Background(), itemID)
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

// SetBookmark marks an item as bookmarked (save-for-later) or not, verifying it
// belongs to the user.
func (s *ItemStore) SetBookmark(userID, itemID int64, bookmark bool) error {
	res, err := s.q.SetItemBookmark(context.Background(), sqlcgen.SetItemBookmarkParams{
		Bookmark: bookmark,
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

// MarkAllReadFiltered marks every unread item matching f read: the feed/author/
// collection scope is matched by item membership, and each tag is an extra AND
// predicate over item_categories. Only unread rows are touched, so an already-
// read item's read_at (which orders the history list) is never re-stamped. It
// returns how many rows were marked.
func (s *ItemStore) MarkAllReadFiltered(userID int64, f ItemFilter) (int64, error) {
	q, args := unreadFilteredQuery(userID, f, true)
	res, err := s.db.ExecContext(context.Background(), q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountUnreadFiltered counts unread items matching f (same predicates as
// MarkAllReadFiltered).
func (s *ItemStore) CountUnreadFiltered(userID int64, f ItemFilter) (int, error) {
	q, args := unreadFilteredQuery(userID, f, false)
	var n int
	if err := s.db.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// unreadFilteredQuery builds the scoped "unread items matching f" statement. When
// mark is true it is the UPDATE that marks them read; otherwise it is the COUNT.
// It delegates the scope to the shared item predicate builder, so feeds, author
// views, tags, groups and time windows all mean the same thing here as in the
// list and count paths.
func unreadFilteredQuery(userID int64, f ItemFilter, mark bool) (string, []any) {
	source := f.itemSource()
	var b strings.Builder
	var args []any
	if mark {
		b.WriteString(`UPDATE items SET read = 1, read_at = ? WHERE user_id = ? AND read = 0
		AND items.id IN (SELECT i.id FROM ` + source + ` i JOIN feeds f ON f.id = i.feed_id WHERE i.user_id = ?`)
		args = append(args, ns(db.Now()), userID, userID)
	} else {
		b.WriteString(`SELECT COUNT(*) FROM items WHERE user_id = ? AND read = 0
		AND items.id IN (SELECT i.id FROM ` + source + ` i JOIN feeds f ON f.id = i.feed_id WHERE i.user_id = ?`)
		args = append(args, userID, userID)
	}
	appendItemFilterPredicates(&b, &args, f, source == "items")
	b.WriteString(")")
	return b.String(), args
}

// timeGroupsClause joins a set of time windows into one predicate with no
// leading AND. It is shared by the normal list path and saved-view branches.
func timeGroupsClause(expr string, groups []TimeGroup) (string, []any) {
	var clauses []string
	var args []any
	for _, g := range groups {
		var terms []string
		if g.After != "" {
			terms = append(terms, expr+" >= ?")
			args = append(args, g.After)
		}
		if g.Before != "" {
			terms = append(terms, expr+" <= ?")
			args = append(args, g.Before)
		}
		if len(terms) == 0 {
			continue
		}
		clause := "(" + strings.Join(terms, " AND ") + ")"
		if g.Exclude {
			clause = "NOT " + clause
		}
		clauses = append(clauses, clause)
	}
	return strings.Join(clauses, " AND "), args
}

// appendTimeGroups adds the time-window predicates to b. It is shared by the
// dynamic list/count query and the mark-all statement, so both evaluate the
// same windows; expr is the item time expression the caller's aliases allow.
func appendTimeGroups(b *strings.Builder, args *[]any, expr string, groups []TimeGroup) {
	clause, timeArgs := timeGroupsClause(expr, groups)
	if clause == "" {
		return
	}
	b.WriteString(" AND ")
	b.WriteString(clause)
	*args = append(*args, timeArgs...)
}

// filterGroupsClause builds the combined predicate for tag/feed groups, with no
// leading AND. It is shared by the normal list path and the author-filter
// preview, so both evaluate groups identically.
func filterGroupsClause(itemID string, tagGroups []TagGroup, feedGroups []FeedGroup) (string, []any) {
	var (
		clauses []string
		args    []any
	)
	for _, g := range tagGroups {
		terms := make([]string, 0, len(g.Tags))
		for _, tag := range g.Tags {
			terms = append(terms, "EXISTS (SELECT 1 FROM item_categories ic WHERE ic.item_id = "+itemID+" AND ic.category = ?)")
			args = append(args, tag)
		}
		if clause := groupClause(terms, g.All, g.Exclude); clause != "" {
			clauses = append(clauses, clause)
		}
	}
	for _, g := range feedGroups {
		terms := make([]string, 0, len(g.FeedIDs))
		for _, feedID := range g.FeedIDs {
			terms = append(terms, "EXISTS (SELECT 1 FROM item_feeds mfg WHERE mfg.item_id = "+itemID+" AND mfg.feed_id = ?)")
			args = append(args, feedID)
		}
		if clause := groupClause(terms, g.All, g.Exclude); clause != "" {
			clauses = append(clauses, clause)
		}
	}
	return strings.Join(clauses, " AND "), args
}

// groupClause joins a group's terms with AND/OR and negates the whole predicate
// for a hide group.
func groupClause(terms []string, all, exclude bool) string {
	if len(terms) == 0 {
		return ""
	}
	joiner := " OR "
	if all {
		joiner = " AND "
	}
	inner := strings.Join(terms, joiner)
	if exclude {
		return "NOT (" + inner + ")"
	}
	return "(" + inner + ")"
}

// appendFilterGroups adds the combined tag/feed group predicate to b.
func appendFilterGroups(b *strings.Builder, args *[]any, itemID string, tagGroups []TagGroup, feedGroups []FeedGroup) {
	clause, groupArgs := filterGroupsClause(itemID, tagGroups, feedGroups)
	if clause == "" {
		return
	}
	b.WriteString(" AND ")
	b.WriteString(clause)
	*args = append(*args, groupArgs...)
}

// MarkAuthorRead marks every item read across all of an author's feeds.
func (s *ItemStore) MarkAuthorRead(userID, authorID int64) error {
	return s.q.MarkAuthorItemsRead(context.Background(), sqlcgen.MarkAuthorItemsReadParams{
		ReadAt:   ns(db.Now()),
		UserID:   userID,
		AuthorID: authorID,
	})
}

// MarkAutoRead applies the auto-read retention policy across a user's feeds.
// Each item is governed by its owner author's override when set, otherwise by
// the account's auto-read window. An effective window of 0 (off) excludes that
// author. It returns how many items were marked read.
func (s *ItemStore) MarkAutoRead(userID int64) (int64, error) {
	return s.markAutoRead(userID, 0)
}

// MarkAutoReadForAuthor applies the same policy to one author's feeds only. It
// is the immediate sweep after an author's auto-read setting changes.
func (s *ItemStore) MarkAutoReadForAuthor(userID, authorID int64) (int64, error) {
	return s.markAutoRead(userID, authorID)
}

func (s *ItemStore) markAutoRead(userID, authorID int64) (int64, error) {
	res, err := s.q.MarkItemsAutoRead(context.Background(), sqlcgen.MarkItemsAutoReadParams{
		ReadAt:   ns(db.Now()),
		Now:      db.FormatTime(time.Now()),
		UserID:   userID,
		AuthorID: authorID,
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

// CountBookmarks counts bookmarked items for a user; feedID 0 means all feeds.
func (s *ItemStore) CountBookmarks(userID, feedID int64) (int, error) {
	n, err := s.q.CountBookmarkItems(context.Background(), sqlcgen.CountBookmarkItemsParams{
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

// CountBookmarksAuthor counts bookmarked items across an author's feeds.
func (s *ItemStore) CountBookmarksAuthor(userID, authorID int64) (int, error) {
	n, err := s.q.CountBookmarkItemsByAuthor(context.Background(), sqlcgen.CountBookmarkItemsByAuthorParams{
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
