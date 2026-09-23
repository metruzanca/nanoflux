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
