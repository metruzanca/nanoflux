package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

type User struct {
	ID                int64
	Username          string
	PasswordHash      string
	IsAdmin           bool
	HasAvatar         bool
	Timezone          string
	Theme             string
	AccentColor       string
	HomeConfig        string // raw JSON home-screen config, "" when unset
	AutoReadAfterDays int    // days after which unread items are auto-marked read; 0 = off
	HideUnreadCounts  bool   // hide the unread/authors badges in the top navigation
	HideUnreadNav     bool   // move the "unread" nav item into the user menu
	GridMaxColumns    int    // max columns for grid-mode item lists (2..6)
	CreatedAt         string
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
	return toUser(u.ID, u.Username, u.PasswordHash, u.IsAdmin, u.AvatarKey, u.Timezone, u.Theme, u.AccentColor, u.HomeConfig.String, u.AutoReadAfterDays, u.HideUnreadCounts, u.HideUnreadNav, u.GridMaxColumns, u.CreatedAt), nil
}

func (s *UserStore) ByID(id int64) (User, error) {
	u, err := s.q.GetUserByID(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return toUser(u.ID, u.Username, u.PasswordHash, u.IsAdmin, u.AvatarKey, u.Timezone, u.Theme, u.AccentColor, u.HomeConfig.String, u.AutoReadAfterDays, u.HideUnreadCounts, u.HideUnreadNav, u.GridMaxColumns, u.CreatedAt), nil
}

func (s *UserStore) ByUsername(username string) (User, error) {
	u, err := s.q.GetUserByUsername(context.Background(), username)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return toUser(u.ID, u.Username, u.PasswordHash, u.IsAdmin, u.AvatarKey, u.Timezone, u.Theme, u.AccentColor, u.HomeConfig.String, u.AutoReadAfterDays, u.HideUnreadCounts, u.HideUnreadNav, u.GridMaxColumns, u.CreatedAt), nil
}

func (s *UserStore) List() ([]User, error) {
	rows, err := s.q.ListUsers(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]User, 0, len(rows))
	for _, u := range rows {
		out = append(out, toUser(u.ID, u.Username, u.PasswordHash, u.IsAdmin, u.AvatarKey, u.Timezone, u.Theme, u.AccentColor, u.HomeConfig.String, u.AutoReadAfterDays, u.HideUnreadCounts, u.HideUnreadNav, u.GridMaxColumns, u.CreatedAt))
	}
	return out, nil
}

func (s *UserStore) Count() (int, error) {
	n, err := s.q.CountUsers(context.Background())
	return int(n), err
}

// CountPersistent returns the number of real (non-ephemeral) users. Bootstrap
// uses it so throwaway demo accounts cannot satisfy the "first account" check.
func (s *UserStore) CountPersistent() (int, error) {
	n, err := s.q.CountPersistentUsers(context.Background())
	return int(n), err
}

// EphemeralStatus reports whether the user is an ephemeral demo account and its
// absolute expiry as a db-formatted UTC timestamp ("" when it never expires).
func (s *UserStore) EphemeralStatus(userID int64) (bool, string, error) {
	row, err := s.q.GetUserEphemeralStatus(context.Background(), userID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", ErrNotFound
	}
	if err != nil {
		return false, "", err
	}
	return row.IsEphemeral == 1, row.ExpiresAt.String, nil
}

// ExpiredEphemeral returns the ids of ephemeral users whose expiry has passed.
// now is a db-formatted UTC timestamp.
func (s *UserStore) ExpiredEphemeral(now string) ([]int64, error) {
	return s.q.ListExpiredEphemeralUsers(context.Background(), now)
}

// CountAdmins returns how many users have the admin flag set.
func (s *UserStore) CountAdmins() (int, error) {
	n, err := s.q.CountAdmins(context.Background())
	return int(n), err
}

// ResetPassword stores a new bcrypt password hash for the user.
func (s *UserStore) ResetPassword(userID int64, passwordHash string) error {
	return s.q.SetUserPassword(context.Background(), sqlcgen.SetUserPasswordParams{
		PasswordHash: passwordHash,
		ID:           userID,
	})
}

