package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
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
	CreatedAt     string
}

// AuthorWithCount joins an author with its number of feeds and unread items.
type AuthorWithCount struct {
	Author
	FeedCount   int
	UnreadCount int
}

type AuthorStore struct{ q *sqlcgen.Queries }

func (s *AuthorStore) Create(userID int64, name, avatarURL, description string) (Author, error) {
	a, err := s.q.CreateAuthor(context.Background(), sqlcgen.CreateAuthorParams{
		UserID:      userID,
		Name:        name,
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

// ByName returns the user's author matching name (case-insensitive), or
// ErrNotFound. Used by search qualifiers.
func (s *AuthorStore) ByName(userID int64, name string) (Author, error) {
	a, err := s.q.GetAuthorByName(context.Background(), sqlcgen.GetAuthorByNameParams{
		UserID: userID,
		Name:   name,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Author{}, ErrNotFound
	}
	if err != nil {
		return Author{}, err
	}
	return toAuthor(a), nil
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
				ID:            r.ID,
				UserID:        r.UserID,
				Name:          r.Name,
				AvatarUrl:     r.AvatarUrl,
				AvatarKey:     r.AvatarKey,
				LastFetchedAt: r.LastFetchedAt,
				Description:   r.Description,
				CreatedAt:     r.CreatedAt,
			}),
			FeedCount:   int(r.FeedCount),
			UnreadCount: int(r.UnreadCount),
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
		Name:        name,
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

// Delete removes an author and cascades to their feeds (and those feeds'
// items and collection links). The cached avatar object is not touched here —
// callers purge it via AvatarKey.
func (s *AuthorStore) Delete(userID, id int64) error {
	res, err := s.q.DeleteAuthor(context.Background(), sqlcgen.DeleteAuthorParams{ID: id, UserID: userID})
	if err != nil {
		return fmt.Errorf("delete author: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
