package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Snapshot describes one stored backup archive.
type Snapshot struct {
	Name     string
	Size     int64
	Modified time.Time
}

// Destination stores and enumerates backup archives. Implementations must make
// Put atomic enough that a failed upload never leaves a partial archive that
// List would report (local writes to a temp file then renames; S3 uploads are
// atomic per object).
type Destination interface {
	Put(ctx context.Context, name string, r io.Reader) error
	List(ctx context.Context) ([]Snapshot, error)
	Delete(ctx context.Context, name string) error
}

// LocalDestination writes archives into a directory on disk.
type LocalDestination struct {
	Dir string
}

func (d *LocalDestination) Put(_ context.Context, name string, r io.Reader) error {
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return err
	}
	// Write to a sibling temp file, then rename: a reader that fails midway
	// never leaves a corrupt archive that List would pick up.
	tmp, err := os.CreateTemp(d.Dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("backup: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return fmt.Errorf("backup: write %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("backup: close %s: %w", name, err)
	}
	if err := os.Rename(tmpName, filepath.Join(d.Dir, name)); err != nil {
		return fmt.Errorf("backup: rename %s: %w", name, err)
	}
	return nil
}

func (d *LocalDestination) List(_ context.Context) ([]Snapshot, error) {
	entries, err := os.ReadDir(d.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("backup: list %s: %w", d.Dir, err)
	}
	var out []Snapshot
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Snapshot{Name: e.Name(), Size: info.Size(), Modified: info.ModTime()})
	}
	return out, nil
}

func (d *LocalDestination) Delete(_ context.Context, name string) error {
	if err := os.Remove(filepath.Join(d.Dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("backup: delete %s: %w", name, err)
	}
	return nil
}

// Prune deletes the oldest archives beyond keep, newest-first. keep <= 0 is a
// no-op (retention disabled). It returns the names that were deleted.
func Prune(ctx context.Context, dest Destination, keep int) ([]string, error) {
	if keep <= 0 {
		return nil, nil
	}
	snaps, err := dest.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(snaps) <= keep {
		return nil, nil
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].Name > snaps[j].Name })
	var deleted []string
	for _, s := range snaps[keep:] {
		if err := dest.Delete(ctx, s.Name); err != nil {
			return deleted, err
		}
		deleted = append(deleted, s.Name)
	}
	return deleted, nil
}
