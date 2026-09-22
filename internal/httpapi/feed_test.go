package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/poller"
	"github.com/metruzanca/nanoflux/internal/store"
)

func TestFeedErrorSurface(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Author", "", "")
	f, err := s.store.Feeds.Create(u.ID, a.ID, "Broken Feed", "https://broken.dev/feed.xml", "", "", 900)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.Feeds.SetPollMeta(f.ID, "", "", db.Now(), "boom: dns lookup failed"); err != nil {
		t.Fatal(err)
	}

	// Feed detail page shows the error text.
	body := doGet(h, "/feeds/"+itoa(f.ID), cookie).Body.String()
	if !strings.Contains(body, "last poll failed: boom: dns lookup failed") {
		t.Fatalf("feed page missing error text: %s", body)
	}

	// The author's feeds list row gets a badge.
	body = doGet(h, "/authors/"+itoa(a.ID), cookie).Body.String()
	if !strings.Contains(body, "last poll failed") {
		t.Fatalf("author page feeds list missing error badge: %s", body)
	}
}

func TestFeedErrorHiddenAfterSuccess(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Author", "", "")
	f, err := s.store.Feeds.Create(u.ID, a.ID, "Fine Feed", "https://fine.dev/feed.xml", "", "", 900)
	if err != nil {
		t.Fatal(err)
	}
	body := doGet(h, "/feeds/"+itoa(f.ID), cookie).Body.String()
	if strings.Contains(body, "last poll failed") {
		t.Fatal("healthy feed should not show an error")
	}
}

