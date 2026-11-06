package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// SavedView is a private, named filter recipe over the user's item lists. It
// stores only the title, the assigned author and the canonical list filter
// params. The view always matches all feeds; tabs and sort are page state.
type SavedView struct {
	ID        int64
	UserID    int64
	AuthorID  int64 // 0 = unassigned
	Title     string
	Filters   []string // canonical ?f= params
	CreatedAt string
}

// MaxSavedViewsPerUser caps how many views an account can keep, so a crafted
// sequence of creates cannot grow the table without bound.
const MaxSavedViewsPerUser = 200

// SavedViewStore persists saved views. It is hand-written rather than sqlc
// generated because the filter list is an opaque JSON array.
type SavedViewStore struct{ db *sql.DB }

const savedViewColumns = `id, user_id, author_id, title, filters, created_at`

// List returns all of a user's saved views, newest first.
func (s *SavedViewStore) List(userID int64) ([]SavedView, error) {
	return s.query(`
		SELECT `+savedViewColumns+`
		FROM saved_views WHERE user_id = ?
		ORDER BY created_at DESC, id DESC`, userID)
}

// ListByAuthor returns the views assigned to one author, newest first.
func (s *SavedViewStore) ListByAuthor(userID, authorID int64) ([]SavedView, error) {
	return s.query(`
		SELECT `+savedViewColumns+`
		FROM saved_views WHERE user_id = ? AND author_id = ?
		ORDER BY created_at DESC, id DESC`, userID, authorID)
}

// ByID returns one of the user's views, or ErrNotFound.
func (s *SavedViewStore) ByID(userID, id int64) (SavedView, error) {
	row := s.db.QueryRowContext(context.Background(), `
		SELECT `+savedViewColumns+`
		FROM saved_views WHERE user_id = ? AND id = ?`, userID, id)
	v, err := scanSavedView(row)
	if errors.Is(err, sql.ErrNoRows) {
		return SavedView{}, ErrNotFound
	}
	if err != nil {
		return SavedView{}, fmt.Errorf("get saved view: %w", err)
	}
	return v, nil
}

// Count returns how many views a user has.
func (s *SavedViewStore) Count(userID int64) (int, error) {
	var n int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM saved_views WHERE user_id = ?`, userID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count saved views: %w", err)
	}
	return n, nil
}

// Create stores a new view after normalizing its opaque fields.
func (s *SavedViewStore) Create(userID int64, v SavedView) (SavedView, error) {
	filters, err := marshalSavedViewFilters(v.Filters)
	if err != nil {
		return SavedView{}, err
	}
	res, err := s.db.ExecContext(context.Background(), `
		INSERT INTO saved_views (
			user_id, author_id, title, filters
		) VALUES (?, ?, ?, ?)`,
		userID, savedViewAuthor(v.AuthorID), v.Title, filters)
	if err != nil {
		return SavedView{}, fmt.Errorf("create saved view: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return SavedView{}, fmt.Errorf("saved view id: %w", err)
	}
	return s.ByID(userID, id)
}

// Update rewrites one of the user's views. ErrNotFound when the id is not the
// user's (or no longer exists).
func (s *SavedViewStore) Update(userID, id int64, v SavedView) error {
	filters, err := marshalSavedViewFilters(v.Filters)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(context.Background(), `
		UPDATE saved_views SET
			author_id = ?, title = ?, filters = ?
		WHERE user_id = ? AND id = ?`,
		savedViewAuthor(v.AuthorID), v.Title, filters, userID, id)
	if err != nil {
		return fmt.Errorf("update saved view: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update saved view rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes one of the user's views.
func (s *SavedViewStore) Delete(userID, id int64) error {
	res, err := s.db.ExecContext(context.Background(),
		`DELETE FROM saved_views WHERE user_id = ? AND id = ?`, userID, id)
	if err != nil {
		return fmt.Errorf("delete saved view: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete saved view rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// query runs a saved-view select and reads every row.
func (s *SavedViewStore) query(q string, args ...any) ([]SavedView, error) {
	rows, err := s.db.QueryContext(context.Background(), q, args...)
	if err != nil {
		return nil, fmt.Errorf("list saved views: %w", err)
	}
	defer rows.Close()
	var out []SavedView
	for rows.Next() {
		v, err := scanSavedView(rows)
		if err != nil {
			return nil, fmt.Errorf("scan saved view: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list saved views: %w", err)
	}
	return out, nil
}

type savedViewScanner interface {
	Scan(dest ...any) error
}

func scanSavedView(row savedViewScanner) (SavedView, error) {
	var (
		v        SavedView
		authorID sql.NullInt64
		filters  string
	)
	if err := row.Scan(
		&v.ID, &v.UserID, &authorID, &v.Title, &filters, &v.CreatedAt,
	); err != nil {
		return SavedView{}, err
	}
	v.AuthorID = authorID.Int64
	v.Filters = unmarshalSavedViewFilters(filters)
	return v, nil
}

func marshalSavedViewFilters(filters []string) (string, error) {
	if filters == nil {
		return "[]", nil
	}
	b, err := json.Marshal(filters)
	if err != nil {
		return "", fmt.Errorf("encode saved view filters: %w", err)
	}
	return string(b), nil
}

func unmarshalSavedViewFilters(raw string) []string {
	var out []string
	if raw == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func savedViewAuthor(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}
