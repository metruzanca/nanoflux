package httpapi

import (
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// TestDomainsGroupsFeedsByRegistrableDomain covers the /domains page that
// replaces auto collections: feeds on the same site collapse into one group,
// and a domain view lists only that site's feeds.
func TestDomainsGroupsFeedsByRegistrableDomain(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")

	one, _ := s.store.Feeds.Create(u.ID, a.ID, "One", "https://www.example.com/rss", "", "", 900)
	two, _ := s.store.Feeds.Create(u.ID, a.ID, "Two", "https://m.example.com/rss", "", "", 900)
	other, _ := s.store.Feeds.Create(u.ID, a.ID, "Other", "https://other.dev/rss", "", "", 900)
	s.store.Items.Upsert(one.ID, store.Item{GUID: "1", Title: "One post", Link: "https://example.com/1", FetchedAt: db.Now()})
	s.store.Items.Upsert(two.ID, store.Item{GUID: "2", Title: "Two post", Link: "https://example.com/2", FetchedAt: db.Now()})
	s.store.Items.Upsert(other.ID, store.Item{GUID: "3", Title: "Other post", Link: "https://other.dev/1", FetchedAt: db.Now()})

	body := doGet(h, "/domains", cookie).Body.String()
	for _, want := range []string{"example.com", "other.dev", "2 feeds · 2 unread", "1 feeds · 1 unread"} {
		if !strings.Contains(body, want) {
			t.Fatalf("domains page missing %q: %s", want, body)
		}
	}

	body = doGet(h, "/domains?domain=example.com", cookie).Body.String()
	if !strings.Contains(body, "One") || !strings.Contains(body, "Two") {
		t.Fatalf("domain view should list both feeds: %s", body)
	}
	if strings.Contains(body, "Other post") || strings.Contains(body, ">Other</strong>") {
		t.Fatalf("domain view should not list another domain's feed: %s", body)
	}
}
