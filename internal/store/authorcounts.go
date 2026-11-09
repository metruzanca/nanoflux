package store

import (
	"context"
	"strings"
)

// AuthorScope is one author page's count scope: the author's own feeds plus any
// saved views assigned to them. It mirrors ItemFilter{AuthorID, Views}.
type AuthorScope struct {
	AuthorID int64
	Views    []ViewFilter
}

// UnreadCountsByAuthor returns unread item counts keyed by the owner feed's
// author, using visible_items so each author's always-on rules apply. It is one
// grouped query for the whole author list instead of a count per author.
func (s *ItemStore) UnreadCountsByAuthor(userID int64) (map[int64]int, error) {
	out := make(map[int64]int)
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT f.author_id, COUNT(*) AS unread
		FROM visible_items i
		JOIN feeds f ON f.id = i.feed_id
		WHERE i.user_id = ? AND i.read = 0 AND f.is_system = 0
		GROUP BY f.author_id`, userID)
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
		clause, clauseArgs := authorScopeClause("i.id", "COALESCE(i.published_at, i.fetched_at)", sc.AuthorID, sc.Views)
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
