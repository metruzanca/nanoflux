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
	FeedTitle  string
	FeedURL    string
	AuthorID   int64
	AuthorName string
	Timezone   string // user's IANA timezone, for relative timestamps in templates
}

type ItemFilter struct {
	UnreadOnly    bool
	ReadOnly      bool
	FavoritesOnly bool
	FeedID        int64 // 0 = all
	AuthorID      int64 // 0 = all
	CollectionID  int64 // 0 = all
	Limit         int
}

type ItemStore struct{ q *sqlcgen.Queries }

// Upsert inserts an item, ignoring duplicates on (feed_id, guid). It reports
// whether a new row was actually inserted.
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
	})
	if err != nil {
		return false, fmt.Errorf("upsert item: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *ItemStore) List(userID int64, f ItemFilter) ([]ItemWithFeed, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.q.ListItems(context.Background(), sqlcgen.ListItemsParams{
		UserID:       userID,
		FeedID:       f.FeedID,
		AuthorID:     f.AuthorID,
		CollectionID: f.CollectionID,
		Unread:       boolInt(f.UnreadOnly),
		Read:         boolInt(f.ReadOnly),
		Favorites:    boolInt(f.FavoritesOnly),
		Limit:        int64(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]ItemWithFeed, 0, len(rows))
	for _, r := range rows {
		out = append(out, toItemWithFeed(r.ID, r.FeedID, r.Guid, r.Title, r.Link, r.Summary,
			r.ImageUrl, r.PublishedAt, r.FetchedAt, r.Read, r.Favorite, r.ReadAt,
			r.FeedTitle, r.FeedUrl, r.AuthorID, r.AuthorName))
	}
	return out, nil
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
		it.FeedTitle, it.FeedUrl, it.AuthorID, it.AuthorName), nil
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
		AuthorID: ni(authorID),
	})
	return int(n), err
}

// CountReadAuthor counts read items across an author's feeds.
func (s *ItemStore) CountReadAuthor(userID, authorID int64) (int, error) {
	n, err := s.q.CountReadItemsByAuthor(context.Background(), sqlcgen.CountReadItemsByAuthorParams{
		UserID:   userID,
		AuthorID: ni(authorID),
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
