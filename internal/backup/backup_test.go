package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
)

// newTestDB opens a migrated database seeded with one user.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rss.db")
	sqldb, err := db.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := db.Migrate(sqldb); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := sqldb.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES ('alice', 'h', ?)`, db.Now()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return sqldb
}

// tarNames returns the entry names in a gzip+tar archive.
func tarNames(t *testing.T, data []byte) []string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer zr.Close()
	var names []string
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		names = append(names, hdr.Name)
	}
	return names
}

func TestWriteArchive(t *testing.T) {
	sqldb := newTestDB(t)

	fsDir := t.TempDir()
	os.MkdirAll(filepath.Join(fsDir, "avatars"), 0o755)
	os.WriteFile(filepath.Join(fsDir, "avatars", "1"), []byte("png"), 0o644)

	var buf bytes.Buffer
	if err := WriteArchive(context.Background(), sqldb, fsDir, true, &buf); err != nil {
		t.Fatalf("WriteArchive: %v", err)
	}
	names := tarNames(t, buf.Bytes())
	var hasDB, hasFile bool
	for _, n := range names {
		if n == "data/rss.db" {
			hasDB = true
		}
		if n == "filestore/avatars/1" {
			hasFile = true
		}
	}
	if !hasDB || !hasFile {
		t.Fatalf("archive entries = %v, want db + filestore", names)
	}

	// includeFiles=false omits the file store (S3-backed blob case).
	buf.Reset()
	if err := WriteArchive(context.Background(), sqldb, fsDir, false, &buf); err != nil {
		t.Fatalf("WriteArchive no files: %v", err)
	}
	for _, n := range tarNames(t, buf.Bytes()) {
		if n == "filestore/avatars/1" {
			t.Fatal("includeFiles=false should omit the file store")
		}
	}
}

func TestLocalDestinationRoundtrip(t *testing.T) {
	dir := t.TempDir()
	d := &LocalDestination{Dir: dir}
	ctx := context.Background()

	for _, name := range []string{"nanoflux-20260101-000000.tar.gz", "nanoflux-20260102-000000.tar.gz"} {
		if err := d.Put(ctx, name, bytes.NewReader([]byte("data-"+name))); err != nil {
			t.Fatalf("Put %s: %v", name, err)
		}
	}
	snaps, err := d.List(ctx)
	if err != nil || len(snaps) != 2 {
		t.Fatalf("List = %v, %v", snaps, err)
	}
	for _, s := range snaps {
		if s.Size == 0 {
			t.Fatalf("snapshot %s has zero size", s.Name)
		}
	}
	if err := d.Delete(ctx, "nanoflux-20260101-000000.tar.gz"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	snaps, _ = d.List(ctx)
	if len(snaps) != 1 {
		t.Fatalf("after delete = %v", snaps)
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	d := &LocalDestination{Dir: dir}
	ctx := context.Background()
	names := []string{
		"nanoflux-20260101-000000.tar.gz",
		"nanoflux-20260102-000000.tar.gz",
		"nanoflux-20260103-000000.tar.gz",
		"nanoflux-20260104-000000.tar.gz",
	}
	for _, n := range names {
		d.Put(ctx, n, bytes.NewReader([]byte("x")))
	}
	deleted, err := Prune(ctx, d, 2)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("deleted = %v, want 2", deleted)
	}
	remaining, _ := d.List(ctx)
	if len(remaining) != 2 {
		t.Fatalf("remaining = %d, want 2", len(remaining))
	}
	for _, s := range remaining {
		if s.Name < "nanoflux-20260103-000000.tar.gz" {
			t.Fatalf("pruned the newest, kept %s", s.Name)
		}
	}

	// keep <= 0 disables retention.
	if d2, _ := Prune(ctx, d, 0); len(d2) != 0 {
		t.Fatalf("keep=0 should prune nothing, got %v", d2)
	}
}

func TestRunnerSnapshot(t *testing.T) {
	sqldb := newTestDB(t)

	dir := t.TempDir()
	dest := &LocalDestination{Dir: dir}
	r := NewRunner(sqldb, Config{Keep: 1}, dest)
	r.now = func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) }

	name, err := r.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if name != "nanoflux-20260101-120000.tar.gz" {
		t.Fatalf("archive name = %q", name)
	}
	st := r.Status()
	if st.LastError != "" || st.LastArchive != name || st.LastBytes == 0 {
		t.Fatalf("status = %+v", st)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Fatalf("archive not written: %v", err)
	}

	// Retention: a second snapshot at a later time keeps only the newest.
	r.now = func() time.Time { return time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC) }
	if _, err := r.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot 2: %v", err)
	}
	snaps, _ := dest.List(context.Background())
	if len(snaps) != 1 || snaps[0].Name != "nanoflux-20260102-120000.tar.gz" {
		t.Fatalf("retention failed, have %+v", snaps)
	}
}

func TestArchiveName(t *testing.T) {
	got := ArchiveName(time.Date(2026, 9, 22, 16, 17, 43, 0, time.UTC))
	if got != "nanoflux-20260922-161743.tar.gz" {
		t.Fatalf("ArchiveName = %q", got)
	}
}

func TestS3KeyPrefix(t *testing.T) {
	base := &S3Destination{prefix: ""}
	if got := base.key("nanoflux-20260101-000000.tar.gz"); got != "nanoflux-20260101-000000.tar.gz" {
		t.Fatalf("no-prefix key = %q", got)
	}
	withPrefix := &S3Destination{prefix: "backups/nanoflux"}
	if got := withPrefix.key("nanoflux-20260101-000000.tar.gz"); got != "backups/nanoflux/nanoflux-20260101-000000.tar.gz" {
		t.Fatalf("prefixed key = %q", got)
	}
}

func TestS3RejectsUnreachable(t *testing.T) {
	// A configured-but-unreachable endpoint must fail fast rather than silently
	// dropping backups.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := NewS3(ctx, S3Options{Endpoint: "http://127.0.0.1:1", Bucket: "x"})
	if err == nil {
		t.Fatal("expected an error for an unreachable endpoint")
	}
}
