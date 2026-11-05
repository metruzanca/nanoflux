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
	a, _ := s.store.Authors.Create(u.ID, "Example Poster", "", "")
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
	_, author, _ := savedViewFixture(t, s)

	form := url.Values{
		"title":            {"Example posts"},
		"author_id":        {itoa(author.ID)},
		"scope_author":     {"0"},
		"scope_collection": {"0"},
		"view":             {"unread"},
		"sort":             {"newest"},
		"filter_row":       {"0"},
		"filter_action":    {"show"},
		"filter_kind":      {"tag"},
		"filter_combine":   {"all"},
		"filter_value_0":   {"u/example-poster"},
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

	// The list toolbar offers the create link pre-filled with the active filter.
	body := doGet(h, "/unread?"+url.Values{"f": {"tag:show:u/example-poster"}}.Encode(), cookie).Body.String()
	if !strings.Contains(body, "/views/new?") || !strings.Contains(body, "save%20this%20list%20as%20a%20view") && !strings.Contains(body, "save this list as a view") {
		t.Fatalf("unread list should offer save-as-view: %s", body)
	}

	// The create page carries the filter rows and the author picker.
	formPage := doGet(h, "/views/new?scope_author=0&view=unread&sort=newest&f="+url.QueryEscape("tag:show:u/example-poster"), cookie).Body.String()
	if !strings.Contains(formPage, `name="title"`) || !strings.Contains(formPage, `name="author_id"`) || !strings.Contains(formPage, `name="filter_value_empty"`) && !strings.Contains(formPage, `name="filter_value_0"`) {
		t.Fatalf("new view form is missing fields: %s", formPage)
	}

	// The index lists it under its assigned author.
	body = doGet(h, "/views", cookie).Body.String()
	if !strings.Contains(body, "Example posts") || !strings.Contains(body, "Example Poster") {
		t.Fatalf("views index should list the saved view under its author: %s", body)
	}

	// The bare view URL redirects to its canonical query.
	bare := doGet(h, "/views/"+id, cookie)
	if bare.Code != http.StatusFound {
		t.Fatalf("bare view should redirect: %d", bare.Code)
	}
	canonical := bare.Header().Get("Location")
	if !strings.Contains(canonical, "view=unread") || !strings.Contains(canonical, "f=") {
		t.Fatalf("canonical view URL missing state: %q", canonical)
	}

	// The canonical page shows the matching post and no other.
	body = doGet(h, canonical, cookie).Body.String()
	if !strings.Contains(body, "Go post") || strings.Contains(body, "Other post") {
		t.Fatalf("view should show only the filtered post: %s", body)
	}
	if !strings.Contains(body, "1 matching") {
		t.Fatalf("view should report the matching count: %s", body)
	}

	// The author page carries a views section.
	body = doGet(h, "/authors/"+itoa(author.ID), cookie).Body.String()
	if !strings.Contains(body, "views (1)") || !strings.Contains(body, "Example posts") {
		t.Fatalf("author page should list the assigned view: %s", body)
	}

	// Edit renames and unassigns the view; the scope stays fixed.
	edit := url.Values{
		"title":            {"Renamed view"},
		"author_id":        {"0"},
		"scope_author":     {"0"},
		"scope_collection": {"0"},
		"view":             {"read"},
		"sort":             {"oldest"},
		"filter_row":       {"0"},
		"filter_action":    {"show"},
		"filter_kind":      {"tag"},
		"filter_combine":   {"all"},
		"filter_value_0":   {"u/example-poster"},
	}
	if rr := doForm(h, http.MethodPost, "/views/"+id+"/edit", edit, cookie); rr.Code != http.StatusSeeOther {
		t.Fatalf("edit view: %d %s", rr.Code, rr.Body.String())
	}
	u, _ := s.store.Users.ByUsername("alice")
	views, _ := s.store.SavedViews.List(u.ID)
	if len(views) != 1 {
		t.Fatalf("expected one saved view, got %d", len(views))
	}
	if views[0].Title != "Renamed view" || views[0].AuthorID != 0 || views[0].BaseView != "read" {
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

func TestSavedViewScopeDropsForeignFeedFilter(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, author, _ := savedViewFixture(t, s)

	// A feed owned by a different author cannot be used as a filter value in an
	// author-scoped view.
	other, _ := s.store.Authors.Create(u.ID, "Other", "", "")
	otherFeed, _ := s.store.Feeds.Create(u.ID, other.ID, "Other feed", "https://other.dev/feed.xml", "", "", 900)

	form := url.Values{
		"title":          {"Scoped"},
		"author_id":      {itoa(author.ID)},
		"scope_author":   {itoa(author.ID)},
		"view":           {"unread"},
		"sort":           {"newest"},
		"filter_row":     {"0"},
		"filter_action":  {"show"},
		"filter_kind":    {"feed"},
		"filter_combine": {"all"},
		"filter_value_0": {itoa(otherFeed.ID)},
	}
	if rr := doForm(h, http.MethodPost, "/views", form, cookie); rr.Code != http.StatusSeeOther {
		t.Fatalf("create scoped view: %d %s", rr.Code, rr.Body.String())
	}
	views, _ := s.store.SavedViews.List(u.ID)
	if len(views) != 1 {
		t.Fatalf("expected one view, got %d", len(views))
	}
	if len(views[0].Filters) != 0 {
		t.Fatalf("foreign feed filter should be dropped, got %v", views[0].Filters)
	}
}

func TestSavedViewReadAllHonorsFilters(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, author, f := savedViewFixture(t, s)

	form := url.Values{
		"title":          {"Go"},
		"author_id":      {itoa(author.ID)},
		"scope_author":   {"0"},
		"view":           {"unread"},
		"sort":           {"newest"},
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

	// Mark-all carries the active filter, so only the matching post is read.
	if rr := doForm(h, http.MethodPost, "/views/"+id+"/read-all?view=unread&sort=newest&f="+url.QueryEscape("tag:show:u/example-poster"), nil, cookie); rr.Code != http.StatusNoContent {
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
