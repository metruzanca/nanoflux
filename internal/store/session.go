package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

type SessionStore struct{ q *sqlcgen.Queries }

// Create stores a session token. expiresAt is a db-formatted UTC timestamp.
func (s *SessionStore) Create(userID int64, token, expiresAt string) error {
	return s.q.CreateSession(context.Background(), sqlcgen.CreateSessionParams{
		Token:     token,
		UserID:    userID,
		ExpiresAt: expiresAt,
	})
}

// UserByToken returns the user for a valid, unexpired session token.
func (s *SessionStore) UserByToken(token string) (User, error) {
	u, err := s.q.GetUserByToken(context.Background(), token)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return toUser(u.ID, u.Username, u.PasswordHash, u.AvatarKey, u.Timezone, u.Theme, u.CreatedAt), nil
}

func (s *SessionStore) Delete(token string) error {
	return s.q.DeleteSession(context.Background(), token)
}

// Touch extends a session's expiry (sliding sessions).
func (s *SessionStore) Touch(token, expiresAt string) error {
	return s.q.TouchSession(context.Background(), sqlcgen.TouchSessionParams{
		ExpiresAt: expiresAt,
		Token:     token,
	})
}

func (s *SessionStore) DeleteExpired() error {
	return s.q.DeleteExpiredSessions(context.Background())
}
