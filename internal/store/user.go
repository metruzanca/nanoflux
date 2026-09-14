package store

import (
	"database/sql"
	"errors"
	"fmt"
)

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	CreatedAt    string
}

type UserStore struct{ db *sql.DB }

func (s *UserStore) Create(username, passwordHash string) (User, error) {
	res, err := s.db.Exec(
		`INSERT INTO users(username, password_hash) VALUES(?, ?)`,
		username, passwordHash,
	)
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, err
	}
	return s.ByID(id)
}

func (s *UserStore) ByID(id int64) (User, error) {
	u, err := scanUser(s.db.QueryRow(
		`SELECT id, username, password_hash, created_at FROM users WHERE id = ?`, id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func (s *UserStore) ByUsername(username string) (User, error) {
	u, err := scanUser(s.db.QueryRow(
		`SELECT id, username, password_hash, created_at FROM users WHERE username = ?`, username,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func (s *UserStore) List() ([]User, error) {
	rows, err := s.db.Query(
		`SELECT id, username, password_hash, created_at FROM users ORDER BY username`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *UserStore) Count() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanUser(row scanner) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt)
	return u, err
}
