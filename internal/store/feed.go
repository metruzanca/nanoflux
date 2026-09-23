package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

type Feed struct {
	ID               int64
	UserID           int64
	AuthorID         int64
	Title            string
	FeedURL          string
	HomeURL          string
	Description      string
	ETag             string
	LastModified     string
	LastPolledAt     string
	LastError        string
	NextPageURL      string
	Kind             string // "feed" (RSS/Atom) or "scrape" (CSS-selector scraper)
	ScrapeConfig     string // JSON selector config for kind == "scrape", "" otherwise
	PollIntervalSec  int
	PollIntervalAuto bool   // derive poll_interval_sec from the posting cadence
	LastItemAt       string // newest item time (published or fetched); "" when none
	NextPollAt       string // "do not poll before" deadline after a rate limit; "" when unset
	Enabled          bool
	CreatedAt        string
}

// ScrapeKind marks feeds built by scraping a page rather than parsing a feed.
const ScrapeKind = "scrape"

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

// CreateScrape creates a feed whose items come from CSS-selector scraping of
// scrapeURL instead of a parsed feed. scrapeConfig holds the selector JSON.
func (s *FeedStore) CreateScrape(userID, authorID int64, title, scrapeURL, homeURL, description, scrapeConfig string, pollIntervalSec int) (Feed, error) {
	f, err := s.q.CreateScrapeFeed(context.Background(), sqlcgen.CreateScrapeFeedParams{
		UserID:          userID,
		AuthorID:        authorID,
		Title:           title,
		FeedUrl:         scrapeURL,
		HomeUrl:         ns(homeURL),
		Description:     ns(description),
		ScrapeConfig:    ns(scrapeConfig),
		PollIntervalSec: int64(pollIntervalSec),
	})
	if err != nil {
		return Feed{}, fmt.Errorf("create scrape feed: %w", err)
	}
	return toFeed(f), nil
}

// UpdateScrape updates a scrape-kind feed's fields, keeping kind set to
// 'scrape' and refreshing its selector config.
func (s *FeedStore) UpdateScrape(userID, id int64, authorID int64, title, scrapeURL, homeURL, description, scrapeConfig string, pollIntervalSec int, pollIntervalAuto bool, enabled bool) error {
	res, err := s.q.UpdateScrapeFeed(context.Background(), sqlcgen.UpdateScrapeFeedParams{
		AuthorID:         authorID,
		Title:            title,
		FeedUrl:          scrapeURL,
		HomeUrl:          ns(homeURL),
		Description:      ns(description),
		ScrapeConfig:     ns(scrapeConfig),
		PollIntervalSec:  int64(pollIntervalSec),
		PollIntervalAuto: boolInt(pollIntervalAuto),
		Enabled:          enabled,
		ID:               id,
		UserID:           userID,
	})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
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

func (s *FeedStore) Update(userID, id int64, authorID int64, title, feedURL, homeURL, description string, pollIntervalSec int, pollIntervalAuto bool, enabled bool) error {
	res, err := s.q.UpdateFeed(context.Background(), sqlcgen.UpdateFeedParams{
		AuthorID:         authorID,
		Title:            title,
		FeedUrl:          feedURL,
		HomeUrl:          ns(homeURL),
		Description:      ns(description),
		PollIntervalSec:  int64(pollIntervalSec),
		PollIntervalAuto: boolInt(pollIntervalAuto),
		Enabled:          enabled,
		ID:               id,
		UserID:           userID,
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

// SetNextPageURL records the feed's next pagination page, or "" when the feed
// is not paginated / its history is exhausted. The "load older items" feature
// reads it to fetch older entries on demand. Not user-scoped: the poller owns
// writing it, the poller and the feed page read it.
func (s *FeedStore) SetNextPageURL(id int64, url string) error {
	return s.q.SetFeedNextPageURL(context.Background(), sqlcgen.SetFeedNextPageURLParams{
		NextPageUrl: url,
		ID:          id,
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

// SetPollInterval records a feed's poll_interval_sec. Not user-scoped: the
// poller owns it (adaptive + stale backoff).
func (s *FeedStore) SetPollInterval(id int64, sec int) error {
	return s.q.SetFeedPollInterval(context.Background(), sqlcgen.SetFeedPollIntervalParams{
		PollIntervalSec: int64(sec),
		ID:              id,
	})
}

// SetLastItemAt records a feed's newest item time. The caller guarantees the
// value is newer than what is stored, so last_item_at only ever moves forward.
func (s *FeedStore) SetLastItemAt(id int64, t string) error {
	return s.q.SetFeedLastItemAt(context.Background(), sqlcgen.SetFeedLastItemAtParams{
		LastItemAt: ns(t),
		ID:         id,
	})
}

// SetNextPollAt sets a "do not poll before" deadline on a feed, used to back off
// when a host rate-limits it. An empty t clears the deadline (poll normally).
func (s *FeedStore) SetNextPollAt(id int64, t string) error {
	return s.q.SetFeedNextPollAt(context.Background(), sqlcgen.SetFeedNextPollAtParams{
		NextPollAt: ns(t),
		ID:         id,
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
			Feed:  toFeed(feedFromUnreadRow(f.ID, f.UserID, f.AuthorID, f.Title, f.FeedUrl, f.HomeUrl, f.Description, f.Etag, f.LastModified, f.LastPolledAt, f.LastError, f.NextPageUrl, f.Kind, f.ScrapeConfig, f.PollIntervalSec, f.PollIntervalAuto, f.LastItemAt, f.NextPollAt, f.Enabled, f.CreatedAt)),
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
