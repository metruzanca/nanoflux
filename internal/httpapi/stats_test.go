package httpapi

import (
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

func TestStatsPageRenders(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Example Blog", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Example", "https://example.dev/feed", "", "", 900)
	if _, err := s.store.Items.Upsert(f.ID, store.Item{
		GUID: "g1", Title: "Hello", Link: "https://example.dev/1",
		Categories: []string{"r/examples"}, FetchedAt: db.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	id, _ := s.store.Items.ByFeedIdentity(f.ID, "g1")
	if err := s.store.Items.SetRead(u.ID, id, true); err != nil {
		t.Fatal(err)
	}

	rr := doGet(h, "/stats", cookie)
	if rr.Code != 200 {
		t.Fatalf("stats: got %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{
		"stats", "reading activity", "when you read", "top sources",
		"topics you read", "backlog", "media watched",
		"Example Blog", "r/examples",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("stats page missing %q", want)
		}
	}
}

func TestStatsPeriodPresets(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	_ = s
	for _, period := range []string{"30d", "90d", "1y", "all"} {
		rr := doGet(h, "/stats?period="+period, cookie)
		if rr.Code != 200 {
			t.Fatalf("period %s: got %d", period, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "stats") {
			t.Fatalf("period %s: unexpected body", period)
		}
	}
}

func TestStatsRequiresLogin(t *testing.T) {
	_, h := newTestServer(t)
	rr := doGetRaw(h, "/stats")
	if rr.Code == 200 {
		t.Fatalf("unauthenticated /stats should not render")
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "/login") {
		t.Fatalf("expected redirect to login, got %d %q", rr.Code, loc)
	}
}
