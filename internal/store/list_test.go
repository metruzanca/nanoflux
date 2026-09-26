package store

import (
	"errors"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
)

func mustFeedWithItem(t *testing.T, s *Store, u User, title, guid string) (Feed, Item) {
	t.Helper()
	a, err := s.Authors.Create(u.ID, "Blog", "", "")
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

func TestSavePage(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")

	// A feed item for contrast, so we can assert the saved page does not join
	// the ordinary streams.
	_, feedItem := mustFeedWithItem(t, s, u, "Feed post", "g1")

	l, err := s.Lists.Ensure(u.ID, "watch later")
	if err != nil {
		t.Fatalf("Ensure list: %v", err)
	}
	// Ensure is idempotent.
	again, err := s.Lists.Ensure(u.ID, "watch later")
	if err != nil || again.ID != l.ID {
		t.Fatalf("Ensure not idempotent: %+v %v", again, err)
	}

	itemID, inserted, err := s.SavePage(u.ID, l.ID, "page:example.com/a", "A page", "https://example.com/a", "desc", "https://example.com/a.png")
	if err != nil {
		t.Fatalf("SavePage: %v", err)
	}
	if !inserted {
		t.Fatal("first save should insert")
	}

	// The system feed is hidden from feed listings and feed counts.
	feeds, _ := s.Feeds.List(u.ID)
	if len(feeds) != 1 || feeds[0].Title != "Feed post" {
		t.Fatalf("Feeds.List leaked system feed: %+v", feeds)
	}
	authors, _ := s.Authors.List(u.ID)
	if len(authors) != 1 || authors[0].Name != "Blog" {
		t.Fatalf("Authors.List leaked system author: %+v", authors)
	}
	if _, err := s.Feeds.ByID(u.ID, mustSystemFeedID(t, s, u.ID)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("system feed must not be reachable via ByID, got %v", err)
	}
	if _, err := s.Authors.ByID(u.ID, mustSystemAuthorID(t, s, u.ID)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("system author must not be reachable via ByID, got %v", err)
	}

	// It appears in the list it was saved to, and in favorites once favorited.
	items, _, err := s.Lists.ItemList(u.ID, l.ID, 0, 10, false)
	if err != nil || len(items) != 1 || items[0].ID != itemID {
		t.Fatalf("ItemList: %v %+v", err, items)
	}
	if !items[0].FeedIsSystem {
		t.Fatal("saved-page item should carry FeedIsSystem")
	}
	if err := s.Items.SetFavorite(u.ID, itemID, true); err != nil {
		t.Fatalf("SetFavorite: %v", err)
	}
	favs, _ := s.Items.List(u.ID, ItemFilter{FavoritesOnly: true, Limit: 10})
	if len(favs) != 1 || favs[0].ID != itemID {
		t.Fatalf("favorites should include the saved page: %+v", favs)
	}

	// It is absent from the unread/read streams and their counts.
	unread, _ := s.Items.List(u.ID, ItemFilter{UnreadOnly: true, Limit: 10})
	for _, it := range unread {
		if it.ID == itemID {
			t.Fatal("saved page must not appear in the unread stream")
		}
	}
	read, _ := s.Items.List(u.ID, ItemFilter{ReadOnly: true, Limit: 10})
	for _, it := range read {
		if it.ID == itemID {
			t.Fatal("saved page must not appear in the read stream")
		}
	}
	if n, _ := s.Items.CountUnread(u.ID, 0); n != 1 {
		t.Fatalf("CountUnread = %d, want 1 (only the feed item)", n)
	}

	// Re-saving the same page is idempotent and reports no insert.
	itemID2, inserted2, err := s.SavePage(u.ID, l.ID, "page:example.com/a", "A page", "https://example.com/a", "", "")
	if err != nil {
		t.Fatalf("re-SavePage: %v", err)
	}
	if inserted2 || itemID2 != itemID {
		t.Fatalf("re-save should reuse the item: inserted=%v id=%d want %d", inserted2, itemID2, itemID)
	}
	// Re-saving does not blank stored metadata when the new fetch has none.
	if got, _ := s.Items.ByID(u.ID, itemID); got.Summary != "desc" {
		t.Fatalf("re-save blanked the summary: %q", got.Summary)
	}

	// A saved page survives list deletion (only its membership goes).
	if err := s.Lists.Delete(u.ID, l.ID); err != nil {
		t.Fatalf("delete list: %v", err)
	}
	if _, err := s.Items.ByID(u.ID, itemID); err != nil {
		t.Fatalf("saved page should survive list deletion: %v", err)
	}
	// The hidden feed remains, but is still not listed.
	if feeds, _ := s.Feeds.List(u.ID); len(feeds) != 1 {
		t.Fatalf("Feeds.List after delete: %+v", feeds)
	}
	_ = feedItem
}

func mustSystemFeedID(t *testing.T, s *Store, userID int64) int64 {
	t.Helper()
	f, err := s.EnsureSystemFeed(userID)
	if err != nil {
		t.Fatalf("EnsureSystemFeed: %v", err)
	}
	return f.ID
}

func mustSystemAuthorID(t *testing.T, s *Store, userID int64) int64 {
	t.Helper()
	a, err := s.EnsureSystemAuthor(userID)
	if err != nil {
		t.Fatalf("EnsureSystemAuthor: %v", err)
	}
	return a.ID
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
