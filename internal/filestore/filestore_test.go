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

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("S3_ENDPOINT", "")
	t.Setenv("S3_BUCKET", "")
	t.Setenv("S3_REGION", "")
	cfg := ConfigFromEnv()
	if cfg.Endpoint != "http://127.0.0.1:8333" || cfg.Bucket != "nanoflux" || cfg.UseSSL {
		t.Fatalf("default config: %+v", cfg)
	}
	if !IsLocalDefault(cfg) {
		t.Fatal("default config should be the local fallback")
	}

	t.Setenv("S3_ENDPOINT", "https://s3.us-east-1.amazonaws.com")
	t.Setenv("S3_BUCKET", "mybucket")
	t.Setenv("S3_ACCESS_KEY", "ak")
	t.Setenv("S3_SECRET_KEY", "sk")
	t.Setenv("S3_REGION", "us-west-2")
	cfg = ConfigFromEnv()
	if cfg.Endpoint != "https://s3.us-east-1.amazonaws.com" || !cfg.UseSSL ||
		cfg.Bucket != "mybucket" || cfg.AccessKey != "ak" || cfg.Region != "us-west-2" {
		t.Fatalf("configured: %+v", cfg)
	}
	if IsLocalDefault(cfg) {
		t.Fatal("an explicit endpoint should not be treated as the local fallback")
	}
}

func TestPortOf(t *testing.T) {
	if got := portOf("http://localhost:8333"); got != "8333" {
		t.Errorf("portOf = %q", got)
	}
	if got := portOf("http://localhost:9000"); got != "9000" {
		t.Errorf("portOf = %q", got)
	}
	if got := portOf("garbage"); got != "8333" {
		t.Errorf("portOf fallback = %q", got)
	}
}
