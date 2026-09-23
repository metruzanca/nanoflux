package poller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// paginatedServer serves `total` Atom/RSS pages via rel="next" links keyed by
// a ?page= query param. Each page carries 2 items with page-unique GUIDs;
// pages beyond the last are empty (as real feeds past their end are), which is
// what lets a ?page=N walk terminate.
func paginatedServer(total int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			page, _ = strconv.Atoi(p)
		}
		if page > total {
			fmt.Fprintf(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>B</title></channel></rss>`)
			return
		}
		var items strings.Builder
		for i := 1; i <= 2; i++ {
			guid := page*100 + i
			items.WriteString(fmt.Sprintf(`<item><guid>%d</guid><title>p%d-%d</title><link>https://b.dev/%d</link></item>`, guid, page, i, guid))
		}
		next := ""
		if page < total {
			next = fmt.Sprintf(`<atom:link rel="next" href="?page=%d"/>`, page+1)
		}
		fmt.Fprintf(w, `<?xml version="1.0"?><rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom"><channel><title>B</title>%s%s</channel></rss>`, next, items.String())
	}))
}

func TestPollOneRecordsNextPageURL(t *testing.T) {
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	u, _ := st.Users.Create("alice", "h")
	a, _ := st.Authors.Create(u.ID, "Metru", "", "")

	srv := paginatedServer(3)
	defer srv.Close()

	f, _ := st.Feeds.Create(u.ID, a.ID, "Blog", srv.URL+"/feed", "", "", 900)
	p := New(st, time.Minute, 1)
	if _, err := p.PollOne(context.Background(), f); err != nil {
		t.Fatalf("PollOne: %v", err)
	}
	got, _ := st.Feeds.ByID(u.ID, f.ID)
	want := srv.URL + "/feed?page=2"
	if got.NextPageURL != want {
		t.Fatalf("NextPageURL = %q, want %q", got.NextPageURL, want)
	}
}

