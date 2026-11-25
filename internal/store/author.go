package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
	"github.com/metruzanca/nanoflux/internal/tagnorm"
)

type Author struct {
	ID            int64
	UserID        int64
	Name          string
	AvatarURL     string
	AvatarKey     string
	LastFetchedAt string
	Description   string
	IsSystem      bool // hidden author owning the system feed; never listed
	// AutoReadAfterDays overrides the account's auto-read window for this
	// author's feeds. nil inherits users.auto_read_after_days; 0 disables
	// auto-read for this author.
	AutoReadAfterDays *int
	// AutoDeleteAfterDays overrides the account's delete window for this
	// author's feeds. nil inherits users.auto_delete_after_days; 0 disables
	// deletion for this author.
	AutoDeleteAfterDays *int
	// IsAggregator marks a feed aggregator: it does not claim its items and
	// hides items claimed by other authors.
	IsAggregator bool
	CreatedAt    string
}

// AuthorWithCount joins an author with its number of feeds and the taste
// signal the author index's magic sort needs: the highest manual rank across
// the author's feeds and the total favorites their feeds own. Unread counts are
// fetched separately for the whole page, so this stays one row per author.
type AuthorWithCount struct {
	Author
	FeedCount int
	MaxRank   int
	FavCount  int
}

type AuthorStore struct {
	q  *sqlcgen.Queries
	db *sql.DB // raw handle for transactional rule cleanup
}

func (s *AuthorStore) Create(userID int64, name, avatarURL, description string) (Author, error) {
	a, err := s.q.CreateAuthor(context.Background(), sqlcgen.CreateAuthorParams{
		UserID:      userID,
		Name:        tagnorm.Label(name),
		AvatarUrl:   ns(avatarURL),
		Description: ns(description),
	})
	if err != nil {
		return Author{}, fmt.Errorf("create author: %w", err)
	}
	return toAuthor(a), nil
}

// IsSystemAuthor reports whether id is the user's hidden system author (the
// owner of the saved-pages feed). Callers use it to redirect instead of
// rendering a normal author page.
func (s *AuthorStore) IsSystemAuthor(userID, id int64) bool {
	a, err := s.q.GetAuthor(context.Background(), sqlcgen.GetAuthorParams{ID: id, UserID: userID})
	return err == nil && a.IsSystem != 0
}

// ByID returns a user's author by id. The hidden system author that owns the
// saved-pages feed is excluded (ErrNotFound): it has no author page and is
// reached only through EnsureSystemAuthor.
func (s *AuthorStore) ByID(userID, id int64) (Author, error) {
	a, err := s.q.GetAuthor(context.Background(), sqlcgen.GetAuthorParams{ID: id, UserID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		return Author{}, ErrNotFound
	}
	if err != nil {
		return Author{}, err
	}
	if a.IsSystem != 0 {
		return Author{}, ErrNotFound
	}
	return toAuthor(a), nil
}

func (s *AuthorStore) List(userID int64) ([]Author, error) {
	rows, err := s.q.ListAuthors(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]Author, 0, len(rows))
	for _, a := range rows {
		out = append(out, toAuthor(a))
	}
	return out, nil
}

// ListWithFeedCount returns the user's authors with their feed and unread item
// counts in one query.
func (s *AuthorStore) ListWithFeedCount(userID int64) ([]AuthorWithCount, error) {
	rows, err := s.q.ListAuthorsWithFeedCount(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]AuthorWithCount, 0, len(rows))
	for _, r := range rows {
		out = append(out, AuthorWithCount{
			Author: toAuthor(sqlcgen.Author{
				ID:                  r.ID,
				UserID:              r.UserID,
				Name:                r.Name,
				AvatarUrl:           r.AvatarUrl,
				AvatarKey:           r.AvatarKey,
				LastFetchedAt:       r.LastFetchedAt,
				Description:         r.Description,
				IsSystem:            0,
				AutoReadAfterDays:   r.AutoReadAfterDays,
				AutoDeleteAfterDays: r.AutoDeleteAfterDays,
				CreatedAt:           r.CreatedAt,
			}),
			FeedCount: int(r.FeedCount),
			MaxRank:   intFromSQL(r.MaxRank),
			FavCount:  int(r.FavCount),
		})
	}
	return out, nil
}

// Count returns the total number of authors across all users.
func (s *AuthorStore) Count() (int, error) {
	n, err := s.q.CountAllAuthors(context.Background())
	return int(n), err
}

