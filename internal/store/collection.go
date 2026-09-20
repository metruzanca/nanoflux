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
	CreatedAt string
}

type CollectionStore struct{ q *sqlcgen.Queries }

func (s *CollectionStore) Create(userID int64, name string) (Collection, error) {
	c, err := s.q.CreateCollection(context.Background(), sqlcgen.CreateCollectionParams{
		UserID: userID,
		Name:   name,
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
		out = append(out, toFeed(f))
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
