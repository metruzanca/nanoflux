package httpapi

import (
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// TestUnreadFilters covers the global /unread filter view: the modal offers
// tag, feed and time, and each kind narrows the list and the match count.
func TestUnreadFilters(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Author", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://metru.dev/rss.xml", "", "", 900)
	f2, _ := s.store.Feeds.Create(u.ID, a.ID, "Other", "https://other.dev/rss.xml", "", "", 900)

	upsert := func(feedID int64, guid, title, published, tag string) {
		t.Helper()
		if _, err := s.store.Items.Upsert(feedID, store.Item{
			GUID: guid, Title: title, Link: "https://metru.dev/" + guid,
			Summary: "body " + title, Categories: []string{tag},
			PublishedAt: published, FetchedAt: db.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	upsert(f.ID, "a", "Go post", "2026-01-10 00:00:00", "r/golang")
	upsert(f.ID, "b", "Rust post", "2026-06-10 00:00:00", "r/rust")
	upsert(f2.ID, "c", "Other post", "2026-06-11 00:00:00", "misc")

	// The global filter view exists and offers all three kinds.
	body := doGet(h, "/unread", cookie).Body.String()
	if !strings.Contains(body, `id="tag-filter-dialog"`) || !strings.Contains(body, `data-open="tag-filter-dialog"`) {
		t.Fatalf("/unread missing the filter dialog: %s", body)
	}
	for _, kind := range []string{"tag", "feed", "time"} {
		if !strings.Contains(body, `<option value="`+kind+`"`) {
			t.Fatalf("/unread filter should offer the %s kind: %s", kind, body)
		}
	}
	// The time kind has its own value template and optional clock inputs.
	for _, want := range []string{
		`data-filter-value-template="time"`,
		`data-filter-time-mode`,
		`data-filter-time-end`,
		`name="filter_time_start_`,
		`name="filter_time_start_clock_`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("/unread time filter missing %q: %s", want, body)
		}
	}

	// Feed filters work globally.
	body = doGet(h, "/unread?f="+url.QueryEscape("feed:show:"+itoa(f2.ID)), cookie).Body.String()
	if !strings.Contains(body, "Other post") || strings.Contains(body, "Go post") || strings.Contains(body, "Rust post") {
		t.Fatalf("feed filter should keep only the other feed's post: %s", body)
	}
	if !strings.Contains(body, "1 matching") {
		t.Fatalf("feed filter should show one match: %s", body)
	}

	// A time window is inclusive and runs in the user's timezone.
	body = doGet(h, "/unread?f="+url.QueryEscape(`time:show:["between","2026-01-01","2026-01-31"]`), cookie).Body.String()
	if !strings.Contains(body, "Go post") || strings.Contains(body, "Rust post") || strings.Contains(body, "Other post") {
		t.Fatalf("time window should keep only the January post: %s", body)
	}
	if !strings.Contains(body, "1 matching") {
		t.Fatalf("time filter should show one match: %s", body)
	}

	// A hide window excludes its items instead.
	body = doGet(h, "/unread?f="+url.QueryEscape(`time:hide:["between","2026-01-01","2026-01-31"]`), cookie).Body.String()
	if strings.Contains(body, "Go post") || !strings.Contains(body, "Rust post") || !strings.Contains(body, "Other post") {
		t.Fatalf("hide time window should drop the January post: %s", body)
	}

	// before/after use the single bound.
	body = doGet(h, "/unread?f="+url.QueryEscape(`time:show:["after","2026-06-01"]`), cookie).Body.String()
	if strings.Contains(body, "Go post") || !strings.Contains(body, "Rust post") {
		t.Fatalf("after should keep only the June posts: %s", body)
	}
	body = doGet(h, "/unread?f="+url.QueryEscape(`time:show:["before","2026-02-01"]`), cookie).Body.String()
	if !strings.Contains(body, "Go post") || strings.Contains(body, "Rust post") {
		t.Fatalf("before should keep only the January post: %s", body)
	}

	// Load-more keeps the active filters in its cursor URL.
	body = doGet(h, "/unread?f="+url.QueryEscape("tag:show:r/golang"), cookie).Body.String()
	if !strings.Contains(body, "tag%3Ashow%3Ar%2Fgolang") {
		t.Fatalf("load-more/sort links should carry the filters: %s", body)
	}
}

// TestTimeFilterParamRoundTrip checks the canonical ?f= spelling survives
// parsing and re-serialization.
func TestTimeFilterParamRoundTrip(t *testing.T) {
	raw := `time:show:["between","2026-01-01","2026-01-31T13:05"]`
	f, ok := parseFilterParam(raw)
	if !ok {
		t.Fatalf("parse %q failed", raw)
	}
	if got := f.param(); got != raw {
		t.Fatalf("param round trip = %q, want %q", got, raw)
	}
	if got := f.label(); !strings.Contains(got, "between") || !strings.Contains(got, "1:05pm") {
		t.Fatalf("time label = %q", got)
	}
}
