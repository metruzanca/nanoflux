package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/discover"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
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

// doGetRaw issues a GET with no session, for unauthenticated routes.
func doGetRaw(h http.Handler, path string) *httptest.ResponseRecorder {
	return doGet(h, path, nil)
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

func TestNormalizeURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://x.com/sama", "https://x.com/sama"},
		{"http://x.com/sama", "http://x.com/sama"},
		{"x.com/sama", "https://x.com/sama"},
		{"  x.com/sama  ", "https://x.com/sama"},
		{"www.example.com/feed.xml", "https://www.example.com/feed.xml"},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := normalizeURL(c.in); got != c.want {
			t.Errorf("normalizeURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStripWWW(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://www.example.com", "https://example.com"},
		{"https://www.example.com/feed.xml", "https://example.com/feed.xml"},
		{"http://www.example.com", "http://example.com"},
		{"https://www.example.com:8080/x", "https://example.com:8080/x"},
		{"https://WWW.Example.COM/path", "https://Example.COM/path"},
		{"https://example.com", "https://example.com"},
		{"https://www", "https://www"},
		{"www.example.com/feed", "www.example.com/feed"}, // no scheme: not a host
		{"", ""},
	}
	for _, c := range cases {
		if got := stripWWW(c.in); got != c.want {
			t.Errorf("stripWWW(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFeedPreviewFormStripsWWW(t *testing.T) {
	s, _ := newTestServer(t)
	// pageURL is deliberately unusable so PageMeta fails fast with no network;
	// the rendered form fields prove the www stripping.
	req := httptest.NewRequest(http.MethodPost, "/fragments/feed-preview", nil)
	w := httptest.NewRecorder()
	s.renderFeedPreviewForm(req, w, discover.Candidate{
		FeedURL: "https://www.example.com/feed.xml",
		Title:   "Example",
		HomeURL: "https://www.example.com",
	}, "not-a-url", "https://www.example.com", nil, 0, nil)
	body := w.Body.String()
	if strings.Contains(body, "www.") {
		t.Fatalf("preview form should strip www from auto-filled urls: %s", body)
	}
	if !strings.Contains(body, `value="https://example.com/feed.xml"`) {
		t.Fatalf("feed url should be stripped: %s", body)
	}
	if !strings.Contains(body, `value="https://example.com"`) {
		t.Fatalf("home url should be stripped: %s", body)
	}
}

func TestFeedPreviewSchemelessURL(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	feedSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rss" {
			w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>RSS Feed</title><link>https://home.dev/</link></channel></rss>`))
			return
		}
		http.NotFound(w, r)
	}))
	defer feedSrv.Close()
	s.client = feedSrv.Client() // trusts the test TLS cert

	// Strip the scheme; normalization must add it back.
	schemeless := strings.TrimPrefix(feedSrv.URL, "https://")
	rr := doForm(h, "POST", "/fragments/feed-preview", url.Values{
		"url": {schemeless + "/rss"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), feedSrv.URL+"/rss") {
		t.Fatalf("schemeless url should resolve to the full feed url: %s", rr.Body.String())
	}
}

