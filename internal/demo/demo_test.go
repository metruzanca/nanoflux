package demo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/filestore"
	"github.com/metruzanca/nanoflux/internal/store"
)

func newDemoStore(t *testing.T) *store.Store {
	t.Helper()
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := db.Migrate(sqldb); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(sqldb)
}

func seedDemo(t *testing.T, s *store.Store, feedCount int) store.User {
	t.Helper()
	seed, err := s.Users.Create("demo", "hash")
	if err != nil {
		t.Fatalf("create seed: %v", err)
	}
	a, err := s.Authors.Create(seed.ID, "Blog", "", "")
	if err != nil {
		t.Fatalf("author: %v", err)
	}
	for i := 0; i < feedCount; i++ {
		if _, err := s.Feeds.Create(seed.ID, a.ID, "Feed", "https://x.dev/rss.xml", "", "", 900); err != nil {
			t.Fatalf("feed %d: %v", i, err)
		}
	}
	return seed
}

func TestProvision(t *testing.T) {
	s := newDemoStore(t)
	seed := seedDemo(t, s, 2)
	m := New(s, filestore.NewMemory(), seed.ID, seed.Username, time.Hour, 5)

	u, err := m.Provision(context.Background())
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if u.IsAdmin {
		t.Fatal("demo user must not be admin")
	}
	if u.Username == seed.Username {
		t.Fatal("demo user should have a random username")
	}
	ephemeral, expiresAt, err := s.Users.EphemeralStatus(u.ID)
	if err != nil || !ephemeral {
		t.Fatalf("ephemeral status: %v %v", err, ephemeral)
	}
	exp, err := db.ParseTime(expiresAt)
	if err != nil || time.Until(exp) <= 0 {
		t.Fatalf("expiry should be in the future: %q %v", expiresAt, err)
	}

	// Every cloned feed starts paused.
	feeds, err := s.Feeds.List(u.ID)
	if err != nil || len(feeds) != 2 {
		t.Fatalf("clone feeds: %v %d", err, len(feeds))
	}
	for _, f := range feeds {
		if f.Enabled {
			t.Fatalf("demo feed should be paused: %+v", f)
		}
	}
}

func TestAddFeedLimitReached(t *testing.T) {
	s := newDemoStore(t)
	seed := seedDemo(t, s, 2)
	m := New(s, filestore.NewMemory(), seed.ID, seed.Username, time.Hour, 5)

	u, err := m.Provision(context.Background())
	if err != nil {
		t.Fatalf("provision: %v", err)
	}

	// A fresh clone has seedCount feeds; the allowance is seedCount + 5.
	reached, err := m.AddFeedLimitReached(u.ID)
	if err != nil || reached {
		t.Fatalf("fresh clone should be under the limit: %v %v", err, reached)
	}

	// A non-ephemeral user is never limited.
	if reached, _ := m.AddFeedLimitReached(seed.ID); reached {
		t.Fatal("seed/ordinary user should never be limited")
	}

	// Add 5 feeds: the sixth should be over the limit.
	feeds, _ := s.Feeds.List(u.ID)
	a := feeds[0].AuthorID
	for i := 0; i < 5; i++ {
		if _, err := s.Feeds.Create(u.ID, a, "extra", "https://y.dev/rss.xml", "", "", 900); err != nil {
			t.Fatalf("extra feed %d: %v", i, err)
		}
	}
	reached, err = m.AddFeedLimitReached(u.ID)
	if err != nil || !reached {
		t.Fatalf("should be at the limit: %v %v", err, reached)
	}
}

func TestCleanupDeletesExpired(t *testing.T) {
	s := newDemoStore(t)
	seed := seedDemo(t, s, 1)
	m := New(s, filestore.NewMemory(), seed.ID, seed.Username, time.Hour, 5)

	// A clone that expired in the past.
	expired, err := s.CloneUser(seed.ID, "old-otter", "hash", "2000-01-01 00:00:00")
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	// A live clone.
	live, err := s.CloneUser(seed.ID, "new-otter", "hash", db.FormatTime(time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatalf("clone: %v", err)
	}

	m.Cleanup(context.Background())

	if _, err := s.Users.ByID(expired.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired demo should be deleted, got %v", err)
	}
	if _, err := s.Users.ByID(live.ID); err != nil {
		t.Fatalf("live demo should survive: %v", err)
	}
}

func TestRandomUsernameShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		u, err := randomUsername()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(u, "-") {
			t.Fatalf("username should be adjective-animal: %q", u)
		}
		seen[u] = true
	}
	if len(seen) < 10 {
		t.Fatalf("usernames should vary, got %d distinct", len(seen))
	}
}
