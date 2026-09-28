package store

import (
	"testing"
)

// TestCloneUser copies a seeded account and checks the invariants the demo layer
// depends on: no admin, ephemeral, feeds paused, parents remapped, state carried.
func TestCloneUser(t *testing.T) {
	s := newTestStore(t)
	seed := mustUser(t, s, "demo")

	a, err := s.Authors.Create(seed.ID, "Blog", "https://blog.example/avatar.png", "a blog")
	if err != nil {
		t.Fatalf("author: %v", err)
	}
	f, err := s.Feeds.Create(seed.ID, a.ID, "Blog feed", "https://blog.example/rss.xml", "https://blog.example", "", 900)
	if err != nil {
		t.Fatalf("feed: %v", err)
	}
	if _, err := s.Items.Upsert(f.ID, Item{
		GUID: "p1", Title: "Hello", Link: "https://blog.example/p1",
		Summary: "body", Categories: []string{"dev"}, FetchedAt: "2026-01-02 03:04:05",
	}); err != nil {
		t.Fatalf("item: %v", err)
	}
	itemID, err := s.Items.ByFeedIdentity(f.ID, "p1")
	if err != nil {
		t.Fatalf("item id: %v", err)
	}
	if err := s.Items.SetRead(seed.ID, itemID, true); err != nil {
		t.Fatalf("set read: %v", err)
	}
	if err := s.Items.SetFavorite(seed.ID, itemID, true); err != nil {
		t.Fatalf("set favorite: %v", err)
	}

	col, err := s.Collections.Create(seed.ID, "Tech")
	if err != nil {
		t.Fatalf("collection: %v", err)
	}
	if err := s.Collections.AddFeed(seed.ID, col.ID, f.ID); err != nil {
		t.Fatalf("add feed to collection: %v", err)
	}
	lst, err := s.Lists.Create(seed.ID, "reading")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := s.Lists.AddItem(seed.ID, lst.ID, itemID); err != nil {
		t.Fatalf("add item to list: %v", err)
	}

	clone, err := s.CloneUser(seed.ID, "swift-otter", "hash", "2026-01-01 00:00:00")
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if clone.Username != "swift-otter" || clone.IsAdmin {
		t.Fatalf("clone shape: %+v", clone)
	}
	ephemeral, expiresAt, err := s.Users.EphemeralStatus(clone.ID)
	if err != nil || !ephemeral || expiresAt != "2026-01-01 00:00:00" {
		t.Fatalf("ephemeral status: %v %v %q", err, ephemeral, expiresAt)
	}

	// The seed is untouched and still listed; the clone is filtered from List.
	users, err := s.Users.List()
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 1 || users[0].ID != seed.ID {
		t.Fatalf("ephemeral user should be hidden from List: %+v", users)
	}
	n, err := s.Users.CountPersistent()
	if err != nil || n != 1 {
		t.Fatalf("CountPersistent: %v %d", err, n)
	}

	// Feeds are cloned paused, with a new id and author.
	feeds, err := s.Feeds.List(clone.ID)
	if err != nil || len(feeds) != 1 {
		t.Fatalf("clone feeds: %v %+v", err, feeds)
	}
	cf := feeds[0]
	if cf.Enabled {
		t.Fatalf("cloned feed should start paused: %+v", cf)
	}
	if cf.ID == f.ID || cf.AuthorID == a.ID {
		t.Fatalf("cloned feed should have a remapped id/author: %+v", cf)
	}
	if cf.FeedURL != f.FeedURL || cf.Title != f.Title {
		t.Fatalf("cloned feed lost fields: %+v", cf)
	}

	// Items carry read/favorite state and the new owner.
	items, err := s.Items.List(clone.ID, ItemFilter{})
	if err != nil || len(items) != 1 {
		t.Fatalf("clone items: %v %+v", err, items)
	}
	ci := items[0]
	if ci.ID == itemID {
		t.Fatalf("cloned item should have a new id")
	}
	if !ci.Read || !ci.Favorite {
		t.Fatalf("cloned item should carry read/favorite: %+v", ci)
	}
	if ci.FeedID != cf.ID {
		t.Fatalf("cloned item should belong to the cloned feed: %+v", ci)
	}
	if len(ci.Categories) != 1 || ci.Categories[0] != "dev" {
		t.Fatalf("cloned item should carry categories: %+v", ci.Categories)
	}

	// Collections and lists are remapped and populated.
	cols, err := s.Collections.ListWithCounts(clone.ID)
	if err != nil || len(cols) != 1 || cols[0].FeedCount != 1 {
		t.Fatalf("clone collections: %v %+v", err, cols)
	}
	lists, err := s.Lists.List(clone.ID)
	if err != nil || len(lists) != 1 || lists[0].ItemCount != 1 {
		t.Fatalf("clone lists: %v %+v", err, lists)
	}

	// The seed keeps exactly its original rows.
	if n, _ := s.Feeds.CountForUser(seed.ID); n != 1 {
		t.Fatalf("seed feed count changed: %d", n)
	}
	if got, _ := s.Items.ByFeedIdentity(f.ID, "p1"); got != itemID {
		t.Fatalf("seed item identity changed: %d vs %d", got, itemID)
	}
}

func TestCloneUserNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CloneUser(999, "swift-otter", "hash", "2026-01-01 00:00:00"); err != ErrNotFound {
		t.Fatalf("clone missing seed: got %v, want ErrNotFound", err)
	}
}

func TestCloneUserUsernameCollision(t *testing.T) {
	s := newTestStore(t)
	seed := mustUser(t, s, "demo")
	mustUser(t, s, "taken")
	if _, err := s.CloneUser(seed.ID, "taken", "hash", "2026-01-01 00:00:00"); err == nil {
		t.Fatal("expected unique violation for a taken username")
	}
}
