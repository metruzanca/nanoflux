package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// setupSortItem creates one author + feed + item for alice.
func setupSortItem(t *testing.T, s *Server) (store.User, store.Item, int64) {
	t.Helper()
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Sort author", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Sort feed", "https://sort.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "s1", Title: "Sortable post", Link: "https://sort.dev/1", FetchedAt: db.Now()})
	itemID, _ := s.store.Items.ByFeedIdentity(f.ID, "s1")
	it, _ := s.store.Items.ByID(u.ID, itemID)
	return u, it, a.ID
}

// Momentum is offered on the scoped lists that span authors (the unread list,
// /search and a collection) and hidden where the list is already one author or
// a native time-ordered list.
func TestMomentumSortSurface(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _, authorID := setupSortItem(t, s)

	// The global unread list offers momentum and renders it.
	body := doGet(h, "/unread", cookie).Body.String()
	if !strings.Contains(body, `data-option="momentum"`) || !strings.Contains(body, "sort=momentum") {
		t.Fatalf("unread page should offer the momentum sort: %s", body)
	}
	got := doGet(h, "/unread?sort=momentum", cookie)
	if got.Code != http.StatusOK {
		t.Fatalf("momentum unread page: %d", got.Code)
	}
	if !strings.Contains(got.Body.String(), "authors with the fewest unread first") {
		t.Fatalf("momentum list should explain its order: %s", got.Body.String())
	}

	// /search offers it on the unread tab and drops it on the read tab, where a
	// momentum request falls back to newest.
	search := doGet(h, "/search", cookie).Body.String()
	if !strings.Contains(search, `data-option="momentum"`) {
		t.Fatalf("search unread tab should offer momentum: %s", search)
	}
	readTab := doGet(h, "/search?tab=read&sort=momentum", cookie)
	if readTab.Code != http.StatusOK {
		t.Fatalf("search read tab: %d", readTab.Code)
	}
	if strings.Contains(readTab.Body.String(), `data-option="momentum"`) {
		t.Fatalf("search read tab should not offer momentum: %s", readTab.Body.String())
	}
	if !strings.Contains(readTab.Body.String(), `data-mode="newest"`) {
		t.Fatalf("momentum on a read tab should coerce to newest: %s", readTab.Body.String())
	}

	// A collection's unread tab offers it.
	c, _ := s.store.Collections.Create(u.ID, "Sorting")
	col := doGet(h, "/collections/"+itoa(c.ID), cookie).Body.String()
	if !strings.Contains(col, `data-option="momentum"`) {
		t.Fatalf("collection unread tab should offer momentum: %s", col)
	}

	// An author page is time-only: no magic, no momentum, and either request
	// coerces to newest.
	author := doGet(h, "/authors/"+itoa(authorID), cookie).Body.String()
	if strings.Contains(author, `data-option="magic"`) || strings.Contains(author, `data-option="momentum"`) {
		t.Fatalf("author page should offer time sorts only: %s", author)
	}
	coerced := doGet(h, "/authors/"+itoa(authorID)+"?sort=magic", cookie).Body.String()
	if !strings.Contains(coerced, `data-mode="newest"`) {
		t.Fatalf("magic on an author page should coerce to newest: %s", coerced)
	}

	// Favorites and bookmarks are time-only too.
	if favs := doGet(h, "/favorites", cookie).Body.String(); strings.Contains(favs, `data-option="magic"`) || strings.Contains(favs, `data-option="momentum"`) {
		t.Fatalf("favorites should offer time sorts only: %s", favs)
	}
	if marks := doGet(h, "/bookmarks", cookie).Body.String(); strings.Contains(marks, `data-option="magic"`) || strings.Contains(marks, `data-option="momentum"`) {
		t.Fatalf("bookmarks should offer time sorts only: %s", marks)
	}
}
