package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// TestAuthorFilterSettings covers the author settings card, persistence, the
// live preview endpoint, and enforcement across author/collection/list/global
// item views.
func TestAuthorFilterSettings(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Filtered", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://filtered.dev/feed.xml", "", "", 900)
	other, _ := s.store.Authors.Create(u.ID, "Other", "", "")
	fo, _ := s.store.Feeds.Create(u.ID, other.ID, "Other", "https://other.dev/feed.xml", "", "", 900)

	upsert := func(feedID int64, guid, title string, cats ...string) int64 {
		t.Helper()
		if _, err := s.store.Items.Upsert(feedID, store.Item{
			GUID: guid, Title: title, Link: "https://x/" + guid,
			Categories: cats, FetchedAt: db.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		id, err := s.store.Items.ByFeedIdentity(feedID, guid)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	goID := upsert(f.ID, "go", "Go post", "r/golang", "tutorial")
	upsert(f.ID, "rust", "Rust post", "r/rust")
	upsert(fo.ID, "other", "Other post", "r/rust")

	authorURL := "/authors/" + itoa(a.ID)
	editURL := authorURL + "/edit"
	body := doGet(h, editURL, cookie).Body.String()
	if !strings.Contains(body, `data-author-filters`) || !strings.Contains(body, `>allow</option>`) || !strings.Contains(body, `>block</option>`) {
		t.Fatalf("edit page missing allow/block filter card: %s", body)
	}
	if !strings.Contains(body, "allowed (2)") || !strings.Contains(body, "blocked (0)") {
		t.Fatalf("initial preview should allow all author posts: %s", body)
	}

	// Save a block rule through the same author form.
	form := url.Values{
		"name":                   {"Filtered"},
		"author_filters_present": {"1"},
		"filter_row":             {"0"},
		"filter_action":          {"hide"},
		"filter_kind":            {"tag"},
		"filter_combine":         {"any"},
		"filter_value_0":         {"r/rust"},
	}
	if rr := doForm(h, http.MethodPost, editURL, form, cookie); rr.Code != http.StatusFound {
		t.Fatalf("save author filters: %d %s", rr.Code, rr.Body.String())
	}

	body = doGet(h, editURL, cookie).Body.String()
	if !strings.Contains(body, `name="filter_value_0" value="r/rust"`) {
		t.Fatalf("edit page should reload the saved block rule: %s", body)
	}
	if !strings.Contains(body, "allowed (1)") || !strings.Contains(body, "blocked (1)") {
		t.Fatalf("saved preview should show one allowed and one blocked: %s", body)
	}

	// The author page hides the blocked posts and counts only visible ones.
	body = doGet(h, authorURL, cookie).Body.String()
	if !strings.Contains(body, "Go post") || strings.Contains(body, "Rust post") {
		t.Fatalf("author page should hide the blocked post: %s", body)
	}
	if !strings.Contains(body, "unread (1)") {
		t.Fatalf("author unread tab should be 1: %s", body)
	}

	// Global unread hides the blocked posts but keeps the other author's.
	body = doGet(h, "/unread", cookie).Body.String()
	if !strings.Contains(body, "Go post") || !strings.Contains(body, "Other post") || strings.Contains(body, "Rust post") {
		t.Fatalf("global unread should hide only the blocked post: %s", body)
	}

	// Collections and lists use the same enforcement.
	c, _ := s.store.Collections.Create(u.ID, "Set")
	if err := s.store.Collections.AddFeed(u.ID, c.ID, f.ID); err != nil {
		t.Fatal(err)
	}
	body = doGet(h, "/collections/"+itoa(c.ID), cookie).Body.String()
	if !strings.Contains(body, "Go post") || strings.Contains(body, "Rust post") {
		t.Fatalf("collection should hide the blocked post: %s", body)
	}

	l, _ := s.store.Lists.Create(u.ID, "Saved")
	for _, id := range []int64{goID} {
		if err := s.store.Lists.AddItem(u.ID, l.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	rustID, _ := s.store.Items.ByFeedIdentity(f.ID, "rust")
	if err := s.store.Lists.AddItem(u.ID, l.ID, rustID); err != nil {
		t.Fatal(err)
	}
	body = doGet(h, "/lists/"+itoa(l.ID), cookie).Body.String()
	if !strings.Contains(body, "Go post") || strings.Contains(body, "Rust post") {
		t.Fatalf("list should hide the blocked post: %s", body)
	}

	// Draft preview: allow only r/golang, so one allowed and two blocked.
	preview := url.Values{
		"filter_row":     {"0"},
		"filter_action":  {"show"},
		"filter_kind":    {"tag"},
		"filter_combine": {"all"},
		"filter_value_0": {"r/golang"},
	}
	rr := doForm(h, http.MethodPost, authorURL+"/filters/preview", preview, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "allowed (1)") || !strings.Contains(rr.Body.String(), "blocked (1)") {
		t.Fatalf("preview body = %s", rr.Body.String())
	}
}
