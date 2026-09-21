package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// setupListsItem creates one author + feed + item for alice.
func setupListsItem(t *testing.T, s *Server) (store.User, store.Item) {
	t.Helper()
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Blog", "", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g1", Title: "Listable post", Link: "https://b.dev/1", Summary: "body", FetchedAt: db.Now()})
	itemID, _ := s.store.Items.ByFeedGUID(f.ID, "g1")
	it, _ := s.store.Items.ByID(u.ID, itemID)
	return u, it
}

func TestListsFlow(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, item := setupListsItem(t, s)

	// Create a list through the web UI (returns the row fragment).
	rr := doForm(h, "POST", "/lists", url.Values{"name": {"reading"}}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("create list: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "/lists/") {
		t.Fatalf("list row should link to the list page: %s", rr.Body.String())
	}
	rows, _ := s.store.Lists.List(u.ID)
	if len(rows) != 1 || rows[0].Name != "reading" {
		t.Fatalf("list not stored: %+v", rows)
	}
	listID := rows[0].ID

	// The lists index shows favorites (the special list) plus the new list.
	body := doGet(h, "/lists", cookie).Body.String()
	if !strings.Contains(body, "favorites") || !strings.Contains(body, "reading") {
		t.Fatalf("lists index missing entries: %s", body)
	}

	// The item modal offers the ⋯ menu and the list picker.
	body = doGet(h, "/items/"+itoa(item.ID)+"/view", cookie).Body.String()
	if !strings.Contains(body, `id="item-menu-btn"`) || !strings.Contains(body, `id="item-lists-dialog"`) {
		t.Fatalf("item modal should include the list menu + picker: %s", body)
	}
	if !strings.Contains(body, "reading") {
		t.Fatalf("picker should list the user's lists: %s", body)
	}

	// Add the item to the reading list and favorites via the picker.
	rr = doForm(h, "POST", "/items/"+itoa(item.ID)+"/lists", url.Values{
		"lists": {itoa(listID), "favorites"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("apply lists: %d %s", rr.Code, rr.Body.String())
	}
	ids, _ := s.store.Lists.ItemListIDs(u.ID, item.ID)
	if len(ids) != 1 || ids[0] != listID {
		t.Fatalf("item not in list: %v", ids)
	}
	after, _ := s.store.Items.ByID(u.ID, item.ID)
	if !after.Favorite {
		t.Fatal("picker should also have favorited the item")
	}

	// The list page shows the item.
	body = doGet(h, "/lists/"+itoa(listID), cookie).Body.String()
	if !strings.Contains(body, "Listable post") {
		t.Fatalf("list page missing item: %s", body)
	}

	// Uncheck everything: removes the item from the list and unfavorites it.
	rr = doForm(h, "POST", "/items/"+itoa(item.ID)+"/lists", url.Values{}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("clear lists: %d %s", rr.Code, rr.Body.String())
	}
	if ids, _ := s.store.Lists.ItemListIDs(u.ID, item.ID); len(ids) != 0 {
		t.Fatalf("item should be removed from list: %v", ids)
	}
	if after, _ := s.store.Items.ByID(u.ID, item.ID); after.Favorite {
		t.Fatal("item should be unfavorited")
	}

	// Delete the list via an htmx row swap; the index re-renders without it.
	req := httptest.NewRequest(http.MethodPost, "/lists/"+itoa(listID)+"/delete", strings.NewReader(""))
	req.Header.Set("HX-Request", "true")
	req.AddCookie(cookie)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete list: %d %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "reading") {
		t.Fatalf("deleted list should be gone from the index: %s", rr.Body.String())
	}
	if _, err := s.store.Lists.ByID(u.ID, listID); err != store.ErrNotFound {
		t.Fatalf("list should be deleted, got %v", err)
	}
}

func TestListCreateValidation(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	rr := doForm(h, "POST", "/lists", url.Values{"name": {"   "}}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "name is required") {
		t.Fatalf("create list error: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `id="add-list-error"`) || !strings.Contains(rr.Body.String(), `role="alert"`) {
		t.Fatalf("list error should be a role=alert OOB banner: %s", rr.Body.String())
	}
}

func TestListShareFlow(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, item := setupListsItem(t, s)

	l, _ := s.store.Lists.Create(u.ID, "reading")
	s.store.Lists.AddItem(u.ID, l.ID, item.ID)

	// Share it; the control shows the public link.
	rr := doForm(h, "POST", "/lists/"+itoa(l.ID)+"/share", url.Values{}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("share list: %d %s", rr.Code, rr.Body.String())
	}
	got, _ := s.store.Lists.ByID(u.ID, l.ID)
	if got.ShareToken == "" {
		t.Fatal("list should have a share token")
	}
	if !strings.Contains(rr.Body.String(), "/l/"+got.ShareToken) {
		t.Fatalf("share control should show the link: %s", rr.Body.String())
	}

	// The public page is reachable WITHOUT a session and links only externally.
	pub := doGetRaw(h, "/l/"+got.ShareToken)
	if pub.Code != http.StatusOK {
		t.Fatalf("public list page: %d %s", pub.Code, pub.Body.String())
	}
	if !strings.Contains(pub.Body.String(), "reading") || !strings.Contains(pub.Body.String(), "Listable post") {
		t.Fatalf("public page should render list + item: %s", pub.Body.String())
	}
	if strings.Contains(pub.Body.String(), `href="/authors/`) || strings.Contains(pub.Body.String(), `href="/feeds/`) {
		t.Fatalf("public page should not expose internal links: %s", pub.Body.String())
	}
	if !strings.Contains(pub.Body.String(), `class="external"`) {
		t.Fatalf("public item link should carry class external: %s", pub.Body.String())
	}

	// Unknown token -> 404.
	if got := doGetRaw(h, "/l/nope"); got.Code != http.StatusNotFound {
		t.Fatalf("unknown token: %d", got.Code)
	}

	// Revoke: the token no longer resolves.
	rr = doForm(h, "POST", "/lists/"+itoa(l.ID)+"/revoke", url.Values{}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rr.Code, rr.Body.String())
	}
	if got := doGetRaw(h, "/l/"+got.ShareToken); got.Code != http.StatusNotFound {
		t.Fatalf("revoked token should 404, got %d", got.Code)
	}
}

func TestFavoritesShareFlow(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, item := setupListsItem(t, s)

	rr := doForm(h, "POST", "/items/"+itoa(item.ID)+"/favorite", url.Values{}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("favorite: %d %s", rr.Code, rr.Body.String())
	}

	// The favorites page offers a share control.
	body := doGet(h, "/favorites", cookie).Body.String()
	if !strings.Contains(body, `hx-post="/favorites/share"`) {
		t.Fatalf("favorites page should offer sharing: %s", body)
	}

	rr = doForm(h, "POST", "/favorites/share", url.Values{}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("share favorites: %d %s", rr.Code, rr.Body.String())
	}
	tok, _ := s.store.Users.FavoritesShareToken(u.ID)
	if tok == "" {
		t.Fatal("favorites share token missing")
	}
	if !strings.Contains(rr.Body.String(), "/f/"+tok) {
		t.Fatalf("favorites share control should show the link: %s", rr.Body.String())
	}

	pub := doGetRaw(h, "/f/"+tok)
	if pub.Code != http.StatusOK {
		t.Fatalf("public favorites page: %d %s", pub.Code, pub.Body.String())
	}
	if !strings.Contains(pub.Body.String(), "Listable post") {
		t.Fatalf("public favorites page missing item: %s", pub.Body.String())
	}
	if strings.Contains(pub.Body.String(), `href="/authors/`) || strings.Contains(pub.Body.String(), `href="/feeds/`) {
		t.Fatalf("public favorites page should not expose internal links: %s", pub.Body.String())
	}

	doForm(h, "POST", "/favorites/revoke", url.Values{}, cookie)
	if got := doGetRaw(h, "/f/"+tok); got.Code != http.StatusNotFound {
		t.Fatalf("revoked favorites token should 404, got %d", got.Code)
	}
}
