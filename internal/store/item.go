package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

type Item struct {
	ID          int64
	FeedID      int64
	GUID        string
	Title       string
	Link        string
	Summary     string
	ImageURL    string
	PublishedAt string
	FetchedAt   string
	Read        bool
	ReadAt      string
	Favorite    bool
}

// ItemWithFeed joins an item with its feed and author for display.
type ItemWithFeed struct {
	Item
	FeedTitle   string
	FeedURL     string
	FeedHomeURL string // the feed's home page, for source-icon lookups
	AuthorID    int64
	AuthorName  string
	Sources     []ItemSource // additional feeds this item appears in (view-time dedup)
	Timezone    string       // user's IANA timezone, for relative timestamps in templates
}

// ItemSource is one alternate copy of an item that was merged into the
// surviving row during view-time dedup.
type ItemSource struct {
	FeedID    int64
	FeedTitle string
	Link      string
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
}

// Upsert inserts an item, ignoring duplicates on (feed_id, guid). It reports
// whether a new row was actually inserted; when the item already exists its
// content snapshot (summary, thumbnail) is refreshed so the listing tracks the
// live feed without touching identity, published_at or read state.
func (s *ItemStore) Upsert(feedID int64, it Item) (inserted bool, err error) {
	res, err := s.q.UpsertItem(context.Background(), sqlcgen.UpsertItemParams{
		FeedID:      feedID,
		Guid:        it.GUID,
		Title:       it.Title,
		Link:        it.Link,
		Summary:     it.Summary,
		ImageUrl:    ns(it.ImageURL),
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
	if n > 0 {
		return true, nil
	}
	if err := s.q.UpdateItemSnapshot(context.Background(), sqlcgen.UpdateItemSnapshotParams{
		Summary:  it.Summary,
		ImageUrl: ns(it.ImageURL),
		FeedID:   feedID,
		Guid:     it.GUID,
	}); err != nil {
		return false, fmt.Errorf("refresh item snapshot: %w", err)
	}
	return false, nil
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
			out = append(out, toItemWithFeed(r.ID, r.FeedID, r.Guid, r.Title, r.Link, r.Summary,
				r.ImageUrl, r.PublishedAt, r.FetchedAt, r.Read, r.Favorite, r.ReadAt,
				r.FeedTitle, r.FeedUrl, r.FeedHomeUrl, r.AuthorID, r.AuthorName))
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
		out = append(out, toItemWithFeed(r.ID, r.FeedID, r.Guid, r.Title, r.Link, r.Summary,
			r.ImageUrl, r.PublishedAt, r.FetchedAt, r.Read, r.Favorite, r.ReadAt,
			r.FeedTitle, r.FeedUrl, r.FeedHomeUrl, r.AuthorID, r.AuthorName))
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

// ByFeedGUID returns the id of the item stored for (feedID, guid), or 0 when
// it does not exist.
func (s *ItemStore) ByFeedGUID(feedID int64, guid string) (int64, error) {
	id, err := s.q.GetItemByFeedGuid(context.Background(), sqlcgen.GetItemByFeedGuidParams{
		FeedID: feedID,
		Guid:   guid,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return id, nil
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
	const sql = `SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.image_url,
       i.published_at, i.fetched_at, i.read, i.favorite, i.read_at,
       f.title AS feed_title, f.feed_url AS feed_url, f.home_url AS feed_home_url,
       a.id AS author_id, a.name AS author_name
FROM items i
JOIN feeds f ON f.id = i.feed_id
LEFT JOIN authors a ON a.id = f.author_id
JOIN items_fts fts ON fts.rowid = i.id
WHERE f.user_id = ?1
  AND items_fts MATCH ?2
  AND (CAST(?3 AS INTEGER) = 0 OR f.id = CAST(?3 AS INTEGER))
  AND (CAST(?4 AS INTEGER) = 0 OR f.author_id = CAST(?4 AS INTEGER))
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
		if err := rows.Scan(&r.ID, &r.FeedID, &r.Guid, &r.Title, &r.Link, &r.Summary,
			&r.ImageUrl, &r.PublishedAt, &r.FetchedAt, &r.Read, &r.Favorite, &r.ReadAt,
			&r.FeedTitle, &r.FeedUrl, &r.FeedHomeUrl, &r.AuthorID, &r.AuthorName); err != nil {
			return nil, false, err
		}
		out = append(out, toItemWithFeed(r.ID, r.FeedID, r.Guid, r.Title, r.Link, r.Summary,
			r.ImageUrl, r.PublishedAt, r.FetchedAt, r.Read, r.Favorite, r.ReadAt,
			r.FeedTitle, r.FeedUrl, r.FeedHomeUrl, r.AuthorID, r.AuthorName))
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

// OneWithFeed returns a single item joined with its feed and author.
func (s *ItemStore) OneWithFeed(userID, itemID int64) (ItemWithFeed, error) {
	it, err := s.q.GetItemWithFeed(context.Background(), sqlcgen.GetItemWithFeedParams{ID: itemID, UserID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		return ItemWithFeed{}, ErrNotFound
	}
	if err != nil {
		return ItemWithFeed{}, err
	}
	return toItemWithFeed(it.ID, it.FeedID, it.Guid, it.Title, it.Link, it.Summary,
		it.ImageUrl, it.PublishedAt, it.FetchedAt, it.Read, it.Favorite, it.ReadAt,
		it.FeedTitle, it.FeedUrl, it.FeedHomeUrl, it.AuthorID, it.AuthorName), nil
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
	return toItemWithFeed(it.ID, it.FeedID, it.Guid, it.Title, it.Link, it.Summary,
		it.ImageUrl, it.PublishedAt, it.FetchedAt, it.Read, it.Favorite, it.ReadAt,
		it.FeedTitle, it.FeedUrl, it.FeedHomeUrl, it.AuthorID, it.AuthorName), nil
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

// MarkBeforeRead marks every unread item in the same feed as itemID that is
// newer than it (listed above it, newest first) as read.
func (s *ItemStore) MarkBeforeRead(userID, itemID int64) error {
	return s.q.MarkItemsBeforeRead(context.Background(), sqlcgen.MarkItemsBeforeReadParams{
		ReadAt: ns(db.Now()),
		ItemID: itemID,
		UserID: userID,
	})
}

// MarkAfterRead marks every unread item in the same feed as itemID that is
// older than it (listed below it, newest first) as read.
func (s *ItemStore) MarkAfterRead(userID, itemID int64) error {
	return s.q.MarkItemsAfterRead(context.Background(), sqlcgen.MarkItemsAfterReadParams{
		ReadAt: ns(db.Now()),
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
