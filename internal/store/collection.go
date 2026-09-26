package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

type Collection struct {
	ID        int64
	UserID    int64
	Name      string
	IsAuto    bool
	CreatedAt string
}

// CollectionWithCounts joins a collection with its feed and item counts for
// the collections index cards.
type CollectionWithCounts struct {
	Collection
	FeedCount int
	Unread    int
	Read      int
}

type CollectionStore struct{ q *sqlcgen.Queries }

func (s *CollectionStore) Create(userID int64, name string) (Collection, error) {
	c, err := s.q.CreateCollection(context.Background(), sqlcgen.CreateCollectionParams{
		UserID: userID,
		Name:   name,
		IsAuto: 0,
	})
	if err != nil {
		return Collection{}, fmt.Errorf("create collection: %w", err)
	}
	return toCollection(c), nil
}

// EnsureAuto returns the auto collection for a hostname, creating it if it
// does not exist. A user-created collection with the same name is left alone;
// auto collections are always separate rows.
func (s *CollectionStore) EnsureAuto(userID int64, host string) (Collection, error) {
	c, err := s.q.GetAutoCollection(context.Background(), sqlcgen.GetAutoCollectionParams{
		UserID: userID,
		Name:   host,
	})
	if err == nil {
		return toCollection(c), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Collection{}, err
	}
	c, err = s.q.CreateCollection(context.Background(), sqlcgen.CreateCollectionParams{
		UserID: userID,
		Name:   host,
		IsAuto: 1,
	})
	if err != nil {
		return Collection{}, fmt.Errorf("create auto collection: %w", err)
	}
	return toCollection(c), nil
}

// AssignAuto adds a feed to the auto collection for its website, derived from
// its home url (falling back to the feed url). It is a no-op when no
// registrable domain can be derived.
func (s *CollectionStore) AssignAuto(userID, feedID int64, homeURL, feedURL string) error {
	host := RegistrableDomain(homeURL)
	if host == "" {
		host = RegistrableDomain(feedURL)
	}
	if host == "" {
		return nil
	}
	c, err := s.EnsureAuto(userID, host)
	if err != nil {
		return err
	}
	return s.AddFeed(userID, c.ID, feedID)
}

// UnassignAuto removes a feed from the auto collection of its previous
// website. Used when a feed's home/feed url changes hosts, so it does not
// linger in the old auto collection.
func (s *CollectionStore) UnassignAuto(userID, feedID int64, homeURL, feedURL string) error {
	host := RegistrableDomain(homeURL)
	if host == "" {
		host = RegistrableDomain(feedURL)
	}
	if host == "" {
		return nil
	}
	c, err := s.q.GetAutoCollection(context.Background(), sqlcgen.GetAutoCollectionParams{
		UserID: userID,
		Name:   host,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.RemoveFeed(userID, c.ID, feedID)
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

// ListWithCounts returns the user's collections with their feed, unread, and
// read counts in one query, for the collections index cards.
func (s *CollectionStore) ListWithCounts(userID int64) ([]CollectionWithCounts, error) {
	rows, err := s.q.ListCollectionsWithCounts(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]CollectionWithCounts, 0, len(rows))
	for _, r := range rows {
		out = append(out, CollectionWithCounts{
			Collection: toCollection(sqlcgen.Collection{
				ID: r.ID, UserID: r.UserID, Name: r.Name, IsAuto: r.IsAuto, CreatedAt: r.CreatedAt,
			}),
			FeedCount: int(r.FeedCount),
			Unread:    int(r.UnreadCount),
			Read:      int(r.ReadCount),
		})
	}
	return out, nil
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

// Rename updates a collection's name (not auto collections are excluded by the
// caller; this only verifies ownership).
func (s *CollectionStore) Rename(userID, id int64, name string) error {
	res, err := s.q.RenameCollection(context.Background(), sqlcgen.RenameCollectionParams{
		Name: name, ID: id, UserID: userID,
	})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddFeed associates a feed with a collection, verifying both belong to the user.
func (s *CollectionStore) AddFeed(userID, collectionID, feedID int64) error {
	n, err := s.q.VerifyCollectionFeed(context.Background(), sqlcgen.VerifyCollectionFeedParams{
		ID:     collectionID,
		ID_2:   feedID,
		UserID: userID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	_, err = s.q.AddFeedToCollection(context.Background(), sqlcgen.AddFeedToCollectionParams{
		CollectionID: collectionID,
		FeedID:       feedID,
	})
	return err
}

func (s *CollectionStore) RemoveFeed(userID, collectionID, feedID int64) error {
	res, err := s.q.RemoveFeedFromCollection(context.Background(), sqlcgen.RemoveFeedFromCollectionParams{
		CollectionID: collectionID,
		FeedID:       feedID,
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
		out = append(out, toFeed(feedFromUnreadRow(f.ID, f.UserID, f.AuthorID, f.Title, f.FeedUrl, f.HomeUrl, f.Description, f.Etag, f.LastModified, f.LastPolledAt, f.LastError, f.NextPageUrl, f.PollIntervalSec, f.PollIntervalAuto, f.LastItemAt, f.NextPollAt, f.PluginName, f.DisabledReason, f.Enabled, f.IsSystem, f.CreatedAt)))
	}
	return out, nil
}

// FeedCollectionIDs returns the ids of a user's collections that contain
// feedID, in one query.
func (s *CollectionStore) FeedCollectionIDs(userID, feedID int64) ([]int64, error) {
	return s.q.CollectionIDsForFeed(context.Background(), sqlcgen.CollectionIDsForFeedParams{
		FeedID: feedID,
		UserID: userID,
	})
}

// CollectionsByAuthorFeed returns, in one query, the collections each of an
// author's feeds belongs to, keyed by feed id. Feeds with no collections are
// absent from the map. Used to render collection tags on the author page.
func (s *CollectionStore) CollectionsByAuthorFeed(userID, authorID int64) (map[int64][]Collection, error) {
	rows, err := s.q.ListCollectionsForAuthorFeeds(context.Background(), sqlcgen.ListCollectionsForAuthorFeedsParams{
		UserID:   userID,
		UserID_2: userID,
		AuthorID: authorID,
	})
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]Collection, len(rows))
	for _, r := range rows {
		out[r.FeedID] = append(out[r.FeedID], Collection{
			ID:     r.ID,
			UserID: userID,
			Name:   r.Name,
			IsAuto: r.IsAuto != 0,
		})
	}
	return out, nil
}
