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

	// Create an author through the web UI.
	if rr := doForm(h, "POST", "/authors", url.Values{
		"name": {"Metru"}, "url": {"https://metru.dev"},
	}, cookie); rr.Code != http.StatusFound {
		t.Fatalf("create author: %d", rr.Code)
	}
	if body := doGet(h, "/authors", cookie).Body.String(); !strings.Contains(body, "Metru") {
		t.Fatal("authors page missing author")
	}

	// Create a feed referencing that author.
	u, _ := s.store.Users.ByUsername("alice")
	authors, _ := s.store.Authors.List(u.ID)
	if rr := doForm(h, "POST", "/feeds", url.Values{
		"title": {"Blog"}, "feed_url": {"https://example.com/rss.xml"},
		"author_id": {itoa(authors[0].ID)}, "poll_interval_sec": {"900"},
	}, cookie); rr.Code != http.StatusFound {
		t.Fatalf("create feed: %d", rr.Code)
	}
	body := doGet(h, "/feeds", cookie).Body.String()
	if !strings.Contains(body, "Blog") || !strings.Contains(body, "Metru") {
		t.Fatal("feeds page missing feed/author")
	}

	// Create a collection and attach the feed.
	if rr := doForm(h, "POST", "/collections", url.Values{
		"name": {"Dev"},
	}, cookie); rr.Code != http.StatusFound {
		t.Fatalf("create collection: %d", rr.Code)
	}
	cols, _ := s.store.Collections.List(u.ID)
	feeds, _ := s.store.Feeds.List(u.ID)
	if rr := doForm(h, "POST", "/collections/"+itoa(cols[0].ID)+"/add-feed", url.Values{
		"feed_id": {itoa(feeds[0].ID)},
	}, cookie); rr.Code != http.StatusOK {
		t.Fatalf("add feed to collection: %d", rr.Code)
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
	n, _ := s.store.Items.CountUnread(u.ID, 0)
	if n != 0 {
		t.Fatalf("unread = %d, want 0", n)
	}
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
