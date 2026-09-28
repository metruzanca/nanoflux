// Package demo provisions throwaway accounts for a public "marketing"
// deployment. When NF_DEMO_MODE is on, a visitor's POST /demo clones a curated
// admin seed account (NF_DEMO_USER) into an ephemeral user that expires after a
// fixed window; the session is then rejected, and a background pass deletes the
// account and its data.
//
// The account lifecycle is deliberately isolated from ordinary users: nothing
// here runs unless demo mode is enabled and a seed exists.
package demo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/filestore"
	"github.com/metruzanca/nanoflux/internal/store"
)

// cleanupInterval is how often expired ephemeral accounts are purged. It is a
// var so tests can shorten it.
var cleanupInterval = 10 * time.Minute

// Manager owns the demo seed and the ephemeral-account lifecycle.
type Manager struct {
	store    *store.Store
	files    filestore.Store
	seedID   int64
	seedName string
	ttl      time.Duration
	maxFeeds int
}

// New builds a Manager for a configured seed user. ttl <= 0 falls back to 2h and
// maxFeeds < 0 falls back to 5.
func New(st *store.Store, files filestore.Store, seedID int64, seedName string, ttl time.Duration, maxFeeds int) *Manager {
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	if maxFeeds < 0 {
		maxFeeds = 5
	}
	return &Manager{store: st, files: files, seedID: seedID, seedName: seedName, ttl: ttl, maxFeeds: maxFeeds}
}

// TTL is the lifetime of a provisioned demo session.
func (m *Manager) TTL() time.Duration { return m.ttl }

// Provision clones the seed into a fresh ephemeral account and returns it. The
// username is a random adjective-animal pair; on the rare collision it retries.
// The returned user is usable only through a session created by the caller — its
// password is random and never disclosed.
func (m *Manager) Provision(ctx context.Context) (store.User, error) {
	password, err := randomHex(24)
	if err != nil {
		return store.User{}, fmt.Errorf("demo password: %w", err)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return store.User{}, fmt.Errorf("demo password hash: %w", err)
	}
	expiresAt := db.FormatTime(time.Now().Add(m.ttl))

	var lastErr error
	for attempt := 0; attempt < 8; attempt++ {
		username, err := randomUsername()
		if err != nil {
			return store.User{}, err
		}
		u, err := m.store.CloneUser(m.seedID, username, hash, expiresAt)
		if err == nil {
			return u, nil
		}
		lastErr = err
		// A username collision is the only expected failure; retry with a new
		// name. Anything else is reported as-is.
		if !strings.Contains(err.Error(), "UNIQUE constraint failed: users.username") {
			return store.User{}, err
		}
	}
	return store.User{}, fmt.Errorf("demo provision: %w", lastErr)
}

// AddFeedLimitReached reports whether userID, if an ephemeral demo account, has
// already added the maximum number of extra feeds. It always returns false for a
// non-ephemeral user, so ordinary installs are unaffected. The allowance is the
// seed's current feed count plus maxFeeds, read live so an operator editing the
// seed changes it without a restart.
func (m *Manager) AddFeedLimitReached(userID int64) (bool, error) {
	ephemeral, _, err := m.store.Users.EphemeralStatus(userID)
	if err != nil {
		return false, err
	}
	if !ephemeral {
		return false, nil
	}
	seedCount, err := m.store.Feeds.CountForUser(m.seedID)
	if err != nil {
		return false, err
	}
	userCount, err := m.store.Feeds.CountForUser(userID)
	if err != nil {
		return false, err
	}
	return userCount >= seedCount+m.maxFeeds, nil
}

// Run purges expired demo accounts immediately and then every cleanup interval
// until ctx is done.
func (m *Manager) Run(ctx context.Context) {
	log.Info("demo cleanup starting", "interval", cleanupInterval)
	m.Cleanup(ctx)
	t := time.NewTicker(cleanupInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("demo cleanup stopped")
			return
		case <-t.C:
			m.Cleanup(ctx)
		}
	}
}

// Cleanup deletes every expired ephemeral account (purging its object-storage
// blobs best-effort first) and drops any lapsed sessions. One account's failure
// is logged and does not stop the rest.
func (m *Manager) Cleanup(ctx context.Context) {
	if err := m.store.Sessions.DeleteExpired(); err != nil {
		log.Error("demo cleanup: delete expired sessions", "err", err)
	}
	ids, err := m.store.Users.ExpiredEphemeral(db.Now())
	if err != nil {
		log.Error("demo cleanup: list expired users", "err", err)
		return
	}
	for _, id := range ids {
		if keys, err := m.store.Users.ListObjectKeys(id); err == nil {
			for _, k := range keys {
				if err := m.files.Delete(ctx, k); err != nil {
					log.Warn("demo cleanup: purge object", "key", k, "err", err)
				}
			}
		}
		if err := m.store.Users.Delete(id); err != nil {
			log.Error("demo cleanup: delete user", "user_id", id, "err", err)
			continue
		}
		log.Info("demo cleanup: deleted expired demo user", "user_id", id)
	}
}

// adjectives and animals build the random demo usernames. Kept short and safe to
// display; "adjective-animal" always has the same shape so the demo UI reads
// consistently.
var adjectives = []string{
	"brisk", "calm", "clever", "curious", "eager", "gentle", "happy", "jolly",
	"keen", "lively", "merry", "nimble", "placid", "quiet", "rapid", "sunny",
	"swift", "vivid", "witty", "zesty", "brave", "bright", "cosmic", "dapper",
}

var animals = []string{
	"badger", "beaver", "bison", "capybara", "cheetah", "crane", "dolphin",
	"falcon", "ferret", "fox", "gecko", "heron", "ibex", "koala", "lemur",
	"lynx", "marten", "meerkat", "otter", "panda", "puffin", "quail", "raven",
	"seal", "shrew", "tapir", "tern", "walrus", "weasel", "wombat",
}

// randomUsername returns an "adjective-animal" name, e.g. "swift-otter".
func randomUsername() (string, error) {
	a, err := randomIndex(len(adjectives))
	if err != nil {
		return "", err
	}
	n, err := randomIndex(len(animals))
	if err != nil {
		return "", err
	}
	return adjectives[a] + "-" + animals[n], nil
}

// randomIndex returns a uniform random int in [0, n).
func randomIndex(n int) (int, error) {
	if n <= 0 {
		return 0, fmt.Errorf("randomIndex: n must be positive")
	}
	var b [1]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		// Rejection sampling keeps the distribution uniform for non-power-of-two
		// ranges; n here is at most 30.
		if int(b[0]) < 256-(256%n) {
			return int(b[0]) % n, nil
		}
	}
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