func TestFeedCreateNormalizesURL(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Blog", "", "")
	rr := doForm(h, "POST", "/feeds", url.Values{
		"title": {"Blog"}, "feed_url": {"example.com/rss.xml"},
		"author_id": {itoa(a.ID)},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("create feed: %d %s", rr.Code, rr.Body.String())
	}
	feeds, _ := s.store.Feeds.List(u.ID)
	if len(feeds) != 1 || feeds[0].FeedURL != "https://example.com/rss.xml" {
		t.Fatalf("stored feed url = %+v", feeds)
	}
}

func TestCreateFormErrors(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	// Missing required fields -> 400 with an OOB swap into the error slot.
	rr := doForm(h, "POST", "/feeds", url.Values{"title": {"Blog"}}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "title and feed url are required") {
		t.Fatalf("feed error: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `id="add-feed-error"`) || !strings.Contains(rr.Body.String(), "hx-swap-oob") {
		t.Fatalf("feed error should OOB into add-feed-error: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `role="alert"`) {
		t.Fatalf("feed error should render the alert banner: %s", rr.Body.String())
	}

	rr = doForm(h, "POST", "/authors", url.Values{}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "name is required") {
		t.Fatalf("author error: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `id="add-author-error"`) {
		t.Fatalf("author error should OOB into add-author-error: %s", rr.Body.String())
	}

	rr = doForm(h, "POST", "/collections", url.Values{}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "name is required") {
		t.Fatalf("collection error: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `id="add-collection-error"`) {
		t.Fatalf("collection error should OOB into add-collection-error: %s", rr.Body.String())
	}
}

func TestLoadMoreFlow(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	for i := 1; i <= 105; i++ {
		guid := "g" + strconv.Itoa(i)
		if _, err := s.store.Items.Upsert(f.ID, store.Item{GUID: guid, Title: "Post " + strconv.Itoa(i), Link: "https://b.dev/" + guid, FetchedAt: db.Now()}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	// Home renders the first page of items plus a load-more button.
	home := doGet(h, "/", cookie).Body.String()
	if !strings.Contains(home, `id="load-more"`) {
		t.Fatalf("home should render a load-more button: %s", home)
	}
	if got := strings.Count(home, `<li id="item-`); got != pageSize {
		t.Fatalf("home should show %d items, got %d", pageSize, got)
	}

	// The first page ends at the pageSize-th newest item.
	first, hasMore, _ := s.store.Items.ListPage(u.ID, store.ItemFilter{UnreadOnly: true, Limit: pageSize})
	if len(first) != pageSize || !hasMore {
		t.Fatalf("first page: %d items, more=%v", len(first), hasMore)
	}
	cursor := first[len(first)-1].ID

	// Loading the next page through the fragment appends rows and an OOB swap.
	body := doGet(h, "/items?before="+itoa(cursor), cookie).Body.String()
	if !strings.Contains(body, `hx-swap-oob`) {
		t.Fatalf("next page should carry an OOB load-more swap: %s", body)
	}
	if strings.Count(body, `<li id="item-`) != pageSize {
		t.Fatalf("next page should append %d rows, got: %s", pageSize, body)
	}

	// Page through the first 4 pages to reach the 100th item; the 5 remaining
	// items form a final short page with nothing after it.
	cur := int64(0)
	for page := 0; page < 4; page++ {
		pg, _, _ := s.store.Items.ListPage(u.ID, store.ItemFilter{UnreadOnly: true, Limit: pageSize, BeforeID: cur})
		cur = pg[len(pg)-1].ID
	}
	last, more, _ := s.store.Items.ListPage(u.ID, store.ItemFilter{UnreadOnly: true, Limit: pageSize, BeforeID: cur})
	if len(last) != 5 || more {
		t.Fatalf("last page: %d items, more=%v", len(last), more)
	}

	// After "mark all read", the section swaps atomically with no stale button.
	rr := doForm(h, "POST", "/items/read-all", url.Values{}, cookie)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), `id="load-more"`) {
		t.Fatalf("mark all read should clear the list and load-more button: %d %s", rr.Code, rr.Body.String())
	}
}

func TestSearchRoute(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g1", Title: "Go concurrency", Link: "https://b.dev/1", FetchedAt: db.Now()})
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g2", Title: "Rust ownership", Link: "https://b.dev/2", FetchedAt: db.Now()})

	body := doGet(h, "/search?q=concurrency", cookie).Body.String()
	if !strings.Contains(body, "Go concurrency") || strings.Contains(body, "Rust ownership") {
		t.Fatalf("search should return only the match: %s", body)
	}

	// The qualifier author: scopes to the author's items.
	body = doGet(h, "/search?q="+url.QueryEscape(`author:Metru Go`), cookie).Body.String()
	if !strings.Contains(body, "Go concurrency") {
		t.Fatalf("author-qualified search missing result: %s", body)
	}

	// Empty query renders the empty results page.
	rr := doGet(h, "/search", cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "search") {
		t.Fatalf("empty search: %d %s", rr.Code, rr.Body.String())
	}

	// The search box lives in the topbar.
	home := doGet(h, "/", cookie).Body.String()
	if !strings.Contains(home, `name="q"`) || !strings.Contains(home, `/search`) {
		t.Fatalf("topbar should carry the search form: %s", home)
	}
	// The mobile hamburger toggle is part of the same header.
	if !strings.Contains(home, `id="nav-toggle"`) || !strings.Contains(home, `id="top-nav"`) {
		t.Fatalf("topbar should carry the mobile nav toggle: %s", home)
	}
}

func TestItemModalShowsEnclosure(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Podcast", "https://p.dev/rss.xml", "", "", 900)
	if _, err := s.store.Items.Upsert(f.ID, store.Item{GUID: "g1", Title: "Episode", Link: "https://p.dev/1", FetchedAt: db.Now()}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	itemID, _ := s.store.Items.ByFeedGUID(f.ID, "g1")
	s.store.Items.ReplaceEnclosures(itemID, []store.Enclosure{
		{URL: "https://p.dev/ep1.mp3", Title: "Episode 1", MIMEType: "audio/mpeg", Size: 100},
	})

	body := doGet(h, "/items/"+itoa(itemID)+"/view", cookie).Body.String()
	if !strings.Contains(body, "<audio") || !strings.Contains(body, `src="https://p.dev/ep1.mp3"`) {
		t.Fatalf("item modal should embed the audio enclosure: %s", body)
	}
	if !strings.Contains(body, "class=\"external\"") {
		t.Fatalf("enclosure download link should be external-marked: %s", body)
	}
}

func TestFeedFilterRulesFlow(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)

	// The edit page shows the filters section.
	edit := doGet(h, "/feeds/"+itoa(f.ID)+"/edit", cookie).Body.String()
	if !strings.Contains(edit, `id="feed-rules"`) {
		t.Fatalf("edit page should include the filters section: %s", edit)
	}

	// Add a hide rule via the edit form.
	rr := doForm(h, "POST", "/feeds/"+itoa(f.ID)+"/filters", url.Values{
		"action": {"hide"}, "field": {"title"}, "pattern": {"sponsored"},
	}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "sponsored") {
		t.Fatalf("add rule: %d %s", rr.Code, rr.Body.String())
	}
	rules, _ := s.store.Filters.ListByFeed(u.ID, f.ID)
	if len(rules) != 1 || rules[0].Pattern != "sponsored" || rules[0].Action != "hide" {
		t.Fatalf("stored rule mismatch: %+v", rules)
	}

	// Invalid regex -> 400 with a visible error, nothing saved.
	rr = doForm(h, "POST", "/feeds/"+itoa(f.ID)+"/filters", url.Values{
		"action": {"hide"}, "field": {"title"}, "pattern": {"("}, "is_regex": {"1"},
	}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), `role="alert"`) {
		t.Fatalf("invalid regex: %d %s", rr.Code, rr.Body.String())
	}

	// Delete the rule via the list fragment.
	rr = doForm(h, "POST", "/filters/"+itoa(rules[0].ID)+"/delete", url.Values{}, cookie)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "sponsored") {
		t.Fatalf("delete rule: %d %s", rr.Code, rr.Body.String())
	}
	rules, _ = s.store.Filters.ListByFeed(u.ID, f.ID)
	if len(rules) != 0 {
		t.Fatalf("expected no rules after delete, got %d", len(rules))
	}
}

func TestCollectionPageDedups(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f1, _ := s.store.Feeds.Create(u.ID, a.ID, "r/videos", "https://v.dev/rss.xml", "", "", 900)
	f2, _ := s.store.Feeds.Create(u.ID, a.ID, "Metru's feed", "https://m.dev/rss.xml", "", "", 900)
	c, _ := s.store.Collections.Create(u.ID, "all")
	s.store.Collections.AddFeed(u.ID, c.ID, f1.ID)
	s.store.Collections.AddFeed(u.ID, c.ID, f2.ID)
	s.store.Items.Upsert(f1.ID, store.Item{GUID: "a", Title: "Funny cat video", Link: "https://v.dev/1", FetchedAt: db.Now()})
	s.store.Items.Upsert(f2.ID, store.Item{GUID: "b", Title: "Funny cat video", Link: "https://m.dev/1", FetchedAt: db.Now()})

	// The collection page shows the post once, with the other feed as a source.
	body := doGet(h, "/collections/"+itoa(c.ID), cookie).Body.String()
	if got := strings.Count(body, "Funny cat video"); got != 1 {
		t.Fatalf("dedup should collapse to one row, saw %d: %s", got, body)
	}
	if !strings.Contains(body, "also in") || !strings.Contains(body, "r/videos") {
		t.Fatalf("row should list the alternate feed as a source: %s", body)
	}

	// The feed page still lists the item (dedup is collection/author-scoped only).
	body = doGet(h, "/feeds/"+itoa(f1.ID), cookie).Body.String()
	if got := strings.Count(body, "Funny cat video"); got != 1 {
		t.Fatalf("feed page should show its own item: %d", got)
	}
}

func TestFeedToggle(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)

	// Pause the feed: the row shows the paused badge.
	rr := doForm(h, "POST", "/feeds/"+itoa(f.ID)+"/toggle", url.Values{}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "paused") {
		t.Fatalf("toggle off: %d %s", rr.Code, rr.Body.String())
	}
	after, _ := s.store.Feeds.ByID(u.ID, f.ID)
	if after.Enabled {
		t.Fatal("feed should be disabled after toggle")
	}

	// The author page's feeds list shows the paused badge too.
	body := doGet(h, "/authors/"+itoa(a.ID), cookie).Body.String()
	if !strings.Contains(body, "paused") {
		t.Fatalf("author page feeds list should show the paused badge: %s", body)
	}

	// Resume: the badge disappears and the feed is enabled again.
	rr = doForm(h, "POST", "/feeds/"+itoa(f.ID)+"/toggle", url.Values{}, cookie)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "paused") {
		t.Fatalf("toggle on: %d %s", rr.Code, rr.Body.String())
	}
	after, _ = s.store.Feeds.ByID(u.ID, f.ID)
	if !after.Enabled {
		t.Fatal("feed should be enabled after toggle")
	}

	// Pausing via the edit form persists the disabled state.
	rr = doForm(h, "POST", "/feeds/"+itoa(f.ID)+"/edit", url.Values{
		"title": {"Blog"}, "feed_url": {"https://b.dev/rss.xml"}, "poll_interval_sec": {"900"},
		"author_id": {itoa(a.ID)},
	}, cookie)
	if rr.Code != http.StatusFound {
		t.Fatalf("edit without enabled checkbox should disable: %d", rr.Code)
	}
	after, _ = s.store.Feeds.ByID(u.ID, f.ID)
	if after.Enabled {
		t.Fatal("edit without the enabled checkbox should pause the feed")
	}
}

func TestShareFlow(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g1", Title: "Shareable post", Link: "https://b.dev/1", Summary: "body text", FetchedAt: db.Now()})
	itemID, _ := s.store.Items.ByFeedGUID(f.ID, "g1")

	// The modal shows a share button when unshared.
	body := doGet(h, "/items/"+itoa(itemID)+"/view", cookie).Body.String()
	if !strings.Contains(body, `hx-post="/items/`+itoa(itemID)+`/share"`) {
		t.Fatalf("item modal should offer sharing: %s", body)
	}

	// Share it.
	rr := doForm(h, "POST", "/items/"+itoa(itemID)+"/share", url.Values{}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("share: %d %s", rr.Code, rr.Body.String())
	}
	sh, err := s.store.Shares.ByItem(u.ID, itemID)
	if err != nil {
		t.Fatalf("share not stored: %v", err)
	}
	if !strings.Contains(rr.Body.String(), "/shared/"+sh.Token) {
		t.Fatalf("share control should show the link: %s", rr.Body.String())
	}

	// The public page is reachable WITHOUT a session and does not require login.
	pub := httptest.NewRequest(http.MethodGet, "/shared/"+sh.Token, nil)
	prr := httptest.NewRecorder()
	h.ServeHTTP(prr, pub)
	if prr.Code != http.StatusOK {
		t.Fatalf("public share page: %d %s", prr.Code, prr.Body.String())
	}
	if !strings.Contains(prr.Body.String(), "Shareable post") || !strings.Contains(prr.Body.String(), "body text") {
		t.Fatalf("public page should render the item: %s", prr.Body.String())
	}
	if strings.Contains(prr.Body.String(), `href="/authors/`) || strings.Contains(prr.Body.String(), `href="/feeds/`) {
		t.Fatalf("public page should not expose internal links: %s", prr.Body.String())
	}

	// Unknown token -> 404.
	got := doGetRaw(h, "/shared/nope")
	if got.Code != http.StatusNotFound {
		t.Fatalf("unknown token: %d", got.Code)
	}

	// Revoke: the token no longer resolves.
	rr = doForm(h, "POST", "/items/"+itoa(itemID)+"/revoke", url.Values{}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rr.Code, rr.Body.String())
	}
	got = doGetRaw(h, "/shared/"+sh.Token)
	if got.Code != http.StatusNotFound {
		t.Fatalf("revoked token should 404, got %d", got.Code)
	}
}

