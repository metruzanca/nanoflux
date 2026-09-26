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
	"github.com/metruzanca/nanoflux/internal/feedparse"
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
	itemID, err := st.Items.ByFeedIdentity(f.ID, "1")
	if err != nil || itemID == 0 {
		t.Fatalf("ByFeedIdentity: %v %d", err, itemID)
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
	// same-host feeds must still run one at a time. Default spacing is off so
	// all four complete in one cycle for the overlap assertion.
	p := New(st, time.Minute, 8)
	p.SetHostSpacing(0)
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

// newPollerStore builds an in-memory store with one user/author.
func newPollerStore(t *testing.T) *store.Store {
	t.Helper()
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	st := store.New(sqldb)
	if _, err := st.Users.Create("alice", "h"); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestPollDueRateLimitPacesHostAndRotates covers the rate-limited-host behavior:
// on a 429 the poller learns a window, spaces the host, and does not attempt the
// host's other feeds in the same cycle; it reports when to wake.
func TestPollDueRateLimitPacesHostAndRotates(t *testing.T) {
	st := newPollerStore(t)
	u, _ := st.Users.ByUsername("alice")
	a, _ := st.Authors.Create(u.ID, "A", "", "")

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Retry-After", "90")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	// Three feeds on the same host.
	for i := 0; i < 3; i++ {
		st.Feeds.Create(u.ID, a.ID, "f", fmt.Sprintf("%s/feed%d", srv.URL, i), "", "", 900)
	}

	p := New(st, time.Minute, 4)
	if _, err := p.PollDue(context.Background()); err != nil {
		t.Fatalf("PollDue: %v", err)
	}
	// Only the first feed is attempted; the host is then paced for its window.
	if hits != 1 {
		t.Fatalf("hits = %d, want 1 (rest of host paced)", hits)
	}
	if _, ok := p.hostWindowFor(store.RegistrableDomain(srv.URL + "/x")); !ok {
		t.Fatal("host window should be learned from Retry-After")
	}

	// A second immediate cycle must not hit the cooling host again.
	if _, err := p.PollDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("cooling host was hit again: hits = %d", hits)
	}

	// Only the attempted feed got its per-feed backoff set; the host's other
	// feeds were never touched, so they stay due to be picked up when the
	// window clears (that is what makes the rotation fair).
	due, _ := st.Feeds.ListDue(db.Now())
	if len(due) != 2 {
		t.Fatalf("paced host's remaining feeds should stay due, got %d", len(due))
	}
}

// TestPollDueRotatesLeastRecentlyPolledFirst asserts the fair-rotation order: a
// host's feeds are attempted oldest-polled-first.
func TestPollDueRotatesLeastRecentlyPolledFirst(t *testing.T) {
	st := newPollerStore(t)
	u, _ := st.Users.ByUsername("alice")
	a, _ := st.Authors.Create(u.ID, "A", "", "")

	var order []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		order = append(order, r.URL.Path)
		mu.Unlock()
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>B</title></channel></rss>`))
	}))
	defer srv.Close()

	// Same host, three feeds. Make their poll ages differ: older = polled long
	// ago (but past the interval so all are due), newest = polled less ago.
	urls := []string{srv.URL + "/a", srv.URL + "/b", srv.URL + "/c"}
	ids := make([]int64, 0, 3)
	for _, url := range urls {
		f, _ := st.Feeds.Create(u.ID, a.ID, "f", url, "", "", 900)
		ids = append(ids, f.ID)
	}
	// b was polled most recently (but still due), c in the middle, a oldest.
	st.Feeds.SetPollMeta(ids[1], "", "", db.FormatTime(time.Now().Add(-20*time.Minute)), "")
	st.Feeds.SetPollMeta(ids[2], "", "", db.FormatTime(time.Now().Add(-30*time.Minute)), "")
	st.Feeds.SetPollMeta(ids[0], "", "", db.FormatTime(time.Now().Add(-40*time.Minute)), "")

	p := New(st, time.Minute, 4)
	p.SetHostSpacing(0) // observe the full rotation order in one cycle
	if _, err := p.PollDue(context.Background()); err != nil {
		t.Fatalf("PollDue: %v", err)
	}
	if len(order) != 3 {
		t.Fatalf("expected 3 hits, got %v", order)
	}
	// Oldest (40m ago -> /a) first, then /c (30m), then /b (20m).
	want := []string{"/a", "/c", "/b"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("rotation order = %v, want %v", order, want)
		}
	}
}

// TestPollDueDefaultSpacingSpreadsMultiFeedHost covers the default per-host
// spacing: a multi-feed host is fetched at most once per spacing window, its
// remaining feeds stay due, and the poller reports when the host may be hit
// again.
func TestPollDueDefaultSpacingSpreadsMultiFeedHost(t *testing.T) {
	st := newPollerStore(t)
	u, _ := st.Users.ByUsername("alice")
	a, _ := st.Authors.Create(u.ID, "A", "", "")

	var hits int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>B</title></channel></rss>`))
	}))
	defer srv.Close()

	for i := 0; i < 3; i++ {
		st.Feeds.Create(u.ID, a.ID, "f", fmt.Sprintf("%s/feed%d", srv.URL, i), "", "", 900)
	}

	p := New(st, time.Minute, 4)
	p.SetHostSpacing(50 * time.Millisecond)
	_, wake, err := p.pollDue(context.Background())
	if err != nil {
		t.Fatalf("pollDue: %v", err)
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1 (host spaced after one fetch)", hits)
	}
	if wake.IsZero() || !wake.After(time.Now()) {
		t.Fatalf("expected a future wake for the spaced host, got %v", wake)
	}
	// The untouched feeds stay due to be picked up when the window clears.
	due, _ := st.Feeds.ListDue(db.Now())
	if len(due) != 2 {
		t.Fatalf("spaced host's remaining feeds should stay due, got %d", len(due))
	}
}

