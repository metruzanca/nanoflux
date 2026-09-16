package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/metruzanca/rss/internal/auth"
	"github.com/metruzanca/rss/internal/config"
	"github.com/metruzanca/rss/internal/db"
	"github.com/metruzanca/rss/internal/httpapi"
	"github.com/metruzanca/rss/internal/poller"
	"github.com/metruzanca/rss/internal/store"
)

func main() {
	cfg := config.Load()

	sqldb, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer sqldb.Close()

	if err := db.Migrate(sqldb); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	st := store.New(sqldb)
	bootstrapUser(st, cfg)
	a := auth.New(st)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p := poller.New(st, cfg.PollInterval, cfg.PollWorkers)
	go p.Run(ctx)

	srv := &http.Server{
		Addr: cfg.Addr,
		Handler: func() http.Handler {
			h := httpapi.New(st, a, cfg)
			h.SetPoller(p)
			return h.Handler()
		}(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("rss server listening on %s", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// bootstrapUser creates the first account from env when the database is empty.
// With no env creds it falls back to a default admin/admin account so a fresh
// instance is immediately usable; the signup page lets other users register.
func bootstrapUser(st *store.Store, cfg config.Config) {
	n, err := st.Users.Count()
	if err != nil {
		log.Fatalf("count users: %v", err)
	}
	if n > 0 {
		return
	}
	if cfg.BootstrapUser != "" && cfg.BootstrapPass != "" {
		hash, err := auth.HashPassword(cfg.BootstrapPass)
		if err != nil {
			log.Fatalf("hash password: %v", err)
		}
		if _, err := st.Users.Create(cfg.BootstrapUser, hash); err != nil {
			log.Fatalf("create bootstrap user: %v", err)
		}
		log.Printf("created bootstrap user %q", cfg.BootstrapUser)
		return
	}
	hash, err := auth.HashPassword("admin")
	if err != nil {
		log.Fatalf("hash password: %v", err)
	}
	if _, err := st.Users.Create("admin", hash); err != nil {
		log.Fatalf("create default admin: %v", err)
	}
	log.Printf("no users found: created default account admin/admin — change the password after logging in")
}