func TestFeedAuthorCollectionFlow(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	// Create an author through the web UI (returns the row fragment).
	rr := doForm(h, "POST", "/authors", url.Values{
		"name": {"Metru"},
	}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "author-") {
		t.Fatalf("create author: %d %s", rr.Code, rr.Body.String())
	}
	if body := doGet(h, "/authors", cookie).Body.String(); !strings.Contains(body, "Metru") {
		t.Fatal("authors page missing author")
	}

	// Global add with an existing author: the feed lands under that author and
	// the response is the author's (refreshed) row, retargeted at it.
	u, _ := s.store.Users.ByUsername("alice")
	authors, _ := s.store.Authors.List(u.ID)
	rr = doForm(h, "POST", "/feeds", url.Values{
		"title": {"Blog"}, "feed_url": {"https://example.com/rss.xml"},
		"author_id": {itoa(authors[0].ID)}, "poll_interval_sec": {"900"},
	}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "author-") {
		t.Fatalf("create feed: %d %s", rr.Code, rr.Body.String())
	}
	if retarget := rr.Header().Get("HX-Retarget"); retarget != "#author-"+itoa(authors[0].ID) {
		t.Fatalf("existing-author add should retarget the author row, got %q", retarget)
	}
	body := doGet(h, "/authors/"+itoa(authors[0].ID), cookie).Body.String()
	if !strings.Contains(body, "Blog") || !strings.Contains(body, "Metru") {
		t.Fatal("author page missing feed/author")
	}

	// A feed without an author is rejected.
	rr = doForm(h, "POST", "/feeds", url.Values{
		"title": {"NoAuthor"}, "feed_url": {"https://example.com/rss2.xml"},
	}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "feed needs an author") {
		t.Fatalf("authorless feed should be rejected: %d %s", rr.Code, rr.Body.String())
	}
	feeds, _ := s.store.Feeds.List(u.ID)
	for _, f := range feeds {
		if f.Title == "NoAuthor" {
			t.Fatal("authorless feed should not have been created")
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
	feeds, _ = s.store.Feeds.List(u.ID)
	rr = doForm(h, "POST", "/collections/"+itoa(cols[0].ID)+"/add-feed", url.Values{
		"feed_id": {itoa(feeds[0].ID)},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("add feed to collection: %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `id="scoped-items"`) {
		t.Fatalf("add-feed response should render the items list: %s", rr.Body.String())
	}
	body = doGet(h, "/collections/"+itoa(cols[0].ID)+"/edit", cookie).Body.String()
	if !strings.Contains(body, "Blog") {
		t.Fatal("collection edit page missing feed")
	}
}

func TestReadPage(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "A", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "B", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g", Title: "Item", Link: "https://b.dev/1", FetchedAt: db.Now()})

	// Mark the item read.
	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	doForm(h, "POST", "/items/"+itoa(items[0].ID)+"/read", url.Values{}, cookie)

	// It shows on the read page but not the unread page.
	body := doGet(h, "/read", cookie).Body.String()
	if !strings.Contains(body, "Item") || !strings.Contains(body, `hx-post="/items/unread-all"`) {
		t.Fatalf("read page missing read item: %s", body)
	}
	if !strings.Contains(body, `class="read-at"`) {
		t.Fatalf("read page missing read-at timestamp: %s", body)
	}
	if body := doGet(h, "/", cookie).Body.String(); strings.Contains(body, `data-item-link="https://b.dev/1"`) {
		t.Fatal("unread page should not show read item")
	}

	// Mark all unread empties the read page and restores the unread count.
	doForm(h, "POST", "/items/unread-all", url.Values{}, cookie)
	n, _ := s.store.Items.CountUnread(u.ID, 0)
	if n != 1 {
		t.Fatalf("unread = %d, want 1", n)
	}
	body = doGet(h, "/read", cookie).Body.String()
	if strings.Contains(body, `data-item-link="https://b.dev/1"`) {
		t.Fatalf("read page should be empty after mark-all-unread: %s", body)
	}
}

func TestYouTubeEmbedURL(t *testing.T) {
	cases := []struct {
		link string
		want string
	}{
		{"https://www.youtube.com/watch?v=H0KAi8AWsnM", "https://www.youtube.com/embed/H0KAi8AWsnM"},
		{"https://m.youtube.com/watch?v=H0KAi8AWsnM&t=10s", "https://www.youtube.com/embed/H0KAi8AWsnM"},
		{"https://youtu.be/H0KAi8AWsnM", "https://www.youtube.com/embed/H0KAi8AWsnM"},
		{"https://www.youtube.com/shorts/H0KAi8AWsnM", "https://www.youtube.com/embed/H0KAi8AWsnM"},
		{"https://www.youtube.com/live/H0KAi8AWsnM", "https://www.youtube.com/embed/H0KAi8AWsnM"},
		{"https://www.youtube.com/embed/H0KAi8AWsnM", "https://www.youtube.com/embed/H0KAi8AWsnM"},
		{"https://example.com/post/1", ""},
		{"https://youtube.com/watch", ""},
		{"not a url", ""},
	}
	for _, c := range cases {
		if got := web.YoutubeEmbedURL(c.link); got != c.want {
			t.Errorf("YoutubeEmbedURL(%q) = %q, want %q", c.link, got, c.want)
		}
	}
}

func TestItemViewYouTubeEmbed(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "bigboxSWE", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "bigboxSWE", "https://www.youtube.com/feeds/videos.xml?channel_id=UCx", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "yt:video:H0KAi8AWsnM", Title: "Video",
		Link: "https://www.youtube.com/watch?v=H0KAi8AWsnM", FetchedAt: db.Now(),
	})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	rr := doGet(h, "/items/"+itoa(items[0].ID)+"/view", cookie)
	body := rr.Body.String()
	if !strings.Contains(body, `https://www.youtube.com/embed/H0KAi8AWsnM`) {
		t.Fatalf("item view should embed the youtube player: %s", body)
	}
	if !strings.Contains(body, "video-embed") {
		t.Fatalf("item view missing video-embed wrapper: %s", body)
	}
	// The player must send an origin Referer so YouTube doesn't return error 153
	// (the page strips referrers via Referrer-Policy: no-referrer).
	if !strings.Contains(body, `referrerpolicy="origin"`) {
		t.Fatalf("youtube embed should carry referrerpolicy=origin: %s", body)
	}
}

func TestItemCardsRenderThumbnails(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "bigboxSWE", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "bigboxSWE", "https://www.youtube.com/feeds/videos.xml?channel_id=UCx", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "yt:video:H0KAi8AWsnM", Title: "Video",
		Link:     "https://www.youtube.com/watch?v=H0KAi8AWsnM",
		ImageURL: "https://i.ytimg.com/vi/H0KAi8AWsnM/hq720.jpg", FetchedAt: db.Now(),
	})
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "p1", Title: "Post", Link: "https://example.com/1",
		Summary: "<p>hello</p>", FetchedAt: db.Now(),
	})
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "p2", Title: "Image Post", Link: "https://example.com/2",
		Summary:  `<a href="https://example.com/2"><img src="https://example.com/pic.jpg" alt="Image Post" /></a>`,
		ImageURL: "https://example.com/pic.jpg", FetchedAt: db.Now(),
	})
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "p3", Title: "Mittens enjoys a sunny nap",
		Link:      "https://old.reddit.com/r/cats/comments/1abcde/mittens_enjoys_a_sunny_nap/",
		ImageURL:  "https://external-preview.redd.it/1q2w3e4r.jpeg?width=320",
		Summary:   `<a href="https://www.reddit.com/r/cats/comments/1abcde/"><img src="https://external-preview.redd.it/1q2w3e4r.jpeg?width=320" alt="Mittens enjoys a sunny nap"></a>`,
		FetchedAt: db.Now(),
	})
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "p4", Title: "Whiskers at golden hour",
		Link:      "https://old.reddit.com/r/cats/comments/1fghij/whiskers_at_golden_hour/",
		ImageURL:  "https://preview.redd.it/5t6y7u8i.jpg?width=140&height=140&crop=1:1,smart&auto=webp&s=x",
		Summary:   `<a href="https://www.reddit.com/r/cats/comments/1fghij/"><img src="https://preview.redd.it/5t6y7u8i.jpg" alt="Whiskers"></a><a href="https://www.reddit.com/gallery/1fghij">[link]</a>`,
		FetchedAt: db.Now(),
	})

	body := doGet(h, "/", cookie).Body.String()
	if !strings.Contains(body, `id="item-1" class="video-card"`) {
		t.Fatalf("video item should render a video-card: %s", body)
	}
	if !strings.Contains(body, `src="https://i.ytimg.com/vi/H0KAi8AWsnM/hq720.jpg"`) {
		t.Fatalf("video card missing thumbnail: %s", body)
	}
	if !strings.Contains(body, `id="item-2" class="text-card"`) {
		t.Fatalf("text item should render a text-card: %s", body)
	}
	if !strings.Contains(body, `id="item-3" class="image-card"`) {
		t.Fatalf("image post should render an image-card: %s", body)
	}
	if !strings.Contains(body, `src="https://example.com/pic.jpg"`) {
		t.Fatalf("image card missing the image: %s", body)
	}
	if !strings.Contains(body, `id="item-4" class="link-card"`) {
		t.Fatalf("link post should render a link-card: %s", body)
	}
	if !strings.Contains(body, `src="https://external-preview.redd.it/1q2w3e4r.jpeg?width=320"`) {
		t.Fatalf("link card missing the external-preview thumbnail: %s", body)
	}
	if !strings.Contains(body, `class="thumb-badge"`) || !strings.Contains(body, ">external</span>") {
		t.Fatalf("link card missing the external badge: %s", body)
	}
	if !strings.Contains(body, `id="item-5" class="image-card"`) {
		t.Fatalf("gallery should render an image-card: %s", body)
	}
	if !strings.Contains(body, `src="https://i.redd.it/5t6y7u8i.jpg"`) {
		t.Fatalf("gallery card should use the full-res i.redd.it thumbnail: %s", body)
	}
	// The row keeps the modal data attrs and the read toggle.
	if !strings.Contains(body, `data-item-link="https://www.youtube.com/watch?v=H0KAi8AWsnM"`) {
		t.Fatalf("video card missing data-item-link: %s", body)
	}
	if !strings.Contains(body, `hx-post="/items/2/read"`) {
		t.Fatalf("text card missing read toggle: %s", body)
	}
	if !strings.Contains(body, `hx-post="/items/3/read"`) {
		t.Fatalf("image card missing read toggle: %s", body)
	}
	if !strings.Contains(body, `hx-post="/items/4/read"`) {
		t.Fatalf("link card missing read toggle: %s", body)
	}
	// Feed titles in the item meta link to the internal feed page, never the RSS url.
	if !strings.Contains(body, `href="/feeds/1">bigboxSWE</a>`) {
		t.Fatalf("item meta should link to /feeds/1 internally: %s", body)
	}
	if strings.Contains(body, `href="https://www.youtube.com/feeds/videos.xml`) {
		t.Fatalf("item meta must not link to the external feed url: %s", body)
	}
}

