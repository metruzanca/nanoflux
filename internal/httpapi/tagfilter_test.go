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
	if !strings.Contains(body, "1 matching") {
		t.Fatalf("filtered list should show the matching post count: %s", body)
	}
	// The dialog offers every tag in scope, including the one not selected.
	if !strings.Contains(body, "r/rust") {
		t.Fatalf("filter dialog should offer r/rust: %s", body)
	}

	// Item modal: a compact #tag list, each tag linking to the author filtered
	// by tag. This runs last because opening the modal marks the item read.
	body = doGet(h, "/items/"+itoa(golangID)+"/view", cookie).Body.String()
	if !strings.Contains(body, `class="item-tags`) {
		t.Fatalf("item modal missing tags section: %s", body)
	}
	if !strings.Contains(body, "#r/golang") || !strings.Contains(body, "#tutorial") {
		t.Fatalf("item modal missing tags: %s", body)
	}
	// Tags are space-separated, not glued together (#a#b).
	if !strings.Contains(body, "#r/golang</a> <a") {
		t.Fatalf("item modal tags should be space-separated: %s", body)
	}
	if !strings.Contains(body, "/authors/"+itoa(a.ID)+"?tags=r%2Fgolang") {
		t.Fatalf("item modal tag should link to the filtered author: %s", body)
	}
}

// TestAuthorFilterShowHide covers negative and grouped author-level filters:
// hidden tags/feeds, per-row any/all, cross-row AND/OR behavior, single-feed
// simplification, and the modal's editable multi-value rows.
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
	upsert(f2.ID, "linux", "Linux post", "r/rust", "linux")

	authorURL := "/authors/" + itoa(a.ID)
	filterURL := func(raw ...string) string {
		q := url.Values{}
		for _, v := range raw {
			q.Add("f", v)
		}
		return authorURL + "?" + q.Encode()
	}
	show := func(body string, titles ...string) {
		t.Helper()
		for _, title := range titles {
			if !strings.Contains(body, title) {
				t.Fatalf("expected %q in body: %s", title, body)
			}
		}
	}
	hide := func(body string, titles ...string) {
		t.Helper()
		for _, title := range titles {
			if strings.Contains(body, title) {
				t.Fatalf("did not expect %q in body: %s", title, body)
			}
		}
	}

	// Hide a tag: matching posts vanish, others stay.
	body := doGet(h, filterURL("tag:hide:r/rust"), cookie).Body.String()
	show(body, "Go post")
	hide(body, "Rust post", "Note post", "Linux post")
	if !strings.Contains(body, `chip chip-hide`) {
		t.Fatalf("hidden filter should render a hide chip: %s", body)
	}
	// The modal lists the applied filter with its inputs.
	if !strings.Contains(body, `name="filter_value_0" value="r/rust"`) {
		t.Fatalf("modal should prefill the hidden tag value: %s", body)
	}
	if !strings.Contains(body, `<option value="hide" selected>hide</option>`) {
		t.Fatalf("modal should select the hide action: %s", body)
	}
	if !strings.Contains(body, `id="filter-row-template"`) || !strings.Contains(body, `data-add-filter-row`) {
		t.Fatalf("modal should render an add-row template and controls: %s", body)
	}
	if !strings.Contains(body, `name="filter_combine"`) {
		t.Fatalf("modal should render a combine select: %s", body)
	}

	// any: show the union of two tags.
	tagAny := `tag:show:any:["r/golang","linux"]`
	body = doGet(h, filterURL(tagAny), cookie).Body.String()
	show(body, "Go post", "Linux post")
	hide(body, "Rust post", "Note post")

	// all: require both tags.
	body = doGet(h, filterURL(`tag:show:all:["r/golang","tutorial"]`), cookie).Body.String()
	show(body, "Go post")
	hide(body, "Rust post", "Note post", "Linux post")

	// Multiple show rows still AND together.
	body = doGet(h, filterURL(tagAny, `tag:show:all:["tutorial"]`), cookie).Body.String()
	show(body, "Go post")
	hide(body, "Rust post", "Note post", "Linux post")

	// Hide any: exclude posts with either tag.
	body = doGet(h, filterURL(`tag:hide:any:["tutorial","linux"]`), cookie).Body.String()
	show(body, "Rust post", "Note post")
	hide(body, "Go post", "Linux post")

	// Hide all: exclude only posts carrying every tag.
	body = doGet(h, filterURL(`tag:hide:all:["r/rust","linux"]`), cookie).Body.String()
	show(body, "Go post", "Rust post", "Note post")
	hide(body, "Linux post")

	// A multi-value feed row works, and the kind select is available with 2
	// feeds.
	body = doGet(h, filterURL(`feed:hide:any:["`+itoa(f1.ID)+`"]`), cookie).Body.String()
	show(body, "Note post", "Linux post")
	hide(body, "Go post", "Rust post")
	if !strings.Contains(body, `data-filter-kind`) || !strings.Contains(body, `<option value="feed" selected>feed</option>`) {
		t.Fatalf("modal should render a feed kind select: %s", body)
	}

	// Mark-all honors a hidden feed: only the visible feed's unread is marked.
	if rr := doForm(h, "POST", authorURL+"/read-all?"+url.Values{"f": {`feed:hide:any:["` + itoa(f1.ID) + `"]`}}.Encode(), nil, cookie); rr.Code != http.StatusNoContent {
		t.Fatalf("hide-filtered read-all: %d %s", rr.Code, rr.Body.String())
	}
	if n, _ := s.store.Items.CountUnread(u.ID, f1.ID); n != 2 {
		t.Fatalf("hidden feed unread = %d, want 2 (untouched)", n)
	}
	if n, _ := s.store.Items.CountUnread(u.ID, f2.ID); n != 0 {
		t.Fatalf("visible feed unread = %d, want 0", n)
	}

	// With one feed, the feed kind is not offered and a redundant feed filter
	// is dropped; the hidden kind defaults to tag.
	one, _ := s.store.Authors.Create(u.ID, "Solo", "", "")
	only, _ := s.store.Feeds.Create(u.ID, one.ID, "Solo feed", "https://solo.dev/rss.xml", "", "", 900)
	upsert(only.ID, "solo", "Solo post", "r/solo")
	oneURL := "/authors/" + itoa(one.ID)
	body = doGet(h, oneURL+"?f="+url.QueryEscape("feed:show:"+itoa(only.ID)), cookie).Body.String()
	if strings.Contains(body, `data-filter-kind`) {
		t.Fatalf("single-feed author should not render a kind select: %s", body)
	}
	if strings.Contains(body, `feed:show:`) || !strings.Contains(body, `name="filter_kind" value="tag"`) {
		t.Fatalf("single-feed feed filter should be dropped and kind default to tag: %s", body)
	}
}

