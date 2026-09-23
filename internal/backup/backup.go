// Package backup snapshots a nanoflux instance (the SQLite database and, when
// blobs live on local disk, the file store) into portable .tar.gz archives and
// ships them to a destination (a local directory or S3-compatible object
// storage). A Runner performs this on an interval and prunes old snapshots.
//
// The archive layout is shared with `nanoflux backup` and `make backup`
// (data/rss.db + filestore/…) so CLI and server archives are interchangeable.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ArchiveName returns the canonical name for a snapshot taken at t (UTC).
// Names sort chronologically, which retention relies on.
func ArchiveName(t time.Time) string {
	return "nanoflux-" + t.UTC().Format("20060102-150405") + ".tar.gz"
}

// WriteArchive writes a consistent backup archive to w: data/rss.db (a
// snapshot taken with VACUUM INTO, so it is consistent even while the server
// runs in WAL mode) and, when includeFiles is true, the local file store under
// filestore/. includeFiles is false when blobs live in object storage, where
// the provider is responsible for them.
func WriteArchive(ctx context.Context, db *sql.DB, fileStoreDir string, includeFiles bool, w io.Writer) error {
	tmp, err := os.MkdirTemp("", "nanoflux-snapshot-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	dataDir := filepath.Join(tmp, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	dbOut := filepath.Join(dataDir, "rss.db")
	if _, err := db.ExecContext(ctx, "VACUUM INTO '"+strings.ReplaceAll(dbOut, "'", "''")+"'"); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}

	zw := gzip.NewWriter(w)
	tw := tar.NewWriter(zw)
	if err := tarAddFile(tw, "data/rss.db", dbOut); err != nil {
		return err
	}
	if includeFiles && fileStoreDir != "" {
		if _, err := os.Stat(fileStoreDir); err == nil {
			if err := tarAddDir(tw, "filestore", fileStoreDir); err != nil {
				return err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

func tarAddFile(tw *tar.Writer, name, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if info.IsDir() {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

func tarAddDir(tw *tar.Writer, prefix, root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return tarAddFile(tw, filepath.Join(prefix, filepath.ToSlash(rel)), path)
	})
}