func TestItemViewImageLightbox(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Pics", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Pics", "https://pics.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "p1", Title: "Image Post", Link: "https://pics.dev/1",
		Summary:  `<a href="https://pics.dev/1"><img src="https://pics.dev/pic.jpg" alt="Image Post" /></a>`,
		ImageURL: "https://pics.dev/pic.jpg", FetchedAt: db.Now(),
	})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	rr := doGet(h, "/items/"+itoa(items[0].ID)+"/view", cookie)
	body := rr.Body.String()
	if !strings.Contains(body, `class="image-lightbox"`) {
		t.Fatalf("image post view missing image-lightbox: %s", body)
	}
	if !strings.Contains(body, `src="https://pics.dev/pic.jpg"`) {
		t.Fatalf("image post view missing the lightbox image: %s", body)
	}
	if strings.Contains(body, "item-body") {
		t.Fatalf("image post view should not render the summary body: %s", body)
	}
}

func TestItemViewMarksRead(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Blog", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{
		GUID: "g", Title: "Item", Link: "https://b.dev/1", FetchedAt: db.Now(),
	})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	id := items[0].ID
	if items[0].Read {
		t.Fatal("item should start unread")
	}
	rr := doGet(h, "/items/"+itoa(id)+"/view", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("item view: %d", rr.Code)
	}
	after, err := s.store.Items.ByID(u.ID, id)
	if err != nil || !after.Read {
		t.Fatalf("viewing should mark the item read: %+v err=%v", after, err)
	}
	// Marking read twice is idempotent.
	doGet(h, "/items/"+itoa(id)+"/view", cookie)
	after, _ = s.store.Items.ByID(u.ID, id)
	if !after.Read {
		t.Fatal("viewing a read item should not toggle it back to unread")
	}
}

func TestItemReadToggle(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "A", "", "")
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

func TestItemFavoriteToggle(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "A", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "B", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g", Title: "Item", Link: "https://b.dev/1", FetchedAt: db.Now()})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	id := items[0].ID
	rr := doForm(h, "POST", "/items/"+itoa(id)+"/favorite", url.Values{}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("toggle favorite: %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "item-"+itoa(id)) {
		t.Fatalf("item row fragment not returned: %s", body)
	}
	if !strings.Contains(body, `class="star on"`) {
		t.Fatalf("star should be filled: %s", body)
	}
	after, err := s.store.Items.ByID(u.ID, id)
	if err != nil || !after.Favorite {
		t.Fatalf("item should be favorited: %v %+v", err, after)
	}

	// Toggling again unfavorites.
	rr = doForm(h, "POST", "/items/"+itoa(id)+"/favorite", url.Values{}, cookie)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), `class="star on"`) {
		t.Fatalf("toggle off: %d %s", rr.Code, rr.Body.String())
	}
	after, _ = s.store.Items.ByID(u.ID, id)
	if after.Favorite {
		t.Fatal("item should no longer be favorited")
	}
}

func TestFavoritesPage(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "A", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "B", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g", Title: "Item", Link: "https://b.dev/1", FetchedAt: db.Now()})
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g2", Title: "Other", Link: "https://b.dev/2", FetchedAt: db.Now()})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	var favID int64
	for _, it := range items {
		if it.Link == "https://b.dev/1" {
			favID = it.ID
		}
	}
	if favID == 0 {
		t.Fatal("test item not found")
	}
	s.store.Items.SetFavorite(u.ID, favID, true)

	body := doGet(h, "/favorites", cookie).Body.String()
	if !strings.Contains(body, "favorites (1)") {
		t.Fatalf("favorites count wrong: %s", body)
	}
	if !strings.Contains(body, `data-item-link="https://b.dev/1"`) {
		t.Fatalf("favorited item missing from page: %s", body)
	}
	if strings.Contains(body, `data-item-link="https://b.dev/2"`) {
		t.Fatalf("unfavorited item should not appear: %s", body)
	}
}

func TestItemView(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
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
	// Modal meta links author and feed internally, never to the external RSS url.
	if !strings.Contains(body, `href="/authors/1">Metru</a>`) {
		t.Fatalf("modal meta should link the author internally: %s", body)
	}
	if !strings.Contains(body, `href="/feeds/1">Blog</a>`) {
		t.Fatalf("modal meta should link the feed internally: %s", body)
	}
	if strings.Contains(body, `href="https://b.dev/rss.xml"`) {
		t.Fatalf("modal meta must not link to the external feed url: %s", body)
	}
	// "open live" lives in the dialog header (layout), not the fragment; the
	// header also carries the empty slot the controls are relocated into.
	home := doGet(h, "/", cookie).Body.String()
	if !strings.Contains(home, `id="item-dialog-live"`) ||
		!strings.Contains(home, `id="item-dialog-controls"`) {
		t.Fatalf("dialog header should carry open live + the controls slot: %s", home)
	}
	if strings.Contains(body, `id="item-dialog-live"`) {
		t.Fatalf("the fragment must not render open live: %s", body)
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
	// An existing author clears the new-author fields via an empty 200 (htmx
	// doesn't swap on 204).
	if rr := doGet(h, "/fragments/author-form?author_id=5", cookie); rr.Code != http.StatusOK || rr.Body.Len() != 0 {
		t.Fatalf("expected an empty 200 for an existing author, got %d %q", rr.Code, rr.Body.String())
	}
}

func TestAuthorPageHasAddFeedDialog(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")

	body := doGet(h, "/authors/"+itoa(a.ID), cookie).Body.String()
	for _, want := range []string{
		`id="add-author-feed-dialog"`,
		`name="author_id" value="` + itoa(a.ID) + `"`,
		`hx-post="/fragments/feed-preview"`,
		"+ add feed",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("author page missing %q: %s", want, body)
		}
	}
}

