package store

import (
	"context"
	"strings"
)

// CollectionCounts is one collection's item totals: unread and read items in
// the collection's scope (its member feeds plus any member views).
type CollectionCounts struct {
	Unread int
	Read   int
}

// CountsByFeed returns unread and read counts keyed by collection, over each
// collection's member feeds only. It is one grouped scan for the whole
// collections index, replacing the two per-collection visible_items subqueries
// that ListCollectionsWithCounts runs. It reads visible_items, so the author
// allow/block rules apply exactly as they do in the lists.
func (s *CollectionStore) CountsByFeed(userID int64) (map[int64]CollectionCounts, error) {
	out := make(map[int64]CollectionCounts)
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT cf.collection_id,
		       COALESCE(SUM(CASE WHEN i.read = 0 THEN 1 ELSE 0 END), 0) AS unread,
		       COALESCE(SUM(CASE WHEN i.read = 1 THEN 1 ELSE 0 END), 0) AS read
		FROM visible_items i
		JOIN collection_feeds cf ON cf.feed_id = i.feed_id
		WHERE i.user_id = ?
		GROUP BY cf.collection_id`, userID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var collectionID int64
		var c CollectionCounts
		if err := rows.Scan(&collectionID, &c.Unread, &c.Read); err != nil {
			return out, err
		}
		out[collectionID] = c
	}
	return out, rows.Err()
}

// CollectionScope is one collection's count scope: its member feeds plus its
// member saved views. It mirrors ItemFilter{CollectionID, CollectionViews}.
type CollectionScope struct {
	CollectionID int64
	Views        []ViewFilter
}

// CountsByScope returns unread and read counts for collections that carry saved
// views. A view is not a feed scope, so each branch reads raw items and spells
// out the rule checks through collectionScopeClause. All scopes run in one
// UNION ALL query, so the cost does not grow into two CountFiltered calls per
// collection.
func (s *CollectionStore) CountsByScope(userID int64, scopes []CollectionScope) (map[int64]CollectionCounts, error) {
	out := make(map[int64]CollectionCounts, len(scopes))
	if len(scopes) == 0 {
		return out, nil
	}
	var b strings.Builder
	args := make([]any, 0, len(scopes)*4)
	for i, sc := range scopes {
		if i > 0 {
			b.WriteString(" UNION ALL ")
		}
		b.WriteString(`SELECT ? AS collection_id,
			COALESCE(SUM(CASE WHEN i.read = 0 THEN 1 ELSE 0 END), 0) AS unread,
			COALESCE(SUM(CASE WHEN i.read = 1 THEN 1 ELSE 0 END), 0) AS read
			FROM items i JOIN feeds f ON f.id = i.feed_id
			WHERE i.user_id = ? AND `)
		args = append(args, sc.CollectionID, userID)
		clause, clauseArgs := collectionScopeClause("i.id", "COALESCE(i.published_at, i.fetched_at)", sc.CollectionID, sc.Views, true)
		if clause == "" {
			b.WriteString("1=0")
		} else {
			b.WriteString(clause)
		}
		args = append(args, clauseArgs...)
	}
	rows, err := s.db.QueryContext(context.Background(), b.String(), args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var collectionID int64
		var c CollectionCounts
		if err := rows.Scan(&collectionID, &c.Unread, &c.Read); err != nil {
			return out, err
		}
		out[collectionID] = c
	}
	return out, rows.Err()
}
