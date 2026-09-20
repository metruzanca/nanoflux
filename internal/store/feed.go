package store

import (
	"database/sql"
	"errors"
	"fmt"
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

type FeedStore struct{ db *sql.DB }

const feedCols = `id, user_id, author_id, title, feed_url, home_url, description,
	etag, last_modified, last_polled_at, poll_interval_sec, enabled, created_at`

func (s *FeedStore) Create(userID, authorID int64, title, feedURL, homeURL, description string, pollIntervalSec int) (Feed, error) {
	res, err := s.db.Exec(
		`INSERT INTO feeds(user_id, author_id, title, feed_url, home_url, description, poll_interval_sec)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		userID, nullInt64(authorID), title, feedURL, nullStr(homeURL), nullStr(description), pollIntervalSec,
	)
	if err != nil {
		return Feed{}, fmt.Errorf("create feed: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Feed{}, err
	}
	return s.ByID(userID, id)
}

func (s *FeedStore) ByID(userID, id int64) (Feed, error) {
	f, err := scanFeed(s.db.QueryRow(
		`SELECT `+feedCols+` FROM feeds WHERE id = ? AND user_id = ?`, id, userID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Feed{}, ErrNotFound
	}
	return f, err
}

// ByIDAny returns a feed by id regardless of user. Used by the poller and CLI.
func (s *FeedStore) ByIDAny(id int64) (Feed, error) {
	f, err := scanFeed(s.db.QueryRow(`SELECT `+feedCols+` FROM feeds WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Feed{}, ErrNotFound
	}
	return f, err
}

func (s *FeedStore) List(userID int64) ([]Feed, error) {
	rows, err := s.db.Query(
		`SELECT `+feedCols+` FROM feeds WHERE user_id = ? ORDER BY title`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Feed
	for rows.Next() {
		f, err := scanFeed(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *FeedStore) ListByAuthor(userID, authorID int64) ([]Feed, error) {
	rows, err := s.db.Query(
		`SELECT `+feedCols+` FROM feeds WHERE user_id = ? AND author_id = ? ORDER BY title`,
		userID, authorID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Feed
	for rows.Next() {
		f, err := scanFeed(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *FeedStore) Update(userID, id int64, authorID int64, title, feedURL, homeURL, description string, pollIntervalSec int, enabled bool) error {
	res, err := s.db.Exec(
		`UPDATE feeds SET author_id = ?, title = ?, feed_url = ?, home_url = ?, description = ?,
		 poll_interval_sec = ?, enabled = ? WHERE id = ? AND user_id = ?`,
		nullInt64(authorID), title, feedURL, nullStr(homeURL), nullStr(description),
		pollIntervalSec, boolInt(enabled), id, userID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *FeedStore) Delete(userID, id int64) error {
	res, err := s.db.Exec(`DELETE FROM feeds WHERE id = ? AND user_id = ?`, id, userID)
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
	_, err := s.db.Exec(
		`UPDATE feeds SET etag = ?, last_modified = ?, last_polled_at = ? WHERE id = ?`,
		nullStr(etag), nullStr(lastModified), lastPolledAt, id,
	)
	return err
}

// ListDue returns enabled feeds that have not been polled within their own
// poll_interval_sec of now.
func (s *FeedStore) ListDue(now string) ([]Feed, error) {
	rows, err := s.db.Query(
		`SELECT `+feedCols+` FROM feeds
		 WHERE enabled = 1 AND (last_polled_at IS NULL OR last_polled_at <= datetime(?, '-' || poll_interval_sec || ' seconds'))`,
		now,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Feed
	for rows.Next() {
		f, err := scanFeed(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func scanFeed(row scanner) (Feed, error) {
	var f Feed
	var homeURL, description, etag, lastModified, lastPolledAt sql.NullString
	var authorID sql.NullInt64
	var enabled int
	err := row.Scan(
		&f.ID, &f.UserID, &authorID, &f.Title, &f.FeedURL,
		&homeURL, &description, &etag, &lastModified, &lastPolledAt,
		&f.PollIntervalSec, &enabled, &f.CreatedAt,
	)
	f.HomeURL, f.Description, f.ETag, f.LastModified, f.LastPolledAt =
		homeURL.String, description.String, etag.String, lastModified.String, lastPolledAt.String
	f.AuthorID = authorID.Int64 // 0 = no author
	f.Enabled = enabled != 0
	return f, err
}

func nullInt64(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
