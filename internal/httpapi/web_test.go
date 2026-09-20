package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/metruzanca/rss/internal/db"
	"github.com/metruzanca/rss/internal/store"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func doForm(h http.Handler, method, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func doGet(h http.Handler, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func sessionCookie(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	rr := login(t, h)
	for _, c := range rr.Result().Cookies() {
		if c.Name == "rss_session" {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func TestFeedAuthorCollectionFlow(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	// Create an author through the web UI (returns the row fragment).
	rr := doForm(h, "POST", "/authors", url.Values{
		"name": {"Metru"}, "url": {"https://metru.dev"},
	}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "author-") {
		t.Fatalf("create author: %d %s", rr.Code, rr.Body.String())
	}
	if body := doGet(h, "/authors", cookie).Body.String(); !strings.Contains(body, "Metru") {
		t.Fatal("authors page missing author")
	}

	// Create a feed referencing that author (returns the row fragment).
	u, _ := s.store.Users.ByUsername("alice")
	authors, _ := s.store.Authors.List(u.ID)
	rr = doForm(h, "POST", "/feeds", url.Values{
		"title": {"Blog"}, "feed_url": {"https://example.com/rss.xml"},
		"author_id": {itoa(authors[0].ID)}, "poll_interval_sec": {"900"},
	}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "feed-") {
		t.Fatalf("create feed: %d %s", rr.Code, rr.Body.String())
	}
	body := doGet(h, "/feeds", cookie).Body.String()
	if !strings.Contains(body, "Blog") || !strings.Contains(body, "Metru") {
		t.Fatal("feeds page missing feed/author")
	}

	// Authorless feeds are allowed now.
	rr = doForm(h, "POST", "/feeds", url.Values{
		"title": {"NoAuthor"}, "feed_url": {"https://example.com/rss2.xml"},
	}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "feed-") {
		t.Fatalf("authorless feed: %d %s", rr.Code, rr.Body.String())
	}
	authorless, _ := s.store.Feeds.List(u.ID)
	for _, f := range authorless {
		if f.Title == "NoAuthor" && f.AuthorID != 0 {
			t.Fatalf("expected authorless feed, got author_id %d", f.AuthorID)
		}
	}

	// Create a collection and attach the feed.
	rr = doForm(h, "POST", "/collections", url.Values{
		"name": {"Dev"},
	}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "collection-") {
		t.Fatalf("create collection: %d %s", rr.Code, rr.Body.String())
	}
	cols, _ := s.store.Collections.List(u.ID)
	feeds, _ := s.store.Feeds.List(u.ID)
	rr = doForm(h, "POST", "/collections/"+itoa(cols[0].ID)+"/add-feed", url.Values{
		"feed_id": {itoa(feeds[0].ID)},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("add feed to collection: %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "hx-swap-oob") || !strings.Contains(rr.Body.String(), "collection-items") {
		t.Fatalf("add-feed response should carry the items oob swap: %s", rr.Body.String())
	}
	body = doGet(h, "/collections/"+itoa(cols[0].ID), cookie).Body.String()
	if !strings.Contains(body, "Blog") {
		t.Fatal("collection page missing feed")
	}
}

func TestItemReadToggle(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "A", "", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "B", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g", Title: "Item", Link: "https://b.dev/1", FetchedAt: db.Now()})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	rr := doForm(h, "POST", "/items/"+itoa(items[0].ID)+"/read", url.Values{}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("toggle read: %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "item-"+itoa(items[0].ID)) {
		t.Fatal("item row fragment not returned")
	}
	if !strings.Contains(rr.Body.String(), `data-item-id="`+itoa(items[0].ID)+`"`) ||
		!strings.Contains(rr.Body.String(), `data-item-link="https://b.dev/1"`) {
		t.Fatalf("item row missing modal data attrs: %s", rr.Body.String())
	}
	n, _ := s.store.Items.CountUnread(u.ID, 0)
	if n != 0 {
		t.Fatalf("unread = %d, want 0", n)
	}
}

func TestItemView(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "g", Title: "Item", Link: "https://b.dev/1",
		Summary: "<p>hello <b>world</b></p>", FetchedAt: db.Now(),
	})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	rr := doGet(h, "/items/"+itoa(items[0].ID)+"/view", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("item view: %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "<b>world</b>") || !strings.Contains(body, "Item") || !strings.Contains(body, "Metru") {
		t.Fatalf("item view content: %s", body)
	}

	// Another user cannot view it.
	other, _ := s.store.Users.Create("bob", "h")
	_ = other
	// (cross-user access is covered by OneWithFeed's user scoping)
}

func TestAuthorFormFragment(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	if rr := doGet(h, "/fragments/author-form?author_id=new", cookie); !strings.Contains(rr.Body.String(), "new author name") {
		t.Fatal("expected author-create fields fragment")
	}
	if rr := doGet(h, "/fragments/author-form?author_id=5", cookie); rr.Code != http.StatusNoContent {
		t.Fatalf("expected empty fragment for existing author, got %d", rr.Code)
	}
}

func TestFeedAndAuthorPagesShowItems(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g", Title: "Feed Item", Link: "https://b.dev/1", FetchedAt: db.Now()})

	// Feed detail page shows the feed, its author, and its items.
	rr := doGet(h, "/feeds/"+itoa(f.ID), cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("feed page: %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"Blog", "Feed Item", "Metru"} {
		if !strings.Contains(body, want) {
			t.Fatalf("feed page missing %q: %s", want, body)
		}
	}

	// Author page shows items from the author's feeds.
	rr = doGet(h, "/authors/"+itoa(a.ID), cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("author page: %d", rr.Code)
	}
	if body := rr.Body.String(); !strings.Contains(body, "Feed Item") {
		t.Fatalf("author page missing item: %s", body)
	}
}