// cadenceServer serves an RSS feed whose items carry published times spaced by
// gap; when timeSinceLatest is non-zero the newest item is older than now.
func cadenceServer(t *testing.T, gap time.Duration, timeSinceLatest time.Duration) *httptest.Server {
	t.Helper()
	now := time.Now().UTC()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var items strings.Builder
		for i := 0; i < 4; i++ {
			pub := now.Add(-timeSinceLatest - gap*time.Duration(i))
			items.WriteString(fmt.Sprintf(
				`<item><guid>%d</guid><title>t%d</title><link>https://b.dev/%d</link><pubDate>%s</pubDate></item>`,
				i, i, i, pub.Format(time.RFC1123Z)))
		}
		fmt.Fprintf(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>B</title>%s</channel></rss>`, items.String())
	}))
}

// A feed with recent, evenly spaced posts gets its poll interval set to the
// average gap between them (adaptive on by default).
func TestPollOneAdaptiveInterval(t *testing.T) {
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	u, _ := st.Users.Create("alice", "h")
	a, _ := st.Authors.Create(u.ID, "Metru", "", "")

	srv := cadenceServer(t, 2*time.Hour, 0)
	defer srv.Close()

	f, _ := st.Feeds.Create(u.ID, a.ID, "Blog", srv.URL+"/feed", "", "", 900)
	p := New(st, time.Minute, 1)
	if _, err := p.PollOne(context.Background(), f); err != nil {
		t.Fatalf("PollOne: %v", err)
	}
	got, _ := st.Feeds.ByID(u.ID, f.ID)
	if got.PollIntervalSec != 7200 {
		t.Fatalf("interval = %d, want the 2h average (7200)", got.PollIntervalSec)
	}
	if got.LastItemAt == "" {
		t.Fatalf("last_item_at should be recorded after a poll")
	}
}

// A feed whose newest post is 8 days old is backed off to a 1-day poll
// interval regardless of the adaptive toggle.
func TestPollOneStaleBacksOff(t *testing.T) {
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	u, _ := st.Users.Create("alice", "h")
	a, _ := st.Authors.Create(u.ID, "Metru", "", "")

	srv := cadenceServer(t, 2*time.Hour, 8*24*time.Hour)
	defer srv.Close()

	// auto is off, but the stale rule still forces 1 day.
	f, _ := st.Feeds.Create(u.ID, a.ID, "Blog", srv.URL+"/feed", "", "", 900)
	st.Feeds.Update(u.ID, f.ID, a.ID, "Blog", srv.URL+"/feed", "", "", 3600, false, true)
	fresh, _ := st.Feeds.ByID(u.ID, f.ID)
	p := New(st, time.Minute, 1)
	if _, err := p.PollOne(context.Background(), fresh); err != nil {
		t.Fatalf("PollOne: %v", err)
	}
	got, _ := st.Feeds.ByID(u.ID, f.ID)
	if got.PollIntervalSec != adaptiveCeil {
		t.Fatalf("interval = %d, want 1 day (%d) for a stale feed", got.PollIntervalSec, adaptiveCeil)
	}
}

func TestPollOneKeepsCursorOnRoutinePoll(t *testing.T) {
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	u, _ := st.Users.Create("alice", "h")
	a, _ := st.Authors.Create(u.ID, "Metru", "", "")

	srv := paginatedServer(3)
	defer srv.Close()

	f, _ := st.Feeds.Create(u.ID, a.ID, "Blog", srv.URL+"/feed", "", "", 900)
	p := New(st, time.Minute, 1)
	if _, err := p.PollOne(context.Background(), f); err != nil {
		t.Fatalf("PollOne: %v", err)
	}

	// The user's "load older items" walk advanced the cursor to page 3.
	if err := st.Feeds.SetNextPageURL(f.ID, srv.URL+"/feed?page=3"); err != nil {
		t.Fatal(err)
	}
	// A routine poll must not clobber it back to page 2.
	f, _ = st.Feeds.ByID(u.ID, f.ID)
	if _, err := p.PollOne(context.Background(), f); err != nil {
		t.Fatalf("PollOne again: %v", err)
	}
	got, _ := st.Feeds.ByID(u.ID, f.ID)
	if got.NextPageURL != srv.URL+"/feed?page=3" {
		t.Fatalf("NextPageURL = %q, want %q preserved", got.NextPageURL, srv.URL+"/feed?page=3")
	}
}

func TestPollOlderImportsAndExhausts(t *testing.T) {
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	u, _ := st.Users.Create("alice", "h")
	a, _ := st.Authors.Create(u.ID, "Metru", "", "")

	srv := paginatedServer(2)
	defer srv.Close()

	f, _ := st.Feeds.Create(u.ID, a.ID, "Blog", srv.URL+"/feed", "", "", 900)
	p := New(st, time.Minute, 1)
	if _, err := p.PollOne(context.Background(), f); err != nil {
		t.Fatalf("PollOne: %v", err)
	}
	f, _ = st.Feeds.ByID(u.ID, f.ID) // now carries the page-2 cursor

	n, exhausted, err := p.PollOlder(context.Background(), f)
	if err != nil {
		t.Fatalf("PollOlder: %v", err)
	}
	if n != 2 {
		t.Fatalf("new items = %d, want 2", n)
	}
	if !exhausted {
		t.Fatal("expected exhausted after last page")
	}
	items, _ := st.Items.List(u.ID, store.ItemFilter{})
	if len(items) != 4 {
		t.Fatalf("total items = %d, want 4", len(items))
	}
	got, _ := st.Feeds.ByID(u.ID, f.ID)
	if got.NextPageURL != "" {
		t.Fatalf("NextPageURL = %q, want cleared", got.NextPageURL)
	}
}

func TestPollOlderRespectsPageCap(t *testing.T) {
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	u, _ := st.Users.Create("alice", "h")
	a, _ := st.Authors.Create(u.ID, "Metru", "", "")

	srv := paginatedServer(7)
	defer srv.Close()

	f, _ := st.Feeds.Create(u.ID, a.ID, "Blog", srv.URL+"/feed", "", "", 900)
	if err := st.Feeds.SetNextPageURL(f.ID, srv.URL+"/feed?page=2"); err != nil {
		t.Fatal(err)
	}
	f, _ = st.Feeds.ByID(u.ID, f.ID)

	p := New(st, time.Minute, 1)
	n, exhausted, err := p.PollOlder(context.Background(), f)
	if err != nil {
		t.Fatalf("PollOlder: %v", err)
	}
	if n != maxBackfillPages*2 {
		t.Fatalf("new items = %d, want %d (5 pages x 2)", n, maxBackfillPages*2)
	}
	if exhausted {
		t.Fatal("should not be exhausted yet: cap hit with more pages left")
	}
	got, _ := st.Feeds.ByID(u.ID, f.ID)
	want := srv.URL + "/feed?page=7"
	if got.NextPageURL != want {
		t.Fatalf("NextPageURL = %q, want %q", got.NextPageURL, want)
	}
}

func TestPollOlderStopsOnEmptyPage(t *testing.T) {
	// A feed whose ?page=2 (and beyond) carries no items: the pagination is
	// detected through the URL's own page param, and the walk must stop and
	// clear the cursor instead of incrementing forever.
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	u, _ := st.Users.Create("alice", "h")
	a, _ := st.Authors.Create(u.ID, "Metru", "", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "" {
			fmt.Fprint(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>B</title></channel></rss>`)
			return
		}
		fmt.Fprint(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>B</title><item><guid>1</guid><title>one</title><link>https://b.dev/1</link></item></channel></rss>`)
	}))
	defer srv.Close()

	f, _ := st.Feeds.Create(u.ID, a.ID, "Blog", srv.URL+"/feed?page=1", "", "", 900)
	p := New(st, time.Minute, 1)
	if _, err := p.PollOne(context.Background(), f); err != nil {
		t.Fatalf("PollOne: %v", err)
	}
	f, _ = st.Feeds.ByID(u.ID, f.ID)
	if f.NextPageURL != srv.URL+"/feed?page=2" {
		t.Fatalf("NextPageURL = %q, want ?page=2", f.NextPageURL)
	}

	n, exhausted, err := p.PollOlder(context.Background(), f)
	if err != nil {
		t.Fatalf("PollOlder: %v", err)
	}
	if n != 0 {
		t.Fatalf("new items = %d, want 0", n)
	}
	if !exhausted {
		t.Fatal("expected exhausted when next page is empty")
	}
	got, _ := st.Feeds.ByID(u.ID, f.ID)
	if got.NextPageURL != "" {
		t.Fatalf("NextPageURL = %q, want cleared", got.NextPageURL)
	}
}

