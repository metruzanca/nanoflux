package config

import (
	"testing"
	"time"
)

func TestBackupConfigFromEnv(t *testing.T) {
	t.Setenv("NF_BACKUP_INTERVAL", "24h")
	t.Setenv("NF_BACKUP_KEEP", "3")
	t.Setenv("NF_BACKUP_S3_ENDPOINT", "https://s3.example.com")
	t.Setenv("NF_BACKUP_S3_BUCKET", "my-backups")
	t.Setenv("NF_BACKUP_S3_PREFIX", "nanoflux")

	c := Load().Backup
	if c.Interval != 24*time.Hour {
		t.Errorf("Interval = %v", c.Interval)
	}
	if c.Keep != 3 {
		t.Errorf("Keep = %d", c.Keep)
	}
	if !c.Enabled() {
		t.Error("expected enabled with interval + S3 endpoint")
	}
	if !c.UsesS3() {
		t.Error("expected UsesS3")
	}
	if c.S3.Bucket != "my-backups" || c.S3.Prefix != "nanoflux" {
		t.Errorf("S3 = %+v", c.S3)
	}
}

func TestBackupDefaultsDisabled(t *testing.T) {
	c := Load().Backup
	if c.Interval != 0 {
		t.Errorf("default Interval = %v, want 0 (disabled)", c.Interval)
	}
	if c.Keep != 7 {
		t.Errorf("default Keep = %d, want 7", c.Keep)
	}
	if c.Enabled() {
		t.Error("backups should default to disabled")
	}
	if c.UsesS3() {
		t.Error("default should be local, not S3")
	}
}

func TestBackupLocalEnabled(t *testing.T) {
	t.Setenv("NF_BACKUP_INTERVAL", "12h")
	t.Setenv("NF_BACKUP_DIR", "/var/backups/nanoflux")
	c := Load().Backup
	if !c.Enabled() || c.UsesS3() {
		t.Fatalf("expected enabled local backup, got %+v", c)
	}
	if c.Dir != "/var/backups/nanoflux" {
		t.Errorf("Dir = %q", c.Dir)
	}
}

func TestAnalyticsDefaultsDisabled(t *testing.T) {
	a := Load().Analytics
	if a.On || a.Enabled() {
		t.Fatalf("analytics should default to off: %+v", a)
	}
	if a.ScriptURL != "" || a.WebsiteID != "" {
		t.Errorf("default analytics should be empty: %+v", a)
	}
}

func TestAnalyticsEnabled(t *testing.T) {
	t.Setenv("NF_ANALYTICS_ENABLED", "1")
	t.Setenv("NF_ANALYTICS_SCRIPT_URL", "https://umami.example.com/script.js")
	t.Setenv("NF_ANALYTICS_WEBSITE_ID", "abc-123")
	t.Setenv("NF_ANALYTICS_HOST_URL", "https://umami.example.com")
	t.Setenv("NF_ANALYTICS_TAG", "demo")

	a := Load().Analytics
	if !a.Enabled() {
		t.Fatalf("expected enabled, got %+v", a)
	}
	if a.ScriptURL != "https://umami.example.com/script.js" || a.WebsiteID != "abc-123" {
		t.Errorf("unexpected values: %+v", a)
	}
	if a.HostURL != "https://umami.example.com" || a.Tag != "demo" {
		t.Errorf("unexpected optional values: %+v", a)
	}
}

func TestAnalyticsPartialConfigStaysOff(t *testing.T) {
	t.Setenv("NF_ANALYTICS_ENABLED", "1")
	t.Setenv("NF_ANALYTICS_SCRIPT_URL", "https://umami.example.com/script.js")
	if Load().Analytics.Enabled() {
		t.Error("enabled without a website id")
	}

	t.Setenv("NF_ANALYTICS_SCRIPT_URL", "")
	t.Setenv("NF_ANALYTICS_WEBSITE_ID", "abc-123")
	if Load().Analytics.Enabled() {
		t.Error("enabled without a script url")
	}
}

func TestAnalyticsRequiresExplicitSwitch(t *testing.T) {
	t.Setenv("NF_ANALYTICS_SCRIPT_URL", "https://umami.example.com/script.js")
	t.Setenv("NF_ANALYTICS_WEBSITE_ID", "abc-123")
	if Load().Analytics.Enabled() {
		t.Error("values alone should not enable analytics without NF_ANALYTICS_ENABLED")
	}
}

func TestPluginsDirDefault(t *testing.T) {
	if got := Load().PluginsDir; got != "./plugins" {
		t.Errorf("default PluginsDir = %q, want ./plugins", got)
	}
	t.Setenv("NF_PLUGINS_DIR", "/plugins")
	if got := Load().PluginsDir; got != "/plugins" {
		t.Errorf("PluginsDir override = %q, want /plugins", got)
	}
}
