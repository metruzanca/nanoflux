package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// ClaimStore maintains item_claims, the derived set of items owned by a
// claiming author. It is rebuildable from author_feeds, saved_views and the
// author rules, so it is never a source of truth.
type ClaimStore struct {
	db      *sql.DB
	views   *SavedViewStore
	authors *AuthorStore
}

// NewClaimStore wires the claim store over the same database handle.
func NewClaimStore(db *sql.DB, views *SavedViewStore, authors *AuthorStore) *ClaimStore {
	return &ClaimStore{db: db, views: views, authors: authors}
}

// RebuildAll replaces every claim row for a user's claiming authors.
func (s *ClaimStore) RebuildAll(userID int64) (int, error) {
	if _, err := s.db.ExecContext(context.Background(), `
		DELETE FROM item_claims WHERE author_id IN (
			SELECT id FROM authors WHERE user_id = ? AND is_system = 0)`, userID); err != nil {
		return 0, fmt.Errorf("clear claims: %w", err)
	}
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT id FROM authors WHERE user_id = ? AND is_system = 0 AND is_aggregator = 0`, userID)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	total := 0
	for _, id := range ids {
		n, err := s.RebuildAuthor(userID, id)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// RebuildAuthor replaces one author's claim rows. An aggregator or the hidden
// system author owns no claims.
func (s *ClaimStore) RebuildAuthor(userID, authorID int64) (int, error) {
	// Validate ownership before deleting anything: a foreign author id must not
	// clear another user's claim rows.
	var aggregator, system int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT is_aggregator, is_system FROM authors WHERE id = ? AND user_id = ?`,
		authorID, userID).Scan(&aggregator, &system); err != nil {
		if err == sql.ErrNoRows {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if _, err := s.db.ExecContext(context.Background(),
		`DELETE FROM item_claims WHERE author_id = ?`, authorID); err != nil {
		return 0, fmt.Errorf("clear author claims: %w", err)
	}
	if aggregator != 0 || system != 0 {
		return 0, nil
	}
	scope, args, err := s.authorScopeSQL(userID, authorID)
	if err != nil {
		return 0, err
	}
	q := `INSERT OR IGNORE INTO item_claims (item_id, author_id)
		SELECT i.id, ? FROM items i JOIN feeds f ON f.id = i.feed_id
		WHERE i.user_id = ? AND f.is_system = 0 AND (` + scope + `)`
	insertArgs := append([]any{authorID, userID}, args...)
	res, err := s.db.ExecContext(context.Background(), q, insertArgs...)
	if err != nil {
		return 0, fmt.Errorf("insert author claims: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// RefreshItems recomputes claims for a set of just-ingested or re-polled items.
// It is the poller's cheap path: only the claiming authors are evaluated, and
// only over these items.
func (s *ClaimStore) RefreshItems(userID int64, itemIDs []int64) error {
	if len(itemIDs) == 0 {
		return nil
	}
	ctx := context.Background()
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM authors WHERE user_id = ? AND is_system = 0 AND is_aggregator = 0`, userID)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Clear the current claims for these items so a changed tag set does not
	// leave a stale claim behind.
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(itemIDs)), ",")
	clearArgs := make([]any, 0, len(itemIDs))
	for _, id := range itemIDs {
		clearArgs = append(clearArgs, id)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM item_claims WHERE item_id IN (`+placeholders+`)
		   AND author_id IN (SELECT id FROM authors WHERE user_id = ?)`,
		append(clearArgs, userID)...); err != nil {
		return err
	}
	for _, authorID := range ids {
		scope, args, err := s.authorScopeSQL(userID, authorID)
		if err != nil {
			return err
		}
		q := `INSERT OR IGNORE INTO item_claims (item_id, author_id)
			SELECT i.id, ? FROM items i JOIN feeds f ON f.id = i.feed_id
			WHERE i.user_id = ? AND f.is_system = 0 AND i.id IN (` + placeholders + `)
			  AND (` + scope + `)`
		insertArgs := []any{authorID, userID}
		for _, id := range itemIDs {
			insertArgs = append(insertArgs, id)
		}
		insertArgs = append(insertArgs, args...)
		if _, err := s.db.ExecContext(ctx, q, insertArgs...); err != nil {
			return fmt.Errorf("refresh author claims: %w", err)
		}
	}
	return nil
}

// authorScopeSQL builds an author's raw source slice: member feeds with the
// author's rules, OR the author's views. It deliberately does not apply an
// aggregator's claim subtraction; claims are the raw slice.
func (s *ClaimStore) authorScopeSQL(userID, authorID int64) (string, []any, error) {
	parts := []string{
		`EXISTS (
			SELECT 1 FROM item_feeds mf
			JOIN author_feeds af ON af.feed_id = mf.feed_id
			WHERE mf.item_id = i.id AND af.author_id = ? AND (` + authorRulesVisibleSQL("i.id", "af.author_id") + `)
		)`,
	}
	args := []any{authorID}
	var tz string
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COALESCE(timezone, '') FROM users WHERE id = ?`, userID).Scan(&tz); err != nil {
		return "", nil, err
	}
	views, err := s.views.FiltersForAuthor(userID, authorID, tz)
	if err != nil {
		return "", nil, err
	}
	for _, v := range views {
		clause, viewArgs := viewFilterClause("i.id", "COALESCE(i.published_at, i.fetched_at)", v)
		if clause == "" {
			continue
		}
		parts = append(parts, "("+clause+")")
		args = append(args, viewArgs...)
	}
	return "(" + strings.Join(parts, " OR ") + ")", args, nil
}

// EnsureBackfilled rebuilds claims once for a user when the derived table has
// never been populated. It is safe to run on every startup.
func (s *ClaimStore) EnsureBackfilled(userID int64) (int, error) {
	key := claimBackfillKey + ":" + strconv.FormatInt(userID, 10)
	var marker string
	err := s.db.QueryRowContext(context.Background(),
		`SELECT value FROM settings WHERE key = ?`, key).Scan(&marker)
	if err == nil && marker == "1" {
		return 0, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	n, err := s.RebuildAll(userID)
	if err != nil {
		return n, err
	}
	if _, err := s.db.ExecContext(context.Background(), `
		INSERT INTO settings (key, value) VALUES (?, '1')
		ON CONFLICT (key) DO UPDATE SET value = '1'`, key); err != nil {
		return n, err
	}
	return n, nil
}

const claimBackfillKey = "data.item_claims_v1"