func TestPollOne(t *testing.T) {
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	u, _ := st.Users.Create("alice", "h")
	a, _ := st.Authors.Create(u.ID, "Metru", "", "")

	var body = `<?xml version="1.0"?>
<rss version="2.0">
<channel>
  <title>Blog</title>
  <link>https://blog.dev/</link>
  <item>
    <guid>1</guid>
    <title>One</title>
    <link>https://blog.dev/1</link>
    <enclosure url="https://blog.dev/ep1.mp3" type="audio/mpeg" length="12345"/>
  </item>
  <item>
    <guid>2</guid>
    <title>Two</title>
    <link>https://blog.dev/2</link>
  </item>
</channel>
</rss>`

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	f, _ := st.Feeds.Create(u.ID, a.ID, "Blog", srv.URL, "https://blog.dev/", "", 900)
	p := New(st, time.Minute, 1)

	// First poll inserts both items.
	ctx := context.Background()
	n, err := p.PollOne(ctx, f)
	if err != nil {
		t.Fatalf("PollOne: %v", err)
	}
	if n != 2 {
		t.Fatalf("new items = %d, want 2", n)
	}

	// Second poll hits the 304 path: no new items, no dupes.
	n, err = p.PollOne(ctx, f)
	if err != nil {
		t.Fatalf("PollOne 304: %v", err)
	}
	if n != 0 {
		t.Fatalf("new items on 304 = %d, want 0", n)
	}
	items, _ := st.Items.List(u.ID, store.ItemFilter{})
	if len(items) != 2 {
		t.Fatalf("total items = %d, want 2", len(items))
	}
	if hits != 2 {
		t.Fatalf("server hits = %d, want 2", hits)
	}

	// Item 1's enclosure is stored and playable.
	itemID, err := st.Items.ByFeedGUID(f.ID, "1")
	if err != nil || itemID == 0 {
		t.Fatalf("ByFeedGUID: %v %d", err, itemID)
	}
	encs, err := st.Items.Enclosures(itemID)
	if err != nil || len(encs) != 1 {
		t.Fatalf("enclosures: %v %+v", err, encs)
	}
	if encs[0].URL != "https://blog.dev/ep1.mp3" || encs[0].MIMEType != "audio/mpeg" || encs[0].Size != 12345 {
		t.Fatalf("enclosure mismatch: %+v", encs[0])
	}

	// Poll metadata recorded.
	got, _ := st.Feeds.ByID(u.ID, f.ID)
	if got.ETag != `"v1"` || got.LastPolledAt == "" {
		t.Fatalf("poll meta not recorded: %+v", got)
	}
}

