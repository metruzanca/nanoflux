package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
)

// savedViewFixture builds an author with two tagged posts and returns the
// author, feed and the tag used by the filter recipe.
func savedViewFixture(t *testing.T, s *Server) (store.User, store.Author, store.Feed) {
	t.Helper()
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Aggregate", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://amphibian.dev/feed.xml", "", "", 900)
	for _, it := range []store.Item{
		{GUID: "go", Title: "Go post", Link: "https://x/go", Categories: []string{"u/example-poster"}, FetchedAt: db.Now()},
		{GUID: "other", Title: "Other post", Link: "https://x/other", Categories: []string{"u/someone-else"}, FetchedAt: db.Now()},
	} {
		if _, err := s.store.Items.Upsert(f.ID, it); err != nil {
			t.Fatal(err)
		}
	}
	return u, a, f
}

func TestSavedViewFilterParamsRoundTrip(t *testing.T) {
	in := listFilters{
		{Kind: filterKindTag, Action: filterShow, Combine: filterAll, Values: []string{"r/golang", "r/rust"}},
		{Kind: filterKindFeed, Action: filterHide, Combine: filterAny, Values: []string{"12", "13"}},
		{Kind: filterKindTime, Action: filterShow, Combine: filterAll, Values: []string{filterTimeBetween, "2026-01-01", "2026-01-31"}},
	}
	params := in.params()
	out := filtersFromParams(params)
	if len(out) != len(in) {
		t.Fatalf("round trip dropped rows: %v -> %v", params, out)
	}
	for i := range in {
		if in[i].param() != out[i].param() {
			t.Fatalf("row %d changed: %q -> %q", i, in[i].param(), out[i].param())
		}
	}
}

