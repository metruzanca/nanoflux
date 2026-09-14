package store

import (
	"database/sql"
	"errors"
	"fmt"
)

type Author struct {
	ID          int64
	UserID      int64
	Name        string
	URL         string
	AvatarURL   string
	Description string
	CreatedAt   string
}

type AuthorStore struct{ db *sql.DB }

func (s *AuthorStore) Create(userID int64, name, url, avatarURL, description string) (Author, error) {
	res, err := s.db.Exec(
		`INSERT INTO authors(user_id, name, url, avatar_url, description) VALUES(?, ?, ?, ?, ?)`,
		userID, name, nullStr(url), nullStr(avatarURL), nullStr(description),
	)
	if err != nil {
		return Author{}, fmt.Errorf("create author: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Author{}, err
	}
	return s.ByID(userID, id)
}

func (s *AuthorStore) ByID(userID, id int64) (Author, error) {
	a, err := scanAuthor(s.db.QueryRow(
		`SELECT id, user_id, name, url, avatar_url, description, created_at
		 FROM authors WHERE id = ? AND user_id = ?`, id, userID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Author{}, ErrNotFound
	}
	return a, err
}

func (s *AuthorStore) List(userID int64) ([]Author, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, name, url, avatar_url, description, created_at
		 FROM authors WHERE user_id = ? ORDER BY name`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Author
	for rows.Next() {
		a, err := scanAuthor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *AuthorStore) Update(userID, id int64, name, url, avatarURL, description string) error {
	res, err := s.db.Exec(
		`UPDATE authors SET name = ?, url = ?, avatar_url = ?, description = ?
		 WHERE id = ? AND user_id = ?`,
		name, nullStr(url), nullStr(avatarURL), nullStr(description), id, userID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes an author. It fails if the author still has feeds
// (the FK on feeds.author_id is RESTRICT).
func (s *AuthorStore) Delete(userID, id int64) error {
	res, err := s.db.Exec(`DELETE FROM authors WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("delete author: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanAuthor(row scanner) (Author, error) {
	var a Author
	var url, avatarURL, description sql.NullString
	err := row.Scan(&a.ID, &a.UserID, &a.Name, &url, &avatarURL, &description, &a.CreatedAt)
	a.URL, a.AvatarURL, a.Description = url.String, avatarURL.String, description.String
	return a, err
}
