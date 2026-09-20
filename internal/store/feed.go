package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

type Feed struct {
	ID              int64
	UserID          int64
	AuthorID        int64
	Title           string
	FeedURL         string
	HomeURL         string
	Description     string
	ETag            string
	LastModified    string
	LastPolledAt    string
	PollIntervalSec int
	Enabled         bool
	CreatedAt       string
}

// FeedWithUnread joins a feed with its author's name and unread item count.
type FeedWithUnread struct {
	Feed
	AuthorName string
	Unread     int
}

type FeedStore struct{ q *sqlcgen.Queries }

func (s *FeedStore) Create(userID, authorID int64, title, feedURL, homeURL, description string, pollIntervalSec int) (Feed, error) {
	f, err := s.q.CreateFeed(context.Background(), sqlcgen.CreateFeedParams{
		UserID:          userID,
		AuthorID:        ni(authorID),
		Title:           title,
		FeedUrl:         feedURL,
		HomeUrl:         ns(homeURL),
		Description:     ns(description),
		PollIntervalSec: int64(pollIntervalSec),
	})
	if err != nil {
		return Feed{}, fmt.Errorf("create feed: %w", err)
	}
	return toFeed(f), nil
}

func (s *FeedStore) ByID(userID, id int64) (Feed, error) {
	f, err := s.q.GetFeed(context.Background(), sqlcgen.GetFeedParams{ID: id, UserID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		return Feed{}, ErrNotFound
	}
	if err != nil {
		return Feed{}, err
	}
	return toFeed(f), nil
}

// ByIDAny returns a feed by id regardless of user. Used by the poller and CLI.
func (s *FeedStore) ByIDAny(id int64) (Feed, error) {
	f, err := s.q.GetFeedAny(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return Feed{}, ErrNotFound
	}
	if err != nil {
		return Feed{}, err
	}
	return toFeed(f), nil
}

func (s *FeedStore) List(userID int64) ([]Feed, error) {
	rows, err := s.q.ListFeeds(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]Feed, 0, len(rows))
	for _, f := range rows {
		out = append(out, toFeed(f))
	}
	return out, nil
}

func (s *FeedStore) ListByAuthor(userID, authorID int64) ([]Feed, error) {
	rows, err := s.q.ListFeedsByAuthor(context.Background(), sqlcgen.ListFeedsByAuthorParams{
		UserID:   userID,
		AuthorID: ni(authorID),
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

// ListWithUnread returns a user's feeds with author name and unread counts in
// one query, avoiding a count query per feed.
func (s *FeedStore) ListWithUnread(userID int64) ([]FeedWithUnread, error) {
	rows, err := s.q.ListFeedsWithUnread(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]FeedWithUnread, 0, len(rows))
	for _, f := range rows {
		out = append(out, toFeedWithUnread(f))
	}
	return out, nil
}

// ListByAuthorWithUnread is ListWithUnread scoped to one author.
func (s *FeedStore) ListByAuthorWithUnread(userID, authorID int64) ([]FeedWithUnread, error) {
	rows, err := s.q.ListFeedsByAuthorWithUnread(context.Background(), sqlcgen.ListFeedsByAuthorWithUnreadParams{
		UserID:   userID,
		AuthorID: ni(authorID),
	})
	if err != nil {
		return nil, err
	}
	out := make([]FeedWithUnread, 0, len(rows))
	for _, f := range rows {
		out = append(out, toFeedByAuthorWithUnread(f))
	}
	return out, nil
}

func (s *FeedStore) Update(userID, id int64, authorID int64, title, feedURL, homeURL, description string, pollIntervalSec int, enabled bool) error {
	res, err := s.q.UpdateFeed(context.Background(), sqlcgen.UpdateFeedParams{
		AuthorID:        ni(authorID),
		Title:           title,
		FeedUrl:         feedURL,
		HomeUrl:         ns(homeURL),
		Description:     ns(description),
		PollIntervalSec: int64(pollIntervalSec),
		Enabled:         enabled,
		ID:              id,
		UserID:          userID,
	})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *FeedStore) Delete(userID, id int64) error {
	res, err := s.q.DeleteFeed(context.Background(), sqlcgen.DeleteFeedParams{ID: id, UserID: userID})
	if err != nil {
		return fmt.Errorf("delete feed: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPollMeta records the result of a fetch: entity tags for conditional GET
// and the time of the poll. Not user-scoped because the poller owns it.
func (s *FeedStore) SetPollMeta(id int64, etag, lastModified, lastPolledAt string) error {
	return s.q.SetFeedPollMeta(context.Background(), sqlcgen.SetFeedPollMetaParams{
		Etag:         ns(etag),
		LastModified: ns(lastModified),
		LastPolledAt: ns(lastPolledAt),
		ID:           id,
	})
}

// ListDue returns enabled feeds that have not been polled within their own
// poll_interval_sec of now.
func (s *FeedStore) ListDue(now string) ([]Feed, error) {
	rows, err := s.q.ListFeedsDue(context.Background(), now)
	if err != nil {
		return nil, err
	}
	out := make([]Feed, 0, len(rows))
	for _, f := range rows {
		out = append(out, toFeed(f))
	}
	return out, nil
}
