package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// TestAuthorAutoReadSettings covers the per-author auto-read override: inherit
// by default, immediate sweep on save, "off", validation, and clearing back to
// inherit.
func TestAuthorAutoReadSettings(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	if err := s.store.Users.SetAutoReadAfterDays(u.ID, 0); err != nil {
		t.Fatal(err)
	}
	a, _ := s.store.Authors.Create(u.ID, "Busy", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Busy feed", "https://busy.dev/feed.xml", "", "", 900)
	old := db.FormatTime(time.Now().AddDate(0, 0, -10))
	if _, err := s.store.Items.Upsert(f.ID, store.Item{GUID: "old", Title: "old", PublishedAt: old, FetchedAt: db.Now()}); err != nil {
		t.Fatal(err)
	}

	editURL := "/authors/" + itoa(a.ID) + "/edit"
	autoReadURL := "/authors/" + itoa(a.ID) + "/auto-read"
	body := doGet(h, editURL, cookie).Body.String()
	if !strings.Contains(body, `name="author_auto_read_after_days"`) || !strings.Contains(body, "use account default") {
		t.Fatalf("edit page missing the auto-read override card: %s", body)
	}

	// Saving a window sweeps this author immediately, even though the account
	// window is off. The htmx path returns the refreshed card.
	if rr := doHtmxForm(h, http.MethodPost, autoReadURL, url.Values{
		"author_auto_read_present":    {"1"},
		"author_auto_read_after_days": {"7"},
	}, cookie); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `id="author-auto-read-card"`) {
		t.Fatalf("htmx auto-read save: %d %s", rr.Code, rr.Body.String())
	}
	if rr := doForm(h, http.MethodPost, autoReadURL, url.Values{
		"author_auto_read_present":    {"1"},
		"author_auto_read_after_days": {"3"},
	}, cookie); rr.Code != http.StatusSeeOther {
		t.Fatalf("save author auto-read: %d %s", rr.Code, rr.Body.String())
	}
	got, _ := s.store.Authors.ByID(u.ID, a.ID)
	if got.AutoReadAfterDays == nil || *got.AutoReadAfterDays != 3 {
		t.Fatalf("author override = %v; want 3", got.AutoReadAfterDays)
	}
	if unread, _ := s.store.Items.CountUnread(u.ID, f.ID); unread != 0 {
		t.Fatalf("saving should sweep the author immediately: unread = %d", unread)
	}

	// An invalid value is rejected and does not overwrite the stored one.
	if rr := doForm(h, http.MethodPost, autoReadURL, url.Values{
		"author_auto_read_present":    {"1"},
		"author_auto_read_after_days": {"99"},
	}, cookie); rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid author auto-read: %d %s", rr.Code, rr.Body.String())
	}
	got, _ = s.store.Authors.ByID(u.ID, a.ID)
	if got.AutoReadAfterDays == nil || *got.AutoReadAfterDays != 3 {
		t.Fatalf("invalid value changed the override: %v", got.AutoReadAfterDays)
	}

	// "off" is a real override, distinct from inherit.
	if rr := doForm(h, http.MethodPost, autoReadURL, url.Values{
		"author_auto_read_present":    {"1"},
		"author_auto_read_after_days": {"off"},
	}, cookie); rr.Code != http.StatusSeeOther {
		t.Fatalf("save off: %d %s", rr.Code, rr.Body.String())
	}
	got, _ = s.store.Authors.ByID(u.ID, a.ID)
	if got.AutoReadAfterDays == nil || *got.AutoReadAfterDays != 0 {
		t.Fatalf("off override = %v; want 0", got.AutoReadAfterDays)
	}

	// Inherit clears the override.
	if rr := doForm(h, http.MethodPost, autoReadURL, url.Values{
		"author_auto_read_present":    {"1"},
		"author_auto_read_after_days": {"inherit"},
	}, cookie); rr.Code != http.StatusSeeOther {
		t.Fatalf("save inherit: %d %s", rr.Code, rr.Body.String())
	}
	got, _ = s.store.Authors.ByID(u.ID, a.ID)
	if got.AutoReadAfterDays != nil {
		t.Fatalf("inherit should clear the override, got %v", got.AutoReadAfterDays)
	}
}

