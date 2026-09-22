package store

import (
	"errors"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
)

func mustFeedWithItem(t *testing.T, s *Store, u User, title, guid string) (Feed, Item) {
	t.Helper()
	a, err := s.Authors.Create(u.ID, "Blog", "", "", "")
	if err != nil {
		t.Fatalf("create author: %v", err)
	}
	f, err := s.Feeds.Create(u.ID, a.ID, title, "https://x.dev/rss.xml", "", "", 900)
	if err != nil {
		t.Fatalf("create feed: %v", err)
	}
	if _, err := s.Items.Upsert(f.ID, Item{GUID: guid, Title: title, Link: "https://x.dev/" + guid, FetchedAt: db.Now()}); err != nil {
		t.Fatalf("upsert item: %v", err)
	}
	id, err := s.Items.ByFeedGUID(f.ID, guid)
	if err != nil {
		t.Fatalf("find item: %v", err)
	}
	it, _ := s.Items.ByID(u.ID, id)
	return f, it
}

func TestListStore(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	other := mustUser(t, s, "bob")
	_, item1 := mustFeedWithItem(t, s, u, "One", "g1")
	_, item2 := mustFeedWithItem(t, s, u, "Two", "g2")

	l, err := s.Lists.Create(u.ID, "reading")
	if err != nil {
		t.Fatalf("create list: %v", err)
	}

	// List starts empty with no items.
	rows, _ := s.Lists.List(u.ID)
	if len(rows) != 1 || rows[0].ItemCount != 0 || rows[0].Name != "reading" {
		t.Fatalf("List: %+v", rows)
	}

	// Add items to the list.
	if err := s.Lists.AddItem(u.ID, l.ID, item1.ID); err != nil {
		t.Fatalf("add item1: %v", err)
	}
	if err := s.Lists.AddItem(u.ID, l.ID, item2.ID); err != nil {
		t.Fatalf("add item2: %v", err)
	}

	items, more, err := s.Lists.ItemList(u.ID, l.ID, 0, 10, false)
	if err != nil {
		t.Fatalf("ItemList: %v", err)
	}
	if more || len(items) != 2 {
		t.Fatalf("ItemList: more=%v items=%d", more, len(items))
	}
	if items[0].ID != item2.ID { // newest-added first
		t.Fatalf("ItemList order: got item %d first, want %d", items[0].ID, item2.ID)
	}

	// ItemListIDs reflects membership.
	ids, _ := s.Lists.ItemListIDs(u.ID, item1.ID)
	if len(ids) != 1 || ids[0] != l.ID {
		t.Fatalf("ItemListIDs(item1): %v", ids)
	}
	if ids, _ := s.Lists.ItemListIDs(u.ID, item2.ID); len(ids) != 1 {
		t.Fatalf("ItemListIDs(item2): %v", ids)
	}

	// Membership is user-scoped: bob's items can't join alice's list.
	if err := s.Lists.AddItem(other.ID, l.ID, item1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user AddItem should be ErrNotFound, got %v", err)
	}

	// Removing one item.
	if err := s.Lists.RemoveItem(u.ID, l.ID, item2.ID); err != nil {
		t.Fatalf("RemoveItem: %v", err)
	}
	items, _, _ = s.Lists.ItemList(u.ID, l.ID, 0, 10, false)
	if len(items) != 1 || items[0].ID != item1.ID {
		t.Fatalf("ItemList after remove: %+v", items)
	}
	if err := s.Lists.RemoveItem(other.ID, l.ID, item1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user RemoveItem should be ErrNotFound, got %v", err)
	}

	// Public listing ignores the user scope.
	pub, _, err := s.Lists.ItemListPublic(l.ID, 0, 10)
	if err != nil || len(pub) != 1 || pub[0].ID != item1.ID {
		t.Fatalf("ItemListPublic: %v %+v", err, pub)
	}

	// Share: token round-trips and clears.
	if err := s.Lists.SetShareToken(u.ID, l.ID, "tok-1"); err != nil {
		t.Fatalf("SetShareToken: %v", err)
	}
	got, _ := s.Lists.ByID(u.ID, l.ID)
	if got.ShareToken != "tok-1" {
		t.Fatalf("ShareToken after set: %q", got.ShareToken)
	}
	if pub, err := s.Lists.ByToken("tok-1"); err != nil || pub.ID != l.ID {
		t.Fatalf("ByToken: %v %+v", err, pub)
	}
	if _, err := s.Lists.ByToken("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ByToken unknown should be ErrNotFound, got %v", err)
	}
	// SetShare is idempotent.
	if tok, err := s.Lists.SetShare(u.ID, l.ID); err != nil || tok != "tok-1" {
		t.Fatalf("SetShare idempotent: %q %v", tok, err)
	}
	if err := s.Lists.SetShareToken(u.ID, l.ID, ""); err != nil {
		t.Fatalf("ClearShareToken: %v", err)
	}
	if got, _ := s.Lists.ByID(u.ID, l.ID); got.ShareToken != "" {
		t.Fatalf("ShareToken after clear: %q", got.ShareToken)
	}

	// Cross-user delete is not allowed; owner delete removes the list and its
	// memberships.
	if err := s.Lists.Delete(other.ID, l.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user Delete should be ErrNotFound, got %v", err)
	}
	if err := s.Lists.Delete(u.ID, l.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Lists.ByID(u.ID, l.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ByID after delete should be ErrNotFound, got %v", err)
	}
}

func TestFavoritesShareToken(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")

	tok, err := s.Users.ShareFavorites(u.ID)
	if err != nil {
		t.Fatalf("ShareFavorites: %v", err)
	}
	if tok == "" {
		t.Fatal("ShareFavorites returned empty token")
	}
	if got, _ := s.Users.FavoritesShareToken(u.ID); got != tok {
		t.Fatalf("FavoritesShareToken: %q, want %q", got, tok)
	}
	// Idempotent.
	if again, _ := s.Users.ShareFavorites(u.ID); again != tok {
		t.Fatalf("ShareFavorites not idempotent: %q vs %q", again, tok)
	}
	owner, err := s.Users.ByFavoritesShareToken(tok)
	if err != nil || owner.ID != u.ID {
		t.Fatalf("ByFavoritesShareToken: %v %+v", err, owner)
	}
	if _, err := s.Users.ByFavoritesShareToken("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ByFavoritesShareToken unknown should be ErrNotFound, got %v", err)
	}
	if err := s.Users.SetFavoritesShareToken(u.ID, ""); err != nil {
		t.Fatalf("clear favorites share: %v", err)
	}
	if got, _ := s.Users.FavoritesShareToken(u.ID); got != "" {
		t.Fatalf("FavoritesShareToken after clear: %q", got)
	}
}
