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
	"github.com/metruzanca/nanoflux/internal/demo"
	"github.com/metruzanca/nanoflux/internal/filestore"
	"github.com/metruzanca/nanoflux/internal/httpapi"
	"github.com/metruzanca/nanoflux/internal/maintenance"
	"github.com/metruzanca/nanoflux/internal/plugin"
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
	a.SetDemoMode(cfg.Demo.Enabled())

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

	// Merge the same reddit post stored once per subscription (a subreddit feed
	// and a user feed) into one item with shared read/favorite state, then
	// create the cross-feed unique index. Idempotent; a no-op after the first
	// run on an already-merged database.
	if rep, err := st.Items.MergeCrossFeedDuplicates(false); err != nil {
		log.Error("merge cross-feed duplicates", "err", err)
	} else if rep.ItemsMerged > 0 {
		log.Info("merged cross-feed duplicate items", "merged", rep.ItemsMerged, "groups", len(rep.Groups))
	}

	p := poller.New(st, cfg.PollInterval, cfg.PollWorkers)
	p.SetHostSpacing(cfg.PollHostSpacing)
	// One cooldown shared by the poller and the plugin host: a rate limit seen
	// by either paces both.
	cooldown := plugin.NewCooldown()
	p.SetHostCooler(cooldown)

	// Load feed plugins (native + external), route fetches through them, and
	// reconcile stored feeds against the loaded set. This must finish before the
	// poller starts: a feed due at boot would otherwise be fetched through the
	// generic parser while its plugin was still loading, and a plugin-only feed
	// would fail to parse and record a spurious "last poll failed".
	plugins := plugin.Setup(ctx, st, p.Client(), cfg.PluginsDir, cooldown)
	defer plugins.Close()

	// With the plugins loaded, hand the store their per-URL site rules so feed
	// create/edit and the canonicalize pass defer site-specific URL handling to
	// the owning plugin (e.g. reddit's redirect-free shape), and their view-time
	// item decoration so lists render plugin-owned attribution and card kinds.
	st.SetURLPolicy(plugin.NewStoreURLPolicy(plugins.Registry))
	st.SetItemDecorator(plugin.NewStoreDecorator(plugins.Registry))
	// A plugin may enrich a newly stored item's body (full text, translation,
	// transcript). The dispatcher groups a poll's items by matching plugin.
	p.SetItemEnricher(plugin.NewDispatcher(plugins.Registry, plugins.Hosts.For))
	// Rewrite stored feed URLs to their canonical form (e.g. old.reddit.com and
	// bare reddit.com -> www.reddit.com, /u/ -> /user/, a user's bare feed ->
	// /submitted.rss). Idempotent; fixes feeds added before canonicalization.
	if n, err := st.Feeds.CanonicalizeFeedURLs(); err != nil {
		log.Error("canonicalize feed urls", "err", err)
	} else if n > 0 {
		log.Info("canonicalized feed urls", "changed", n)
	}

	go p.Run(ctx)

	backupRunner := newBackupRunner(ctx, cfg, sqldb, version)
	if backupRunner != nil {
		go backupRunner.Run(ctx)
	}

	// Mark stale unread items read for users who opted in to auto-read.
	go maintenance.New(st, maintenance.DefaultInterval).Run(ctx)

	// Demo mode: clone a curated seed account for each visitor and purge expired
	// ones in the background. Built before the server closure so its manager can
	// be attached to the handler.
	var demoMgr *demo.Manager
	if cfg.Demo.Mode && cfg.Demo.User == "" {
		log.Fatal("NF_DEMO_MODE is on but NF_DEMO_USER is not set")
	}
	if cfg.Demo.Enabled() {
		seed, err := st.Users.ByUsername(cfg.Demo.User)
		if err != nil {
			log.Fatal("demo seed user not found", "user", cfg.Demo.User, "err", err)
		}
		if !seed.IsAdmin {
			log.Warn("demo seed user is not an admin; it will still be cloned", "user", cfg.Demo.User)
		}
		demoMgr = demo.New(st, files, seed.ID, seed.Username, cfg.Demo.TTL, cfg.Demo.MaxFeeds)
		go demoMgr.Run(ctx)
		log.Info("demo mode enabled", "seed", cfg.Demo.User, "ttl", cfg.Demo.TTL, "max_extra_feeds", cfg.Demo.MaxFeeds)
	}

	srv := &http.Server{
		Addr: cfg.Addr,
		Handler: func() http.Handler {
			h := httpapi.New(st, a, cfg, files)
			h.SetPoller(p)
			h.SetBackupRunner(backupRunner)
			h.SetPlugins(plugins.Registry, plugins.Hosts)
			h.SetDemo(demoMgr)
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
func newBackupRunner(ctx context.Context, cfg config.Config, sqldb *sql.DB, version string) *backup.Runner {
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
		Version:      version,
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
	n, err := st.Users.CountPersistent()
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