func TestPollOneAppliesFilters(t *testing.T) {
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	u, _ := st.Users.Create("alice", "h")
	a, _ := st.Authors.Create(u.ID, "Metru", "", "")

	var body = `<?xml version="1.0"?>
<rss version="2.0"><channel>
  <title>Blog</title>
  <link>https://blog.dev/</link>
  <item><guid>1</guid><title>hide me</title><link>https://blog.dev/1</link></item>
  <item><guid>2</guid><title>spoiler alert</title><link>https://blog.dev/2</link></item>
  <item><guid>3</guid><title>normal post</title><link>https://blog.dev/3</link></item>
</channel></rss>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	f, _ := st.Feeds.Create(u.ID, a.ID, "Blog", srv.URL, "", "", 900)
	if _, err := st.Filters.Create(u.ID, f.ID, "hide", "title", "hide me", false); err != nil {
		t.Fatalf("create hide rule: %v", err)
	}
	if _, err := st.Filters.Create(u.ID, f.ID, "mark_read", "title", "^spoiler", true); err != nil {
		t.Fatalf("create mark_read rule: %v", err)
	}

	p := New(st, time.Minute, 1)
	n, err := p.PollOne(context.Background(), f)
	if err != nil {
		t.Fatalf("PollOne: %v", err)
	}
	if n != 2 {
		t.Fatalf("new items = %d, want 2 (hidden item dropped)", n)
	}

	items, _ := st.Items.List(u.ID, store.ItemFilter{})
	if len(items) != 2 {
		t.Fatalf("stored items = %d, want 2", len(items))
	}
	byTitle := map[string]bool{}
	for _, it := range items {
		byTitle[it.Title] = true
	}
	if byTitle["hide me"] {
		t.Fatal("hidden item should not be stored")
	}
	if !byTitle["spoiler alert"] || !byTitle["normal post"] {
		t.Fatalf("expected both non-hidden items, got %v", byTitle)
	}
	// The mark_read rule stored the spoiler item as read.
	for _, it := range items {
		if it.Title == "spoiler alert" && !it.Read {
			t.Fatal("spoiler item should be stored read")
		}
	}
}

func TestPollDue(t *testing.T) {
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	u, _ := st.Users.Create("alice", "h")
	a, _ := st.Authors.Create(u.ID, "Metru", "", "")

	body := `<?xml version="1.0"?><rss version="2.0"><channel><title>B</title><item><guid>1</guid><title>One</title><link>https://b.dev/1</link></item></channel></rss>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	f, _ := st.Feeds.Create(u.ID, a.ID, "B", srv.URL, "", "", 900)
	p := New(st, time.Minute, 1)

	n, err := p.PollDue(context.Background())
	if err != nil {
		t.Fatalf("PollDue: %v", err)
	}
	if n != 1 {
		t.Fatalf("new items = %d, want 1", n)
	}

	// Freshly polled -> not due -> nothing fetched.
	if _, err := p.PollDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Feeds.ByID(u.ID, f.ID)
	if got.LastPolledAt == "" {
		t.Fatal("last_polled_at not set")
	}

	// A broken feed is recorded as polled so it is not retried every tick.
	f2, _ := st.Feeds.Create(u.ID, a.ID, "Broken", "http://127.0.0.1:1/rss", "", "", 900)
	if _, err := p.PollOne(context.Background(), f2); err == nil {
		t.Fatal("expected PollOne to fail for unreachable feed")
	}
	got2, _ := st.Feeds.ByID(u.ID, f2.ID)
	if got2.LastPolledAt == "" {
		t.Fatal("broken feed poll attempt not recorded")
	}
}