func TestAuthorLinks(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")

	// The edit form carries editable link rows and an add-row template; the
	// author page no longer has a links heading or add-link dialog.
	edit := doGet(h, "/authors/"+itoa(a.ID)+"/edit", cookie).Body.String()
	for _, want := range []string{
		`id="author-link-fields"`,
		`id="author-link-row-template"`,
		`name="link_url"`,
		`name="link_label"`,
		"addAuthorLinkRow()",
	} {
		if !strings.Contains(edit, want) {
			t.Fatalf("author edit page missing %q: %s", want, edit)
		}
	}
	page := doGet(h, "/authors/"+itoa(a.ID), cookie).Body.String()
	if strings.Contains(page, `id="add-author-link-dialog"`) || strings.Contains(page, ">links<") {
		t.Fatalf("author page should not have a links section header or dialog: %s", page)
	}

	// Add two links (one labeled, one not) by posting the edit form.
	rr := doForm(h, "POST", "/authors/"+itoa(a.ID)+"/edit", url.Values{
		"name":          {"Metru"},
		"links_present": {"1"},
		"link_id":       {"", ""},
		"link_label":    {"Twitch", ""},
		"link_url":      {"https://twitch.tv/ThePrimeagen", "https://www.example.com/page"},
	}, cookie)
	if rr.Code != http.StatusFound {
		t.Fatalf("save with links: %d %s", rr.Code, rr.Body.String())
	}
	links, _ := s.store.AuthorLinks.ListByAuthor(u.ID, a.ID)
	if len(links) != 2 {
		t.Fatalf("links persisted: %d", len(links))
	}

	// The author page renders them inline in the card, external-marked, with the
	// hostname fallback for the unlabeled one.
	page = doGet(h, "/authors/"+itoa(a.ID), cookie).Body.String()
	if !strings.Contains(page, `href="https://twitch.tv/ThePrimeagen"`) ||
		!strings.Contains(page, `class="external"`) || !strings.Contains(page, ">Twitch</a>") {
		t.Fatalf("author page should show the labeled link: %s", page)
	}
	if !strings.Contains(page, ">example.com</a>") {
		t.Fatalf("unlabeled link should fall back to the hostname: %s", page)
	}

	// Editing an existing link's label/url updates it in place.
	rr = doForm(h, "POST", "/authors/"+itoa(a.ID)+"/edit", url.Values{
		"name":          {"Metru"},
		"links_present": {"1"},
		"link_id":       {itoa(links[0].ID), itoa(links[1].ID)},
		"link_label":    {"Twitch", ""},
		"link_url":      {"https://twitch.tv/SomeoneElse", "https://www.example.com/page"},
	}, cookie)
	if rr.Code != http.StatusFound {
		t.Fatalf("update link: %d", rr.Code)
	}
	got, _ := s.store.AuthorLinks.ByID(u.ID, links[0].ID)
	if got.URL != "https://twitch.tv/SomeoneElse" {
		t.Fatalf("link not updated in place: %+v", got)
	}
	if after, _ := s.store.AuthorLinks.ListByAuthor(u.ID, a.ID); len(after) != 2 {
		t.Fatalf("update should not add or drop links: %d", len(after))
	}

	// Omitting a link's row (its ✕ removes it) deletes it.
	rr = doForm(h, "POST", "/authors/"+itoa(a.ID)+"/edit", url.Values{
		"name":          {"Metru"},
		"links_present": {"1"},
		"link_id":       {itoa(links[1].ID)},
		"link_label":    {""},
		"link_url":      {"https://www.example.com/page"},
	}, cookie)
	if rr.Code != http.StatusFound {
		t.Fatalf("delete by omission: %d", rr.Code)
	}
	if after, _ := s.store.AuthorLinks.ListByAuthor(u.ID, a.ID); len(after) != 1 || after[0].ID != links[1].ID {
		t.Fatalf("removed row should delete its link: %+v", after)
	}

	// A blank/omitted url on a new row is ignored, not a crash or a bad link.
	if _, err := s.store.AuthorLinks.ByID(u.ID, links[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("dropped link should be gone: %v", err)
	}
}

func TestAuthorLinkScopedToOwner(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	link, _ := s.store.AuthorLinks.Create(u.ID, a.ID, "", "https://example.com")

	// Saving another (nonexistent) author's edit form 404s and leaves the link.
	if rr := doForm(h, "POST", "/authors/999999/edit", url.Values{
		"name": {"X"}, "link_id": {itoa(link.ID)}, "link_label": {""}, "link_url": {"https://x.dev"},
	}, cookie); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown author edit should 404, got %d", rr.Code)
	}
	if _, err := s.store.AuthorLinks.ByID(u.ID, link.ID); err != nil {
		t.Fatalf("link should remain: %v", err)
	}

	// A save that doesn't carry the link fields (links_present absent) leaves
	// the links untouched, so non-form clients can't wipe them by omission.
	if rr := doForm(h, "POST", "/authors/"+itoa(a.ID)+"/edit", url.Values{"name": {"Metru"}}, cookie); rr.Code != http.StatusFound {
		t.Fatalf("bare edit save: %d", rr.Code)
	}
	if _, err := s.store.AuthorLinks.ByID(u.ID, link.ID); err != nil {
		t.Fatalf("link should survive a save without link fields: %v", err)
	}
}

func TestExternalLinksCarryMarker(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "https://b.dev", "", 900)
	s.store.AuthorLinks.Create(u.ID, a.ID, "twitch", "https://twitch.tv/metru")

	// Feed page: "feed" and "home" links are external, carry the ↗ marker
	// class, and are hardened against referrer leakage.
	body := doGet(h, "/feeds/"+itoa(f.ID), cookie).Body.String()
	if !strings.Contains(body, `href="https://b.dev/rss.xml" class="external" referrerpolicy="no-referrer">feed`) {
		t.Fatalf("feed page feed link should be external-marked: %s", body)
	}
	if !strings.Contains(body, `href="https://b.dev" target="_blank" rel="noopener noreferrer" referrerpolicy="no-referrer" class="external">home`) {
		t.Fatalf("feed page home link should be external-marked: %s", body)
	}

	// Author page: a link bookmark is external and marked.
	body = doGet(h, "/authors/"+itoa(a.ID), cookie).Body.String()
	if !strings.Contains(body, `href="https://twitch.tv/metru" target="_blank" rel="noopener noreferrer" referrerpolicy="no-referrer" class="external">`) {
		t.Fatalf("author link should be external-marked: %s", body)
	}

	// Author page feeds list row: the "feed" link is external and marked.
	body = doGet(h, "/authors/"+itoa(a.ID), cookie).Body.String()
	if !strings.Contains(body, `href="https://b.dev/rss.xml" class="external" referrerpolicy="no-referrer">feed`) {
		t.Fatalf("author page feeds list feed link should be external-marked: %s", body)
	}

	// "open live" in the dialog header is external and marked.
	body = doGet(h, "/", cookie).Body.String()
	if !strings.Contains(body, `class="small external"`) {
		t.Fatalf("open live should be external-marked: %s", body)
	}
}

func TestReferrerPolicyHeader(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	if got := doGet(h, "/", cookie).Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q, want no-referrer", got)
	}
}

func TestFeedAndAuthorPagesShowItems(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
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

func TestScopedReadUnreadTabs(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g1", Title: "Unread Item", Link: "https://b.dev/1", FetchedAt: db.Now()})
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g2", Title: "Read Item", Link: "https://b.dev/2", FetchedAt: db.Now()})
	coll, _ := s.store.Collections.Create(u.ID, "Dev")
	s.store.Collections.AddFeed(u.ID, coll.ID, f.ID)

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	var readID int64
	for _, it := range items {
		if it.Link == "https://b.dev/2" {
			readID = it.ID
		}
	}
	if readID == 0 {
		t.Fatal("read item not found")
	}
	if err := s.store.Items.SetRead(u.ID, readID, true); err != nil {
		t.Fatalf("SetRead: %v", err)
	}

	for _, scope := range []struct {
		base string // page URL
		frag string // fragment URL
	}{
		{"/feeds/" + itoa(f.ID), "/feeds/" + itoa(f.ID) + "/items"},
		{"/authors/" + itoa(a.ID), "/authors/" + itoa(a.ID) + "/items"},
		{"/collections/" + itoa(coll.ID), "/collections/" + itoa(coll.ID) + "/items"},
	} {
		// Default view is unread: only the unread item, tabs show both counts.
		body := doGet(h, scope.base, cookie).Body.String()
		for _, want := range []string{`class="tabs"`, "unread (1)", "read (1)", `data-item-link="https://b.dev/1"`} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s default view missing %q: %s", scope.base, want, body)
			}
		}
		if strings.Contains(body, `data-item-link="https://b.dev/2"`) {
			t.Fatalf("%s default view should not show the read item: %s", scope.base, body)
		}
		if strings.Contains(body, "read-at") {
			t.Fatalf("%s default view should not show read-at: %s", scope.base, body)
		}

		// ?view=read shows only the read item, with its read-at timestamp.
		body = doGet(h, scope.base+"?view=read", cookie).Body.String()
		if !strings.Contains(body, `data-item-link="https://b.dev/2"`) ||
			strings.Contains(body, `data-item-link="https://b.dev/1"`) {
			t.Fatalf("%s read view wrong list: %s", scope.base, body)
		}
		if !strings.Contains(body, `class="read-at"`) {
			t.Fatalf("%s read view missing read-at timestamp: %s", scope.base, body)
		}

		// The fragment endpoint returns the tabbed list for the requested view.
		rr := doGet(h, scope.frag+"?view=read", cookie)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `data-item-link="https://b.dev/2"`) {
			t.Fatalf("%s fragment: %d %s", scope.frag, rr.Code, rr.Body.String())
		}
	}
}

