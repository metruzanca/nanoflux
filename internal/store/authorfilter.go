package store

import (
	"context"
	"database/sql"
	"fmt"
)

// AuthorFilterStore persists per-author always-on filter rules. The rows use the
// same FilterRule shape as transient list filters; visible_items applies them on
// every item read.
type AuthorFilterStore struct{ db *sql.DB }

// MaxAuthorFilterRules caps a single author's persistent rule rows so a crafted
// form cannot build an unbounded read-side predicate.
const MaxAuthorFilterRules = 50

// List returns an author's rules in insertion order with their values. It is
// scoped to the owner so a foreign author id yields no rows.
func (s *AuthorFilterStore) List(userID, authorID int64) ([]FilterRule, error) {
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT r.id, r.kind, r.action, r.combine, v.value
		FROM author_filter_rules r
		JOIN authors a ON a.id = r.author_id
		LEFT JOIN author_filter_values v ON v.rule_id = r.id
		WHERE r.author_id = ? AND a.user_id = ?
		ORDER BY r.id, v.value`, authorID, userID)
	if err != nil {
		return nil, fmt.Errorf("list author filters: %w", err)
	}
	defer rows.Close()
	var (
		out  []FilterRule
		last int64
	)
	for rows.Next() {
		var (
			id      int64
			kind    string
			action  string
			combine string
			value   sql.NullString
		)
		if err := rows.Scan(&id, &kind, &action, &combine, &value); err != nil {
			return nil, fmt.Errorf("scan author filter: %w", err)
		}
		if len(out) == 0 || id != last {
			out = append(out, FilterRule{Kind: kind, Action: action, Combine: combine})
			last = id
		}
		if value.Valid && value.String != "" {
			out[len(out)-1].Values = append(out[len(out)-1].Values, value.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list author filters: %w", err)
	}
	return out, nil
}

// Replace atomically replaces every rule for an author's owned feeds. Rules
// whose author id does not belong to userID are rejected, and invalid/empty
// rules are dropped.
func (s *AuthorFilterStore) Replace(userID, authorID int64, rules []FilterRule) error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin author filters: %w", err)
	}
	defer tx.Rollback()
	ctx := context.Background()

	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM authors WHERE id = ? AND user_id = ? AND is_system = 0`,
		authorID, userID).Scan(&exists); err != nil {
		return fmt.Errorf("check author: %w", err)
	}
	if exists == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM author_filter_rules WHERE author_id = ?`, authorID); err != nil {
		return fmt.Errorf("clear author filters: %w", err)
	}
	rules = NormalizeFilterRules(rules)
	if len(rules) > MaxAuthorFilterRules {
		rules = rules[:MaxAuthorFilterRules]
	}
	for _, r := range rules {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO author_filter_rules (author_id, action, kind, combine)
			VALUES (?, ?, ?, ?)`, authorID, r.Action, r.Kind, r.Combine)
		if err != nil {
			return fmt.Errorf("insert author filter: %w", err)
		}
		ruleID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("author filter id: %w", err)
		}
		for _, value := range r.Values {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO author_filter_values (rule_id, value) VALUES (?, ?)`,
				ruleID, value); err != nil {
				return fmt.Errorf("insert author filter value: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit author filters: %w", err)
	}
	return nil
}
