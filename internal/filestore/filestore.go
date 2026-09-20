// Package filestore stores blobs (avatars, custom icons) in S3-compatible
// object storage. When no S3 endpoint is configured it defaults to a local
// SeaweedFS S3 gateway (http://localhost:8333), which runs in Allow-All mode
// without credentials.
package filestore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// maxBlob caps the size of objects this app reads.
const maxBlob = 16 << 20

// ErrNotFound is returned by Get for a missing key.
var ErrNotFound = errors.New("not found")

// Store is the blob interface the rest of the app depends on.
type Store interface {
	Put(ctx context.Context, key, contentType string, data []byte) error
	Get(ctx context.Context, key string) (contentType string, data []byte, err error)
	Delete(ctx context.Context, key string) error
	EnsureBucket(ctx context.Context) error
}

// Config describes either a local disk store or an S3-compatible endpoint.
// When Endpoint is empty the store is backed by the local disk directory Dir.
type Config struct {
	Endpoint  string
	Dir       string
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string
	UseSSL    bool
}

// IsDisk reports whether the config selects the local disk store (the default
// when no S3 endpoint is configured).
func (c Config) IsDisk() bool { return c.Endpoint == "" }

// ConfigFromEnv reads S3_* environment variables. With no S3_ENDPOINT the
// store is a local disk directory (Dir); with one, blobs go to S3-compatible
// object storage.
func ConfigFromEnv() Config {
	endpoint := os.Getenv("S3_ENDPOINT")
	return Config{
		Endpoint:  endpoint,
		Dir:       os.Getenv("RSS_FILE_STORE"),
		Bucket:    getenv("S3_BUCKET", "nanoflux"),
		AccessKey: os.Getenv("S3_ACCESS_KEY"),
		SecretKey: os.Getenv("S3_SECRET_KEY"),
		Region:    getenv("S3_REGION", "us-east-1"),
		UseSSL:    strings.HasPrefix(endpoint, "https://"),
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type s3Store struct {
	client *minio.Client
	bucket string
	region string
}

// New returns an S3-backed Store.
func New(cfg Config) (Store, error) {
	endpoint := strings.TrimPrefix(cfg.Endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("filestore: %w", err)
	}
	return &s3Store{client: client, bucket: cfg.Bucket, region: cfg.Region}, nil
}

func (s *s3Store) EnsureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("filestore: check bucket %q: %w", s.bucket, err)
	}
	if exists {
		return nil
	}
	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: s.region}); err != nil {
		return fmt.Errorf("filestore: create bucket %q: %w", s.bucket, err)
	}
	return nil
}

func (s *s3Store) Put(ctx context.Context, key, contentType string, data []byte) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("filestore: put %s: %w", key, err)
	}
	return nil
}

func (s *s3Store) Get(ctx context.Context, key string) (string, []byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return "", nil, fmt.Errorf("filestore: get %s: %w", key, err)
	}
	defer obj.Close()
	stat, err := obj.Stat()
	if err != nil {
		if minio.ToErrorResponse(err).StatusCode == 404 {
			return "", nil, ErrNotFound
		}
		return "", nil, fmt.Errorf("filestore: stat %s: %w", key, err)
	}
	data, err := io.ReadAll(io.LimitReader(obj, maxBlob))
	if err != nil {
		return "", nil, fmt.Errorf("filestore: read %s: %w", key, err)
	}
	return stat.ContentType, data, nil
}

func (s *s3Store) Delete(ctx context.Context, key string) error {
	err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("filestore: delete %s: %w", key, err)
	}
	return nil
}

// Memory is an in-memory Store for tests.
type Memory struct {
	mu sync.Mutex
	m  map[string]memObj
}

type memObj struct {
	ct   string
	data []byte
}

func NewMemory() *Memory { return &Memory{m: map[string]memObj{}} }

func (m *Memory) EnsureBucket(context.Context) error { return nil }

func (m *Memory) Put(_ context.Context, key, ct string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.m[key] = memObj{ct: ct, data: data}
	return nil
}

func (m *Memory) Get(_ context.Context, key string) (string, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.m[key]
	if !ok {
		return "", nil, ErrNotFound
	}
	return o.ct, o.data, nil
}

func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.m, key)
	return nil
}