func TestPollOneRecordsLastError(t *testing.T) {
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	u, _ := st.Users.Create("alice", "h")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	a, _ := st.Authors.Create(u.ID, "Broken", "", "")
	f, _ := st.Feeds.Create(u.ID, a.ID, "Broken", srv.URL, "", "", 900)
	p := New(st, time.Minute, 1)

	if _, err := p.PollOne(context.Background(), f); err == nil {
		t.Fatal("expected poll error")
	}
	got, _ := st.Feeds.ByID(u.ID, f.ID)
	if got.LastError == "" {
		t.Fatal("last_error should be recorded on failure")
	}

	// A successful poll clears it.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>B</title><item><guid>1</guid><title>One</title></item></channel></rss>`))
	}))
	defer ok.Close()
	if err := st.Feeds.Update(u.ID, f.ID, a.ID, "Broken", ok.URL, "", "", 900, false, true); err != nil {
		t.Fatal(err)
	}
	fresh, _ := st.Feeds.ByID(u.ID, f.ID)
	if _, err := p.PollOne(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Feeds.ByID(u.ID, f.ID)
	if got.LastError != "" {
		t.Fatalf("last_error should clear on success, got %q", got.LastError)
	}
}

func TestPollOneScrapesScrapeKindFeed(t *testing.T) {
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	u, _ := st.Users.Create("alice", "h")
	a, _ := st.Authors.Create(u.ID, "Scraper", "", "")

	body := `<!DOCTYPE html><html><head><title>Scrape Blog</title></head><body>
<article><h2><a href="/p/1">One</a></h2><time datetime="2026-02-01T09:00:00Z"></time></article>
<article><h2><a href="/p/2">Two</a></h2><time datetime="2026-01-31T09:00:00Z"></time></article>
</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(body))
	}))
	defer srv.Close()

	f, _ := st.Feeds.CreateScrape(u.ID, a.ID, "Scraped", srv.URL, srv.URL, "", `{"item":"article","link":"h2 a"}`, 900)
	p := New(st, time.Minute, 1)

	n, err := p.PollOne(context.Background(), f)
	if err != nil {
		t.Fatalf("PollOne scrape: %v", err)
	}
	if n != 2 {
		t.Fatalf("new items = %d, want 2", n)
	}
	items, _ := st.Items.List(u.ID, store.ItemFilter{})
	if len(items) != 2 {
		t.Fatalf("total items = %d, want 2", len(items))
	}
	if items[0].Title != "One" || items[1].Title != "Two" {
		t.Fatalf("items wrong: %+v", items)
	}
	// Items carry the resolved absolute links, not the relative ones.
	if items[0].Link != srv.URL+"/p/1" || items[1].Link != srv.URL+"/p/2" {
		t.Fatalf("item links not resolved: %+v", items)
	}
}

func TestPollOneScrapeBadConfigFailsGracefully(t *testing.T) {
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	u, _ := st.Users.Create("alice", "h")
	a, _ := st.Authors.Create(u.ID, "Scraper", "", "")

	f, _ := st.Feeds.CreateScrape(u.ID, a.ID, "Broken", "https://x.dev/page", "https://x.dev", "", "not json", 900)
	p := New(st, time.Minute, 1)

	if _, err := p.PollOne(context.Background(), f); err == nil {
		t.Fatal("expected poll error for bad scrape config")
	}
	got, _ := st.Feeds.ByID(u.ID, f.ID)
	if got.LastError == "" {
		t.Fatal("last_error should be recorded for a bad scrape config")
	}
}

// rateLimitedServer serves a 429 (with a retry hint) for requests to /limited
// and a valid feed for /feed. Both live on the same httptest host.
func rateLimitedServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "limited") {
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>B</title><item><guid>g</guid><title>t</title><link>https://b.dev/1</link></item></channel></rss>`))
	}))
}