func TestSavedViewCreateRenderAndEdit(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _, _ := savedViewFixture(t, s)
	author, _ := s.store.Authors.Create(u.ID, "Example Poster", "", "")

	form := url.Values{
		"title":          {"Example posts"},
		"author_id":      {itoa(author.ID)},
		"filter_row":     {"0"},
		"filter_action":  {"show"},
		"filter_kind":    {"tag"},
		"filter_combine": {"all"},
		"filter_value_0": {"u/example-poster"},
	}
	rr := doForm(h, http.MethodPost, "/views", form, cookie)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("create view: %d %s", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "/views/") {
		t.Fatalf("create should redirect to the view, got %q", loc)
	}
	id := strings.TrimPrefix(loc, "/views/")

	// The filter dialog saves the draft rows as a view, and the add menu links to
	// the empty create form.
	body := doGet(h, "/unread?"+url.Values{"f": {"tag:show:u/example-poster"}}.Encode(), cookie).Body.String()
	if !strings.Contains(body, `formaction="/views/new"`) || !strings.Contains(body, `href="/views/new"`) {
		t.Fatalf("unread should offer save-as-view from the filter dialog and add menu: %s", body)
	}

	// The create page carries the filter rows and the author picker.
	formPage := doGet(h, "/views/new?f="+url.QueryEscape("tag:show:u/example-poster"), cookie).Body.String()
	if !strings.Contains(formPage, `name="title"`) || !strings.Contains(formPage, `name="author_id"`) || !strings.Contains(formPage, `name="filter_value_empty"`) && !strings.Contains(formPage, `name="filter_value_0"`) {
		t.Fatalf("new view form is missing fields: %s", formPage)
	}

	// The index lists it under its assigned author.
	body = doGet(h, "/views", cookie).Body.String()
	if !strings.Contains(body, "Example posts") || !strings.Contains(body, "Example Poster") {
		t.Fatalf("views index should list the saved view under its author: %s", body)
	}

	// An assigned view redirects to the author page, which now carries its items.
	bare := doGet(h, "/views/"+id, cookie)
	if bare.Code != http.StatusSeeOther {
		t.Fatalf("assigned view should redirect to its author: %d", bare.Code)
	}
	authorLoc := bare.Header().Get("Location")
	if authorLoc != "/authors/"+itoa(author.ID)+"?view="+id {
		t.Fatalf("assigned view redirect = %q", authorLoc)
	}
	body = doGet(h, authorLoc, cookie).Body.String()
	if !strings.Contains(body, "Go post") || strings.Contains(body, "Other post") {
		t.Fatalf("view-scoped author page should show only the filtered post: %s", body)
	}
	if !strings.Contains(body, "unread (1)") {
		t.Fatalf("view-scoped author unread count should be 1: %s", body)
	}
	if !strings.Contains(body, "?view="+id+"&amp;tab=read") {
		t.Fatalf("read tab should preserve the selected view: %s", body)
	}

	// The default author page aggregates assigned views, so it shows the post.
	body = doGet(h, "/authors/"+itoa(author.ID), cookie).Body.String()
	if !strings.Contains(body, "Go post") || strings.Contains(body, "Other post") {
		t.Fatalf("default author page should include view items: %s", body)
	}
	if !strings.Contains(body, "unread (1)") {
		t.Fatalf("default author unread count should include the view: %s", body)
	}
	if !strings.Contains(body, "<strong>1</strong> posts") {
		t.Fatalf("author stats should include view items: %s", body)
	}

	// /authors lists the view's unread count and view count.
	body = doGet(h, "/authors", cookie).Body.String()
	if !strings.Contains(body, `data-unread="1"`) || !strings.Contains(body, "1 view") {
		t.Fatalf("authors index should count view unread items and views: %s", body)
	}

	// An empty view is rejected: it would pull every post into the author.
	empty := url.Values{"title": {"Empty"}, "author_id": {itoa(author.ID)}}
	if rr := doForm(h, http.MethodPost, "/views", empty, cookie); rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "add at least one filter") {
		t.Fatalf("empty view should be rejected: %d", rr.Code)
	}

	// The author page carries the assigned view in its combined sources section.
	body = doGet(h, "/authors/"+itoa(author.ID), cookie).Body.String()
	if !strings.Contains(body, "sources (1)") || !strings.Contains(body, "Example posts") {
		t.Fatalf("author page should list the assigned view under sources: %s", body)
	}

	// Edit renames and unassigns the view.
	edit := url.Values{
		"title":          {"Renamed view"},
		"author_id":      {"0"},
		"filter_row":     {"0"},
		"filter_action":  {"show"},
		"filter_kind":    {"tag"},
		"filter_combine": {"all"},
		"filter_value_0": {"u/example-poster"},
	}
	if rr := doForm(h, http.MethodPost, "/views/"+id+"/edit", edit, cookie); rr.Code != http.StatusSeeOther {
		t.Fatalf("edit view: %d %s", rr.Code, rr.Body.String())
	}
	u, _ = s.store.Users.ByUsername("alice")
	views, _ := s.store.SavedViews.List(u.ID)
	if len(views) != 1 {
		t.Fatalf("expected one saved view, got %d", len(views))
	}
	if views[0].Title != "Renamed view" || views[0].AuthorID != 0 {
		t.Fatalf("view not updated: %+v", views[0])
	}

	// Delete returns to the index and removes the row.
	if rr := doForm(h, http.MethodPost, "/views/"+id+"/delete", nil, cookie); rr.Code != http.StatusSeeOther {
		t.Fatalf("delete view: %d %s", rr.Code, rr.Body.String())
	}
	views, _ = s.store.SavedViews.List(u.ID)
	if len(views) != 0 {
		t.Fatalf("view should be deleted, got %+v", views)
	}
}

func TestSavedViewDropsUnknownFeedFilter(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, author, _ := savedViewFixture(t, s)

	// A feed id that does not belong to the user is dropped; the tag survives.
	form := url.Values{
		"title":          {"Filtered"},
		"author_id":      {itoa(author.ID)},
		"filter_row":     {"0", "1"},
		"filter_action":  {"show", "show"},
		"filter_kind":    {"feed", "tag"},
		"filter_combine": {"all", "all"},
		"filter_value_0": {"999999"},
		"filter_value_1": {"u/example-poster"},
	}
	if rr := doForm(h, http.MethodPost, "/views", form, cookie); rr.Code != http.StatusSeeOther {
		t.Fatalf("create view: %d %s", rr.Code, rr.Body.String())
	}
	views, _ := s.store.SavedViews.List(u.ID)
	if len(views) != 1 {
		t.Fatalf("expected one view, got %d", len(views))
	}
	if len(views[0].Filters) != 1 || !strings.HasPrefix(views[0].Filters[0], "tag:") {
		t.Fatalf("unknown feed filter should be dropped and the tag kept, got %v", views[0].Filters)
	}
}

