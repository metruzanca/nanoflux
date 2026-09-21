package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

// UrlMapping is a user-defined "url pattern -> feed url" rule. The pattern is
// a Go regex with named groups; the template references captures with
// {name}. Mappings are only applied when adding a feed — existing feeds keep
// the feed url they were created with.
type UrlMapping struct {
	ID        int64
	UserID    int64
	Pattern   string
	Template  string
	CreatedAt string
}

type UrlMappingStore struct{ q *sqlcgen.Queries }

// List returns the user's mappings, oldest first (first match wins when
// applying).
func (s *UrlMappingStore) List(userID int64) ([]UrlMapping, error) {
	rows, err := s.q.ListUrlMappings(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]UrlMapping, 0, len(rows))
	for _, r := range rows {
		out = append(out, toUrlMapping(r.ID, r.UserID, r.Pattern, r.Template, r.CreatedAt))
	}
	return out, nil
}

// ByID returns a user's mapping by id, scoped to the user.
func (s *UrlMappingStore) ByID(userID, id int64) (UrlMapping, error) {
	m, err := s.q.GetUrlMapping(context.Background(), sqlcgen.GetUrlMappingParams{ID: id, UserID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		return UrlMapping{}, ErrNotFound
	}
	if err != nil {
		return UrlMapping{}, err
	}
	return toUrlMapping(m.ID, m.UserID, m.Pattern, m.Template, m.CreatedAt), nil
}

// Create registers a mapping. It returns ErrExists when the user already has
// a mapping with the same pattern.
func (s *UrlMappingStore) Create(userID int64, pattern, template string) (UrlMapping, error) {
	m, err := s.q.CreateUrlMapping(context.Background(), sqlcgen.CreateUrlMappingParams{
		UserID:   userID,
		Pattern:  pattern,
		Template: template,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return UrlMapping{}, ErrExists
		}
		return UrlMapping{}, fmt.Errorf("create url mapping: %w", err)
	}
	return toUrlMapping(m.ID, m.UserID, m.Pattern, m.Template, m.CreatedAt), nil
}

// Update edits a mapping's pattern/template. It returns ErrExists when the
// new pattern collides with another of the user's mappings.
func (s *UrlMappingStore) Update(userID, id int64, pattern, template string) error {
	err := s.q.UpdateUrlMapping(context.Background(), sqlcgen.UpdateUrlMappingParams{
		Pattern:  pattern,
		Template: template,
		ID:       id,
		UserID:   userID,
	})
	if err != nil && isUniqueViolation(err) {
		return ErrExists
	}
	return err
}

// Delete removes a user's mapping.
func (s *UrlMappingStore) Delete(userID, id int64) error {
	return s.q.DeleteUrlMapping(context.Background(), sqlcgen.DeleteUrlMappingParams{ID: id, UserID: userID})
}
