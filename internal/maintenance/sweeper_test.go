package maintenance

import (
	"errors"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

func sweepItem(t *testing.T, s *store.Store, feedID int64, guid string, published string) int64 {
	t.Helper()
	res, err := s.Items.UpsertMany(feedID, []store.Item{{
		GUID: guid, Title: guid, PublishedAt: published, FetchedAt: db.Now(),
	}})
	if err != nil {
		t.Fatalf("upsert %s: %v", guid, err)
	}
	return res[0].ItemID
}

func setReadAt(t *testing.T, s *store.Store, itemID int64, at string) {
	t.Helper()
	if _, err := s.DB().Exec(`UPDATE items SET read = 1, read_at = ? WHERE id = ?`, at, itemID); err != nil {
		t.Fatalf("set read_at: %v", err)
	}
}

func itemExists(t *testing.T, s *store.Store, userID, itemID int64) bool {
	t.Helper()
	_, err := s.Items.ByID(userID, itemID)
	if err == nil {
		return true
	}
	if errors.Is(err, store.ErrNotFound) {
		return false
	}
	t.Fatalf("item lookup: %v", err)
	return false
}

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

// TestSweepDeletesOldReadItems verifies the retention sweep removes a read item
// once its read_at passes the delete window, while an item auto-read in the
// same pass keeps its fresh read_at and survives.
func TestSweepDeletesOldReadItems(t *testing.T) {
	s := newTestStore(t)
	alice, err := s.Users.Create("alice", "hash")
	if err != nil {
		t.Fatal(err)
	}
	f := mustFeed(t, s, alice)
	if err := s.Users.SetAutoReadAfterDays(alice.ID, 30); err != nil {
		t.Fatal(err)
	}
	if err := s.Users.SetAutoDeleteAfterDays(alice.ID, 30); err != nil {
		t.Fatal(err)
	}

	old := db.FormatTime(time.Now().AddDate(0, 0, -40))
	oldRead := sweepItem(t, s, f.ID, "old-read", old)
	justRead := sweepItem(t, s, f.ID, "just-read", old)
	recentRead := sweepItem(t, s, f.ID, "recent-read", db.Now())
	protected := sweepItem(t, s, f.ID, "protected", old)

	setReadAt(t, s, oldRead, old)
	setReadAt(t, s, recentRead, db.FormatTime(time.Now().AddDate(0, 0, -2)))
	setReadAt(t, s, protected, old)
	if err := s.Items.SetFavorite(alice.ID, protected, true); err != nil {
		t.Fatal(err)
	}

	New(s, 0).Sweep()

	if itemExists(t, s, alice.ID, oldRead) {
		t.Fatalf("old read item should have been deleted")
	}
	if !itemExists(t, s, alice.ID, justRead) {
		t.Fatalf("item auto-read in this pass should keep its fresh read_at")
	}
	if !itemExists(t, s, alice.ID, recentRead) {
		t.Fatalf("recently read item should survive")
	}
	if !itemExists(t, s, alice.ID, protected) {
		t.Fatalf("favorited item should never be deleted")
	}
}

// TestSweepHonorsAuthorOverride verifies an author's window applies even when
// the account window is off, and that other authors keep inheriting the off
// account setting.
func TestSweepHonorsAuthorOverride(t *testing.T) {
	s := newTestStore(t)
	alice, err := s.Users.Create("alice", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Users.SetAutoReadAfterDays(alice.ID, 0); err != nil {
		t.Fatal(err)
	}
	af := mustFeed(t, s, alice)
	bf := mustFeed(t, s, alice)

	old := db.FormatTime(time.Now().AddDate(0, 0, -10))
	s.Items.Upsert(af.ID, store.Item{GUID: "a-old", PublishedAt: old, FetchedAt: db.Now()})
	s.Items.Upsert(bf.ID, store.Item{GUID: "b-old", PublishedAt: old, FetchedAt: db.Now()})

	three := 3
	if err := s.Authors.SetAutoReadAfterDays(alice.ID, af.AuthorID, &three); err != nil {
		t.Fatal(err)
	}
	New(s, 0).Sweep()

	if unread, _ := s.Items.CountUnread(alice.ID, af.ID); unread != 0 {
		t.Fatalf("override author unread = %d; want 0 (3-day window)", unread)
	}
	if unread, _ := s.Items.CountUnread(alice.ID, bf.ID); unread != 1 {
		t.Fatalf("inheriting author unread = %d; want 1 (account off)", unread)
	}
}
