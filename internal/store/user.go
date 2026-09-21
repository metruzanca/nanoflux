package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	HasAvatar    bool
	Timezone     string
	Theme        string
	AccentColor  string
	CreatedAt    string
}

type UserStore struct{ q *sqlcgen.Queries }

func (s *UserStore) Create(username, passwordHash string) (User, error) {
	ctx := context.Background()
	u, err := s.q.CreateUser(ctx, sqlcgen.CreateUserParams{
		Username:     username,
		PasswordHash: passwordHash,
	})
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return toUser(u.ID, u.Username, u.PasswordHash, u.AvatarKey, u.Timezone, u.Theme, u.AccentColor, u.CreatedAt), nil
}

func (s *UserStore) ByID(id int64) (User, error) {
	u, err := s.q.GetUserByID(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return toUser(u.ID, u.Username, u.PasswordHash, u.AvatarKey, u.Timezone, u.Theme, u.AccentColor, u.CreatedAt), nil
}

func (s *UserStore) ByUsername(username string) (User, error) {
	u, err := s.q.GetUserByUsername(context.Background(), username)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return toUser(u.ID, u.Username, u.PasswordHash, u.AvatarKey, u.Timezone, u.Theme, u.AccentColor, u.CreatedAt), nil
}

func (s *UserStore) List() ([]User, error) {
	rows, err := s.q.ListUsers(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]User, 0, len(rows))
	for _, u := range rows {
		out = append(out, toUser(u.ID, u.Username, u.PasswordHash, u.AvatarKey, u.Timezone, u.Theme, u.AccentColor, u.CreatedAt))
	}
	return out, nil
}

func (s *UserStore) Count() (int, error) {
	n, err := s.q.CountUsers(context.Background())
	return int(n), err
}

// AvatarKey returns the object-storage key of the user's profile picture,
// or "" when none is set.
func (s *UserStore) AvatarKey(userID int64) (string, error) {
	key, err := s.q.GetUserAvatarKey(context.Background(), userID)
	if err != nil {
		return "", err
	}
	return key.String, nil
}

// SetAvatarKey stores the object-storage key of the user's profile picture.
func (s *UserStore) SetAvatarKey(userID int64, key string) error {
	return s.q.SetUserAvatarKey(context.Background(), sqlcgen.SetUserAvatarKeyParams{
		AvatarKey: ns(key),
		ID:        userID,
	})
}

// SetTimezone stores the user's IANA timezone name ("" = server time).
func (s *UserStore) SetTimezone(userID int64, tz string) error {
	return s.q.SetUserTimezone(context.Background(), sqlcgen.SetUserTimezoneParams{
		Timezone: ns(tz),
		ID:       userID,
	})
}

// SetTheme stores the user's theme preference: "dark", "light", or "system".
func (s *UserStore) SetTheme(userID int64, theme string) error {
	return s.q.SetUserTheme(context.Background(), sqlcgen.SetUserThemeParams{
		Theme: theme,
		ID:    userID,
	})
}

// SetAccentColor stores the user's accent color as a "#rrggbb" hex value.
func (s *UserStore) SetAccentColor(userID int64, color string) error {
	return s.q.SetUserAccentColor(context.Background(), sqlcgen.SetUserAccentColorParams{
		AccentColor: color,
		ID:          userID,
	})
}
