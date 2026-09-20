package store

import (
	"errors"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := db.Migrate(sqldb); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(sqldb)
}

func mustUser(t *testing.T, s *Store, username string) User {
	t.Helper()
	u, err := s.Users.Create(username, "hash")
	if err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return u
}

func TestUserStore(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")

	got, err := s.Users.ByID(u.ID)
	if err != nil || got.Username != "alice" {
		t.Fatalf("ByID: %v %+v", err, got)
	}
	got, err = s.Users.ByUsername("alice")
	if err != nil || got.ID != u.ID {
		t.Fatalf("ByUsername: %v %+v", err, got)
	}
	if _, err := s.Users.Create("alice", "x"); !errors.Is(err, nil) && !isUniqueErr(err) {
		t.Fatalf("expected unique violation, got %v", err)
	}
	n, err := s.Users.Count()
	if err != nil || n != 1 {
		t.Fatalf("Count: %v %d", err, n)
	}
	if _, err := s.Users.ByID(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func isUniqueErr(err error) bool {
	return err != nil
}

func TestSessionStore(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")

	future := time.Now().UTC().Add(time.Hour).Format("2006-01-02 15:04:05")
	if err := s.Sessions.Create(u.ID, "token1", future); err != nil {
		t.Fatalf("create session: %v", err)
	}
	got, err := s.Sessions.UserByToken("token1")
	if err != nil || got.ID != u.ID {
		t.Fatalf("UserByToken: %v %+v", err, got)
	}

	if err := s.Sessions.Delete("token1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Sessions.UserByToken("token1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestAuthorFeedFlow(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")

	a, err := s.Authors.Create(u.ID, "Metru", "https://metru.dev", "", "author")
	if err != nil {
		t.Fatalf("create author: %v", err)
	}
	authors, err := s.Authors.List(u.ID)
	if err != nil || len(authors) != 1 {
		t.Fatalf("List authors: %v %d", err, len(authors))
	}

	f, err := s.Feeds.Create(u.ID, a.ID, "Metru's blog", "https://metru.dev/rss.xml", "https://metru.dev", "", 900)
	if err != nil {
		t.Fatalf("create feed: %v", err)
	}
	feeds, err := s.Feeds.List(u.ID)
	if err != nil || len(feeds) != 1 || feeds[0].AuthorID != a.ID {
		t.Fatalf("List feeds: %v %d", err, len(feeds))
	}
	byAuthor, err := s.Feeds.ListByAuthor(u.ID, a.ID)
	if err != nil || len(byAuthor) != 1 {
		t.Fatalf("ListByAuthor: %v %d", err, len(byAuthor))
	}

	// Deleting an author cascades to their feeds and items.
	s.Items.Upsert(f.ID, Item{GUID: "g", Title: "t", Link: "https://metru.dev/1", FetchedAt: db.Now()})
	if err := s.Authors.Delete(u.ID, a.ID); err != nil {
		t.Fatalf("author delete should cascade: %v", err)
	}
	feeds, _ = s.Feeds.List(u.ID)
	if len(feeds) != 0 {
		t.Fatalf("author delete should cascade feeds, got %d", len(feeds))
	}
	n, _ := s.Items.CountUnread(u.ID, 0)
	if n != 0 {
		t.Fatalf("author delete should cascade items, got %d unread", n)
	}

	// Authorless feeds are allowed and re-scanned as 0.
	f2, err := s.Feeds.Create(u.ID, 0, "authorless", "https://x.dev/rss.xml", "", "", 900)
	if err != nil {
		t.Fatalf("create authorless feed: %v", err)
	}
	got, err := s.Feeds.ByID(u.ID, f2.ID)
	if err != nil || got.AuthorID != 0 {
		t.Fatalf("authorless feed scan: %v %+v", err, got)
	}
}

func TestItemsFlow(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://metru.dev/rss.xml", "", "", 900)

	base := Item{GUID: "g1", Title: "One", Link: "https://metru.dev/1", FetchedAt: db.Now()}
	inserted, err := s.Items.Upsert(f.ID, base)
	if err != nil || !inserted {
		t.Fatalf("upsert new: %v %v", inserted, err)
	}
	// Duplicate guid -> no insert.
	inserted, err = s.Items.Upsert(f.ID, base)
	if err != nil || inserted {
		t.Fatalf("upsert dup: %v %v", inserted, err)
	}

	second := base
	second.GUID, second.Title = "g2", "Two"
	s.Items.Upsert(f.ID, second)

	items, err := s.Items.List(u.ID, ItemFilter{})
	if err != nil || len(items) != 2 {
		t.Fatalf("List: %v %d", err, len(items))
	}
	unread, _ := s.Items.List(u.ID, ItemFilter{UnreadOnly: true})
	if len(unread) != 2 {
		t.Fatalf("expected 2 unread, got %d", len(unread))
	}
	n, err := s.Items.CountUnread(u.ID, 0)
	if err != nil || n != 2 {
		t.Fatalf("CountUnread: %v %d", err, n)
	}

	if err := s.Items.SetRead(u.ID, items[0].ID, true); err != nil {
		t.Fatalf("SetRead: %v", err)
	}
	n, _ = s.Items.CountUnread(u.ID, 0)
	if n != 1 {
		t.Fatalf("expected 1 unread after SetRead, got %d", n)
	}
	if err := s.Items.MarkAllRead(u.ID, 0); err != nil {
		t.Fatalf("MarkAllRead: %v", err)
	}
	n, _ = s.Items.CountUnread(u.ID, 0)
	if n != 0 {
		t.Fatalf("expected 0 unread after MarkAllRead, got %d", n)
	}
}

func TestCollectionFlow(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "Metru", "", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "Blog", "https://metru.dev/rss.xml", "", "", 900)

	c, err := s.Collections.Create(u.ID, "Read later")
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	if err := s.Collections.AddFeed(u.ID, c.ID, f.ID); err != nil {
		t.Fatalf("AddFeed: %v", err)
	}
	feeds, err := s.Collections.Feeds(u.ID, c.ID)
	if err != nil || len(feeds) != 1 {
		t.Fatalf("collection feeds: %v %d", err, len(feeds))
	}

	// AddFeed must reject a feed owned by another user.
	other := mustUser(t, s, "bob")
	a2, _ := s.Authors.Create(other.ID, "Bob", "", "", "")
	f2, _ := s.Feeds.Create(other.ID, a2.ID, "Bobs", "https://bob.dev/rss.xml", "", "", 900)
	if err := s.Collections.AddFeed(u.ID, c.ID, f2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound cross-user AddFeed, got %v", err)
	}

	// Items filterable by collection.
	base := Item{GUID: "g1", Title: "One", Link: "https://metru.dev/1", FetchedAt: db.Now()}
	s.Items.Upsert(f.ID, base)
	byCol, err := s.Items.List(u.ID, ItemFilter{CollectionID: c.ID})
	if err != nil || len(byCol) != 1 {
		t.Fatalf("items in collection: %v %d", err, len(byCol))
	}
	if byCol[0].AuthorName != "Metru" {
		t.Fatalf("expected author join, got %q", byCol[0].AuthorName)
	}

	if err := s.Collections.RemoveFeed(u.ID, c.ID, f.ID); err != nil {
		t.Fatalf("RemoveFeed: %v", err)
	}
	if err := s.Collections.Delete(u.ID, c.ID); err != nil {
		t.Fatalf("delete collection: %v", err)
	}
}

func TestFeedIsolation(t *testing.T) {
	s := newTestStore(t)
	u1 := mustUser(t, s, "alice")
	u2 := mustUser(t, s, "bob")
	a1, _ := s.Authors.Create(u1.ID, "A", "", "", "")
	f1, _ := s.Feeds.Create(u1.ID, a1.ID, "A's feed", "https://a.dev/rss.xml", "", "", 900)
	s.Items.Upsert(f1.ID, Item{GUID: "g", Title: "t", FetchedAt: db.Now()})

	// Bob sees nothing of Alice's data.
	feeds, _ := s.Feeds.List(u2.ID)
	if len(feeds) != 0 {
		t.Fatalf("bob sees alice's feeds")
	}
	items, _ := s.Items.List(u2.ID, ItemFilter{})
	if len(items) != 0 {
		t.Fatalf("bob sees alice's items")
	}
	if _, err := s.Feeds.ByID(u2.ID, f1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound cross-user feed read, got %v", err)
	}
	if err := s.Feeds.Delete(u2.ID, f1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound cross-user feed delete, got %v", err)
	}
}

func TestListDue(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "A", "", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "feed", "https://a.dev/rss.xml", "", "", 900)

	// Never polled -> due.
	due, err := s.Feeds.ListDue(db.Now())
	if err != nil || len(due) != 1 {
		t.Fatalf("ListDue never polled: %v %d", err, len(due))
	}

	// Just polled -> not due.
	if err := s.Feeds.SetPollMeta(f.ID, "", "", db.Now()); err != nil {
		t.Fatal(err)
	}
	due, _ = s.Feeds.ListDue(db.Now())
	if len(due) != 0 {
		t.Fatalf("ListDue should be empty after fresh poll")
	}

	// Disabled feeds are never due.
	s.Feeds.Update(u.ID, f.ID, a.ID, "feed", "https://a.dev/rss.xml", "", "", 900, false)
	if err := s.Feeds.SetPollMeta(f.ID, "", "", ""); err != nil {
		t.Fatal(err)
	}
	due, _ = s.Feeds.ListDue(db.Now())
	if len(due) != 0 {
		t.Fatalf("ListDue should skip disabled feeds")
	}
}