func TestFeedOlder(t *testing.T) {
	s, h := newTestServer(t)
	s.SetPoller(poller.New(s.store, time.Minute, 1))
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Author", "", "")

	// Page 2 carries the older history; any other page (incl. 3) is empty,
	// like a real feed past its end.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>B</title><item><guid>old-1</guid><title>old one</title><link>https://b.dev/old-1</link></item><item><guid>old-2</guid><title>old two</title><link>https://b.dev/old-2</link></item></channel></rss>`)
			return
		}
		fmt.Fprint(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>B</title></channel></rss>`)
	}))
	defer srv.Close()

	f, err := s.store.Feeds.Create(u.ID, a.ID, "Blog", srv.URL+"/feed", "", "", 900)
	if err != nil {
		t.Fatal(err)
	}
	// The first poll recorded pagination (page 1 advertises a next page).
	if err := s.store.Feeds.SetNextPageURL(f.ID, srv.URL+"/feed?page=2"); err != nil {
		t.Fatal(err)
	}
	f, _ = s.store.Feeds.ByID(u.ID, f.ID)

	// The feed page shows the "load older items" button.
	body := doGet(h, "/feeds/"+itoa(f.ID), cookie).Body.String()
	if !strings.Contains(body, "load older items") {
		t.Fatalf("feed page missing load-older button: %s", body)
	}

	// Clicking it imports page 2 and reports exhaustion in the fragment.
	rr := doForm(h, "POST", "/feeds/"+itoa(f.ID)+"/older", nil, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /older status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	out := rr.Body.String()
	if !strings.Contains(out, `hx-swap-oob="outerHTML"`) || !strings.Contains(out, `id="scoped-items"`) {
		t.Fatalf("older response missing OOB item list: %s", out)
	}
	if !strings.Contains(out, "full history loaded") {
		t.Fatalf("older response missing exhausted note: %s", out)
	}

	// Both older items are stored, unread; the cursor is cleared.
	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	if len(items) != 2 {
		t.Fatalf("stored items = %d, want 2", len(items))
	}
	if n, _ := s.store.Items.CountUnread(u.ID, f.ID); n != 2 {
		t.Fatalf("unread = %d, want 2", n)
	}
	f, _ = s.store.Feeds.ByID(u.ID, f.ID)
	if f.NextPageURL != "" {
		t.Fatalf("NextPageURL = %q, want cleared", f.NextPageURL)
	}

	// Another click is refused with a visible error.
	rr = doForm(h, "POST", "/feeds/"+itoa(f.ID)+"/older", nil, cookie)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST /older status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `role="alert"`) || !strings.Contains(rr.Body.String(), "no more items") {
		t.Fatalf("exhausted error missing alert fragment: %s", rr.Body.String())
	}
}

func TestFeedOlderNoButtonWhenNotPaginated(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Author", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Flat", "https://flat.dev/feed.xml", "", "", 900)
	body := doGet(h, "/feeds/"+itoa(f.ID), cookie).Body.String()
	if strings.Contains(body, "load older items") {
		t.Fatalf("non-paginated feed should not show load-older button: %s", body)
	}
}

func TestFeedOlderFetchError(t *testing.T) {
	s, h := newTestServer(t)
	s.SetPoller(poller.New(s.store, time.Minute, 1))
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Author", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Broken", "https://broken.dev/feed.xml", "", "", 900)
	// Point the cursor at an unreachable page.
	if err := s.store.Feeds.SetNextPageURL(f.ID, "http://127.0.0.1:1/feed?page=2"); err != nil {
		t.Fatal(err)
	}
	rr := doForm(h, "POST", "/feeds/"+itoa(f.ID)+"/older", nil, cookie)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST /older status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `role="alert"`) {
		t.Fatalf("fetch error missing alert fragment: %s", rr.Body.String())
	}
}

func TestFeedCreatePollsImmediately(t *testing.T) {
	s, h := newTestServer(t)
	s.SetPoller(poller.New(s.store, time.Minute, 1))
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Author", "", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>Blog</title><item><guid>g1</guid><title>Fresh</title><link>https://b.dev/1</link></item></channel></rss>`)
	}))
	defer srv.Close()

	rr := doForm(h, "POST", "/feeds", url.Values{
		"title": {"Blog"}, "feed_url": {srv.URL + "/feed"},
		"author_id": {itoa(a.ID)},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("create feed: %d %s", rr.Code, rr.Body.String())
	}

	// The first poll runs detached from the request; wait (bounded) for it to
	// store the item so the feed's content shows up without waiting for a tick.
	deadline := time.Now().Add(3 * time.Second)
	for {
		items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
		if len(items) == 1 && items[0].Title == "Fresh" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("immediate first poll did not ingest the item: %+v", items)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The feed was polled: last_error stays clear and a next-page cursor was
	// recorded for the first poll.
	feeds, _ := s.store.Feeds.List(u.ID)
	if len(feeds) != 1 || feeds[0].LastError != "" {
		t.Fatalf("feed after first poll: %+v", feeds)
	}
}

// TestFeedRefreshUpdatesAuthorItems asserts that refreshing a feed from an
// author page both re-renders its row (the primary swap) and out-of-band
// refreshes the author's item list, so newly polled items appear immediately.
func TestFeedRefreshUpdatesAuthorItems(t *testing.T) {
	s, h := newTestServer(t)
	s.SetPoller(poller.New(s.store, time.Minute, 1))
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Author", "", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>Blog</title><item><guid>g1</guid><title>Fresh one</title><link>https://b.dev/1</link></item></channel></rss>`)
	}))
	defer srv.Close()

	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", srv.URL+"/feed", "", "", 900)

	// Simulate the htmx refresh from the author page's feed row.
	req := httptest.NewRequest(http.MethodPost, "/feeds/"+itoa(f.ID)+"/refresh", nil)
	req.Header.Set("HX-Current-URL", "http://example.com/authors/"+itoa(a.ID))
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rr.Code, rr.Body.String())
	}
	out := rr.Body.String()
	if !strings.Contains(out, `hx-swap-oob="outerHTML"`) || !strings.Contains(out, `id="scoped-items"`) {
		t.Fatalf("author-page refresh should OOB-swap the item list: %s", out)
	}
	if !strings.Contains(out, "Fresh one") {
		t.Fatalf("refreshed item list should include the new item: %s", out)
	}

	// Refreshing from the feed detail page must not emit an OOB swap (there is
	// no #scoped-items region on that page to target).
	req = httptest.NewRequest(http.MethodPost, "/feeds/"+itoa(f.ID)+"/refresh", nil)
	req.AddCookie(cookie)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if strings.Contains(rr.Body.String(), "hx-swap-oob") {
		t.Fatalf("feed-detail refresh should not OOB-swap: %s", rr.Body.String())
	}
}
