package cli

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metruzanca/nanoflux/internal/backup"
	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/filestore"
	"github.com/metruzanca/nanoflux/internal/store"
)

// backupCmd writes a timestamped tarball to --out (default backups/) containing
// a consistent snapshot of the database (data/rss.db) and, when blobs live on
// local disk, the file store (filestore/). The layout matches `make backup` so
// CLI and compose archives are interchangeable. S3-backed stores are the
// provider's responsibility; the database is always included.
func backupCmd(st *store.Store, env Env, out, errOut io.Writer) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Snapshot the database and file store into backups/",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			ts := time.Now().UTC().Format("20060102-150405")
			archive := filepath.Join(dir, "nanoflux-"+ts+".tar.gz")

			f, err := os.Create(archive)
			if err != nil {
				return err
			}
			defer f.Close()

			fcfg := filestore.ConfigFromEnv()
			includeFiles := fcfg.IsDisk() && env.FileStoreDir != ""
			if err := backup.WriteArchive(cmd.Context(), st.DB(), env.FileStoreDir, includeFiles, f); err != nil {
				return err
			}
			if !includeFiles {
				fmt.Fprintln(errOut, "note: blob storage is S3-backed; the database was backed up, but S3 objects must be backed up by your provider")
			}
			fmt.Fprintf(out, "backup written to %s\n", archive)
			return nil
		},
	}
	cmd.Flags().StringVarP(&dir, "out", "o", "backups", "output directory")
	return cmd
}

// restoreCmd replaces the database (and disk file store) from a backup archive
// produced by `nanoflux backup` or `make backup`. It refuses to write over a
// database that is actively being written (server running) and validates the
// archive before touching the live files.
func restoreCmd(st *store.Store, env Env, out, errOut io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore <archive>",
		Short: "Restore the database and file store from a backup",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			archive := args[0]
			if _, err := os.Stat(archive); err != nil {
				return fmt.Errorf("backup %q: %w", archive, err)
			}
			if databaseLocked(env.DBPath) {
				return fmt.Errorf("database %q is in use — stop the instance first (make stop) and try again", env.DBPath)
			}

			tmp, err := os.MkdirTemp("", "nanoflux-restore-")
			if err != nil {
				return err
			}
			defer os.RemoveAll(tmp)

			hasDB, hasFilestore, err := extractArchive(archive, tmp)
			if err != nil {
				return err
			}
			if !hasDB {
				return fmt.Errorf("%s does not contain data/rss.db — not a nanoflux backup", archive)
			}

			// Validate the snapshot before replacing anything.
			restored, err := db.Open(filepath.Join(tmp, "data", "rss.db"))
			if err != nil {
				return fmt.Errorf("backup database does not open: %w", err)
			}
			defer restored.Close()
			if err := db.Migrate(restored); err != nil {
				return fmt.Errorf("backup database does not migrate: %w", err)
			}

			if err := swapDB(env.DBPath, filepath.Join(tmp, "data", "rss.db")); err != nil {
				return err
			}
			if hasFilestore && env.FileStoreDir != "" {
				if err := swapDir(env.FileStoreDir, filepath.Join(tmp, "filestore")); err != nil {
					return err
				}
			}
			fmt.Fprintf(out, "restore complete — start the instance (make start) when ready\n")
			return nil
		},
	}
	return cmd
}

// databaseLocked reports whether another process holds a write lock on the
// database. In WAL mode an idle server holds no lock, so this is a best-effort
// guard — restoring into a running instance is documented as unsupported.
func databaseLocked(path string) bool {
	if path == "" || path == ":memory:" {
		return false
	}
	sqldb, err := sql.Open("sqlite", path)
	if err != nil {
		return false
	}
	defer sqldb.Close()
	sqldb.SetMaxOpenConns(1)
	if _, err := sqldb.Exec("PRAGMA busy_timeout=500"); err != nil {
		return false
	}
	if _, err := sqldb.Exec("BEGIN IMMEDIATE"); err != nil {
		return true
	}
	sqldb.Exec("ROLLBACK")
	return false
}

// swapDB atomically replaces the database at path (dropping stale -wal/-shm
// sidecars) with a snapshot at src. src commonly lives on a different
// filesystem than the database (e.g. the container's /tmp vs a mounted data
// volume), so the snapshot is first copied to a sibling temp file in the
// database's own directory, then renamed into place — os.Rename alone would
// fail with EXDEV across devices.
func swapDB(path, src string) error {
	os.Remove(path + "-wal")
	os.Remove(path + "-shm")

	tmp := path + ".new"
	os.Remove(tmp)
	if err := copyFile(src, tmp); err != nil {
		return err
	}

	backup := path + ".old"
	os.Remove(backup)
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, backup); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Rename(backup, path)
		os.Remove(tmp)
		return err
	}
	os.Remove(backup)
	return nil
}

// copyFile copies src to dst, creating dst if needed.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// swapDir replaces the contents of dir with the contents of src.
func swapDir(dir, src string) error {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return copyDir(src, dir)
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}

// extractArchive unpacks a backup tarball into dir, returning whether it
// contained data/rss.db and filestore entries. It refuses to write outside
// dir (path-traversal guard).
func extractArchive(archive, dir string) (hasDB, hasFilestore bool, err error) {
	f, err := os.Open(archive)
	if err != nil {
		return false, false, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return false, false, fmt.Errorf("not a gzip archive: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return hasDB, hasFilestore, err
		}
		name := filepath.Clean(hdr.Name)
		if name == "." || strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return hasDB, hasFilestore, fmt.Errorf("archive contains unsafe path %q", hdr.Name)
		}
		target := filepath.Join(dir, name)
		if !strings.HasPrefix(target, filepath.Clean(dir)+string(filepath.Separator)) {
			return hasDB, hasFilestore, fmt.Errorf("archive path escapes %q", hdr.Name)
		}
		switch {
		case name == "data/rss.db":
			hasDB = true
		case strings.HasPrefix(name, "filestore/"):
			hasFilestore = true
		}
		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return hasDB, hasFilestore, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return hasDB, hasFilestore, err
		}
		w, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return hasDB, hasFilestore, err
		}
		if _, err := io.Copy(w, tr); err != nil {
			w.Close()
			return hasDB, hasFilestore, err
		}
		w.Close()
	}
	return hasDB, hasFilestore, nil
}
