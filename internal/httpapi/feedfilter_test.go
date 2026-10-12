package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// TestFeedFilters covers the per-feed, view-level content filters: adding a
// rule hides matching items from the author stream without deleting them, the
// preview splits shown/hidden, allow mode keeps only matches, and removing the
// rule re-shows everything.
func TestFeedFilters(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g1", Title: "Go post", Link: "https://b.dev/1", Categories: []string{"r/golang"}, FetchedAt: db.Now()})
	s.store.Items.Upsert(f.ID, store.Item{GUID: "g2", Title: "Rust post", Link: "https://b.dev/2", Categories: []string{"r/rust"}, FetchedAt: db.Now()})

	authorPage := "/authors/" + itoa(a.ID)
	configURL := "/feeds/" + itoa(f.ID) + "/filter-config"
	addURL := "/feeds/" + itoa(f.ID) + "/filters"

	// No filter: both items show.
	body := doGet(h, authorPage, cookie).Body.String()
	if !strings.Contains(body, "Go post") || !strings.Contains(body, "Rust post") {
		t.Fatalf("unfiltered author page should show both items: %s", body)
	}

	// Block r/rust.
	if rr := doForm(h, "POST", configURL, url.Values{"mode": {"block"}, "combine": {"any"}}, cookie); rr.Code != http.StatusOK {
		t.Fatalf("set block mode: %d %s", rr.Code, rr.Body.String())
	}
	if rr := doForm(h, "POST", addURL, url.Values{"field": {"category"}, "op": {"is"}, "value": {"r/rust"}}, cookie); rr.Code != http.StatusOK {
		t.Fatalf("add block rule: %d %s", rr.Code, rr.Body.String())
	}

	body = doGet(h, authorPage, cookie).Body.String()
	if strings.Contains(body, "Rust post") || !strings.Contains(body, "Go post") {
		t.Fatalf("block list should hide r/rust but keep r/golang: %s", body)
	}

	// The item is still stored (view-level filtering).
	items, _ := s.store.Items.List(u.ID, store.ItemFilter{})
	if len(items) != 2 {
		t.Fatalf("both items should stay stored, got %d", len(items))
	}

	// The preview splits shown and hidden.
	pv := doGet(h, "/feeds/"+itoa(f.ID)+"/filter-preview", cookie).Body.String()
	if !strings.Contains(pv, "shown (1)") || !strings.Contains(pv, "hidden (1)") {
		t.Fatalf("preview should report 1 shown and 1 hidden: %s", pv)
	}
	if !strings.Contains(pv, "Go post") || !strings.Contains(pv, "Rust post") {
		t.Fatalf("preview should list both items: %s", pv)
	}

	// Swap the rule for r/golang and switch to an allow list: keeps Go, hides
	// Rust.
	rules, _ := s.store.FeedFilters.List(u.ID, f.ID)
	if len(rules) != 1 {
		t.Fatalf("want 1 rule, got %d", len(rules))
	}
	if rr := doForm(h, "POST", "/feeds/"+itoa(f.ID)+"/filters/"+itoa(rules[0].ID)+"/delete", url.Values{}, cookie); rr.Code != http.StatusOK {
		t.Fatalf("delete rule: %d", rr.Code)
	}
	if rr := doForm(h, "POST", addURL, url.Values{"field": {"category"}, "op": {"is"}, "value": {"r/golang"}}, cookie); rr.Code != http.StatusOK {
		t.Fatalf("add allow rule: %d", rr.Code)
	}
	if rr := doForm(h, "POST", configURL, url.Values{"mode": {"allow"}, "combine": {"any"}}, cookie); rr.Code != http.StatusOK {
		t.Fatalf("set allow mode: %d", rr.Code)
	}
	body = doGet(h, authorPage, cookie).Body.String()
	if strings.Contains(body, "Rust post") || !strings.Contains(body, "Go post") {
		t.Fatalf("allow list should keep only r/golang: %s", body)
	}

	// Removing the rule (allow list, no rules) re-shows everything.
	rules, _ = s.store.FeedFilters.List(u.ID, f.ID)
	if len(rules) != 1 {
		t.Fatalf("want 1 rule, got %d", len(rules))
	}
	if rr := doForm(h, "POST", "/feeds/"+itoa(f.ID)+"/filters/"+itoa(rules[0].ID)+"/delete", url.Values{}, cookie); rr.Code != http.StatusOK {
		t.Fatalf("delete rule: %d", rr.Code)
	}
	body = doGet(h, authorPage, cookie).Body.String()
	if !strings.Contains(body, "Rust post") || !strings.Contains(body, "Go post") {
		t.Fatalf("removing the rule should re-show everything: %s", body)
	}
}
