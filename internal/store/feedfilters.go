package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/metruzanca/nanoflux/internal/feedfilter"
	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

// FeedFilter is one per-feed, view-level content-filter rule. Rules never
// delete or alter an item; they only decide whether it is shown (see
// item_feeds.filtered).
type FeedFilter struct {
	ID        int64
	UserID    int64
	FeedID    int64
	Field     string
	Op        string
	Value     string
	CreatedAt string
}

// Rule converts a stored rule to the evaluator's shape.
func (f FeedFilter) Rule() feedfilter.Rule {
	return feedfilter.Rule{Field: f.Field, Op: f.Op, Value: f.Value}
}

func toFeedFilter(f sqlcgen.FeedFilter) FeedFilter {
	return FeedFilter{
		ID: f.ID, UserID: f.UserID, FeedID: f.FeedID,
		Field: f.Field, Op: f.Op, Value: f.Value, CreatedAt: f.CreatedAt,
	}
}

// FeedFilterStore owns a feed's content-filter rules.
type FeedFilterStore struct{ q *sqlcgen.Queries }

// List returns a feed's rules, oldest first.
func (s *FeedFilterStore) List(userID, feedID int64) ([]FeedFilter, error) {
	rows, err := s.q.ListFeedFilters(context.Background(), feedID)
	if err != nil {
		return nil, err
	}
	out := make([]FeedFilter, 0, len(rows))
	for _, r := range rows {
		if r.UserID != userID {
			continue
		}
		out = append(out, toFeedFilter(r))
	}
	return out, nil
}

// Create registers a new rule for a feed.
func (s *FeedFilterStore) Create(userID, feedID int64, field, op, value string) (FeedFilter, error) {
	f, err := s.q.CreateFeedFilter(context.Background(), sqlcgen.CreateFeedFilterParams{
		UserID: userID, FeedID: feedID, Field: field, Op: op, Value: value,
	})
	if err != nil {
		return FeedFilter{}, fmt.Errorf("create feed filter: %w", err)
	}
	return toFeedFilter(f), nil
}

// Delete removes one of a user's rules.
func (s *FeedFilterStore) Delete(userID, id int64) error {
	res, err := s.q.DeleteFeedFilter(context.Background(), sqlcgen.DeleteFeedFilterParams{ID: id, UserID: userID})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Rules returns a feed's rules in the evaluator's shape.
func (s *FeedFilterStore) Rules(userID, feedID int64) ([]feedfilter.Rule, error) {
	rules, err := s.List(userID, feedID)
	if err != nil {
		return nil, err
	}
	out := make([]feedfilter.Rule, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.Rule())
	}
	return out, nil
}

// SetFilterConfig sets a feed's filter mode ('off'/'block'/'allow') and combine
// ('any'/'all'), normalizing both. It does not recompute: the caller follows up
// with ItemStore.RecomputeFeedFilter.
func (s *FeedStore) SetFilterConfig(userID, feedID int64, mode, combine string) error {
	res, err := s.q.SetFeedFilterConfig(context.Background(), sqlcgen.SetFeedFilterConfigParams{
		FilterMode:    feedfilter.NormalizeMode(mode),
		FilterCombine: feedfilter.NormalizeCombine(combine),
		ID:            feedID,
		UserID:        userID,
	})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// FeedFilterItem is one item a feed is a member of, with that feed's filter
// verdict on it.
type FeedFilterItem struct {
	Item     ItemWithFeed
	Filtered bool
}

// ListFeedItemsForFilter returns a feed's member items with their filter
// verdict, newest first, for the filter-preview view.
func (s *ItemStore) ListFeedItemsForFilter(userID, feedID int64) ([]FeedFilterItem, error) {
	rows, err := s.q.ListFeedItemsForFilter(context.Background(), sqlcgen.ListFeedItemsForFilterParams{
		FeedID: feedID,
		UserID: userID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]FeedFilterItem, 0, len(rows))
	items := make([]ItemWithFeed, 0, len(rows))
	for _, r := range rows {
		item := toItemWithFeed(r.ID, r.FeedID, r.Guid, r.Title, r.Link, r.Summary, r.Categories,
			r.ImageUrl, r.ImageCacheKey, r.DurationSec, r.PublishedAt, r.FetchedAt, r.Read, r.Favorite, r.Bookmark, r.ReadAt,
			r.FeedTitle, r.FeedUrl, r.FeedHomeUrl, r.FeedIsSystem, r.AuthorID, r.AuthorName)
		out = append(out, FeedFilterItem{Item: item, Filtered: r.Filtered != 0})
		items = append(items, item)
	}
	if err := s.attachSources(userID, items); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Item = items[i]
	}
	return out, nil
}

// SetItemFeedFiltered records whether one feed's filter hides an item it is a
// member of. The verdict is per membership.
func (s *ItemStore) SetItemFeedFiltered(itemID, feedID int64, filtered bool) error {
	return s.q.SetItemFeedFiltered(context.Background(), sqlcgen.SetItemFeedFilteredParams{
		Filtered: boolInt(filtered),
		ItemID:   itemID,
		FeedID:   feedID,
	})
}

// RecomputeFeedFilter re-evaluates a feed's rules over every item it is a
// member of and updates item_feeds.filtered. It runs after a rule/mode/combine
// change. With no rules (or mode off) every membership is cleared, so relaxing
// or removing filters re-shows items.
func (s *ItemStore) RecomputeFeedFilter(userID, feedID int64) error {
	feed, err := s.q.GetFeed(context.Background(), sqlcgen.GetFeedParams{ID: feedID, UserID: userID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	mode := feedfilter.NormalizeMode(feed.FilterMode)
	combine := feedfilter.NormalizeCombine(feed.FilterCombine)

	var rules []feedfilter.Rule
	if mode != feedfilter.ModeOff {
		rows, err := s.q.ListFeedFilters(context.Background(), feedID)
		if err != nil {
			return err
		}
		for _, r := range rows {
			rules = append(rules, feedfilter.Rule{Field: r.Field, Op: r.Op, Value: r.Value})
		}
	}

	items, err := s.q.ListFeedItemsForFilter(context.Background(), sqlcgen.ListFeedItemsForFilterParams{
		FeedID: feedID,
		UserID: userID,
	})
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	for _, it := range items {
		allowed := feedfilter.Allowed(mode, combine, rules, feedfilter.Fields{
			Title:      it.Title,
			Link:       it.Link,
			Summary:    it.Summary,
			Categories: splitCategories(it.Categories),
		})
		if err := qtx.SetItemFeedFiltered(context.Background(), sqlcgen.SetItemFeedFilteredParams{
			Filtered: boolInt(!allowed),
			ItemID:   it.ID,
			FeedID:   feedID,
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}