func TestSavedViewReadAllHonorsFilters(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, author, f := savedViewFixture(t, s)

	form := url.Values{
		"title":          {"Go"},
		"author_id":      {itoa(author.ID)},
		"filter_row":     {"0"},
		"filter_action":  {"show"},
		"filter_kind":    {"tag"},
		"filter_combine": {"all"},
		"filter_value_0": {"u/example-poster"},
	}
	rr := doForm(h, http.MethodPost, "/views", form, cookie)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("create view: %d %s", rr.Code, rr.Body.String())
	}
	id := strings.TrimPrefix(rr.Header().Get("Location"), "/views/")

	// Mark-all on the view-scoped author page carries the selected view, so only
	// the matching post is read even though the author also owns the feed.
	action := "/authors/" + itoa(author.ID) + "/read-all?view=" + id
	if rr := doForm(h, http.MethodPost, action, nil, cookie); rr.Code != http.StatusNoContent {
		t.Fatalf("mark view read: %d %s", rr.Code, rr.Body.String())
	}
	goID, _ := s.store.Items.ByFeedIdentity(f.ID, "go")
	otherID, _ := s.store.Items.ByFeedIdentity(f.ID, "other")
	goItem, _ := s.store.Items.ByID(u.ID, goID)
	otherItem, _ := s.store.Items.ByID(u.ID, otherID)
	if !goItem.Read {
		t.Fatalf("matching post should be read")
	}
	if otherItem.Read {
		t.Fatalf("non-matching post should stay unread")
	}
}

// TestSavedViewTagOptionsAllFeeds covers the edit form and filter dialog for an
// all-feeds view: the option lookup has no author or collection id, and it must
// still offer the user's tags.
func TestSavedViewTagOptionsAllFeeds(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, _ := s.store.Users.ByUsername("alice")
	a, _ := s.store.Authors.Create(u.ID, "Blogger", "", "")
	f, _ := s.store.Feeds.Create(u.ID, a.ID, "Blog", "https://example.com/feed.xml", "", "", 900)
	for _, it := range []store.Item{
		{GUID: "go", Title: "Go post", Link: "https://example.com/go", Categories: []string{"r/golang"}, FetchedAt: db.Now()},
		{GUID: "rust", Title: "Rust post", Link: "https://example.com/rust", Categories: []string{"r/rust"}, FetchedAt: db.Now()},
	} {
		if _, err := s.store.Items.Upsert(f.ID, it); err != nil {
			t.Fatal(err)
		}
	}

	form := url.Values{
		"title":          {"All feeds"},
		"author_id":      {"0"},
		"filter_row":     {"0"},
		"filter_action":  {"show"},
		"filter_kind":    {"tag"},
		"filter_combine": {"all"},
		"filter_value_0": {"r/golang"},
	}
	rr := doForm(h, http.MethodPost, "/views", form, cookie)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("create view: %d %s", rr.Code, rr.Body.String())
	}
	id := strings.TrimPrefix(rr.Header().Get("Location"), "/views/")

	editPage := doGet(h, "/views/"+id+"/edit", cookie).Body.String()
	for _, tag := range []string{"r/golang", "r/rust"} {
		option := `data-combo-option="filter_value_0:` + tag + `"`
		if !strings.Contains(editPage, option) {
			t.Fatalf("edit form should offer tag %q for an all-feeds view", tag)
		}
	}

	viewPage := doGet(h, "/views/"+id+"?view=unread&sort=newest&f="+url.QueryEscape("tag:show:r/golang"), cookie).Body.String()
	if !strings.Contains(viewPage, `data-combo-option="filter_value_0:r/rust"`) {
		t.Fatalf("filter dialog should offer the user's tags for an all-feeds view")
	}
}
