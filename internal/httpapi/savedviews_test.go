package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
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

	// The index lists it under its assigned author, with compact stats only.
	body = doGet(h, "/views", cookie).Body.String()
	if !strings.Contains(body, "Example posts") || !strings.Contains(body, "Example Poster") {
		t.Fatalf("views index should list the saved view under its author: %s", body)
	}
	if !strings.Contains(body, "assigned to") || !strings.Contains(body, "1 unread") {
		t.Fatalf("views index card should show assignment and unread stats: %s", body)
	}
	if strings.Contains(body, "u/example-poster") {
		t.Fatalf("views index card should not render its filter chips: %s", body)
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
	// The selected view is shown as a removable chip carrying its title; the
	// chip drops ?view=<id> and keeps the tab/sort.
	if !strings.Contains(body, `title="remove view filter"`) || !strings.Contains(body, "Example posts <span") {
		t.Fatalf("view-scoped author page missing the view chip: %s", body)
	}
	if !strings.Contains(body, `href="/authors/`+itoa(author.ID)+`?tab=unread&amp;sort=newest"`) {
		t.Fatalf("view chip should link back to the un-scoped author list: %s", body)
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

	viewPage := doGet(h, "/views/"+id+"?tab=unread&sort=newest", cookie).Body.String()
	if strings.Contains(viewPage, `id="tag-filter-dialog"`) {
		t.Fatalf("the saved view page should not expose a transient filter dialog: %s", viewPage)
	}
	if !strings.Contains(viewPage, "recipe:") || !strings.Contains(viewPage, "show r/golang") {
		t.Fatalf("the saved view page should show its fixed recipe: %s", viewPage)
	}
	if !strings.Contains(viewPage, "?tab=read") || strings.Contains(viewPage, "?view=unread") {
		t.Fatalf("saved view tab links should use ?tab=: %s", viewPage)
	}
}

// TestCollectionViewsScopeAndCounts covers the union between a collection's
// member feeds and its member views: view-matched posts join the item stream and
// counts, and removing the view drops them again.
func TestCollectionViewsScopeAndCounts(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, author, f := savedViewFixture(t, s)

	if _, err := s.store.Items.Upsert(f.ID, store.Item{
		GUID: "other", Title: "Other post", Link: "https://x/other",
		Categories: []string{"u/someone-else"}, FetchedAt: db.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	other, _ := s.store.Feeds.Create(u.ID, author.ID, "Other", "https://other.example/feed.xml", "", "", 900)
	if _, err := s.store.Items.Upsert(other.ID, store.Item{
		GUID: "rust", Title: "Rust post", Link: "https://other.example/rust",
		Categories: []string{"r/rust"}, FetchedAt: db.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Items.Upsert(other.ID, store.Item{
		GUID: "misc", Title: "Misc post", Link: "https://other.example/misc",
		FetchedAt: db.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	c, _ := s.store.Collections.Create(u.ID, "Dev")
	if err := s.store.Collections.AddFeed(u.ID, c.ID, f.ID); err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"title":          {"Rust view"},
		"author_id":      {"0"},
		"collections":    {itoa(c.ID)},
		"filter_row":     {"0"},
		"filter_action":  {"show"},
		"filter_kind":    {"tag"},
		"filter_combine": {"all"},
		"filter_value_0": {"r/rust"},
	}
	rr := doForm(h, http.MethodPost, "/views", form, cookie)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("create view: %d %s", rr.Code, rr.Body.String())
	}
	viewID := strings.TrimPrefix(rr.Header().Get("Location"), "/views/")
	viewIDNum, err := strconv.ParseInt(viewID, 10, 64)
	if err != nil {
		t.Fatalf("view id %q: %v", viewID, err)
	}

	ids, err := s.store.SavedViews.CollectionIDs(u.ID, viewIDNum)
	if err != nil || len(ids) != 1 || ids[0] != c.ID {
		t.Fatalf("view collection membership = %v, %v", ids, err)
	}

	base := "/collections/" + itoa(c.ID)
	body := doGet(h, base, cookie).Body.String()
	if !strings.Contains(body, "Rust post") || !strings.Contains(body, "Go post") {
		t.Fatalf("collection should include feed and view items: %s", body)
	}
	if strings.Contains(body, "Misc post") {
		t.Fatalf("non-matching view item leaked into the collection: %s", body)
	}
	// The post that only the view brings in names that view and links to it.
	if !strings.Contains(body, "from view") || !strings.Contains(body, `href="/views/`+viewID+`"`) {
		t.Fatalf("view-sourced item should link its view: %s", body)
	}
	if !strings.Contains(body, "sources (2)") {
		t.Fatalf("collection sources count should include the view: %s", body)
	}

	body = doGet(h, base+"?tab=feeds", cookie).Body.String()
	if !strings.Contains(body, "Rust view") || !strings.Contains(body, `id="collection-view-`+viewID+`"`) {
		t.Fatalf("sources tab should list the view card: %s", body)
	}

	// The index card's unread count is the union, not just the member feed.
	body = doGet(h, "/collections", cookie).Body.String()
	if !strings.Contains(body, "3 unread") {
		t.Fatalf("collection index should count feed and view unread items: %s", body)
	}

	if rr := doForm(h, http.MethodPost, base+"/remove-view/"+viewID, nil, cookie); rr.Code != http.StatusNoContent {
		t.Fatalf("remove view: %d %s", rr.Code, rr.Body.String())
	}
	body = doGet(h, base, cookie).Body.String()
	if strings.Contains(body, "Rust post") {
		t.Fatalf("removed view items should leave the collection: %s", body)
	}
}

// TestCollectionViewSourceOnlyWhenViewSoleSource checks that the "from view"
// line appears only for items the view is the sole reason for: an item already
// in a member feed does not claim the view even when it matches.
func TestCollectionViewSourceOnlyWhenViewSoleSource(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, author, member := savedViewFixture(t, s)

	if _, err := s.store.Items.Upsert(member.ID, store.Item{
		GUID: "member-rust", Title: "Member Rust post", Link: "https://x/member-rust",
		Categories: []string{"r/rust"}, FetchedAt: db.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	outside, _ := s.store.Feeds.Create(u.ID, author.ID, "Outside", "https://outside.example/feed.xml", "", "", 900)
	if _, err := s.store.Items.Upsert(outside.ID, store.Item{
		GUID: "outside-rust", Title: "Outside Rust post", Link: "https://outside.example/rust",
		Categories: []string{"r/rust"}, FetchedAt: db.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	outsideID, _ := s.store.Items.ByFeedIdentity(outside.ID, "outside-rust")

	c, _ := s.store.Collections.Create(u.ID, "Dev")
	if err := s.store.Collections.AddFeed(u.ID, c.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	view, err := s.store.SavedViews.Create(u.ID, store.SavedView{
		Title: "Rust", Filters: []string{"tag:show:r/rust"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.SavedViews.SetCollections(u.ID, view.ID, []int64{c.ID}); err != nil {
		t.Fatal(err)
	}

	body := doGet(h, "/collections/"+itoa(c.ID), cookie).Body.String()
	if !strings.Contains(body, "Member Rust post") || !strings.Contains(body, "Outside Rust post") {
		t.Fatalf("collection should show both rust posts: %s", body)
	}
	if got := strings.Count(body, "from view"); got != 1 {
		t.Fatalf("only the view-only item should claim the view, got %d: %s", got, body)
	}
	if !strings.Contains(body, `href="/views/`+itoa(view.ID)+`"`) ||
		!strings.Contains(body, "from view") {
		t.Fatalf("view-only item should link its view: %s", body)
	}
	if !strings.Contains(body, `id="item-`+itoa(outsideID)+`"`) {
		t.Fatalf("outside item should be the linked view row: %s", body)
	}

	modal := doGet(h, "/items/"+itoa(outsideID)+"/view?collection="+itoa(c.ID), cookie).Body.String()
	if !strings.Contains(modal, "from view") || !strings.Contains(modal, `href="/views/`+itoa(view.ID)+`"`) {
		t.Fatalf("item modal should link the view when opened from the collection: %s", modal)
	}
	plainModal := doGet(h, "/items/"+itoa(outsideID)+"/view", cookie).Body.String()
	if strings.Contains(plainModal, "from view") {
		t.Fatalf("item modal outside a collection should not claim a view source: %s", plainModal)
	}

	// A row toggle carries the collection context and keeps the link.
	rr := doForm(h, "POST", "/items/"+itoa(outsideID)+"/favorite", url.Values{"collection": {itoa(c.ID)}}, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "from view") {
		t.Fatalf("toggled row should keep its view link: %d %s", rr.Code, rr.Body.String())
	}
}

// TestAuthorEditViewPanel covers the inline, auto-saving view editor on the
// author edit page: unprefixed fields, a form in the lazy fragment, and a
// save that re-renders the panel with its new title.
func TestAuthorEditViewPanel(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)
	u, author, _ := savedViewFixture(t, s)
	c, _ := s.store.Collections.Create(u.ID, "Dev")

	created, err := s.store.SavedViews.Create(u.ID, store.SavedView{
		AuthorID: author.ID, Title: "Inline view",
		Filters: []string{"tag:show:u/example-poster"},
	})
	if err != nil {
		t.Fatal(err)
	}
	page := doGet(h, "/authors/"+itoa(author.ID)+"/edit?view="+itoa(created.ID), cookie).Body.String()
	for _, want := range []string{
		`id="view-acc-` + itoa(created.ID) + `"`,
		`hx-post="/views/` + itoa(created.ID) + `/edit"`,
		`name="title"`,
		`name="collections"`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("author edit view panel missing %q: %s", want, page)
		}
	}
	if strings.Contains(page, `name="view_ids"`) {
		t.Fatalf("view panels should not carry the old combined-form marker: %s", page)
	}

	frag := doGet(h, "/views/"+itoa(created.ID)+"/edit-fragment", cookie).Body.String()
	if !strings.Contains(frag, `<form`) || !strings.Contains(frag, `name="title"`) || !strings.Contains(frag, `data-filter-row-token`) {
		t.Fatalf("view edit fragment should be an unprefixed auto-saving form: %s", frag)
	}

	form := url.Values{
		"title":          {"Inline renamed"},
		"author_id":      {itoa(author.ID)},
		"collections":    {itoa(c.ID)},
		"filter_row":     {"0"},
		"filter_action":  {"show"},
		"filter_kind":    {"tag"},
		"filter_combine": {"all"},
		"filter_value_0": {"u/example-poster"},
	}
	rr := doHtmxForm(h, http.MethodPost, "/views/"+itoa(created.ID)+"/edit", form, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Inline renamed") {
		t.Fatalf("auto-save inline view: %d %s", rr.Code, rr.Body.String())
	}
	got, err := s.store.SavedViews.ByID(u.ID, created.ID)
	if err != nil || got.Title != "Inline renamed" || got.AuthorID != author.ID {
		t.Fatalf("inline view not saved: %+v %v", got, err)
	}
	ids, _ := s.store.SavedViews.CollectionIDs(u.ID, created.ID)
	if len(ids) != 1 || ids[0] != c.ID {
		t.Fatalf("inline view collections = %v", ids)
	}

	// A validation failure re-renders the panel with its error, not a redirect.
	bad := url.Values{
		"title":          {""},
		"author_id":      {itoa(author.ID)},
		"filter_row":     {"0"},
		"filter_action":  {"show"},
		"filter_kind":    {"tag"},
		"filter_combine": {"all"},
		"filter_value_0": {"u/example-poster"},
	}
	rr = doHtmxForm(h, http.MethodPost, "/views/"+itoa(created.ID)+"/edit", bad, cookie)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "title is required") {
		t.Fatalf("invalid view auto-save should keep the panel: %d %s", rr.Code, rr.Body.String())
	}
}
