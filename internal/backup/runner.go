package backup

import (
	"bytes"
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/charmbracelet/log"
)

// Config describes a backup schedule and its destination.
type Config struct {
	Interval     time.Duration // 0 disables the runner
	Keep         int           // archives to retain; <= 0 keeps everything
	FileStore    string        // local file-store dir; included when IncludeFiles
	IncludeFiles bool          // false when blobs live in object storage
}

// Status is the outcome of the most recent run.
type Status struct {
	LastRun     time.Time
	LastError   string // "" on success
	LastArchive string
	LastBytes   int64
}

// Runner periodically snapshots the database (and local file store) to a
// Destination, pruning old archives. It is safe to trigger manually while the
// scheduled loop is running.
type Runner struct {
	db   *sql.DB
	cfg  Config
	dest Destination
	now  func() time.Time

	runMu  sync.Mutex // serializes snapshots (scheduled vs. manual)
	mu     sync.Mutex
	status Status
}

// NewRunner builds a Runner. cfg.Interval <= 0 yields a runner whose Run
// returns immediately (used when automatic backups are disabled), but whose
// Snapshot can still be called on demand.
func NewRunner(db *sql.DB, cfg Config, dest Destination) *Runner {
	return &Runner{db: db, cfg: cfg, dest: dest, now: time.Now}
}

// Status returns a copy of the most recent run's outcome.
func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// Run snapshots immediately, then every cfg.Interval until ctx is done. A
// non-positive interval disables scheduling entirely.
func (r *Runner) Run(ctx context.Context) {
	if r.cfg.Interval <= 0 {
		return
	}
	log.Info("backup runner starting", "interval", r.cfg.Interval, "keep", r.cfg.Keep)
	r.snapshotLogged(ctx)

	t := time.NewTicker(r.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("backup runner stopped")
			return
		case <-t.C:
			r.snapshotLogged(ctx)
		}
	}
}

func (r *Runner) snapshotLogged(ctx context.Context) {
	if _, err := r.Snapshot(ctx); err != nil {
		log.Error("backup failed", "err", err)
	}
}

// Snapshot writes one archive to the destination, prunes, and records the
// outcome. It returns the archive name written. Concurrent calls are
// serialized, so a manual run never overlaps a scheduled one.
func (r *Runner) Snapshot(ctx context.Context) (string, error) {
	r.runMu.Lock()
	defer r.runMu.Unlock()

	name := ArchiveName(r.now())

	var buf bytes.Buffer
	if err := WriteArchive(ctx, r.db, r.cfg.FileStore, r.cfg.IncludeFiles, &buf); err != nil {
		r.record(Status{LastRun: r.now(), LastError: err.Error()})
		return "", err
	}
	if err := r.dest.Put(ctx, name, bytes.NewReader(buf.Bytes())); err != nil {
		r.record(Status{LastRun: r.now(), LastError: err.Error()})
		return "", err
	}

	r.record(Status{LastRun: r.now(), LastArchive: name, LastBytes: int64(buf.Len())})

	if deleted, err := Prune(ctx, r.dest, r.cfg.Keep); err != nil {
		// The snapshot itself succeeded; a prune failure is a warning, not a
		// failed backup, but surface it so the admin sees it.
		log.Warn("backup prune", "err", err)
	} else if len(deleted) > 0 {
		log.Info("backup pruned", "removed", len(deleted))
	}
	log.Info("backup written", "archive", name, "bytes", buf.Len())
	return name, nil
}

func (r *Runner) record(s Status) {
	r.mu.Lock()
	r.status = s
	r.mu.Unlock()
}
