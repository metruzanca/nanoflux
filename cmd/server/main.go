package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/backup"
	"github.com/metruzanca/nanoflux/internal/cli"
	"github.com/metruzanca/nanoflux/internal/config"
	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/filestore"
	"github.com/metruzanca/nanoflux/internal/httpapi"
	"github.com/metruzanca/nanoflux/internal/poller"
	"github.com/metruzanca/nanoflux/internal/store"
)

// version is set at build time via ldflags (see .goreleaser.yaml).
var version = "dev"

// main dispatches between the server and the admin CLI. Running with no
// arguments (or `nanoflux server`) starts the web server — the docker image's
// ENTRYPOINT. Any other first argument routes to the CLI (e.g. `nanoflux user
// list`); unknown commands print usage instead of silently binding a port.
func main() {
	if len(os.Args) > 1 && os.Args[1] != "server" {
		os.Exit(cli.Run(version, os.Args[1:]))
	}
	runServer()
}

func runServer() {
	cfg := config.Load()
	setLogLevel(cfg.LogLevel)
	log.Info("nanoflux starting", "version", version)

	sqldb, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatal("open database", "err", err)
	}
	defer sqldb.Close()

	if err := db.Migrate(sqldb); err != nil {
		log.Fatal("migrate database", "err", err)
	}

	st := store.New(sqldb)
	bootstrapUser(st, cfg)
	a := auth.New(st)

	files, err := newFileStore(cfg)
	if err != nil {
		log.Fatal("file storage", "err", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Move any avatars/icons that predate object storage.
	if err := st.MigrateLegacyFiles(ctx, files); err != nil {
		log.Fatal("migrate legacy files", "err", err)
	}

	p := poller.New(st, cfg.PollInterval, cfg.PollWorkers)
	go p.Run(ctx)

	backupRunner := newBackupRunner(ctx, cfg, sqldb)
	if backupRunner != nil {
		go backupRunner.Run(ctx)
	}

	srv := &http.Server{
		Addr: cfg.Addr,
		Handler: func() http.Handler {
			h := httpapi.New(st, a, cfg, files)
			h.SetPoller(p)
			h.SetBackupRunner(backupRunner)
			return h.Handler()
		}(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("nanoflux server listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("server", "err", err)
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("shutdown", "err", err)
	}
}

// newFileStore builds the blob store for avatars and custom icons. When
// NF_S3_ENDPOINT is set it uses S3-compatible object storage; otherwise it
// falls back to a local disk directory (cfg.FileStoreDir). A configured-but-
// unreachable S3 endpoint is a hard error so file features are never silently
// broken.
func newFileStore(cfg config.Config) (filestore.Store, error) {
	fcfg := filestore.ConfigFromEnv()
	if fcfg.IsDisk() {
		log.Info("file storage", "local", cfg.FileStoreDir)
	} else {
		log.Info("file storage", "endpoint", fcfg.Endpoint, "bucket", fcfg.Bucket, "local", false)
	}
	return filestore.NewFromConfig(fcfg, cfg.FileStoreDir)
}

// newBackupRunner builds the automatic-backup runner from config, or returns
// nil when automatic backups are disabled. It selects the destination (local
// directory or S3) and includes the local file store only when blobs are not in
// object storage (S3 objects are the provider's job).
func newBackupRunner(ctx context.Context, cfg config.Config, sqldb *sql.DB) *backup.Runner {
	bc := cfg.Backup
	if !bc.Enabled() {
		return nil
	}
	var dest backup.Destination
	if bc.UsesS3() {
		d, err := backup.NewS3(ctx, backup.S3Options{
			Endpoint:  bc.S3.Endpoint,
			Bucket:    bc.S3.Bucket,
			AccessKey: bc.S3.AccessKey,
			SecretKey: bc.S3.SecretKey,
			Region:    bc.S3.Region,
			Prefix:    bc.S3.Prefix,
		})
		if err != nil {
			log.Fatal("backup destination", "err", err)
		}
		dest = d
		log.Info("automatic backups", "destination", "s3", "bucket", bc.S3.Bucket, "prefix", bc.S3.Prefix)
	} else {
		dest = &backup.LocalDestination{Dir: bc.Dir}
		log.Info("automatic backups", "destination", "local", "dir", bc.Dir)
	}
	// Only include the file store when blobs live on local disk; with S3 blobs
	// the provider backs them up.
	includeFiles := filestore.ConfigFromEnv().IsDisk()
	return backup.NewRunner(sqldb, backup.Config{
		Interval:     bc.Interval,
		Keep:         bc.Keep,
		FileStore:    cfg.FileStoreDir,
		IncludeFiles: includeFiles,
	}, dest)
}

// setLogLevel maps the NF_LOG_LEVEL value onto the logger.
func setLogLevel(level string) {
	switch level {
	case "debug":
		log.SetLevel(log.DebugLevel)
	case "warn":
		log.SetLevel(log.WarnLevel)
	case "error":
		log.SetLevel(log.ErrorLevel)
	default:
		log.SetLevel(log.InfoLevel)
	}
}

// bootstrapUser creates the first account from env when the database is empty.
// The first account is always an admin so the instance has someone who can
// manage users. With no env creds it falls back to a default admin/admin
// account so a fresh instance is immediately usable; the signup page lets
// other users register.
func bootstrapUser(st *store.Store, cfg config.Config) {
	n, err := st.Users.Count()
	if err != nil {
		log.Fatal("count users", "err", err)
	}
	if n > 0 {
		return
	}
	if cfg.BootstrapUser != "" && cfg.BootstrapPass != "" {
		hash, err := auth.HashPassword(cfg.BootstrapPass)
		if err != nil {
			log.Fatal("hash password", "err", err)
		}
		u, err := st.Users.Create(cfg.BootstrapUser, hash)
		if err != nil {
			log.Fatal("create bootstrap user", "err", err)
		}
		if err := st.Users.SetAdmin(u.ID, true); err != nil {
			log.Fatal("grant admin", "err", err)
		}
		log.Info("created bootstrap user", "username", cfg.BootstrapUser, "admin", true)
		return
	}
	hash, err := auth.HashPassword("admin")
	if err != nil {
		log.Fatal("hash password", "err", err)
	}
	u, err := st.Users.Create("admin", hash)
	if err != nil {
		log.Fatal("create default admin", "err", err)
	}
	if err := st.Users.SetAdmin(u.ID, true); err != nil {
		log.Fatal("grant admin", "err", err)
	}
	log.Warn("no users found: created default account admin/admin — change the password after logging in")
}