func TestAuthorPageDropsSelfLinks(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g", Title: "Feed Item", Link: "https://b.dev/1", FetchedAt: db.Now()})

	self := `href="/authors/` + itoa(a.ID) + `">`
	authorPage := doGet(h, "/authors/"+itoa(a.ID), cookie).Body.String()
	if strings.Contains(authorPage, self) {
		t.Fatalf("author page must not link back to itself in item/feed meta: %s", authorPage)
	}
	// The feed row meta now omits the author entirely.
	if strings.Contains(authorPage, `>Metru</a>`) {
		t.Fatalf("feed rows should not render the author name as a link: %s", authorPage)
	}

	// Home (cross-author) list keeps the author links.
	home := doGet(h, "/", cookie).Body.String()
	if !strings.Contains(home, self) {
		t.Fatalf("home list should still link the author: %s", home)
	}

	// Read/favorite toggles propagate the suppression so a swapped row on the
	// author page stays consistent.
	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	if len(items) != 1 {
		t.Fatalf("expected one item, got %d", len(items))
	}
	hidden := doForm(h, "POST", "/items/"+itoa(items[0].ID)+"/read", url.Values{"hideAuthor": {"1"}}, cookie)
	if strings.Contains(hidden.Body.String(), self) {
		t.Fatalf("row swapped on an author page must hide the author link: %s", hidden.Body.String())
	}
	shown := doForm(h, "POST", "/items/"+itoa(items[0].ID)+"/read", url.Values{}, cookie)
	if !strings.Contains(shown.Body.String(), self) {
		t.Fatalf("row swapped elsewhere must keep the author link: %s", shown.Body.String())
	}
}

func TestDisplayModeControlPresent(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g", Title: "Feed Item", Link: "https://b.dev/1", FetchedAt: db.Now()})
	coll, _ := s.store.Collections.Create(u.ID, "Dev")
	s.store.Collections.AddFeed(u.ID, coll.ID, f.ID)
	list, _ := s.store.Lists.Create(u.ID, "Reading")

	option := `role="menuitemradio" data-option="grid"`
	// Each page's picker carries the page path in data-scope so the client
	// remembers the list/grid choice per author/feed/collection/page.
	cases := []struct{ path, scope string }{
		{"/unread", "/unread"},
		{"/read", "/read"},
		{"/favorites", "/favorites"},
		{"/authors/" + itoa(a.ID), "/authors/" + itoa(a.ID)},
		{"/feeds/" + itoa(f.ID), "/feeds/" + itoa(f.ID)},
		{"/collections/" + itoa(coll.ID), "/collections/" + itoa(coll.ID)},
		{"/lists/" + itoa(list.ID), "/lists/" + itoa(list.ID)},
	}
	for _, c := range cases {
		body := doGet(h, c.path, cookie).Body.String()
		if !strings.Contains(body, `class="picker" data-picker="display" data-scope="`+c.scope+`"`) ||
			!strings.Contains(body, option) {
			t.Fatalf("%s missing scoped display-mode control (scope %q): %s", c.path, c.scope, body)
		}
	}
}

func TestAuthorPageFeedShowsCollectionTags(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	f2, _ := s.store.Feeds.Create(u.ID, a.ID, "Other", "https://o.dev/rss.xml", "", "", 900)
	cats, _ := s.store.Collections.Create(u.ID, "cats")
	dev, _ := s.store.Collections.Create(u.ID, "dev")
	s.store.Collections.AddFeed(u.ID, cats.ID, f.ID)
	s.store.Collections.AddFeed(u.ID, dev.ID, f.ID)
	// An auto collection also holds the feed but must not render a tag.
	yt, _ := s.store.Collections.EnsureAuto(u.ID, "youtube.com")
	s.store.Collections.AddFeed(u.ID, yt.ID, f.ID)

	body := doGet(h, "/authors/"+itoa(a.ID), cookie).Body.String()
	// Both collections render as clickable "#name" tags linking to the
	// collection page, next to the feed they belong to.
	for _, want := range []string{
		`class="tag" href="/collections/` + itoa(cats.ID) + `"`,
		`class="tag" href="/collections/` + itoa(dev.ID) + `"`,
		"#cats", "#dev",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("author page missing collection tag %q: %s", want, body)
		}
	}
	// Auto collections don't render a tag (they mirror the feed's own site).
	if strings.Contains(body, `href="/collections/`+itoa(yt.ID)+`"`) || strings.Contains(body, "#youtube.com") {
		t.Fatalf("auto collection should not render a tag: %s", body)
	}
	// The feed with no collections renders no tags.
	body2 := doGet(h, "/feeds/"+itoa(f2.ID), cookie).Body.String()
	if strings.Contains(body2, `class="tag"`) {
		t.Fatalf("collection-free feed page should have no tags: %s", body2)
	}

	// Removing the feed from a collection drops its tag.
	s.store.Collections.RemoveFeed(u.ID, cats.ID, f.ID)
	body = doGet(h, "/authors/"+itoa(a.ID), cookie).Body.String()
	if strings.Contains(body, "#cats") {
		t.Fatalf("removed collection tag should be gone: %s", body)
	}
	if !strings.Contains(body, "#dev") {
		t.Fatalf("remaining collection tag should stay: %s", body)
	}
}

func TestAuthorsPageSortControl(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	body := doGet(h, "/authors", cookie).Body.String()
	if !strings.Contains(body, `class="picker" data-picker="authors"`) ||
		!strings.Contains(body, `data-option="newest"`) ||
		!strings.Contains(body, `data-option="unread"`) ||
		!strings.Contains(body, "abc") {
		t.Fatalf("authors page should carry the sort picker: %s", body)
	}
}

