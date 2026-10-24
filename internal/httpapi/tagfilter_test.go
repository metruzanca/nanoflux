package httpapi

import (
	"net/http"
	"net/url"
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

	// Scoped list: active tag chips and the filter dialog with options.
	body := doGet(h, "/authors/"+itoa(a.ID)+"?feed="+itoa(f.ID)+"&tags=r/golang", cookie).Body.String()
	if !strings.Contains(body, `class="chips active-tags"`) {
		t.Fatalf("feed list missing active tag chips: %s", body)
	}
	if !strings.Contains(body, `id="tag-filter-dialog"`) {
		t.Fatalf("feed list missing filter dialog: %s", body)
	}
	if !strings.Contains(body, `data-open="tag-filter-dialog"`) {
		t.Fatalf("feed list missing filter button: %s", body)
	}
	if !strings.Contains(body, "Go post") || strings.Contains(body, "Rust post") {
		t.Fatalf("tag filter should keep only the matching item: %s", body)
	}
	// The dialog offers every tag in scope, including the one not selected.
	if !strings.Contains(body, "r/rust") {
		t.Fatalf("filter dialog should offer r/rust: %s", body)
	}

	// Item modal: a tags section with a chip linking to the author filtered by
	// tag. This runs last because opening the modal marks the item read.
	body = doGet(h, "/items/"+itoa(golangID)+"/view", cookie).Body.String()
	if !strings.Contains(body, `class="item-tags"`) {
		t.Fatalf("item modal missing tags section: %s", body)
	}
	if !strings.Contains(body, "r/golang") || !strings.Contains(body, "tutorial") {
		t.Fatalf("item modal missing tag chips: %s", body)
	}
	if !strings.Contains(body, "/authors/"+itoa(a.ID)+"?tags=r%2Fgolang") {
		t.Fatalf("item modal tag chip should link to the filtered author: %s", body)
	}
}

// TestAuthorFilterShowHide covers negative author-level filters: a hidden tag
// excludes matching posts, a hidden feed excludes that feed, show and hide
// coexist, and the modal renders applied filters as editable rows.
func TestAuthorFilterShowHide(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Author", "", "")
	f1, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)
	f2, _ := s.store.Feeds.Create(u.ID, a.ID, "Notes", "https://n.dev/rss.xml", "", "", 900)
	upsert := func(feedID int64, guid, title string, cats ...string) {
		t.Helper()
		if _, err := s.store.Items.Upsert(feedID, store.Item{
			GUID: guid, Title: title, Link: "https://b.dev/" + guid,
			Categories: cats, FetchedAt: db.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	upsert(f1.ID, "go", "Go post", "r/golang", "tutorial")
	upsert(f1.ID, "rust", "Rust post", "r/rust")
	upsert(f2.ID, "note", "Note post", "r/rust")

	authorURL := "/authors/" + itoa(a.ID)
	filterURL := func(raw ...string) string {
		q := url.Values{}
		for _, v := range raw {
			q.Add("f", v)
		}
		return authorURL + "?" + q.Encode()
	}

	// Hide a tag: matching posts vanish, others stay.
	body := doGet(h, filterURL("tag:hide:r/rust"), cookie).Body.String()
	if !strings.Contains(body, "Go post") || strings.Contains(body, "Rust post") || strings.Contains(body, "Note post") {
		t.Fatalf("hidden tag should exclude both r/rust posts: %s", body)
	}
	if !strings.Contains(body, `chip chip-hide`) {
		t.Fatalf("hidden filter should render a hide chip: %s", body)
	}
	// The modal lists the applied filter with its inputs.
	if !strings.Contains(body, `name="filter_value" value="r/rust"`) {
		t.Fatalf("modal should prefill the hidden tag value: %s", body)
	}
	if !strings.Contains(body, `<option value="hide" selected>hide</option>`) {
		t.Fatalf("modal should select the hide action: %s", body)
	}

	// A show tag and a hide tag combine: must have the show tag and not the
	// hide tag.
	body = doGet(h, filterURL("tag:show:r/golang", "tag:hide:tutorial"), cookie).Body.String()
	if strings.Contains(body, "Go post") || strings.Contains(body, "Rust post") || strings.Contains(body, "Note post") {
		t.Fatalf("show+hide tags should keep no post: %s", body)
	}

	// Hide a feed: only the other feed's posts remain.
	body = doGet(h, filterURL("feed:hide:"+itoa(f2.ID)), cookie).Body.String()
	if !strings.Contains(body, "Go post") || !strings.Contains(body, "Rust post") || strings.Contains(body, "Note post") {
		t.Fatalf("hidden feed should exclude its posts: %s", body)
	}
	if !strings.Contains(body, `name="filter_kind" value="feed"`) {
		t.Fatalf("modal should render a feed filter row: %s", body)
	}

	// Mark-all honors a hidden feed: only the visible feed's unread is marked.
	if rr := doForm(h, "POST", authorURL+"/read-all?"+url.Values{"f": {"feed:hide:" + itoa(f2.ID)}}.Encode(), nil, cookie); rr.Code != http.StatusNoContent {
		t.Fatalf("hide-filtered read-all: %d %s", rr.Code, rr.Body.String())
	}
	if n, _ := s.store.Items.CountUnread(u.ID, f1.ID); n != 0 {
		t.Fatalf("visible feed unread = %d, want 0", n)
	}
	if n, _ := s.store.Items.CountUnread(u.ID, f2.ID); n != 1 {
		t.Fatalf("hidden feed unread = %d, want 1 (untouched)", n)
	}
}

func TestFiltersFromValues(t *testing.T) {
	cases := []struct {
		name string
		vals url.Values
		want listFilters
	}{
		{
			name: "canonical",
			vals: url.Values{"f": {"tag:show:r/golang", "tag:hide:r/rust", "feed:hide:12"}},
			want: listFilters{
				{Kind: filterKindTag, Action: filterShow, Value: "r/golang", Label: "r/golang"},
				{Kind: filterKindTag, Action: filterHide, Value: "r/rust", Label: "r/rust"},
				{Kind: filterKindFeed, Action: filterHide, Value: "12"},
			},
		},
		{
			name: "legacy",
			vals: url.Values{"tags": {"r/golang"}, "feed": {"7"}},
			want: listFilters{
				{Kind: filterKindTag, Action: filterShow, Value: "r/golang", Label: "r/golang"},
				{Kind: filterKindFeed, Action: filterShow, Value: "7"},
			},
		},
		{
			name: "form rows",
			vals: url.Values{
				"filter_kind":   {"tag", "tag", "feed"},
				"filter_action": {"show", "hide", "hide"},
				"filter_value":  {"r/golang", "", "9"},
			},
			want: listFilters{
				{Kind: filterKindTag, Action: filterShow, Value: "r/golang", Label: "r/golang"},
				{Kind: filterKindFeed, Action: filterHide, Value: "9"},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := filtersFromValues(c.vals)
			if len(got) != len(c.want) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("filter %d = %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}
