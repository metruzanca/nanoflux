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
	return toUser(u.ID, u.Username, u.PasswordHash, u.IsAdmin, u.AvatarKey, u.Timezone, u.Theme, u.AccentColor, u.HomeConfig.String, u.AutoReadAfterDays, u.CreatedAt), nil
}

func (s *SessionStore) Delete(token string) error {
	return s.q.DeleteSession(context.Background(), token)
}

// DeleteUserSessions revokes every session belonging to a user (used when a
// password is reset, logging out all devices).
func (s *SessionStore) DeleteUserSessions(userID int64) error {
	return s.q.DeleteSessionsByUser(context.Background(), userID)
}

// DeleteUserSessionsExcept revokes every session belonging to a user except
// keepToken, which survives (used when a password is changed: log out all
// devices but keep the one making the change).
func (s *SessionStore) DeleteUserSessionsExcept(userID int64, keepToken string) error {
	return s.q.DeleteSessionsByUserExcept(context.Background(), sqlcgen.DeleteSessionsByUserExceptParams{
		UserID: userID,
		Token:  keepToken,
	})
}

// Session is a user's active session for display and management.
type Session struct {
	Token     string
	CreatedAt string
	ExpiresAt string
}

// ListUserSessions returns a user's sessions, newest first.
func (s *SessionStore) ListUserSessions(userID int64) ([]Session, error) {
	rows, err := s.q.ListSessionsByUser(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, Session{Token: r.Token, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt})
	}
	return out, nil
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
