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
	LastError       string
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
		AuthorID:        authorID,
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

// ByTitle returns the user's feed matching title (case-insensitive), or
// ErrNotFound. Used by search qualifiers.
func (s *FeedStore) ByTitle(userID int64, title string) (Feed, error) {
	f, err := s.q.GetFeedByTitle(context.Background(), sqlcgen.GetFeedByTitleParams{
		UserID: userID,
		Title:  title,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Feed{}, ErrNotFound
	}
	if err != nil {
		return Feed{}, err
	}
	return toFeed(f), nil
}

func (s *FeedStore) ListByAuthor(userID, authorID int64) ([]Feed, error) {
	rows, err := s.q.ListFeedsByAuthor(context.Background(), sqlcgen.ListFeedsByAuthorParams{
		UserID:   userID,
		AuthorID: authorID,
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
		AuthorID: authorID,
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
		AuthorID:        authorID,
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
// SetPollMeta records etag/last_modified/last_polled_at and the last poll
// outcome. lastError is "" on success and the (truncated) failure text
// otherwise.
func (s *FeedStore) SetPollMeta(id int64, etag, lastModified, lastPolledAt, lastError string) error {
	return s.q.SetFeedPollMeta(context.Background(), sqlcgen.SetFeedPollMetaParams{
		Etag:         ns(etag),
		LastModified: ns(lastModified),
		LastPolledAt: ns(lastPolledAt),
		LastError:    ns(lastError),
		ID:           id,
	})
}

// SetEnabled pauses or resumes polling for a feed. Disabled feeds keep their
// items but are skipped by the poller.
func (s *FeedStore) SetEnabled(userID, id int64, enabled bool) error {
	if err := s.q.SetFeedEnabled(context.Background(), sqlcgen.SetFeedEnabledParams{
		Enabled: enabled,
		ID:      id,
		UserID:  userID,
	}); err != nil {
		return err
	}
	return nil
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

// FeedWithOwner is a feed joined with its owner's username for admin views.
type FeedWithOwner struct {
	Feed
	Owner string
}

// ListAll returns every feed across all users, ordered by owner then title.
// Used by the admin CLI's `feed list`; item summaries and content are
// deliberately excluded so nothing potentially NSFW is rendered.
func (s *FeedStore) ListAll() ([]FeedWithOwner, error) {
	rows, err := s.q.ListAllFeeds(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]FeedWithOwner, 0, len(rows))
	for _, f := range rows {
		out = append(out, FeedWithOwner{
			Feed:  toFeed(feedFromUnreadRow(f.ID, f.UserID, f.AuthorID, f.Title, f.FeedUrl, f.HomeUrl, f.Description, f.Etag, f.LastModified, f.LastPolledAt, f.LastError, f.PollIntervalSec, f.Enabled, f.CreatedAt)),
			Owner: f.Owner,
		})
	}
	return out, nil
}

// Count returns the total number of feeds across all users.
func (s *FeedStore) Count() (int, error) {
	n, err := s.q.CountAllFeeds(context.Background())
	return int(n), err
}