// TestPollDueDefaultSpacingSingleFeedNotDelayed asserts a host with one feed is
// fetched immediately and never made to wait on the spacing it just set.
func TestPollDueDefaultSpacingSingleFeedNotDelayed(t *testing.T) {
	st := newPollerStore(t)
	u, _ := st.Users.ByUsername("alice")
	a, _ := st.Authors.Create(u.ID, "A", "", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>B</title></channel></rss>`))
	}))
	defer srv.Close()

	st.Feeds.Create(u.ID, a.ID, "f", srv.URL+"/feed", "", "", 900)
	p := New(st, time.Minute, 4)
	p.SetHostSpacing(50 * time.Millisecond)
	_, wake, err := p.pollDue(context.Background())
	if err != nil {
		t.Fatalf("pollDue: %v", err)
	}
	if !wake.IsZero() {
		t.Fatalf("single-feed host should not be paced, got wake %v", wake)
	}
	got, _ := st.Feeds.ListDue(db.Now())
	if len(got) != 0 {
		t.Fatalf("single feed should be fetched, still due: %d", len(got))
	}
}

// TestPollDueLearnedWindowOverridesDefaultSpacing asserts a learned rate-limit
// window wins over the smaller default spacing.
func TestPollDueLearnedWindowOverridesDefaultSpacing(t *testing.T) {
	st := newPollerStore(t)
	u, _ := st.Users.ByUsername("alice")
	a, _ := st.Authors.Create(u.ID, "A", "", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>B</title></channel></rss>`))
	}))
	defer srv.Close()

	st.Feeds.Create(u.ID, a.ID, "f", fmt.Sprintf("%s/feed0", srv.URL), "", "", 900)
	st.Feeds.Create(u.ID, a.ID, "f", fmt.Sprintf("%s/feed1", srv.URL), "", "", 900)

	p := New(st, time.Minute, 4)
	p.SetHostSpacing(50 * time.Millisecond)
	p.learnWindow(store.RegistrableDomain(srv.URL+"/x"), time.Hour)

	_, wake, err := p.pollDue(context.Background())
	if err != nil {
		t.Fatalf("pollDue: %v", err)
	}
	if wake.Before(time.Now().Add(55 * time.Minute)) {
		t.Fatalf("wake %v should reflect the learned hour window, not the default spacing", wake)
	}
}

// fakeCooler is a hostCooler stub for the shared-cooldown tests.
type fakeCooler struct {
	mu    sync.Mutex
	until map[string]time.Time
}

func newFakeCooler() *fakeCooler { return &fakeCooler{until: map[string]time.Time{}} }

func (c *fakeCooler) Cooling(rawURL string) bool {
	_, ok := c.Until(rawURL)
	return ok
}

func (c *fakeCooler) Cool(rawURL string, t time.Time) {
	host := store.RegistrableDomain(rawURL)
	if host == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.until[host] = t
}

