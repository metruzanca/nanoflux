package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

// SettingStore reads and writes global key/value settings (the settings
// table). Missing keys read as their default value.
type SettingStore struct{ q *sqlcgen.Queries }

// AllowSignup reports whether new users can register via the signup page. The
// default when the setting is absent is true (signup open).
func (s *SettingStore) AllowSignup() (bool, error) {
	v, err := s.q.GetSetting(context.Background(), "allow_signup")
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return v == "1", nil
}

// SetAllowSignup enables or disables the signup page.
func (s *SettingStore) SetAllowSignup(allow bool) error {
	v := "0"
	if allow {
		v = "1"
	}
	return s.q.UpsertSetting(context.Background(), sqlcgen.UpsertSettingParams{
		Key:   "allow_signup",
		Value: v,
	})
}