// Polling a feed that is rate limited records the backoff deadline and a clear
// error, and the feed is not due again until the deadline passes.
func TestPollOneRateLimitBacksOff(t *testing.T) {
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	u, _ := st.Users.Create("alice", "h")
	a, _ := st.Authors.Create(u.ID, "A", "", "")

	srv := rateLimitedServer(t)
	defer srv.Close()

	f, _ := st.Feeds.Create(u.ID, a.ID, "R", srv.URL+"/limited.rss", "", "", 900)
	p := New(st, time.Minute, 1)
	if _, err := p.PollOne(context.Background(), f); err == nil {
		t.Fatal("expected a rate-limit error")
	}

	got, _ := st.Feeds.ByID(u.ID, f.ID)
	if got.NextPollAt == "" {
		t.Fatal("NextPollAt should be set after a rate limit")
	}
	if got.LastError == "" {
		t.Fatal("LastError should mention the rate limit")
	}
	until, err := db.ParseTime(got.NextPollAt)
	if err != nil || time.Until(until) < 9*time.Minute {
		t.Fatalf("NextPollAt = %q, want ~600s in the future", got.NextPollAt)
	}
	// Not due while the backoff is in effect.
	if due, _ := st.Feeds.ListDue(db.Now()); len(due) != 0 {
		t.Fatalf("rate-limited feed should not be due, got %d", len(due))
	}
}

// A successful poll clears a previously recorded rate-limit backoff.
func TestPollOneSuccessClearsBackoff(t *testing.T) {
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	u, _ := st.Users.Create("alice", "h")
	a, _ := st.Authors.Create(u.ID, "A", "", "")

	srv := rateLimitedServer(t)
	defer srv.Close()

	f, _ := st.Feeds.Create(u.ID, a.ID, "B", srv.URL+"/feed.rss", "", "", 900)
	st.Feeds.SetNextPollAt(f.ID, db.FormatTime(time.Now().Add(time.Hour)))

	p := New(st, time.Minute, 1)
	if _, err := p.PollOne(context.Background(), f); err != nil {
		t.Fatalf("PollOne: %v", err)
	}
	got, _ := st.Feeds.ByID(u.ID, f.ID)
	if got.NextPollAt != "" {
		t.Fatalf("successful poll should clear NextPollAt, got %q", got.NextPollAt)
	}
}

// PollDue serializes fetches per host: requests to the same host never overlap,
// while the whole cycle still completes.
func TestPollDueSerializesPerHost(t *testing.T) {
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	u, _ := st.Users.Create("alice", "h")
	a, _ := st.Authors.Create(u.ID, "A", "", "")

	var mu sync.Mutex
	var inflight, maxInflight int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inflight++
		if inflight > maxInflight {
			maxInflight = inflight
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		inflight--
		mu.Unlock()
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>B</title></channel></rss>`))
	}))
	defer srv.Close()

	// Four feeds on the same host, all due.
	for i := 0; i < 4; i++ {
		st.Feeds.Create(u.ID, a.ID, "f", fmt.Sprintf("%s/feed%d", srv.URL, i), "", "", 900)
	}

	// A generous worker pool means any cross-host parallelism is available; the
	// same-host feeds must still run one at a time.
	p := New(st, time.Minute, 8)
	if _, err := p.PollDue(context.Background()); err != nil {
		t.Fatalf("PollDue: %v", err)
	}
	if maxInflight > 1 {
		t.Fatalf("same-host fetches overlapped: maxInflight = %d", maxInflight)
	}
}

// groupByHost buckets feeds by registrable domain and keeps distinct hosts
// separate, including a distinct subdomain collapse.
func TestGroupByHost(t *testing.T) {
	feeds := []store.Feed{
		{FeedURL: "https://www.reddit.com/r/a/.rss"},
		{FeedURL: "https://old.reddit.com/r/b/.rss"},
		{FeedURL: "https://example.com/feed"},
	}
	groups := groupByHost(feeds)
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
	var reddit, example int
	for _, g := range groups {
		switch store.RegistrableDomain(g[0].FeedURL) {
		case "reddit.com":
			reddit = len(g)
		case "example.com":
			example = len(g)
		}
	}
	if reddit != 2 || example != 1 {
		t.Fatalf("group sizes reddit=%d example=%d, want 2/1", reddit, example)
	}
}
