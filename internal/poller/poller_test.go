package poller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/metruzanca/rss/internal/db"
	"github.com/metruzanca/rss/internal/store"
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

	// Poll metadata recorded.
	got, _ := st.Feeds.ByID(u.ID, f.ID)
	if got.ETag != `"v1"` || got.LastPolledAt == "" {
		t.Fatalf("poll meta not recorded: %+v", got)
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
