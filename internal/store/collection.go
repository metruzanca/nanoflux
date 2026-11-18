package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
	"github.com/metruzanca/nanoflux/internal/tagnorm"
)

// Collection is a simple user-managed group of authors. A collection's item
// scope is the union of its member authors' pages, so adding an author brings
// that author's feeds and views.
type Collection struct {
	ID        int64
	UserID    int64
	Name      string
	CreatedAt string
}

// CollectionWithCounts joins a collection with its author and item counts for
// the collections index cards.
type CollectionWithCounts struct {
	Collection
	AuthorCount int
	Unread      int
	Read        int
}

type CollectionStore struct {
	q  *sqlcgen.Queries
	db *sql.DB
}

func (s *CollectionStore) Create(userID int64, name string) (Collection, error) {
	c, err := s.q.CreateCollection(context.Background(), sqlcgen.CreateCollectionParams{
		UserID: userID,
		Name:   tagnorm.Label(name),
	})
	if err != nil {
		return Collection{}, fmt.Errorf("create collection: %w", err)
	}
	return toCollection(c), nil
}

func (s *CollectionStore) ByID(userID, id int64) (Collection, error) {
	c, err := s.q.GetCollection(context.Background(), sqlcgen.GetCollectionParams{ID: id, UserID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		return Collection{}, ErrNotFound
	}
	if err != nil {
		return Collection{}, err
	}
	return toCollection(c), nil
}

func (s *CollectionStore) List(userID int64) ([]Collection, error) {
	rows, err := s.q.ListCollections(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]Collection, 0, len(rows))
	for _, c := range rows {
		out = append(out, toCollection(c))
	}
	return out, nil
}

// ListWithCounts returns the user's collections with their author, unread, and
// read counts in one query, for the collections index cards.
func (s *CollectionStore) ListWithCounts(userID int64) ([]CollectionWithCounts, error) {
	rows, err := s.q.ListCollectionsWithCounts(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]CollectionWithCounts, 0, len(rows))
	for _, r := range rows {
		out = append(out, CollectionWithCounts{
			Collection: Collection{
				ID: r.ID, UserID: r.UserID, Name: r.Name, CreatedAt: r.CreatedAt,
			},
			AuthorCount: int(r.AuthorCount),
			Unread:      int(r.UnreadCount),
			Read:        int(r.ReadCount),
		})
	}
	return out, nil
}

// ListForIndex returns the user's collections with only the counts that are
// cheap to derive: member authors. Item counts come from the collections
// index's per-user cache.
func (s *CollectionStore) ListForIndex(userID int64) ([]CollectionWithCounts, error) {
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT c.id, c.user_id, c.name, c.created_at,
		       COUNT(DISTINCT ca.author_id) AS author_count
		FROM collections c
		LEFT JOIN collection_authors ca ON ca.collection_id = c.id
		WHERE c.user_id = ?
		GROUP BY c.id
		ORDER BY c.name, c.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CollectionWithCounts
	for rows.Next() {
		var c CollectionWithCounts
		if err := rows.Scan(&c.ID, &c.UserID, &c.Name, &c.CreatedAt, &c.AuthorCount); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *CollectionStore) Delete(userID, id int64) error {
	res, err := s.q.DeleteCollection(context.Background(), sqlcgen.DeleteCollectionParams{ID: id, UserID: userID})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Rename updates a collection's name.
func (s *CollectionStore) Rename(userID, id int64, name string) error {
	res, err := s.q.RenameCollection(context.Background(), sqlcgen.RenameCollectionParams{
		Name: tagnorm.Label(name), ID: id, UserID: userID,
	})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddAuthor associates an author with a collection, verifying both belong to
// the user.
func (s *CollectionStore) AddAuthor(userID, collectionID, authorID int64) error {
	n, err := s.q.VerifyCollectionAuthor(context.Background(), sqlcgen.VerifyCollectionAuthorParams{
		ID:     collectionID,
		ID_2:   authorID,
		UserID: userID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	_, err = s.q.AddAuthorToCollection(context.Background(), sqlcgen.AddAuthorToCollectionParams{
		CollectionID: collectionID,
		AuthorID:     authorID,
	})
	return err
}

func (s *CollectionStore) RemoveAuthor(userID, collectionID, authorID int64) error {
	res, err := s.q.RemoveAuthorFromCollection(context.Background(), sqlcgen.RemoveAuthorFromCollectionParams{
		CollectionID: collectionID,
		AuthorID:     authorID,
		UserID:       userID,
		UserID_2:     userID,
	})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AuthorIDs returns a collection's member author ids.
func (s *CollectionStore) AuthorIDs(userID, collectionID int64) ([]int64, error) {
	return s.q.AuthorIDsInCollection(context.Background(), sqlcgen.AuthorIDsInCollectionParams{
		CollectionID: collectionID,
		UserID:       userID,
	})
}

// CollectionIDsForAuthor returns the ids of the user's collections that hold
// the author.
func (s *CollectionStore) CollectionIDsForAuthor(userID, authorID int64) ([]int64, error) {
	return s.q.CollectionIDsForAuthor(context.Background(), sqlcgen.CollectionIDsForAuthorParams{
		AuthorID: authorID,
		UserID:   userID,
	})
}

// ListForAuthor returns the collections an author belongs to.
func (s *CollectionStore) ListForAuthor(userID, authorID int64) ([]Collection, error) {
	rows, err := s.q.ListCollectionsForAuthor(context.Background(), sqlcgen.ListCollectionsForAuthorParams{
		UserID:   userID,
		AuthorID: authorID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Collection, 0, len(rows))
	for _, r := range rows {
		out = append(out, Collection{ID: r.ID, UserID: r.UserID, Name: r.Name, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

// Feeds returns every feed brought in by a collection's member authors.
func (s *CollectionStore) Feeds(userID, collectionID int64) ([]Feed, error) {
	rows, err := s.q.ListFeedsInCollection(context.Background(), sqlcgen.ListFeedsInCollectionParams{
		CollectionID: collectionID,
		UserID:       userID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Feed, 0, len(rows))
	for _, f := range rows {
		out = append(out, toFeed(feedFromUnreadRow(f.ID, f.UserID, f.AuthorID, f.Title, f.FeedUrl, f.HomeUrl, f.Description, f.Etag, f.LastModified, f.LastPolledAt, f.LastError, f.NextPageUrl, f.PollIntervalSec, f.PollIntervalAuto, f.LastItemAt, f.NextPollAt, f.NextPollScheduled, f.PluginName, f.DisabledReason, f.Enabled, f.IsSystem, f.Rank, f.CacheImages, f.CreatedAt)))
	}
	return out, nil
}
