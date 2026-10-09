package httpapi

import (
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// TestTagFilterUI covers the tag filter's rendering: the item modal's tags
// section, the scoped list's active-filter chips and tag modal, and filtering a
// list by tag.
func TestTagFilterUI(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Author", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://metru.dev/rss.xml", "", "", 900)

	upsert := func(guid, title string, cats ...string) int64 {
		t.Helper()
		if _, err := s.store.Items.Upsert(f.ID, store.Item{
			GUID: guid, Title: title, Link: "https://metru.dev/" + guid,
			Summary: "body " + title, Categories: cats, FetchedAt: db.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		id, _ := s.store.Items.ByFeedIdentity(f.ID, guid)
		return id
	}
	golangID := upsert("a", "Go post", "r/golang", "tutorial")
	upsert("b", "Rust post", "r/rust")

	// Scoped list: active tag chips and the tag-filter dialog with options.
	body := doGet(h, "/feeds/"+itoa(f.ID)+"?tags=r/golang", cookie).Body.String()
	if !strings.Contains(body, `class="chips active-tags"`) {
		t.Fatalf("feed list missing active tag chips: %s", body)
	}
	if !strings.Contains(body, `id="tag-filter-dialog"`) {
		t.Fatalf("feed list missing tag-filter dialog: %s", body)
	}
	if !strings.Contains(body, `data-open="tag-filter-dialog"`) {
		t.Fatalf("feed list missing tag filter button: %s", body)
	}
	if !strings.Contains(body, "Go post") || strings.Contains(body, "Rust post") {
		t.Fatalf("tag filter should keep only the matching item: %s", body)
	}
	// The dialog offers every tag in scope, including the one not selected.
	if !strings.Contains(body, "r/rust") {
		t.Fatalf("tag dialog should offer r/rust: %s", body)
	}

	// Item modal: a tags section with a chip linking to the feed filtered by tag.
	// This runs last because opening the modal marks the item read.
	body = doGet(h, "/items/"+itoa(golangID)+"/view", cookie).Body.String()
	if !strings.Contains(body, `class="item-tags"`) {
		t.Fatalf("item modal missing tags section: %s", body)
	}
	if !strings.Contains(body, "r/golang") || !strings.Contains(body, "tutorial") {
		t.Fatalf("item modal missing tag chips: %s", body)
	}
	if !strings.Contains(body, "/feeds/"+itoa(f.ID)+"?tags=r%2Fgolang") {
		t.Fatalf("item modal tag chip should link to the filtered feed: %s", body)
	}
}