// SetAdmin grants or revokes the admin flag.
func (s *UserStore) SetAdmin(userID int64, admin bool) error {
	return s.q.SetUserAdmin(context.Background(), sqlcgen.SetUserAdminParams{
		IsAdmin: admin,
		ID:      userID,
	})
}

// BannerDismissed reports whether the user has dismissed the admin signup
// banner (a per-user UI preference).
func (s *UserStore) BannerDismissed(userID int64) (bool, error) {
	return s.q.GetUserSignupBannerDismissed(context.Background(), userID)
}

// SetBannerDismissed sets the user's signup-banner dismissal flag.
func (s *UserStore) SetBannerDismissed(userID int64, dismissed bool) error {
	return s.q.SetUserSignupBannerDismissed(context.Background(), sqlcgen.SetUserSignupBannerDismissedParams{
		SignupBannerDismissed: dismissed,
		ID:                    userID,
	})
}

// Delete removes the user; sessions, feeds, items and other per-user rows are
// removed by the FK cascade. Object-storage bytes (avatar, icons) are not
// touched here — callers purge them via ListObjectKeys.
func (s *UserStore) Delete(userID int64) error {
	return s.q.DeleteUser(context.Background(), userID)
}

// ListObjectKeys returns the object-storage keys owned by the user (avatar,
// custom icon blobs, and cached author avatars), for cleanup when the user is
// deleted.
func (s *UserStore) ListObjectKeys(userID int64) ([]string, error) {
	rows, err := s.q.ListUserIconKeys(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(rows)+1)
	for _, k := range rows {
		if k.Valid && k.String != "" {
			keys = append(keys, k.String)
		}
	}
	if k, err := s.AvatarKey(userID); err != nil {
		return nil, err
	} else if k != "" {
		keys = append(keys, k)
	}
	authorKeys, err := s.q.ListAuthorsAvatarKeys(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	for _, k := range authorKeys {
		if k.Valid && k.String != "" {
			keys = append(keys, k.String)
		}
	}
	return keys, nil
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

// SetHomeConfig stores the user's home-screen configuration as raw JSON. An
// empty string clears it (the home screen falls back to its default).
func (s *UserStore) SetHomeConfig(userID int64, config string) error {
	return s.q.SetUserHomeConfig(context.Background(), sqlcgen.SetUserHomeConfigParams{
		HomeConfig: ns(config),
		ID:         userID,
	})
}

// SetAutoReadAfterDays stores the user's auto-read retention window in days.
// A value of 0 disables the sweep; negative values are clamped to 0.
func (s *UserStore) SetAutoReadAfterDays(userID int64, days int) error {
	if days < 0 {
		days = 0
	}
	return s.q.SetUserAutoReadAfterDays(context.Background(), sqlcgen.SetUserAutoReadAfterDaysParams{
		AutoReadAfterDays: int64(days),
		ID:                userID,
	})
}

// SetHideUnreadCounts stores whether the top navigation hides its numeric
// unread/authors badges.
func (s *UserStore) SetHideUnreadCounts(userID int64, hide bool) error {
	return s.q.SetUserHideUnreadCounts(context.Background(), sqlcgen.SetUserHideUnreadCountsParams{
		HideUnreadCounts: hide,
		ID:               userID,
	})
}

// SetHideUnreadNav stores whether the "unread" nav item is moved into the user
// menu instead of the top navigation.
func (s *UserStore) SetHideUnreadNav(userID int64, hide bool) error {
	return s.q.SetUserHideUnreadNav(context.Background(), sqlcgen.SetUserHideUnreadNavParams{
		HideUnreadNav: hide,
		ID:            userID,
	})
}

// SetGridMaxColumns stores the maximum column count for grid-mode item lists,
// clamped to the supported 2..6 range.
func (s *UserStore) SetGridMaxColumns(userID int64, n int) error {
	return s.q.SetUserGridMaxColumns(context.Background(), sqlcgen.SetUserGridMaxColumnsParams{
		GridMaxColumns: int64(ClampGridColumns(n)),
		ID:             userID,
	})
}

// ClampGridColumns maps an arbitrary grid column count onto the supported 2..6
// range, defaulting to 2.
func ClampGridColumns(n int) int {
	if n < 2 {
		return 2
	}
	if n > 6 {
		return 6
	}
	return n
}

// FavoritesShareToken returns the user's public favorites share token, or ""
// when the favorites list is not shared.
func (s *UserStore) FavoritesShareToken(userID int64) (string, error) {
	token, err := s.q.GetFavoritesShareToken(context.Background(), userID)
	if err != nil {
		return "", err
	}
	return token.String, nil
}

// SetFavoritesShareToken stores (or, with an empty token, clears) the user's
// public favorites share token.
func (s *UserStore) SetFavoritesShareToken(userID int64, token string) error {
	return s.q.SetFavoritesShareToken(context.Background(), sqlcgen.SetFavoritesShareTokenParams{
		Token:  ns(token),
		UserID: userID,
	})
}

// ShareFavorites creates a public share token for the favorites list, reusing
// an existing one when the list is already shared.
func (s *UserStore) ShareFavorites(userID int64) (string, error) {
	tok, err := s.FavoritesShareToken(userID)
	if err != nil {
		return "", err
	}
	if tok != "" {
		return tok, nil
	}
	tok, err = newToken()
	if err != nil {
		return "", err
	}
	if err := s.SetFavoritesShareToken(userID, tok); err != nil {
		return "", err
	}
	return tok, nil
}

// ByFavoritesShareToken resolves the owner of a public favorites share link.
func (s *UserStore) ByFavoritesShareToken(token string) (User, error) {
	u, err := s.q.GetUserByFavoritesShareToken(context.Background(), ns(token))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return toUser(u.ID, u.Username, u.PasswordHash, u.IsAdmin, u.AvatarKey, u.Timezone, u.Theme, u.AccentColor, "", u.AutoReadAfterDays, u.HideUnreadCounts, u.HideUnreadNav, u.GridMaxColumns, u.CreatedAt), nil
}

// BookmarksShareToken returns the user's public bookmarks share token, or ""
// when the bookmarks list is not shared.
func (s *UserStore) BookmarksShareToken(userID int64) (string, error) {
	token, err := s.q.GetBookmarksShareToken(context.Background(), userID)
	if err != nil {
		return "", err
	}
	return token.String, nil
}

// SetBookmarksShareToken stores (or, with an empty token, clears) the user's
// public bookmarks share token.
func (s *UserStore) SetBookmarksShareToken(userID int64, token string) error {
	return s.q.SetBookmarksShareToken(context.Background(), sqlcgen.SetBookmarksShareTokenParams{
		Token:  ns(token),
		UserID: userID,
	})
}

// ShareBookmarks creates a public share token for the bookmarks list, reusing an
// existing one when the list is already shared.
func (s *UserStore) ShareBookmarks(userID int64) (string, error) {
	tok, err := s.BookmarksShareToken(userID)
	if err != nil {
		return "", err
	}
	if tok != "" {
		return tok, nil
	}
	tok, err = newToken()
	if err != nil {
		return "", err
	}
	if err := s.SetBookmarksShareToken(userID, tok); err != nil {
		return "", err
	}
	return tok, nil
}

// ByBookmarksShareToken resolves the owner of a public bookmarks share link.
func (s *UserStore) ByBookmarksShareToken(token string) (User, error) {
	u, err := s.q.GetUserByBookmarksShareToken(context.Background(), ns(token))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return toUser(u.ID, u.Username, u.PasswordHash, u.IsAdmin, u.AvatarKey, u.Timezone, u.Theme, u.AccentColor, "", u.AutoReadAfterDays, u.HideUnreadCounts, u.HideUnreadNav, u.GridMaxColumns, u.CreatedAt), nil
}
