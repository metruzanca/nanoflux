package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/config"
	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/filestore"
	"github.com/metruzanca/nanoflux/internal/httpapi"
	"github.com/metruzanca/nanoflux/internal/poller"
	"github.com/metruzanca/nanoflux/internal/store"
)

// version is set at build time via ldflags (see .goreleaser.yaml).
var version = "dev"

func main() {
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

	files, stopFiles, err := newFileStore(filepath.Join(filepath.Dir(cfg.DBPath), "seaweedfs"))
	if err != nil {
		log.Fatal("file storage", "err", err)
	}
	if stopFiles != nil {
		defer stopFiles()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Move any avatars/icons that predate object storage.
	if err := st.MigrateLegacyFiles(ctx, files); err != nil {
		log.Fatal("migrate legacy files", "err", err)
	}

	p := poller.New(st, cfg.PollInterval, cfg.PollWorkers)
	go p.Run(ctx)

	srv := &http.Server{
		Addr: cfg.Addr,
		Handler: func() http.Handler {
			h := httpapi.New(st, a, cfg, files)
			h.SetPoller(p)
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

// newFileStore builds the blob store from S3_* env vars. When S3_ENDPOINT is
// not set it boots an in-process SeaweedFS cluster (master+volume+filer+S3)
// and points at it; a configured-but-unreachable endpoint is a hard error so
// file features are never silently broken.
func newFileStore(dataDir string) (filestore.Store, func(), error) {
	cfg := filestore.ConfigFromEnv()

	var stop func()
	if filestore.IsLocalDefault(cfg) {
		var err error
		stop, err = filestore.StartEmbedded(context.Background(), dataDir, cfg)
		if err != nil {
			return nil, nil, err
		}
	}

	files, err := filestore.New(cfg)
	if err != nil {
		return nil, nil, err
	}
	log.Info("file storage", "endpoint", cfg.Endpoint, "bucket", cfg.Bucket, "local", filestore.IsLocalDefault(cfg))

	bctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := files.EnsureBucket(bctx); err != nil {
		if stop != nil {
			stop()
		}
		return nil, nil, fmt.Errorf("reach %s (set S3_ENDPOINT): %w", cfg.Endpoint, err)
	}
	return files, stop, nil
}

// setLogLevel maps the RSS_LOG_LEVEL value onto the logger.
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
// With no env creds it falls back to a default admin/admin account so a fresh
// instance is immediately usable; the signup page lets other users register.
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
		if _, err := st.Users.Create(cfg.BootstrapUser, hash); err != nil {
			log.Fatal("create bootstrap user", "err", err)
		}
		log.Info("created bootstrap user", "username", cfg.BootstrapUser)
		return
	}
	hash, err := auth.HashPassword("admin")
	if err != nil {
		log.Fatal("hash password", "err", err)
	}
	if _, err := st.Users.Create("admin", hash); err != nil {
		log.Fatal("create default admin", "err", err)
	}
	log.Warn("no users found: created default account admin/admin — change the password after logging in")
}