// TestAuthorEditAutoSaveLayout covers the per-section forms on the author edit
// page: basics, auto-read, filters (manual apply), and one sources accordion
// with no page-level save button.
func TestAuthorEditAutoSaveLayout(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Metru", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://b.dev/rss.xml", "", "", 900)

	body := doGet(h, "/authors/"+itoa(a.ID)+"/edit?feed="+itoa(f.ID), cookie).Body.String()
	for _, want := range []string{
		`id="author-basics-card"`,
		`hx-post="/authors/` + itoa(a.ID) + `/edit"`,
		`hx-trigger="change delay:600ms, submit"`,
		`id="author-auto-read-card"`,
		`hx-post="/authors/` + itoa(a.ID) + `/auto-read"`,
		`id="author-filter-card"`,
		`hx-post="/authors/` + itoa(a.ID) + `/filters"`,
		`<h2>sources</h2>`,
		`move this feed and its items to a <button`,
		`hx-post="/feeds/` + itoa(f.ID) + `/edit"`,
		`data-autosave`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("author edit missing %q: %s", want, body)
		}
	}
	for _, unwanted := range []string{"+ new view", "save author and feeds"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("author edit should not contain %q: %s", unwanted, body)
		}
	}
	// With no saved rules the filter card starts collapsed.
	if strings.Contains(body, ` open data-author-filters`) {
		t.Fatalf("empty filter card should start collapsed: %s", body)
	}
}

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

	// Save a block rule through the filter card's apply action.
	form := url.Values{
		"author_filters_present": {"1"},
		"filter_row":             {"0"},
		"filter_action":          {"hide"},
		"filter_kind":            {"tag"},
		"filter_combine":         {"any"},
		"filter_value_0":         {"r/rust"},
	}
	if rr := doForm(h, http.MethodPost, "/authors/"+itoa(a.ID)+"/filters", form, cookie); rr.Code != http.StatusSeeOther {
		t.Fatalf("save author filters: %d %s", rr.Code, rr.Body.String())
	}
	if rr := doHtmxForm(h, http.MethodPost, "/authors/"+itoa(a.ID)+"/filters", form, cookie); rr.Code != http.StatusOK ||
		!strings.Contains(rr.Body.String(), `id="author-filter-card"`) {
		t.Fatalf("htmx save author filters: %d %s", rr.Code, rr.Body.String())
	}

	body = doGet(h, editURL, cookie).Body.String()
	if !strings.Contains(body, `name="filter_value_0" value="r/rust"`) {
		t.Fatalf("edit page should reload the saved block rule: %s", body)
	}
	if !strings.Contains(body, ` open data-author-filters`) {
		t.Fatalf("filter card should auto-open when rules exist: %s", body)
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

// TestAuthorViewFollowsAssignedAuthorRules verifies the saved-view rule source
// end to end: a view is filtered by the author it is assigned to, not by the
// author who owns the feed it draws from, and an unassigned view has no rules.
func TestAuthorViewFollowsAssignedAuthorRules(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")

	aggregate, _ := s.store.Authors.Create(u.ID, "Aggregate", "", "")
	feed, _ := s.store.Feeds.Create(u.ID, aggregate.ID, "Sub", "https://sub.example/rss.xml", "", "", 900)
	captain, _ := s.store.Authors.Create(u.ID, "Captain", "", "")

	upsert := func(guid, title, tag string) int64 {
		t.Helper()
		if _, err := s.store.Items.Upsert(feed.ID, store.Item{
			GUID: guid, Title: title, Link: "https://x/" + guid,
			Categories: []string{tag}, FetchedAt: db.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		id, err := s.store.Items.ByFeedIdentity(feed.ID, guid)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	upsert("agg", "Aggregate post", "u/aggregate")
	upsert("cap", "Captain post", "u/captain")

	if err := s.store.AuthorFilters.Replace(u.ID, aggregate.ID, []store.FilterRule{{
		Kind: store.FilterKindTag, Action: store.FilterActionHide, Combine: store.FilterCombineAny, Values: []string{"u/captain"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.AuthorFilters.Replace(u.ID, captain.ID, []store.FilterRule{{
		Kind: store.FilterKindTag, Action: store.FilterActionHide, Combine: store.FilterCombineAny, Values: []string{"u/aggregate"},
	}}); err != nil {
		t.Fatal(err)
	}

	createView := func(authorID int64, title, filter string) store.SavedView {
		t.Helper()
		v, err := s.store.SavedViews.Create(u.ID, store.SavedView{
			AuthorID: authorID, Title: title, Filters: []string{filter},
		})
		if err != nil {
			t.Fatalf("create view: %v", err)
		}
		return v
	}
	capView := createView(captain.ID, "Cap view", "tag:show:u/captain")
	aggView := createView(captain.ID, "Agg view", "tag:show:u/aggregate")
	freeView := createView(0, "Free view", "tag:show:u/captain")

	captainURL := "/authors/" + itoa(captain.ID)
	aggregateURL := "/authors/" + itoa(aggregate.ID)

	// The aggregator still hides the captain's post from its own page.
	body := doGet(h, aggregateURL, cookie).Body.String()
	if !strings.Contains(body, "Aggregate post") || strings.Contains(body, "Captain post") {
		t.Fatalf("aggregator page should hide the blocked post: %s", body)
	}

	// The captain's page shows it: the view follows the captain's rules.
	body = doGet(h, captainURL, cookie).Body.String()
	if !strings.Contains(body, "Captain post") || strings.Contains(body, "Aggregate post") {
		t.Fatalf("captain page should show its view item and hide its own block: %s", body)
	}
	// The view matching the post the captain blocks stays empty.
	body = doGet(h, captainURL+"?view="+itoa(aggView.ID), cookie).Body.String()
	if strings.Contains(body, "Aggregate post") || strings.Contains(body, "Captain post") {
		t.Fatalf("captain's blocked view should be empty: %s", body)
	}

	// Global unread still follows the owner author's rule.
	body = doGet(h, "/unread", cookie).Body.String()
	if !strings.Contains(body, "Aggregate post") || strings.Contains(body, "Captain post") {
		t.Fatalf("global unread should hide the aggregator-blocked post: %s", body)
	}

	// An unassigned view has no author rules, so it surfaces the post the
	// aggregator hides from itself.
	body = doGet(h, "/views/"+itoa(freeView.ID)+"?view=unread&sort=newest&f="+url.QueryEscape("tag:show:u/captain"), cookie).Body.String()
	if !strings.Contains(body, "Captain post") {
		t.Fatalf("unassigned view should be unfiltered: %s", body)
	}

	// A collection's member view carries the same rule source.
	col, _ := s.store.Collections.Create(u.ID, "Slice")
	if err := s.store.SavedViews.SetCollections(u.ID, capView.ID, []int64{col.ID}); err != nil {
		t.Fatal(err)
	}
	body = doGet(h, "/collections/"+itoa(col.ID), cookie).Body.String()
	if !strings.Contains(body, "Captain post") || strings.Contains(body, "Aggregate post") {
		t.Fatalf("collection view scope should follow the assigned author: %s", body)
	}
}
