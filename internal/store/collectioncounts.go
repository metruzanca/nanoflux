package store

import (
	"context"
	"strings"
)

// CollectionCounts is one collection's item totals: unread and read items in
// the collection's scope (its member authors' feeds plus their views).
type CollectionCounts struct {
	Unread int
	Read   int
}

// CountsByAuthor returns unread and read counts keyed by collection, over each
// collection's member authors' owned feeds only. It is one grouped scan for the
// whole collections index. It reads visible_items, so the author allow/block
// rules apply exactly as they do in the lists. View-bearing collections get
// exact scopes from CountsByScope on top.
func (s *CollectionStore) CountsByAuthor(userID int64) (map[int64]CollectionCounts, error) {
	out := make(map[int64]CollectionCounts)
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT ca.collection_id,
		       COUNT(DISTINCT CASE WHEN i.read = 0 THEN i.id END) AS unread,
		       COUNT(DISTINCT CASE WHEN i.read = 1 THEN i.id END) AS read
		FROM items i
		JOIN feeds f ON f.id = i.feed_id AND f.is_system = 0
		JOIN item_feeds mf ON mf.item_id = i.id
		JOIN author_feeds af ON af.feed_id = mf.feed_id
		JOIN collection_authors ca ON ca.author_id = af.author_id
		WHERE i.user_id = ? AND `+authorRulesVisibleSQL("i.id", "af.author_id")+`
		GROUP BY ca.collection_id`, userID)
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

// CollectionScope is one collection's count scope: its member authors' feeds
// plus the views of those authors. It mirrors
// ItemFilter{CollectionID, CollectionViews}.
type CollectionScope struct {
	CollectionID int64
	Views        []ViewFilter
}

// CountsByScope returns unread and read counts for collections that inherit
// saved views from their member authors. A view is not a feed scope, so each
// branch reads raw items and spells out the rule checks through
// collectionScopeClause. All scopes run in one UNION ALL query, so the cost does
// not grow into two CountFiltered calls per collection.
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
		clause, clauseArgs := collectionScopeClause("i.id", "COALESCE(i.published_at, i.fetched_at)", sc.CollectionID, sc.Views)
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
