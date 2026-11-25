package store

import (
	"context"
	"strings"
)

// AuthorScope is one author page's count scope: the author's feeds plus any
// saved views assigned to them. It mirrors ItemFilter{AuthorID, Views}.
type AuthorScope struct {
	AuthorID      int64
	Views         []ViewFilter
	UnclaimedOnly bool
}

// UnreadCountsByAuthor returns unread item counts keyed by the author who
// selects the feed. It applies each author's rules and the aggregator claim
// subtraction, and covers view-bearing authors through
// UnreadCountsByAuthorScope in the caller.
func (s *ItemStore) UnreadCountsByAuthor(userID int64) (map[int64]int, error) {
	out := make(map[int64]int)
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT af.author_id, COUNT(DISTINCT i.id) AS unread
		FROM items i
		JOIN item_feeds mf ON mf.item_id = i.id
		JOIN author_feeds af ON af.feed_id = mf.feed_id
		JOIN feeds f ON f.id = i.feed_id
		WHERE i.user_id = ? AND i.read = 0 AND f.is_system = 0
		  AND `+authorRulesVisibleSQL(`i.id`, `af.author_id`)+`
		  AND (
		    NOT EXISTS (SELECT 1 FROM authors a WHERE a.id = af.author_id AND a.is_aggregator = 1)
		    OR NOT EXISTS (SELECT 1 FROM item_claims ic WHERE ic.item_id = i.id AND ic.author_id <> af.author_id)
		  )
		GROUP BY af.author_id`, userID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var authorID int64
		var n int
		if err := rows.Scan(&authorID, &n); err != nil {
			return out, err
		}
		out[authorID] = n
	}
	return out, rows.Err()
}

// UnreadCountsByFeedScope returns unread counts keyed by feed for one author
// page's scope: the author's selected feeds ORed with the assigned view
// predicates, with the author's always-on rules applied. It is the grouped form
// of CountFiltered(ItemFilter{AuthorID, Views, FeedID, UnreadOnly}), so a page
// with many feed cards does not run one count per feed.
func (s *ItemStore) UnreadCountsByFeedScope(userID, authorID int64, views []ViewFilter, unclaimedOnly bool) (map[int64]int, error) {
	out := make(map[int64]int)
	clause, clauseArgs := authorScopeClauseWith("i.id", "COALESCE(i.published_at, i.fetched_at)", authorID, views, unclaimedOnly)
	if clause == "" {
		return out, nil
	}
	var b strings.Builder
	b.WriteString(`SELECT mf.feed_id, COUNT(*) AS unread
		FROM items i JOIN feeds f ON f.id = i.feed_id
		JOIN item_feeds mf ON mf.item_id = i.id
		WHERE i.user_id = ? AND i.read = 0 AND f.is_system = 0 AND `)
	b.WriteString(clause)
	b.WriteString(` GROUP BY mf.feed_id`)
	args := append([]any{userID}, clauseArgs...)
	rows, err := s.db.QueryContext(context.Background(), b.String(), args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var feedID int64
		var n int
		if err := rows.Scan(&feedID, &n); err != nil {
			return out, err
		}
		out[feedID] = n
	}
	return out, rows.Err()
}

// UnreadCountsByAuthorScope returns unread counts for author pages that also
// carry assigned views. The author's own feeds and each view are ORed, matching
// CountFiltered with AuthorID+Views. All scopes run in one UNION ALL query, so
// the cost does not grow with the number of view-bearing authors.
func (s *ItemStore) UnreadCountsByAuthorScope(userID int64, scopes []AuthorScope) (map[int64]int, error) {
	out := make(map[int64]int, len(scopes))
	if len(scopes) == 0 {
		return out, nil
	}
	var b strings.Builder
	args := make([]any, 0, len(scopes)*4)
	for i, sc := range scopes {
		if i > 0 {
			b.WriteString(" UNION ALL ")
		}
		b.WriteString(`SELECT ? AS author_id, COUNT(*) AS unread
			FROM items i JOIN feeds f ON f.id = i.feed_id
			WHERE i.user_id = ? AND i.read = 0 AND `)
		args = append(args, sc.AuthorID, userID)
		clause, clauseArgs := authorScopeClauseWith("i.id", "COALESCE(i.published_at, i.fetched_at)", sc.AuthorID, sc.Views, sc.UnclaimedOnly)
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
		var authorID int64
		var n int
		if err := rows.Scan(&authorID, &n); err != nil {
			return out, err
		}
		out[authorID] = n
	}
	return out, rows.Err()
}