func TestFiltersFromValues(t *testing.T) {
	cases := []struct {
		name string
		vals url.Values
		want listFilters
	}{
		{
			name: "canonical single",
			vals: url.Values{"f": {"tag:show:r/golang", "tag:hide:r/rust", "feed:hide:12"}},
			want: listFilters{
				{Kind: filterKindTag, Action: filterShow, Combine: filterAll, Values: []string{"r/golang"}, Labels: []string{"r/golang"}},
				{Kind: filterKindTag, Action: filterHide, Combine: filterAll, Values: []string{"r/rust"}, Labels: []string{"r/rust"}},
				{Kind: filterKindFeed, Action: filterHide, Combine: filterAll, Values: []string{"12"}},
			},
		},
		{
			name: "canonical multi",
			vals: url.Values{"f": {`tag:show:any:["r/golang","r/rust"]`}},
			want: listFilters{
				{Kind: filterKindTag, Action: filterShow, Combine: filterAny, Values: []string{"r/golang", "r/rust"}, Labels: []string{"r/golang", "r/rust"}},
			},
		},
		{
			name: "legacy",
			vals: url.Values{"tags": {"r/golang"}, "feed": {"7"}},
			want: listFilters{
				{Kind: filterKindTag, Action: filterShow, Combine: filterAll, Values: []string{"r/golang"}, Labels: []string{"r/golang"}},
				{Kind: filterKindFeed, Action: filterShow, Combine: filterAll, Values: []string{"7"}},
			},
		},
		{
			name: "form rows",
			vals: url.Values{
				"filter_row":     {"0", "1", "2"},
				"filter_kind":    {"tag", "feed", "tag"},
				"filter_action":  {"show", "hide", "show"},
				"filter_combine": {"any", "all", "all"},
				"filter_value_0": {"r/golang", "r/rust"},
				"filter_value_1": {"9"},
				"filter_value_2": {""},
			},
			want: listFilters{
				{Kind: filterKindTag, Action: filterShow, Combine: filterAny, Values: []string{"r/golang", "r/rust"}, Labels: []string{"r/golang", "r/rust"}},
				{Kind: filterKindFeed, Action: filterHide, Combine: filterAll, Values: []string{"9"}},
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
				if got[i].Kind != c.want[i].Kind || got[i].Action != c.want[i].Action ||
					got[i].Combine != c.want[i].Combine ||
					strings.Join(got[i].Values, "|") != strings.Join(c.want[i].Values, "|") ||
					strings.Join(got[i].Labels, "|") != strings.Join(c.want[i].Labels, "|") {
					t.Fatalf("filter %d = %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}
