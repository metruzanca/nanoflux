package poller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

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
	a, _ := st.Authors.Create(u.ID, "Metru", "", "", "")

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
	a, _ := st.Authors.Create(u.ID, "Metru", "", "", "")

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
	a, _ := st.Authors.Create(u.ID, "Metru", "", "", "")

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

	a, _ := st.Authors.Create(u.ID, "Broken", "", "", "")
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
	if err := st.Feeds.Update(u.ID, f.ID, a.ID, "Broken", ok.URL, "", "", 900, true); err != nil {
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