func (c *fakeCooler) Until(rawURL string) (time.Time, bool) {
	host := store.RegistrableDomain(rawURL)
	if host == "" {
		return time.Time{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.until[host]
	if !ok || time.Now().After(t) {
		return time.Time{}, false
	}
	return t, true
}

// TestPollDueSkipsCoolingHost asserts the poller consults the shared cooldown: a
// host a plugin cooled is not fetched, and the poller wakes when it clears.
func TestPollDueSkipsCoolingHost(t *testing.T) {
	st := newPollerStore(t)
	u, _ := st.Users.ByUsername("alice")
	a, _ := st.Authors.Create(u.ID, "A", "", "")

	var hits int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>B</title></channel></rss>`))
	}))
	defer srv.Close()

	st.Feeds.Create(u.ID, a.ID, "f", srv.URL+"/feed", "", "", 900)

	cool := newFakeCooler()
	cool.Cool(srv.URL, time.Now().Add(time.Hour))
	p := New(st, time.Minute, 4)
	p.SetHostCooler(cool)

	_, wake, err := p.pollDue(context.Background())
	if err != nil {
		t.Fatalf("pollDue: %v", err)
	}
	if hits != 0 {
		t.Fatalf("cooling host was fetched: hits = %d", hits)
	}
	if wake.IsZero() {
		t.Fatal("expected a wake at the cooldown's expiry")
	}
}

// TestPollOneRateLimitCoolsSharedHost asserts a poller-observed limit teaches
// the shared cooldown, so plugins back off the same host.
func TestPollOneRateLimitCoolsSharedHost(t *testing.T) {
	st := newPollerStore(t)
	u, _ := st.Users.ByUsername("alice")
	a, _ := st.Authors.Create(u.ID, "A", "", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	f, _ := st.Feeds.Create(u.ID, a.ID, "f", srv.URL+"/feed", "", "", 900)
	cool := newFakeCooler()
	p := New(st, time.Minute, 1)
	p.SetHostCooler(cool)

	if _, err := p.PollOne(context.Background(), f); err == nil {
		t.Fatal("expected a rate-limit error")
	}
	if !cool.Cooling(srv.URL) {
		t.Fatal("poller rate limit should cool the shared host")
	}
}

// fakeIdentityPlugin is a feedparse.Plugin that returns one item whose GUID
// changes shape between "polls" for the same stable Identity. It reproduces the
// GUID-scheme change without a network.
type fakeIdentityPlugin struct {
	guid string
}

func (fakeIdentityPlugin) MatchFetch(feedparse.FetchRequest) bool { return true }
func (p *fakeIdentityPlugin) FetchPlugin(context.Context, feedparse.FetchRequest) (feedparse.Result, error) {
	return feedparse.Result{
		Feed: feedparse.Feed{Title: "Blog", HomeURL: "https://b.dev/"},
		Items: []feedparse.Item{{
			GUID:        p.guid,
			Identity:    "post:1",
			Title:       "Post 1",
			Link:        "https://b.dev/post/1",
			PublishedAt: "2026-09-22 22:04:56",
		}},
	}, nil
}

// TestPollIdentityPreventGuidSchemeDuplicates proves the end-to-end fix: when a
// plugin's GUID changes shape but its Identity is stable, the poller does not
// store the entry twice.
func TestPollIdentityPreventGuidSchemeDuplicates(t *testing.T) {
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
	f, _ := st.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/feed", "", "", 900)

	plug := &fakeIdentityPlugin{guid: "https://b.dev/post/1"}
	feedparse.SetPlugin(plug)
	defer feedparse.SetPlugin(nil)

	p := New(st, time.Minute, 1)
	if _, err := p.PollOne(context.Background(), f); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	// The same entry, now under the new GUID scheme.
	plug.guid = "post:1"
	if _, err := p.PollOne(context.Background(), f); err != nil {
		t.Fatalf("second poll: %v", err)
	}

	items, _ := st.Items.List(u.ID, store.ItemFilter{FeedID: f.ID, Limit: 10})
	if len(items) != 1 {
		t.Fatalf("GUID change must not duplicate the item: got %d items", len(items))
	}
	// The survivor keeps its first-seen GUID (identity/read state are not
	// rewritten on re-poll); the durable key is what prevented the duplicate.
	if items[0].GUID != "https://b.dev/post/1" {
		t.Fatalf("stored GUID = %q, want the first-seen %q", items[0].GUID, "https://b.dev/post/1")
	}
}
