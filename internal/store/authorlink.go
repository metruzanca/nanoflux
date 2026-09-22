package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

// AuthorLink is a plain external bookmark attached to an author (e.g. a Twitch
// or Discord page). Unlike a Feed it is never polled and holds no items.
type AuthorLink struct {
	ID        int64
	UserID    int64
	AuthorID  int64
	Label     string
	URL       string
	CreatedAt string
}

type AuthorLinkStore struct{ q *sqlcgen.Queries }

func (s *AuthorLinkStore) Create(userID, authorID int64, label, url string) (AuthorLink, error) {
	l, err := s.q.CreateAuthorLink(context.Background(), sqlcgen.CreateAuthorLinkParams{
		UserID:   userID,
		AuthorID: authorID,
		Label:    ns(label),
		Url:      url,
	})
	if err != nil {
		return AuthorLink{}, fmt.Errorf("create author link: %w", err)
	}
	return toAuthorLink(l), nil
}

func (s *AuthorLinkStore) ByID(userID, id int64) (AuthorLink, error) {
	l, err := s.q.GetAuthorLink(context.Background(), sqlcgen.GetAuthorLinkParams{ID: id, UserID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorLink{}, ErrNotFound
	}
	if err != nil {
		return AuthorLink{}, err
	}
	return toAuthorLink(l), nil
}

func (s *AuthorLinkStore) ListByAuthor(userID, authorID int64) ([]AuthorLink, error) {
	rows, err := s.q.ListAuthorLinks(context.Background(), sqlcgen.ListAuthorLinksParams{
		UserID:   userID,
		AuthorID: authorID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]AuthorLink, 0, len(rows))
	for _, r := range rows {
		out = append(out, toAuthorLink(r))
	}
	return out, nil
}

// Update changes an existing link's label and url (the author is unchanged).
func (s *AuthorLinkStore) Update(userID, id int64, label, url string) error {
	res, err := s.q.UpdateAuthorLink(context.Background(), sqlcgen.UpdateAuthorLinkParams{
		Label:  ns(label),
		Url:    url,
		ID:     id,
		UserID: userID,
	})
	if err != nil {
		return fmt.Errorf("update author link: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *AuthorLinkStore) Delete(userID, id int64) error {
	res, err := s.q.DeleteAuthorLink(context.Background(), sqlcgen.DeleteAuthorLinkParams{
		ID:     id,
		UserID: userID,
	})
	if err != nil {
		return fmt.Errorf("delete author link: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
