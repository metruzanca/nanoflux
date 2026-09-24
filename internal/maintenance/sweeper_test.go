package maintenance

import (
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
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

func mustFeed(t *testing.T, s *store.Store, u store.User) store.Feed {
	t.Helper()
	a, err := s.Authors.Create(u.ID, "A", "", "")
	if err != nil {
		t.Fatalf("create author: %v", err)
	}
	f, err := s.Feeds.Create(u.ID, a.ID, "Feed", "https://x.dev/rss.xml", "", "", 900)
	if err != nil {
		t.Fatalf("create feed: %v", err)
	}
	return f
}

// TestSweepMarksStaleItems verifies Sweep only touches opted-in users' old
// unread items and leaves disabled users and recent items alone.
func TestSweepMarksStaleItems(t *testing.T) {
	s := newTestStore(t)
	alice, err := s.Users.Create("alice", "hash")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.Users.Create("bob", "hash")
	if err != nil {
		t.Fatal(err)
	}
	af := mustFeed(t, s, alice)
	bf := mustFeed(t, s, bob)

	old := db.FormatTime(time.Now().AddDate(0, 0, -40))
	recent := db.FormatTime(time.Now().AddDate(0, 0, -2))

	s.Items.Upsert(af.ID, store.Item{GUID: "a-old", PublishedAt: old, FetchedAt: db.Now()})
	s.Items.Upsert(af.ID, store.Item{GUID: "a-recent", PublishedAt: recent, FetchedAt: db.Now()})
	s.Items.Upsert(bf.ID, store.Item{GUID: "b-old", PublishedAt: old, FetchedAt: db.Now()})

	// Alice keeps the default 30 days; Bob disables the feature.
	if err := s.Users.SetAutoReadAfterDays(bob.ID, 0); err != nil {
		t.Fatal(err)
	}

	New(s, 0).Sweep()

	aliceUnread, _ := s.Items.CountUnread(alice.ID, 0)
	if aliceUnread != 1 {
		t.Fatalf("alice unread = %d; want 1 (recent only)", aliceUnread)
	}
	bobUnread, _ := s.Items.CountUnread(bob.ID, 0)
	if bobUnread != 1 {
		t.Fatalf("bob unread = %d; want 1 (disabled, untouched)", bobUnread)
	}
}