func TestItemModalShowsImageEnclosure(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Photos", "https://p.dev/rss.xml", "", "", 900)
	if _, err := s.store.Items.Upsert(f.ID, store.Item{GUID: "g1", Title: "Shot", Link: "https://p.dev/1", FetchedAt: db.Now()}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	itemID, _ := s.store.Items.ByFeedGUID(f.ID, "g1")
	s.store.Items.ReplaceEnclosures(itemID, []store.Enclosure{
		{URL: "https://p.dev/photo.jpg?e=1790070194&t=signed", Title: "Photo", MIMEType: "image/jpeg", Size: 100},
		{URL: "https://p.dev/notes.txt", Title: "Notes", MIMEType: "text/plain", Size: 10},
	})

	body := doGet(h, "/items/"+itoa(itemID)+"/view", cookie).Body.String()
	if !strings.Contains(body, `<img src="https://p.dev/photo.jpg?e=1790070194&amp;t=signed"`) {
		t.Fatalf("item modal should embed the image enclosure: %s", body)
	}
	// The image enclosure is rendered inline, not as a bare download link; the
	// non-image enclosure still is.
	if strings.Contains(body, `<a href="https://p.dev/photo.jpg`) {
		t.Fatalf("image enclosure should not appear as a bare link: %s", body)
	}
	if !strings.Contains(body, `<a href="https://p.dev/notes.txt"`) {
		t.Fatalf("non-image enclosure should keep its download link: %s", body)
	}
}

func TestItemModalSkipsImageEnclosureAlreadyInBody(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Photos", "https://p.dev/rss.xml", "", "", 900)
	inline := "https://p.dev/inline.jpg"
	if _, err := s.store.Items.Upsert(f.ID, store.Item{
		GUID: "g1", Title: "Shot", Link: "https://p.dev/1",
		Summary: `<p>a caption</p><img src="` + inline + `">`, FetchedAt: db.Now(),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	itemID, _ := s.store.Items.ByFeedGUID(f.ID, "g1")
	enc := "https://p.dev/enclosure.jpg?e=1&t=signed"
	s.store.Items.ReplaceEnclosures(itemID, []store.Enclosure{
		{URL: enc, Title: "Photo", MIMEType: "image/jpeg", Size: 100},
	})

	body := doGet(h, "/items/"+itoa(itemID)+"/view", cookie).Body.String()
	// The body's own image renders; the enclosure duplicates it, so it must
	// not be shown inline nor as a bare link.
	if !strings.Contains(body, `<img src="https://p.dev/inline.jpg"`) {
		t.Fatalf("item body should render its embedded image: %s", body)
	}
	if strings.Contains(body, "enclosure.jpg") {
		t.Fatalf("image enclosure duplicating the body should not render: %s", body)
	}
}

func TestAuthorsRowIsCondensed(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g1", Title: "One", Link: "https://b.dev/1", FetchedAt: db.Now()})

	body := doGet(h, "/authors", cookie).Body.String()
	// The row is the linked name + unread/feed counts; the external url is gone.
	if !strings.Contains(body, `<a href="/authors/1"><strong>Metru</strong></a>`) {
		t.Fatalf("author name should be linked: %s", body)
	}
	if !strings.Contains(body, `<span class="unread-count">1 unread</span>`) ||
		!strings.Contains(body, "1 feeds") {
		t.Fatalf("authors row should show the unread and feed counts: %s", body)
	}
	if strings.Contains(body, "https://metru.dev") {
		t.Fatalf("condensed row should drop the external url: %s", body)
	}
}

func TestAuthorEditDeleteFlow(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	_, _ = s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)

	// The list row no longer carries a delete button; the edit page does.
	list := doGet(h, "/authors", cookie).Body.String()
	if strings.Contains(list, "/authors/"+itoa(a.ID)+"/delete") {
		t.Fatalf("authors list should not offer row-level delete: %s", list)
	}
	edit := doGet(h, "/authors/"+itoa(a.ID)+"/edit", cookie).Body.String()
	if !strings.Contains(edit, `form class="stack delete-form"`) ||
		!strings.Contains(edit, `action="/authors/`+itoa(a.ID)+`/delete"`) ||
		!strings.Contains(edit, `class="danger"`) || !strings.Contains(edit, ">delete<") {
		t.Fatalf("edit page missing the delete form: %s", edit)
	}

	// Deleting redirects back to the authors list and removes the author (and
	// cascades its feed).
	rr := doForm(h, "POST", "/authors/"+itoa(a.ID)+"/delete", url.Values{}, cookie)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/authors" {
		t.Fatalf("delete: %d %q", rr.Code, rr.Header().Get("Location"))
	}
	if _, err := s.store.Authors.ByID(u.ID, a.ID); err == nil {
		t.Fatal("author should be gone after delete")
	}
	if feeds, _ := s.store.Feeds.ListByAuthor(u.ID, a.ID); len(feeds) != 0 {
		t.Fatalf("feed should cascade with author: %+v", feeds)
	}
}

func TestItemMenuOnCards(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g", Title: "Post", Link: "https://b.dev/1", FetchedAt: db.Now()})

	// Each card in the list/grid carries the ⋯ menu with all three options.
	body := doGet(h, "/", cookie).Body.String()
	if !strings.Contains(body, `class="item-menu"`) ||
		!strings.Contains(body, "mark all before as read") ||
		!strings.Contains(body, "mark all after as read") ||
		!strings.Contains(body, `hx-post="/items/1/read-before"`) ||
		!strings.Contains(body, `hx-post="/items/1/read-after"`) {
		t.Fatalf("item cards should carry the ⋯ menu: %s", body)
	}
}

func TestMarkRangeReadHTTP(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	f2, _ := s.store.Feeds.Create(u.ID, a.ID, "Other", "https://o.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "a1", Title: "newest", Link: "https://b.dev/1", PublishedAt: "2026-01-03 00:00:00", FetchedAt: db.Now()})
	s.store.Items.Upsert(f.ID, store.Item{GUID: "a2", Title: "middle", Link: "https://b.dev/2", PublishedAt: "2026-01-02 00:00:00", FetchedAt: db.Now()})
	s.store.Items.Upsert(f.ID, store.Item{GUID: "a3", Title: "oldest", Link: "https://b.dev/3", PublishedAt: "2026-01-01 00:00:00", FetchedAt: db.Now()})
	s.store.Items.Upsert(f2.ID, store.Item{GUID: "b1", Title: "other", Link: "https://o.dev/1", PublishedAt: "2026-01-10 00:00:00", FetchedAt: db.Now()})

	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	var middle int64
	for _, it := range items {
		if it.Title == "middle" {
			middle = it.ID
		}
	}
	readState := func(title string) bool {
		for _, it := range items {
			if it.Title == title {
				got, _ := s.store.Items.ByID(u.ID, it.ID)
				return got.Read
			}
		}
		t.Fatalf("missing item %q", title)
		return false
	}

	rr := doForm(h, "POST", "/items/"+itoa(middle)+"/read-before", url.Values{}, cookie)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("read-before: %d", rr.Code)
	}
	for _, c := range []struct {
		title string
		want  bool
	}{
		{"newest", true}, {"middle", false}, {"oldest", false}, {"other", false},
	} {
		if got := readState(c.title); got != c.want {
			t.Fatalf("after read-before: %s read=%v want %v", c.title, got, c.want)
		}
	}

	rr = doForm(h, "POST", "/items/"+itoa(middle)+"/read-after", url.Values{}, cookie)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("read-after: %d", rr.Code)
	}
	if got := readState("oldest"); !got {
		t.Fatalf("after read-after: oldest should be read")
	}
}

func TestFeedEditDeleteFlow(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)

	// The author page's feed rows no longer carry pause/delete buttons.
	author := doGet(h, "/authors/"+itoa(a.ID), cookie).Body.String()
	if strings.Contains(author, "/toggle") || strings.Contains(author, "/feeds/"+itoa(f.ID)+"/delete") {
		t.Fatalf("feed rows should not offer pause/delete outside the edit page: %s", author)
	}

	// The feed edit page carries the delete form.
	edit := doGet(h, "/feeds/"+itoa(f.ID)+"/edit", cookie).Body.String()
	if !strings.Contains(edit, `action="/feeds/`+itoa(f.ID)+`/delete"`) ||
		!strings.Contains(edit, `class="danger"`) {
		t.Fatalf("feed edit page should carry a delete form: %s", edit)
	}

	// Deleting redirects back to the author page and removes the feed.
	rr := doForm(h, "POST", "/feeds/"+itoa(f.ID)+"/delete", url.Values{}, cookie)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/authors/"+itoa(a.ID) {
		t.Fatalf("feed delete: %d %q", rr.Code, rr.Header().Get("Location"))
	}
	if _, err := s.store.Feeds.ByID(u.ID, f.ID); err == nil {
		t.Fatal("feed should be gone after delete")
	}
}

func TestCollectionsIndexShowsStats(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f1, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	f2, _ := s.store.Feeds.Create(u.ID, a.ID, "Other", "https://o.dev/rss.xml", "", "", 900)
	c, _ := s.store.Collections.Create(u.ID, "Dev")
	s.store.Collections.AddFeed(u.ID, c.ID, f1.ID)
	s.store.Collections.AddFeed(u.ID, c.ID, f2.ID)
	s.store.Items.Upsert(f1.ID, store.Item{GUID: "a", Title: "a", Link: "https://b.dev/1", FetchedAt: db.Now()})
	s.store.Items.Upsert(f1.ID, store.Item{GUID: "b", Title: "b", Link: "https://b.dev/2", FetchedAt: db.Now()})
	s.store.Items.Upsert(f2.ID, store.Item{GUID: "c", Title: "c", Link: "https://o.dev/1", FetchedAt: db.Now()})
	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	if err := s.store.Items.SetRead(u.ID, items[0].ID, true); err != nil {
		t.Fatalf("SetRead: %v", err)
	}

	body := doGet(h, "/collections", cookie).Body.String()
	if !strings.Contains(body, `class="card"`) ||
		!strings.Contains(body, "2 feeds · 2 unread · 1 read") {
		t.Fatalf("collections index should render cards with stats: %s", body)
	}
}

func TestAuthorEditHasAvatarFields(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	edit := doGet(h, "/authors/"+itoa(a.ID)+"/edit", cookie).Body.String()
	// The avatar url input lives with the avatar image/heading inside the form.
	if !strings.Contains(edit, `name="avatar_url"`) ||
		!strings.Contains(edit, `<h2>avatar</h2>`) ||
		!strings.Contains(edit, `id="author-avatar-card"`) ||
		!strings.Contains(edit, `hx-target="#author-avatar-card"`) {
		t.Fatalf("author edit should group the avatar url with the avatar section: %s", edit)
	}
}

