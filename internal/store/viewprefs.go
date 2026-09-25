package store

import (
	"context"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

// ViewMode is a per-scope item-list layout.
const (
	ViewModeList = "list"
	ViewModeGrid = "grid"
)

// NormalizeViewMode maps any stored/submitted value onto a known mode,
// defaulting to the list layout.
func NormalizeViewMode(mode string) string {
	if mode == ViewModeGrid {
		return ViewModeGrid
	}
	return ViewModeList
}

// ViewPrefStore stores each user's display preference (list or grid) per page
// scope, so the choice follows the account rather than the browser.
type ViewPrefStore struct{ q *sqlcgen.Queries }

// Modes returns the user's saved mode for every scope, keyed by scope. A scope
// with no saved row is absent; callers default it to list.
func (s *ViewPrefStore) Modes(userID int64) (map[string]string, error) {
	rows, err := s.q.ListViewPrefs(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Scope] = NormalizeViewMode(r.Mode)
	}
	return out, nil
}

// Mode returns the user's saved display mode for one scope, defaulting to list
// when none is stored.
func (s *ViewPrefStore) Mode(userID int64, scope string) string {
	mode, err := s.q.GetViewPref(context.Background(), sqlcgen.GetViewPrefParams{
		UserID: userID,
		Scope:  scope,
	})
	if err != nil {
		return ViewModeList
	}
	return NormalizeViewMode(mode)
}

// Set records the user's display mode for one scope.
func (s *ViewPrefStore) Set(userID int64, scope, mode string) error {
	return s.q.UpsertViewPref(context.Background(), sqlcgen.UpsertViewPrefParams{
		UserID: userID,
		Scope:  scope,
		Mode:   NormalizeViewMode(mode),
	})
}
