package store

import (
	"database/sql"
	"errors"
)

type SessionStore struct{ db *sql.DB }

// Create stores a session token. expiresAt is a db-formatted UTC timestamp.
func (s *SessionStore) Create(userID int64, token, expiresAt string) error {
	_, err := s.db.Exec(
		`INSERT INTO sessions(token, user_id, expires_at) VALUES(?, ?, ?)`,
		token, userID, expiresAt,
	)
	return err
}

// UserByToken returns the user for a valid, unexpired session token.
func (s *SessionStore) UserByToken(token string) (User, error) {
	u, err := scanUser(s.db.QueryRow(
		`SELECT u.id, u.username, u.password_hash, u.created_at
		 FROM sessions se
		 JOIN users u ON u.id = se.user_id
		 WHERE se.token = ? AND se.expires_at > datetime('now')`,
		token,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func (s *SessionStore) Delete(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// Touch extends a session's expiry (sliding sessions).
func (s *SessionStore) Touch(token, expiresAt string) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET expires_at = ? WHERE token = ?`, expiresAt, token,
	)
	return err
}

func (s *SessionStore) DeleteExpired() error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at <= datetime('now')`)
	return err
}
