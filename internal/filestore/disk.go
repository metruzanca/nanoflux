package filestore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// diskStore stores blobs as plain files under a root directory. It is the
// default when no S3 endpoint is configured, so avatars and custom icons work
// out of the box without any object-storage service.
type diskStore struct {
	root string
}

// NewDisk returns a Store backed by the directory at root. The directory is
// created lazily by EnsureBucket.
func NewDisk(root string) Store {
	return &diskStore{root: root}
}

func (d *diskStore) EnsureBucket(context.Context) error {
	return os.MkdirAll(d.root, 0o755)
}

func (d *diskStore) Put(_ context.Context, key, contentType string, data []byte) error {
	path, err := d.pathFor(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("filestore: mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("filestore: put %s: %w", key, err)
	}
	// Persist the content type alongside the bytes so Get can return it.
	if err := os.WriteFile(path+".ct", []byte(contentType), 0o644); err != nil {
		return fmt.Errorf("filestore: put %s: %w", key, err)
	}
	return nil
}

func (d *diskStore) Get(_ context.Context, key string) (string, []byte, error) {
	path, err := d.pathFor(key)
	if err != nil {
		return "", nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, ErrNotFound
		}
		return "", nil, fmt.Errorf("filestore: open %s: %w", key, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBlob))
	if err != nil {
		return "", nil, fmt.Errorf("filestore: read %s: %w", key, err)
	}
	ct, _ := os.ReadFile(path + ".ct")
	return string(ct), data, nil
}

func (d *diskStore) Delete(_ context.Context, key string) error {
	path, err := d.pathFor(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("filestore: delete %s: %w", key, err)
	}
	os.Remove(path + ".ct")
	return nil
}

// pathFor resolves a storage key to a file path under root, rejecting keys
// that would escape the root directory.
func (d *diskStore) pathFor(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("filestore: invalid key %q", key)
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("filestore: invalid key %q", key)
		}
	}
	path := filepath.Join(d.root, filepath.FromSlash(key))
	root := filepath.Clean(d.root)
	if path != root && !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return "", fmt.Errorf("filestore: invalid key %q", key)
	}
	return path, nil
}
