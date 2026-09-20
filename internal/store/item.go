package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/metruzanca/nanoflux/internal/db"
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

type ItemStore struct{ db *sql.DB }

// Upsert inserts an item, ignoring duplicates on (feed_id, guid). It reports
// whether a new row was actually inserted.
func (s *ItemStore) Upsert(feedID int64, it Item) (inserted bool, err error) {
	res, err := s.db.Exec(
		`INSERT INTO items(feed_id, guid, title, link, summary, image_url, published_at, fetched_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(feed_id, guid) DO NOTHING`,
		feedID, it.GUID, it.Title, it.Link, it.Summary, nullStr(it.ImageURL),
		nullStr(it.PublishedAt), it.FetchedAt,
	)
	if err != nil {
		return false, fmt.Errorf("upsert item: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *ItemStore) List(userID int64, f ItemFilter) ([]ItemWithFeed, error) {
	conds := []string{"f.user_id = ?"}
	args := []any{userID}
	if f.UnreadOnly {
		conds = append(conds, "i.read = 0")
	}
	if f.ReadOnly {
		conds = append(conds, "i.read = 1")
	}
	if f.FavoritesOnly {
		conds = append(conds, "i.favorite = 1")
	}
	if f.FeedID != 0 {
		conds = append(conds, "f.id = ?")
		args = append(args, f.FeedID)
	}
	if f.AuthorID != 0 {
		conds = append(conds, "f.author_id = ?")
		args = append(args, f.AuthorID)
	}
	if f.CollectionID != 0 {
		conds = append(conds,
			"i.feed_id IN (SELECT feed_id FROM collection_feeds WHERE collection_id = ?)")
		args = append(args, f.CollectionID)
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	rows, err := s.db.Query(
		`SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.image_url,
		        i.published_at, i.fetched_at, i.read, i.favorite, i.read_at,
		        f.title, f.feed_url, a.id, a.name
		 FROM items i
		 JOIN feeds f ON f.id = i.feed_id
		 LEFT JOIN authors a ON a.id = f.author_id
		 WHERE `+strings.Join(conds, " AND ")+`
		 ORDER BY COALESCE(i.published_at, i.fetched_at) DESC, i.id DESC
		 LIMIT ?`,
		append(args, limit)...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ItemWithFeed
	for rows.Next() {
		it, err := scanItemWithFeed(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *ItemStore) ByID(userID, id int64) (Item, error) {
	it, err := scanItem(s.db.QueryRow(
		`SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.image_url,
		        i.published_at, i.fetched_at, i.read, i.favorite, i.read_at
		 FROM items i JOIN feeds f ON f.id = i.feed_id
		 WHERE i.id = ? AND f.user_id = ?`, id, userID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	return it, err
}

// OneWithFeed returns a single item joined with its feed and author.
func (s *ItemStore) OneWithFeed(userID, itemID int64) (ItemWithFeed, error) {
	it, err := scanItemWithFeed(s.db.QueryRow(
		`SELECT i.id, i.feed_id, i.guid, i.title, i.link, i.summary, i.image_url,
		        i.published_at, i.fetched_at, i.read, i.favorite, i.read_at,
		        f.title, f.feed_url, a.id, a.name
		 FROM items i
		 JOIN feeds f ON f.id = i.feed_id
		 LEFT JOIN authors a ON a.id = f.author_id
		 WHERE i.id = ? AND f.user_id = ?`, itemID, userID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return ItemWithFeed{}, ErrNotFound
	}
	return it, err
}

// SetRead marks an item read/unread, verifying it belongs to the user. When an
// item is marked read its read_at timestamp is recorded; unread clears it.
func (s *ItemStore) SetRead(userID, itemID int64, read bool) error {
	var readAt any
	if read {
		readAt = db.Now()
	}
	res, err := s.db.Exec(
		`UPDATE items SET read = ?, read_at = ?
		 WHERE id = ? AND feed_id IN (SELECT id FROM feeds WHERE user_id = ?)`,
		boolInt(read), readAt, itemID, userID,
	)
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
	res, err := s.db.Exec(
		`UPDATE items SET favorite = ?
		 WHERE id = ? AND feed_id IN (SELECT id FROM feeds WHERE user_id = ?)`,
		boolInt(fav), itemID, userID,
	)
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
	_, err := s.db.Exec(
		`UPDATE items SET read = 1, read_at = ?
		 WHERE feed_id IN (SELECT id FROM feeds WHERE user_id = ?)
		   AND (? = 0 OR feed_id = ?)`,
		db.Now(), userID, feedID, feedID,
	)
	return err
}

// MarkAllUnread marks every item unread for a user; pass feedID 0 for all feeds.
func (s *ItemStore) MarkAllUnread(userID, feedID int64) error {
	_, err := s.db.Exec(
		`UPDATE items SET read = 0, read_at = NULL
		 WHERE feed_id IN (SELECT id FROM feeds WHERE user_id = ?)
		   AND (? = 0 OR feed_id = ?)`,
		userID, feedID, feedID,
	)
	return err
}

// CountUnread counts unread items for a user; feedID 0 means all feeds.
func (s *ItemStore) CountUnread(userID, feedID int64) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM items i JOIN feeds f ON f.id = i.feed_id
		 WHERE f.user_id = ? AND i.read = 0 AND (? = 0 OR f.id = ?)`,
		userID, feedID, feedID,
	).Scan(&n)
	return n, err
}

// CountRead counts read items for a user; feedID 0 means all feeds.
func (s *ItemStore) CountRead(userID, feedID int64) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM items i JOIN feeds f ON f.id = i.feed_id
		 WHERE f.user_id = ? AND i.read = 1 AND (? = 0 OR f.id = ?)`,
		userID, feedID, feedID,
	).Scan(&n)
	return n, err
}

// CountFavorites counts favorited items for a user; feedID 0 means all feeds.
func (s *ItemStore) CountFavorites(userID, feedID int64) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM items i JOIN feeds f ON f.id = i.feed_id
		 WHERE f.user_id = ? AND i.favorite = 1 AND (? = 0 OR f.id = ?)`,
		userID, feedID, feedID,
	).Scan(&n)
	return n, err
}

// CountUnreadAuthor counts unread items across an author's feeds.
func (s *ItemStore) CountUnreadAuthor(userID, authorID int64) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM items i JOIN feeds f ON f.id = i.feed_id
		 WHERE f.user_id = ? AND i.read = 0 AND f.author_id = ?`,
		userID, authorID,
	).Scan(&n)
	return n, err
}

// CountReadAuthor counts read items across an author's feeds.
func (s *ItemStore) CountReadAuthor(userID, authorID int64) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM items i JOIN feeds f ON f.id = i.feed_id
		 WHERE f.user_id = ? AND i.read = 1 AND f.author_id = ?`,
		userID, authorID,
	).Scan(&n)
	return n, err
}

// CountUnreadCollection counts unread items across a collection's feeds.
func (s *ItemStore) CountUnreadCollection(userID, collectionID int64) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM items i JOIN feeds f ON f.id = i.feed_id
		 WHERE f.user_id = ? AND i.read = 0
		   AND i.feed_id IN (SELECT feed_id FROM collection_feeds WHERE collection_id = ?)`,
		userID, collectionID,
	).Scan(&n)
	return n, err
}

// CountReadCollection counts read items across a collection's feeds.
func (s *ItemStore) CountReadCollection(userID, collectionID int64) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM items i JOIN feeds f ON f.id = i.feed_id
		 WHERE f.user_id = ? AND i.read = 1
		   AND i.feed_id IN (SELECT feed_id FROM collection_feeds WHERE collection_id = ?)`,
		userID, collectionID,
	).Scan(&n)
	return n, err
}

func scanItem(row scanner) (Item, error) {
	var it Item
	var imageURL, publishedAt, readAt sql.NullString
	var read, favorite int
	err := row.Scan(
		&it.ID, &it.FeedID, &it.GUID, &it.Title, &it.Link, &it.Summary,
		&imageURL, &publishedAt, &it.FetchedAt, &read, &favorite, &readAt,
	)
	it.ImageURL, it.PublishedAt = imageURL.String, publishedAt.String
	it.Read = read != 0
	it.ReadAt = readAt.String
	it.Favorite = favorite != 0
	return it, err
}

func scanItemWithFeed(row scanner) (ItemWithFeed, error) {
	var it ItemWithFeed
	var imageURL, publishedAt, authorName, readAt sql.NullString
	var authorID sql.NullInt64
	var read, favorite int
	err := row.Scan(
		&it.ID, &it.FeedID, &it.GUID, &it.Title, &it.Link, &it.Summary,
		&imageURL, &publishedAt, &it.FetchedAt, &read, &favorite, &readAt,
		&it.FeedTitle, &it.FeedURL, &authorID, &authorName,
	)
	it.ImageURL, it.PublishedAt = imageURL.String, publishedAt.String
	it.AuthorID, it.AuthorName = authorID.Int64, authorName.String
	it.Read = read != 0
	it.ReadAt = readAt.String
	it.Favorite = favorite != 0
	return it, err
}
