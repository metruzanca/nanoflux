package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

// Filter is an ingest-time rule: when a new item's selected field matches
// pattern, the action applies. FeedID 0 means the rule applies to every feed.
type Filter struct {
	ID        int64
	UserID    int64
	FeedID    int64 // 0 = all feeds
	Action    string
	Field     string
	Pattern   string
	IsRegex   bool
	CreatedAt string
}

type FilterStore struct{ q *sqlcgen.Queries }

func toFilter(f sqlcgen.Filter) Filter {
	return Filter{
		ID:        f.ID,
		UserID:    f.UserID,
		FeedID:    f.FeedID.Int64,
		Action:    f.Action,
		Field:     f.Field,
		Pattern:   f.Pattern,
		IsRegex:   f.IsRegex != 0,
		CreatedAt: f.CreatedAt,
	}
}

// List returns all of a user's filter rules, newest first.
func (s *FilterStore) List(userID int64) ([]Filter, error) {
	rows, err := s.q.ListFilters(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]Filter, 0, len(rows))
	for _, r := range rows {
		out = append(out, toFilter(r))
	}
	return out, nil
}

// ListByFeed returns the rules that apply to a feed: its own rules plus any
// feed-wide rules.
func (s *FilterStore) ListByFeed(userID, feedID int64) ([]Filter, error) {
	rows, err := s.q.ListFiltersByFeed(context.Background(), sqlcgen.ListFiltersByFeedParams{
		UserID: userID,
		FeedID: ni(feedID),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Filter, 0, len(rows))
	for _, r := range rows {
		out = append(out, toFilter(r))
	}
	return out, nil
}

// Create registers a new filter rule.
func (s *FilterStore) Create(userID, feedID int64, action, field, pattern string, isRegex bool) (Filter, error) {
	f, err := s.q.CreateFilter(context.Background(), sqlcgen.CreateFilterParams{
		UserID:  userID,
		FeedID:  ni(feedID),
		Action:  action,
		Field:   field,
		Pattern: pattern,
		IsRegex: boolInt(isRegex),
	})
	if err != nil {
		return Filter{}, fmt.Errorf("create filter: %w", err)
	}
	return toFilter(f), nil
}

// ByID returns a user's filter rule by id.
func (s *FilterStore) ByID(userID, id int64) (Filter, error) {
	f, err := s.q.GetFilter(context.Background(), sqlcgen.GetFilterParams{ID: id, UserID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		return Filter{}, ErrNotFound
	}
	if err != nil {
		return Filter{}, err
	}
	return toFilter(f), nil
}

// Delete removes a user's filter rule.
func (s *FilterStore) Delete(userID, id int64) error {
	res, err := s.q.DeleteFilter(context.Background(), sqlcgen.DeleteFilterParams{ID: id, UserID: userID})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}