func TestGlobalAddCreatesAuthorWithFeed(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")

	// The global add with a new author yields an author with their first feed.
	rr := doForm(h, "POST", "/feeds", url.Values{
		"title": {"Blog"}, "feed_url": {"https://example.com/rss.xml"},
		"author_id": {"new"}, "author_name": {"Metru"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("global add: %d %s", rr.Code, rr.Body.String())
	}
	if retarget := rr.Header().Get("HX-Retarget"); retarget != "#authors-list" {
		t.Fatalf("new-author add should append to the authors list, retarget %q", retarget)
	}
	if !strings.Contains(rr.Body.String(), `id="author-`) {
		t.Fatalf("global add should respond with an author row: %s", rr.Body.String())
	}
	authors, _ := s.store.Authors.List(u.ID)
	if len(authors) != 1 || authors[0].Name != "Metru" {
		t.Fatalf("expected one author: %+v", authors)
	}
	feeds, _ := s.store.Feeds.ListByAuthor(u.ID, authors[0].ID)
	if len(feeds) != 1 || feeds[0].Title != "Blog" {
		t.Fatalf("author should own the first feed: %+v", feeds)
	}

	// The authors page shows the author with a feed count; the author page the feed.
	body := doGet(h, "/authors", cookie).Body.String()
	if !strings.Contains(body, "Metru") || !strings.Contains(body, "1 feeds") {
		t.Fatalf("authors page should show the author and feed count: %s", body)
	}
	body = doGet(h, "/authors/"+itoa(authors[0].ID), cookie).Body.String()
	if !strings.Contains(body, "Blog") {
		t.Fatalf("author page should list the feed: %s", body)
	}
}

func TestAuthorPageAddFeed(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")

	rr := doForm(h, "POST", "/authors/"+itoa(a.ID)+"/feeds", url.Values{
		"title": {"Blog"}, "feed_url": {"https://example.com/rss.xml"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("author add feed: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `id="feed-`) {
		t.Fatalf("author add feed should respond with a feed row: %s", rr.Body.String())
	}
	feeds, _ := s.store.Feeds.ListByAuthor(u.ID, a.ID)
	if len(feeds) != 1 || feeds[0].AuthorID != a.ID {
		t.Fatalf("feed should belong to the author: %+v", feeds)
	}

	// Missing fields are rejected visibly.
	rr = doForm(h, "POST", "/authors/"+itoa(a.ID)+"/feeds", url.Values{}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "title and feed url are required") {
		t.Fatalf("missing fields: %d %s", rr.Code, rr.Body.String())
	}
}

func TestFeedsPageRemoved(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	// The top-level feeds listing page is gone.
	rr := doGet(h, "/feeds", cookie)
	if rr.Code != http.StatusNotFound && rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("/feeds should no longer serve a page, got %d", rr.Code)
	}
	// The topbar no longer links to it.
	home := doGet(h, "/", cookie).Body.String()
	if strings.Contains(home, `href="/feeds"`) {
		t.Fatal("topbar should not link to a feeds page")
	}
}

func TestCollectionAddFeedGroupedByAuthor(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	c, _ := s.store.Collections.Create(u.ID, "Dev")

	body := doGet(h, "/collections/"+itoa(c.ID), cookie).Body.String()
	if !strings.Contains(body, `<optgroup label="Metru">`) {
		t.Fatalf("collection add-feed dropdown should group feeds by author: %s", body)
	}
}

func TestStaleFeedBadge(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "https://b.dev", "", 900)

	// No items yet -> no warning.
	body := doGet(h, "/authors/"+itoa(a.ID), cookie).Body.String()
	if strings.Contains(body, "no new posts") || strings.Contains(body, "may be abandoned") {
		t.Fatalf("no stale warning expected with no items: %s", body)
	}

	// A feed quiet for 10 days -> amber badge on the author page.
	old := time.Now().Add(-10 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	if err := s.store.Feeds.SetLastItemAt(f.ID, old); err != nil {
		t.Fatal(err)
	}
	body = doGet(h, "/authors/"+itoa(a.ID), cookie).Body.String()
	if !strings.Contains(body, "badge warn") || !strings.Contains(body, "no new posts in 10d") {
		t.Fatalf("expected stale badge on author page: %s", body)
	}

	// Quiet for 35 days -> "may be abandoned" on the feed page.
	abandoned := time.Now().Add(-35 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	if err := s.store.Feeds.SetLastItemAt(f.ID, abandoned); err != nil {
		t.Fatal(err)
	}
	body = doGet(h, "/feeds/"+itoa(f.ID), cookie).Body.String()
	if !strings.Contains(body, "notice") || !strings.Contains(body, "may be abandoned") {
		t.Fatalf("expected abandoned notice on feed page: %s", body)
	}
}

func TestFeedEditAutoInterval(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)

	// Edit form shows the auto checkbox (new feeds default to on).
	edit := doGet(h, "/feeds/"+itoa(f.ID)+"/edit", cookie).Body.String()
	if !strings.Contains(edit, `name="poll_interval_auto"`) {
		t.Fatalf("edit form should render the auto-adjust checkbox: %s", edit)
	}

	// With auto on, the posted interval is ignored and the stored one kept.
	if err := s.store.Feeds.SetPollInterval(f.ID, 7200); err != nil {
		t.Fatal(err)
	}
	rr := doForm(h, "POST", "/feeds/"+itoa(f.ID)+"/edit", url.Values{
		"title": {"Blog"}, "feed_url": {"https://b.dev/rss.xml"},
		"poll_interval_sec": {"9999"}, "poll_interval_auto": {"1"},
		"author_id": {itoa(a.ID)}, "enabled": {"1"},
	}, cookie)
	if rr.Code != http.StatusFound {
		t.Fatalf("edit: %d", rr.Code)
	}
	after, _ := s.store.Feeds.ByID(u.ID, f.ID)
	if after.PollIntervalSec != 7200 || !after.PollIntervalAuto {
		t.Fatalf("auto on should preserve stored interval: %+v", after)
	}

	// Without the checkbox, the manual interval is used and auto turns off.
	rr = doForm(h, "POST", "/feeds/"+itoa(f.ID)+"/edit", url.Values{
		"title": {"Blog"}, "feed_url": {"https://b.dev/rss.xml"},
		"poll_interval_sec": {"600"},
		"author_id":         {itoa(a.ID)}, "enabled": {"1"},
	}, cookie)
	if rr.Code != http.StatusFound {
		t.Fatalf("edit manual: %d", rr.Code)
	}
	after, _ = s.store.Feeds.ByID(u.ID, f.ID)
	if after.PollIntervalSec != 600 || after.PollIntervalAuto {
		t.Fatalf("auto off should use the manual interval: %+v", after)
	}
}

func TestFeedCreateBlocksDuplicateURL(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")

	rr := doForm(h, "POST", "/authors/"+itoa(a.ID)+"/feeds", url.Values{
		"title": {"Blog"}, "feed_url": {"https://b.dev/rss.xml"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("first add: %d %s", rr.Code, rr.Body.String())
	}
	// The same feed URL again is blocked with a visible warning.
	rr = doForm(h, "POST", "/authors/"+itoa(a.ID)+"/feeds", url.Values{
		"title": {"Blog again"}, "feed_url": {"https://b.dev/rss.xml"},
	}, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "already have this feed") {
		t.Fatalf("duplicate feed should be blocked: %d %s", rr.Code, rr.Body.String())
	}
	if feeds, _ := s.store.Feeds.List(u.ID); len(feeds) != 1 {
		t.Fatalf("duplicate feed should not be created: %+v", feeds)
	}
	// The same title/home with a different feed URL is allowed.
	rr = doForm(h, "POST", "/authors/"+itoa(a.ID)+"/feeds", url.Values{
		"title": {"Blog"}, "feed_url": {"https://b.dev/other.xml"},
	}, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("a different feed url should be allowed: %d %s", rr.Code, rr.Body.String())
	}
}

func TestItemListSortDirection(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "a", Title: "Old", Link: "https://b.dev/1", PublishedAt: "2026-01-01 00:00:00", FetchedAt: db.Now()})
	s.store.Items.Upsert(f.ID, store.Item{GUID: "b", Title: "New", Link: "https://b.dev/2", PublishedAt: "2026-01-02 00:00:00", FetchedAt: db.Now()})

	// Default (desc): newest first, before= cursor.
	body := doGet(h, "/", cookie).Body.String()
	if strings.Index(body, "New") > strings.Index(body, "Old") {
		t.Fatalf("default should list newest first: %s", body)
	}
	// Ascending: oldest first with the asc sort control and after= links.
	body = doGet(h, "/?dir=asc", cookie).Body.String()
	if strings.Index(body, "Old") > strings.Index(body, "New") {
		t.Fatalf("ascending should list oldest first: %s", body)
	}
	if !strings.Contains(body, `data-picker="dir"`) || !strings.Contains(body, "dir=asc") {
		t.Fatalf("ascending page should carry the sort-direction picker: %s", body)
	}
}

func TestCollectionEditAndDelete(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	c, _ := s.store.Collections.Create(u.ID, "Dev")

	// The index has no delete button; the edit page does.
	idx := doGet(h, "/collections", cookie).Body.String()
	if strings.Contains(idx, "/collections/"+itoa(c.ID)+"/delete") {
		t.Fatalf("collections index should not offer delete: %s", idx)
	}
	edit := doGet(h, "/collections/"+itoa(c.ID)+"/edit", cookie).Body.String()
	if !strings.Contains(edit, `action="/collections/`+itoa(c.ID)+`/edit"`) ||
		!strings.Contains(edit, `action="/collections/`+itoa(c.ID)+`/delete"`) ||
		!strings.Contains(edit, ">delete<") {
		t.Fatalf("collection edit page missing rename/delete: %s", edit)
	}

	rr := doForm(h, "POST", "/collections/"+itoa(c.ID)+"/edit", url.Values{"name": {"Reading"}}, cookie)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("rename collection: %d", rr.Code)
	}
	if got, _ := s.store.Collections.ByID(u.ID, c.ID); got.Name != "Reading" {
		t.Fatalf("collection not renamed: %+v", got)
	}

	rr = doForm(h, "POST", "/collections/"+itoa(c.ID)+"/delete", url.Values{}, cookie)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/collections" {
		t.Fatalf("delete collection: %d %q", rr.Code, rr.Header().Get("Location"))
	}
}
