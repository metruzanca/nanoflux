package httpapi

import (
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// addSearchItem stores one item with a tag for the global list tests.
func addSearchItem(t *testing.T, s *Server, userID, feedID int64, guid, title, tag string) store.Item {
	t.Helper()
	if _, err := s.store.Items.Upsert(feedID, store.Item{
		GUID: guid, Title: title, Link: "https://example.test/" + guid,
		FetchedAt: db.Now(), Categories: []string{tag},
	}); err != nil {
		t.Fatal(err)
	}
	id, err := s.store.Items.ByFeedIdentity(feedID, guid)
	if err != nil {
		t.Fatal(err)
	}
	it, err := s.store.Items.ByID(userID, id)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func TestSearchPageTabsAndFilters(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Example", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Example", "https://example.test/rss.xml", "", "", 900)

	golang := addSearchItem(t, s, u.ID, f.ID, "g1", "Golang post", "golang")
	rust := addSearchItem(t, s, u.ID, f.ID, "g2", "Rust post", "rust")

	body := doGet(h, "/search", cookie).Body.String()
	for _, want := range []string{"Golang post", "Rust post", "unread (2)", "read (0)", "favorites (0)", `id="nav-search"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("/search missing %q: %s", want, body)
		}
	}

	// A tag filter narrows the list and leaves a removable chip.
	body = doGet(h, "/search?f="+url.QueryEscape("tag:show:golang"), cookie).Body.String()
	if !strings.Contains(body, "Golang post") || strings.Contains(body, "Rust post") {
		t.Fatalf("tag filter should keep only the golang post: %s", body)
	}
	if !strings.Contains(body, "1 matching") || !strings.Contains(body, "golang") {
		t.Fatalf("filtered list should show its chip and matching count: %s", body)
	}

	// Favorites and read are their own tabs.
	if err := s.store.Items.SetFavorite(u.ID, rust.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Items.SetRead(u.ID, golang.ID, true); err != nil {
		t.Fatal(err)
	}
	fav := doGet(h, "/search?tab=favorites", cookie).Body.String()
	if !strings.Contains(fav, "Rust post") || strings.Contains(fav, "Golang post") {
		t.Fatalf("favorites tab should show only the favorite: %s", fav)
	}
	read := doGet(h, "/search/items?tab=read&sort=newest", cookie).Body.String()
	if !strings.Contains(read, "Golang post") || strings.Contains(read, "Rust post") {
		t.Fatalf("read tab fragment should show only the read item: %s", read)
	}
}

func TestFavoritesBookmarksAndHistoryFilters(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Example", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Example", "https://example.test/rss.xml", "", "", 900)

	golang := addSearchItem(t, s, u.ID, f.ID, "f1", "Golang favorite", "golang")
	rust := addSearchItem(t, s, u.ID, f.ID, "f2", "Rust favorite", "rust")
	book := addSearchItem(t, s, u.ID, f.ID, "f3", "Golang bookmark", "golang")

	for _, id := range []int64{golang.ID, rust.ID} {
		if err := s.store.Items.SetFavorite(u.ID, id, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.Items.SetBookmark(u.ID, book.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{golang.ID, rust.ID} {
		if err := s.store.Items.SetRead(u.ID, id, true); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		path string
		want string
		drop string
	}{
		{"/favorites?f=" + url.QueryEscape("tag:show:golang"), "Golang favorite", "Rust favorite"},
		{"/bookmarks?f=" + url.QueryEscape("tag:show:golang"), "Golang bookmark", ""},
		{"/read?f=" + url.QueryEscape("tag:show:golang"), "Golang favorite", "Rust favorite"},
	}
	for _, tc := range cases {
		body := doGet(h, tc.path, cookie).Body.String()
		if !strings.Contains(body, tc.want) {
			t.Fatalf("%s missing %q: %s", tc.path, tc.want, body)
		}
		if tc.drop != "" && strings.Contains(body, tc.drop) {
			t.Fatalf("%s should not include %q: %s", tc.path, tc.drop, body)
		}
		// The filter toolbar is present on each page.
		if !strings.Contains(body, "tag-filter-dialog") || !strings.Contains(body, "matching") {
			t.Fatalf("%s should render the filter toolbar: %s", tc.path, body)
		}
	}
}

func TestCustomListFilters(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Example", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Example", "https://example.test/rss.xml", "", "", 900)

	golang := addSearchItem(t, s, u.ID, f.ID, "l1", "Golang list item", "golang")
	rust := addSearchItem(t, s, u.ID, f.ID, "l2", "Rust list item", "rust")
	l, _ := s.store.Lists.Create(u.ID, "reading")
	for _, id := range []int64{golang.ID, rust.ID} {
		if err := s.store.Lists.AddItem(u.ID, l.ID, id); err != nil {
			t.Fatal(err)
		}
	}

	body := doGet(h, "/lists/"+itoa(l.ID)+"?f="+url.QueryEscape("tag:show:golang"), cookie).Body.String()
	if !strings.Contains(body, "Golang list item") || strings.Contains(body, "Rust list item") {
		t.Fatalf("list filter should keep only the golang item: %s", body)
	}
	if !strings.Contains(body, "1 matching") {
		t.Fatalf("list filter should show its matching count: %s", body)
	}
	// A list cannot become a saved view: its membership is part of the scope.
	if strings.Contains(body, `formaction="/views/new"`) {
		t.Fatalf("list filter dialog should not offer save as view: %s", body)
	}
}
