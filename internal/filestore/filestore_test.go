package filestore

import (
	"context"
	"errors"
	"testing"
)

func TestMemoryStore(t *testing.T) {
	ctx := context.Background()
	fs := NewMemory()

	if err := fs.Put(ctx, "k1", "image/png", []byte("data")); err != nil {
		t.Fatal(err)
	}
	ct, data, err := fs.Get(ctx, "k1")
	if err != nil || ct != "image/png" || string(data) != "data" {
		t.Fatalf("Get: %v %q %q", err, ct, data)
	}
	// Overwrite.
	if err := fs.Put(ctx, "k1", "image/jpeg", []byte("new")); err != nil {
		t.Fatal(err)
	}
	ct, data, _ = fs.Get(ctx, "k1")
	if ct != "image/jpeg" || string(data) != "new" {
		t.Fatalf("overwrite: %q %q", ct, data)
	}
	if err := fs.Delete(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fs.Get(ctx, "k1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDiskStore(t *testing.T) {
	ctx := context.Background()
	fs := NewDisk(t.TempDir())

	if err := fs.EnsureBucket(ctx); err != nil {
		t.Fatal(err)
	}
	if err := fs.Put(ctx, "avatars/1", "image/png", []byte("data")); err != nil {
		t.Fatal(err)
	}
	ct, data, err := fs.Get(ctx, "avatars/1")
	if err != nil || ct != "image/png" || string(data) != "data" {
		t.Fatalf("Get: %v %q %q", err, ct, data)
	}
	// Overwrite.
	if err := fs.Put(ctx, "avatars/1", "image/jpeg", []byte("new")); err != nil {
		t.Fatal(err)
	}
	ct, data, _ = fs.Get(ctx, "avatars/1")
	if ct != "image/jpeg" || string(data) != "new" {
		t.Fatalf("overwrite: %q %q", ct, data)
	}
	if err := fs.Delete(ctx, "avatars/1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fs.Get(ctx, "avatars/1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDiskStoreRejectsEscapingKeys(t *testing.T) {
	ctx := context.Background()
	fs := NewDisk(t.TempDir())
	if err := fs.EnsureBucket(ctx); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "..", "../x", "a/../b", "/etc/passwd", "a//b", "./x"} {
		if err := fs.Put(ctx, key, "text/plain", []byte("x")); err == nil {
			t.Errorf("Put(%q) should fail", key)
		}
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("NF_S3_ENDPOINT", "")
	t.Setenv("NF_S3_BUCKET", "")
	t.Setenv("NF_S3_REGION", "")
	t.Setenv("NF_FILE_STORE", "/var/lib/nanoflux/files")
	cfg := ConfigFromEnv()
	if !cfg.IsDisk() {
		t.Fatalf("empty endpoint should select disk: %+v", cfg)
	}
	if cfg.Dir != "/var/lib/nanoflux/files" || cfg.Bucket != "nanoflux" || cfg.UseSSL {
		t.Fatalf("disk config: %+v", cfg)
	}

	t.Setenv("NF_S3_ENDPOINT", "https://s3.us-east-1.amazonaws.com")
	t.Setenv("NF_S3_BUCKET", "mybucket")
	t.Setenv("NF_S3_ACCESS_KEY", "ak")
	t.Setenv("NF_S3_SECRET_KEY", "sk")
	t.Setenv("NF_S3_REGION", "us-west-2")
	cfg = ConfigFromEnv()
	if cfg.IsDisk() {
		t.Fatalf("an explicit endpoint should select S3: %+v", cfg)
	}
	if cfg.Endpoint != "https://s3.us-east-1.amazonaws.com" || !cfg.UseSSL ||
		cfg.Bucket != "mybucket" || cfg.AccessKey != "ak" || cfg.Region != "us-west-2" {
		t.Fatalf("configured: %+v", cfg)
	}
}
