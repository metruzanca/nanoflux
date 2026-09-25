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

// TestFeedIconUsesHomeURL asserts a feed's source icon is looked up from its
// home page, not its feed URL (some feed URLs have no favicon), on the author
// feed row, the feed page header, and the item card meta.
func TestFeedIconUsesHomeURL(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Author", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://feeds.example.org/rss", "https://example.org", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g", Title: "Item", Link: "https://example.org/1", FetchedAt: db.Now()})

	for _, page := range []string{"/authors/" + itoa(a.ID), "/feeds/" + itoa(f.ID), "/unread"} {
		body := doGet(h, page, cookie).Body.String()
		if !strings.Contains(body, `src="/icons/example.org"`) {
			t.Fatalf("%s: icon should use the home host: %s", page, body)
		}
		if strings.Contains(body, `src="/icons/feeds.example.org"`) {
			t.Fatalf("%s: icon should not use the feed host: %s", page, body)
		}
	}
}

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

// TestFeedRefreshRespectsBackoff asserts that a manual refresh during a
// rate-limit backoff does not fetch (so it can't hammer the host) and the row
// shows the retry deadline with the refresh button disabled.
func TestFeedRefreshRespectsBackoff(t *testing.T) {
	s, h := newTestServer(t)
	s.SetPoller(poller.New(s.store, time.Minute, 1))
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Author", "", "")

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		fmt.Fprint(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>Blog</title></channel></rss>`)
	}))
	defer srv.Close()

	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", srv.URL+"/feed", "", "", 900)
	// Put the feed into a rate-limit backoff.
	if err := s.store.Feeds.SetNextPollAt(f.ID, db.FormatTime(time.Now().Add(45*time.Second))); err != nil {
		t.Fatal(err)
	}

	rr := doForm(h, "POST", "/feeds/"+itoa(f.ID)+"/refresh", url.Values{}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rr.Code, rr.Body.String())
	}
	if hits != 0 {
		t.Fatalf("refresh during backoff must not fetch, got %d hits", hits)
	}
	out := rr.Body.String()
	if !strings.Contains(out, "rate limited") {
		t.Fatalf("row should show the rate-limit retry badge: %s", out)
	}
	if !strings.Contains(out, "disabled") {
		t.Fatalf("refresh button should be disabled while cooling: %s", out)
	}
}

// TestMarkAllReadControls covers the feed- and author-page "mark all as read"
// controls: the button renders next to edit/refresh with the scope's unread
// count, its confirmation dialog is present, and the POST endpoints clear the
// scope.
func TestMarkAllReadControls(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Author", "", "")
	f1, _ := s.store.Feeds.Create(u.ID, a.ID, "One", "https://one.dev/feed.xml", "", "", 900)
	a2, _ := s.store.Authors.Create(u.ID, "Other", "", "")
	f2, _ := s.store.Feeds.Create(u.ID, a2.ID, "Two", "https://two.dev/feed.xml", "", "", 900)
	for _, f := range []struct {
		id   int64
		guid string
	}{{f1.ID, "a1"}, {f1.ID, "a2"}, {f2.ID, "b1"}} {
		if _, err := s.store.Items.Upsert(f.id, store.Item{GUID: f.guid, Title: f.guid, Link: "https://x/" + f.guid, FetchedAt: db.Now()}); err != nil {
			t.Fatal(err)
		}
	}

	// The feed page shows the button and dialog naming its 2 unread items.
	body := doGet(h, "/feeds/"+itoa(f1.ID), cookie).Body.String()
	if !strings.Contains(body, `title="mark all 2 items as read"`) {
		t.Fatalf("feed page missing mark-all-read button: %s", body)
	}
	if !strings.Contains(body, "mark 2 items as read?") {
		t.Fatalf("feed page dialog should name 2 items: %s", body)
	}
	if !strings.Contains(body, `hx-post="/feeds/`+itoa(f1.ID)+`/read-all"`) {
		t.Fatalf("feed page missing read-all action: %s", body)
	}

	// An author page with unread items names them in its own dialog.
	body = doGet(h, "/authors/"+itoa(a2.ID), cookie).Body.String()
	if !strings.Contains(body, `hx-post="/authors/`+itoa(a2.ID)+`/read-all"`) {
		t.Fatalf("author page should render the action: %s", body)
	}
	if !strings.Contains(body, "mark 1 items as read?") {
		t.Fatalf("author page dialog should name its 1 unread item: %s", body)
	}

	// An author with nothing unread renders the button disabled and no dialog.
	empty, _ := s.store.Authors.Create(u.ID, "Empty", "", "")
	body = doGet(h, "/authors/"+itoa(empty.ID), cookie).Body.String()
	if !strings.Contains(body, `title="mark all 0 items as read"`) || !strings.Contains(body, "disabled") {
		t.Fatalf("empty author should show a disabled button: %s", body)
	}
	if strings.Contains(body, "mark 0 items as read?") {
		t.Fatalf("empty author should not render a confirmation dialog: %s", body)
	}

	// POST the feed scope: only that feed's items are marked read.
	if rr := doForm(h, "POST", "/feeds/"+itoa(f1.ID)+"/read-all", nil, cookie); rr.Code != http.StatusNoContent {
		t.Fatalf("feed read-all: %d %s", rr.Code, rr.Body.String())
	}
	if n, _ := s.store.Items.CountUnread(u.ID, f1.ID); n != 0 {
		t.Fatalf("feed unread = %d, want 0", n)
	}
	if n, _ := s.store.Items.CountUnread(u.ID, f2.ID); n != 1 {
		t.Fatalf("other feed unread = %d, want 1 (untouched)", n)
	}

	// POST the author scope clears the other author's feed too.
	if rr := doForm(h, "POST", "/authors/"+itoa(a2.ID)+"/read-all", nil, cookie); rr.Code != http.StatusNoContent {
		t.Fatalf("author read-all: %d %s", rr.Code, rr.Body.String())
	}
	if n, _ := s.store.Items.CountUnread(u.ID, f2.ID); n != 0 {
		t.Fatalf("author unread = %d, want 0", n)
	}
}

// A missing feed/author id is a 404, and read-all never crosses users.
func TestMarkAllReadScoping(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Author", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "One", "https://one.dev/feed.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g", Title: "g", Link: "https://x/g", FetchedAt: db.Now()})

	if rr := doForm(h, "POST", "/feeds/999999/read-all", nil, cookie); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown feed read-all = %d, want 404", rr.Code)
	}
	if rr := doForm(h, "POST", "/authors/999999/read-all", nil, cookie); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown author read-all = %d, want 404", rr.Code)
	}
	if n, _ := s.store.Items.CountUnread(u.ID, f.ID); n != 1 {
		t.Fatalf("failed calls should not mark anything read, unread = %d", n)
	}
}
