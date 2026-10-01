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
// default when the setting is absent is false (signup closed); fresh installs
// open signup only through the no-users bootstrap window.
func (s *SettingStore) AllowSignup() (bool, error) {
	v, err := s.q.GetSetting(context.Background(), "allow_signup")
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
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

// pluginSettingPrefix namespaces a plugin's settings in the global settings
// table: plugin.<plugin name>.<field name>.
const pluginSettingPrefix = "plugin."

// pluginSettingKey is the storage key for one plugin field.
func pluginSettingKey(plugin, field string) string {
	return pluginSettingPrefix + plugin + "." + field
}

// PluginSettings reads the stored values for the named fields. A missing key
// reads as "" so a plugin starts unconfigured.
func (s *SettingStore) PluginSettings(plugin string, fields []string) map[string]string {
	out := make(map[string]string, len(fields))
	for _, name := range fields {
		v, err := s.q.GetSetting(context.Background(), pluginSettingKey(plugin, name))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			continue
		}
		out[name] = v
	}
	return out
}

// SetPluginSettings stores a plugin's values, overwriting the named fields. A
// field absent from values is left unchanged, so a write-only secret (a
// password field the UI did not re-submit) survives a save.
func (s *SettingStore) SetPluginSettings(plugin string, values map[string]string) error {
	for name, value := range values {
		if err := s.q.UpsertSetting(context.Background(), sqlcgen.UpsertSettingParams{
			Key:   pluginSettingKey(plugin, name),
			Value: value,
		}); err != nil {
			return err
		}
	}
	return nil
}