func (s *AuthorStore) Update(userID, id int64, name, avatarURL, description string) error {
	res, err := s.q.UpdateAuthor(context.Background(), sqlcgen.UpdateAuthorParams{
		Name:        tagnorm.Label(name),
		AvatarUrl:   ns(avatarURL),
		Description: ns(description),
		ID:          id,
		UserID:      userID,
	})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAvatarKey records the object-storage key of the author's cached avatar
// and when it was fetched. The key is deterministic (one object per author),
// so re-fetching overwrites in place and never orphans a file.
func (s *AuthorStore) SetAvatarKey(userID, id int64, key, fetchedAt string) error {
	res, err := s.q.SetAuthorAvatarKey(context.Background(), sqlcgen.SetAuthorAvatarKeyParams{
		AvatarKey:     ns(key),
		LastFetchedAt: ns(fetchedAt),
		ID:            id,
		UserID:        userID,
	})
	if err != nil {
		return fmt.Errorf("set author avatar key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearAvatarKey drops the object-storage key so a cached avatar is no longer
// served.
func (s *AuthorStore) ClearAvatarKey(userID, id int64) error {
	return s.SetAvatarKey(userID, id, "", "")
}

// SetAutoReadAfterDays stores the author's auto-read override. nil inherits the
// account window, 0 disables auto-read for this author, and a positive value is
// its own retention window.
func (s *AuthorStore) SetAutoReadAfterDays(userID, id int64, days *int) error {
	var value sql.NullInt64
	if days != nil {
		value = sql.NullInt64{Int64: int64(*days), Valid: true}
	}
	res, err := s.q.SetAuthorAutoReadAfterDays(context.Background(), sqlcgen.SetAuthorAutoReadAfterDaysParams{
		AutoReadAfterDays: value,
		ID:                id,
		UserID:            userID,
	})
	if err != nil {
		return fmt.Errorf("set author auto-read: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAutoDeleteAfterDays stores the author's delete-window override. nil
// inherits the account window, 0 disables deletion for this author, and a
// positive value is its own retention window.
func (s *AuthorStore) SetAutoDeleteAfterDays(userID, id int64, days *int) error {
	var value sql.NullInt64
	if days != nil {
		value = sql.NullInt64{Int64: int64(*days), Valid: true}
	}
	res, err := s.q.SetAuthorAutoDeleteAfterDays(context.Background(), sqlcgen.SetAuthorAutoDeleteAfterDaysParams{
		AutoDeleteAfterDays: value,
		ID:                  id,
		UserID:              userID,
	})
	if err != nil {
		return fmt.Errorf("set author auto-delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAggregator marks an author as a feed aggregator. An aggregator does not
// claim its items and hides items claimed by other authors. The caller rebuilds
// or clears the author's claims after the toggle.
func (s *AuthorStore) SetAggregator(userID, authorID int64, aggregator bool) error {
	res, err := s.q.SetAuthorAggregator(context.Background(), sqlcgen.SetAuthorAggregatorParams{
		IsAggregator: aggregator,
		ID:           authorID,
		UserID:       userID,
	})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes an author and cascades to their feeds (and those feeds'
// items and collection links). The cached avatar object is not touched here —
// callers purge it via AvatarKey. Author-filter rules owned by the deleted
// author cascade away; any other author's author-kind rule that named it drops
// that value, and a rule left empty is removed.
func (s *AuthorStore) Delete(userID, id int64) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin author delete: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM author_filter_values
		WHERE value = CAST(? AS TEXT)
		  AND rule_id IN (SELECT r.id FROM author_filter_rules r
		                    JOIN authors a ON a.id = r.author_id
		                   WHERE a.user_id = ? AND r.kind = 'author')`,
		id, userID); err != nil {
		return fmt.Errorf("remove author from author filters: %w", err)
	}
	// Feeds are library rows now: deleting an author removes only its
	// memberships (the author_feeds cascade). The feeds keep polling and stay on
	// /feeds.
	res, err := s.q.WithTx(tx).DeleteAuthor(ctx, sqlcgen.DeleteAuthorParams{ID: id, UserID: userID})
	if err != nil {
		return fmt.Errorf("delete author: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM author_filter_rules
		WHERE id NOT IN (SELECT rule_id FROM author_filter_values)`); err != nil {
		return fmt.Errorf("delete empty author filters: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit author delete: %w", err)
	}
	return nil
}
