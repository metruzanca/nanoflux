package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

// Saved views are private named filter recipes. They reuse the transient list
// filter machinery (filters.go) and the scoped item list rendering; the only
// new state is the recipe row itself.

const maxSavedViewTitle = 120

// savedViewRow is one view in the index or on an author card, with its resolved
// display fields and matching-item count.
type savedViewRow struct {
	ID         int64
	Title      string
	AuthorID   int64
	AuthorName string
	BaseView   string
	Sort       itemSort
	Filters    listFilters
	Count      int
	CreatedAt  string
}

// savedViewGroup is one assigned-author block on the views index.
type savedViewGroup struct {
	AuthorID   int64
	AuthorName string
	Rows       []savedViewRow
}

type savedViewsPageData struct {
	Groups []savedViewGroup
	Total  int
}

// savedViewForm is the create/edit page's data.
type savedViewForm struct {
	ID            int64
	Title         string
	BaseView      string
	Sort          itemSort
	Filters       listFilters
	AuthorID      int64
	AuthorOptions []comboItem
	TagOptions    []comboItem
	FeedOptions   []comboItem
	Error         string
}

type savedViewPageData struct {
	Row     savedViewRow
	Scoped  scopedItemsData
	MarkAll markAllReadData
}

// normalizeSavedViewBase maps a base tab onto the supported set.
func normalizeSavedViewBase(v string) string {
	switch v {
	case "read", "favorites", "bookmarks":
		return v
	case "unread":
		return "unread"
	}
	return "unread"
}

// savedViewAuthorItems is the assignment picker: an explicit "unassigned"
// option plus every author.
func savedViewAuthorItems(authors []store.Author) []comboItem {
	items := make([]comboItem, 0, len(authors)+1)
	items = append(items, comboItem{Value: "0", Label: "unassigned"})
	for _, a := range authors {
		items = append(items, comboItem{Value: strconv.FormatInt(a.ID, 10), Label: a.Name})
	}
	return items
}

// savedViewAuthorValue renders the picker's current value ("0" for none).
func savedViewAuthorValue(id int64) string {
	if id <= 0 {
		return "0"
	}
	return strconv.FormatInt(id, 10)
}

// savedViewsIndex groups every saved view by its assigned author, then shows
// unassigned views last.
func (s *Server) savedViewsIndex(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	views, err := s.store.SavedViews.List(u.ID)
	if err != nil {
		log.Error("list saved views", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	rows := make([]savedViewRow, 0, len(views))
	for _, v := range views {
		rows = append(rows, s.savedViewRow(u.ID, v, u.Timezone))
	}
	web.Render(w, r, basePage("views", u, savedViewsPage(savedViewsPageData{
		Groups: groupSavedViews(rows), Total: len(rows),
	})))
}

// savedViewsGroupRows keeps assigned views in author order and unassigned last.
func groupSavedViews(rows []savedViewRow) []savedViewGroup {
	var groups []savedViewGroup
	index := map[int64]int{}
	for _, row := range rows {
		i, ok := index[row.AuthorID]
		if !ok {
			index[row.AuthorID] = len(groups)
			groups = append(groups, savedViewGroup{AuthorID: row.AuthorID, AuthorName: row.AuthorName})
			i = len(groups) - 1
		}
		groups[i].Rows = append(groups[i].Rows, row)
	}
	// Push the unassigned group to the end.
	for i, g := range groups {
		if g.AuthorID != 0 {
			continue
		}
		groups = append(groups[:i], groups[i+1:]...)
		groups = append(groups, g)
		break
	}
	return groups
}

// savedViewRow resolves one view's display fields and matching count.
func (s *Server) savedViewRow(userID int64, v store.SavedView, tz string) savedViewRow {
	filters := s.savedViewFilters(userID, filtersFromParams(v.Filters))
	base := normalizeSavedViewBase(v.BaseView)
	count, _ := s.savedViewCount(userID, base, filters, tz)
	row := savedViewRow{
		ID: v.ID, Title: v.Title, AuthorID: v.AuthorID, BaseView: base,
		Sort: itemSortFromParam(v.Sort), Filters: filters,
		Count: count, CreatedAt: v.CreatedAt,
	}
	if v.AuthorID > 0 {
		if a, err := s.store.Authors.ByID(userID, v.AuthorID); err == nil {
			row.AuthorName = a.Name
		}
	}
	return row
}

// savedViewCount counts the matching items for a view's base tab and filters.
func (s *Server) savedViewCount(userID int64, view string, filters listFilters, tz string) (int, error) {
	f := scopedFilter(view, sortNewest, 0, 0, 0, filters, tz)
	f.Limit = 0
	return s.store.Items.CountFiltered(userID, f)
}

// savedViewFilters validates a view's filter rows against the user's feeds:
// feed ids must belong to the user and labels are resolved from their titles.
// Stale rows are dropped rather than failing the whole view.
func (s *Server) savedViewFilters(userID int64, filters listFilters) listFilters {
	if len(filters) == 0 {
		return nil
	}
	titles := s.savedViewFeedTitles(userID)
	out := make(listFilters, 0, len(filters))
	for _, f := range filters {
		if f.isFeed() {
			values := make([]string, 0, len(f.Values))
			for _, value := range f.Values {
				id, err := strconv.ParseInt(value, 10, 64)
				if err != nil {
					continue
				}
				if _, ok := titles[id]; ok {
					values = append(values, value)
				}
			}
			if len(values) == 0 {
				continue
			}
			f.Values = values
		}
		out = append(out, f)
	}
	return filterFeedTitles(normalizeFilters(out), titles)
}

// savedViewFeedTitles maps the feeds a view may filter on.
func (s *Server) savedViewFeedTitles(userID int64) map[int64]string {
	feeds, _ := s.store.Feeds.List(userID)
	titles := make(map[int64]string, len(feeds))
	for _, f := range feeds {
		titles[f.ID] = f.Title
	}
	return titles
}

// savedViewAssignedAuthor validates the assignment picker.
func (s *Server) savedViewAssignedAuthor(userID int64, raw string) (int64, bool) {
	id, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if id <= 0 {
		return 0, true
	}
	if _, err := s.store.Authors.ByID(userID, id); err != nil {
		return 0, false
	}
	return id, true
}

// savedViewFormData builds the create/edit form's option lists. A view always
// spans all of the user's feeds.
func (s *Server) savedViewFormData(userID int64, v savedViewForm) savedViewForm {
	authors, _ := s.store.Authors.List(userID)
	v.AuthorOptions = savedViewAuthorItems(authors)
	cats, _ := s.store.Items.ListCategoriesFilter(userID, store.ItemFilter{})
	v.TagOptions = tagItems(cats)
	fs, _ := s.store.Feeds.List(userID)
	feeds := make([]comboItem, 0, len(fs))
	for _, f := range fs {
		feeds = append(feeds, comboItem{Value: strconv.FormatInt(f.ID, 10), Label: f.Title})
	}
	v.FeedOptions = feeds
	return v
}

// savedViewNew renders the create form prefilled from the list the user saved
// from (?view= or ?tab=, ?sort=, ?author_id=, and the filter fields).
func (s *Server) savedViewNew(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	authorID, ok := s.savedViewAssignedAuthor(u.ID, r.URL.Query().Get("author_id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	filters := s.savedViewFilters(u.ID, filtersFromRequest(r))
	baseView := r.URL.Query().Get("tab")
	if baseView == "" {
		baseView = r.URL.Query().Get("view")
	}
	form := savedViewForm{
		Title:    "",
		BaseView: normalizeSavedViewBase(baseView),
		Sort:     itemSortOf(r),
		Filters:  filters,
		AuthorID: authorID,
	}
	web.Render(w, r, basePage("new view", u, savedViewFormPage(u, s.savedViewFormData(u.ID, form))))
}

// savedViewCreate stores the submitted view.
func (s *Server) savedViewCreate(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	form, ok := s.savedViewFormFromRequest(u.ID, r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if n, err := s.store.SavedViews.Count(u.ID); err != nil {
		log.Error("count saved views", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if n >= store.MaxSavedViewsPerUser {
		form.Error = "you have reached the maximum number of saved views"
		s.renderSavedViewForm(w, r, u, form, http.StatusBadRequest)
		return
	}
	if form.Error != "" {
		s.renderSavedViewForm(w, r, u, form, http.StatusBadRequest)
		return
	}
	created, err := s.store.SavedViews.Create(u.ID, store.SavedView{
		AuthorID: form.AuthorID, Title: form.Title, BaseView: form.BaseView,
		Sort: form.Sort.param(), Filters: form.Filters.params(),
	})
	if err != nil {
		log.Error("create saved view", "err", err)
		form.Error = "could not save this view"
		s.renderSavedViewForm(w, r, u, form, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/views/"+strconv.FormatInt(created.ID, 10), http.StatusSeeOther)
}

// savedViewEdit renders the edit form.
func (s *Server) savedViewEdit(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v, err := s.store.SavedViews.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	form := savedViewForm{
		ID: v.ID, Title: v.Title, BaseView: normalizeSavedViewBase(v.BaseView),
		Sort:     itemSortFromParam(v.Sort),
		Filters:  s.savedViewFilters(u.ID, filtersFromParams(v.Filters)),
		AuthorID: v.AuthorID,
	}
	web.Render(w, r, basePage("edit view", u, savedViewFormPage(u, s.savedViewFormData(u.ID, form))))
}

// savedViewUpdate persists the edit form.
func (s *Server) savedViewUpdate(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := s.store.SavedViews.ByID(u.ID, id); err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	form, ok := s.savedViewFormFromRequest(u.ID, r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	form.ID = id
	if form.Error != "" {
		s.renderSavedViewForm(w, r, u, form, http.StatusBadRequest)
		return
	}
	form.Filters = s.savedViewFilters(u.ID, form.Filters)
	if err := s.store.SavedViews.Update(u.ID, id, store.SavedView{
		AuthorID: form.AuthorID, Title: form.Title, BaseView: form.BaseView,
		Sort: form.Sort.param(), Filters: form.Filters.params(),
	}); err != nil {
		log.Error("update saved view", "err", err)
		form.Error = "could not save this view"
		s.renderSavedViewForm(w, r, u, form, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/views/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// renderSavedViewForm re-renders the create/edit page with a validation or
// persistence error.
func (s *Server) renderSavedViewForm(w http.ResponseWriter, r *http.Request, u store.User, form savedViewForm, status int) {
	title := "new view"
	if form.ID > 0 {
		title = "edit view"
	}
	w.WriteHeader(status)
	web.Render(w, r, basePage(title, u, savedViewFormPage(u, s.savedViewFormData(u.ID, form))))
}

// savedViewFormFromRequest reads and validates the shared create/edit fields.
func (s *Server) savedViewFormFromRequest(userID int64, r *http.Request) (savedViewForm, bool) {
	form := savedViewForm{
		Title:    strings.TrimSpace(r.FormValue("title")),
		BaseView: normalizeSavedViewBase(r.FormValue("view")),
		Sort:     itemSortFromParam(r.FormValue("sort")),
		Filters:  filtersFromValues(r.Form),
	}
	var ok bool
	form.Filters = s.savedViewFilters(userID, form.Filters)
	form.AuthorID, ok = s.savedViewAssignedAuthor(userID, r.FormValue("author_id"))
	if !ok {
		return form, false
	}
	if form.Title == "" {
		form.Error = "title is required"
	} else if len(form.Title) > maxSavedViewTitle {
		form.Error = "title is too long"
	} else if len(form.Filters) == 0 {
		form.Error = "add at least one filter"
	}
	return form, true
}

// savedViewDelete removes a view and returns to the index.
func (s *Server) savedViewDelete(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.SavedViews.Delete(u.ID, id); err != nil && err != store.ErrNotFound {
		log.Error("delete saved view", "id", id, "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/views", http.StatusSeeOther)
}

// savedView shows one view. A bare /views/{id} redirects to the canonical
// query, so the normal list links, filter dialog and "clear all" all operate on
// explicit params rather than falling back to the stored recipe.
func (s *Server) savedView(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v, err := s.store.SavedViews.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// An assigned view is part of its author's page now; the standalone page is
	// only for unassigned views.
	if v.AuthorID > 0 {
		http.Redirect(w, r, "/authors/"+strconv.FormatInt(v.AuthorID, 10)+"?view="+strconv.FormatInt(v.ID, 10), http.StatusSeeOther)
		return
	}
	if !hasSavedViewListParams(r) {
		http.Redirect(w, r, s.savedViewCanonicalURL(u.ID, v), http.StatusFound)
		return
	}
	web.Render(w, r, basePage(v.Title, u, savedViewPage(u, s.savedViewPageData(u, v, r))))
}

// savedViewItems serves the view's load-more fragment.
func (s *Server) savedViewItems(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v, err := s.store.SavedViews.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	view := savedViewTab(r, v)
	sort := savedViewSort(r, v)
	filters := s.savedViewActiveFilters(u.ID, v, r)
	if cursor := cursorID(r, sort); cursor > 0 {
		items, more, err := s.store.Items.ListPage(u.ID,
			scopedFilter(view, sort, cursor, 0, 0, filters, u.Timezone))
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		base := s.savedViewItemsBase(v, view, sort, filters)
		web.Render(w, r, ItemsPage(withTZ(u.Timezone, dedupItems(items)),
			pageCursorFor(base, items, more, sort, int(cursor)), false))
		return
	}
	web.Render(w, r, ScopedItems(s.savedViewScopedData(u, v, view, sort, filters)))
}

// savedViewReadAll marks the view's matching unread items read.
func (s *Server) savedViewReadAll(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v, err := s.store.SavedViews.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	filters := s.savedViewActiveFilters(u.ID, v, r)
	tagGroups, feedGroups, _ := filters.storeGroups()
	if _, err := s.store.Items.MarkAllReadFiltered(u.ID, store.ItemFilter{
		TagGroups: tagGroups, FeedGroups: feedGroups,
		TimeGroups: filters.timeGroups(u.Timezone),
	}); err != nil {
		log.Error("mark saved view read", "view_id", id, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) savedViewPageData(u store.User, v store.SavedView, r *http.Request) savedViewPageData {
	view := savedViewTab(r, v)
	sort := savedViewSort(r, v)
	filters := s.savedViewActiveFilters(u.ID, v, r)
	unread, _ := s.savedViewCount(u.ID, "unread", filters, u.Timezone)
	markAll := markAllReadData{
		Action: savedViewFiltersURL("/views/"+strconv.FormatInt(v.ID, 10)+"/read-all", filters),
		Unread: unread,
	}
	return savedViewPageData{
		Row:     s.savedViewRow(u.ID, v, u.Timezone),
		Scoped:  s.savedViewScopedData(u, v, view, sort, filters),
		MarkAll: markAll,
	}
}

// savedViewTab reads ?view=, falling back to the view's stored base tab.
func savedViewTab(r *http.Request, v store.SavedView) string {
	if raw := r.URL.Query().Get("view"); raw != "" {
		switch raw {
		case "read", "favorites", "bookmarks", "unread":
			return raw
		}
	}
	return normalizeSavedViewBase(v.BaseView)
}

// savedViewSort reads ?sort=, falling back to the stored sort.
func savedViewSort(r *http.Request, v store.SavedView) itemSort {
	if raw := r.URL.Query().Get("sort"); raw != "" {
		return itemSortFromParam(raw)
	}
	return itemSortFromParam(v.Sort)
}

// savedViewActiveFilters returns explicit request filters when the list was
// rendered with them, otherwise the stored recipe. A filter dialog submission
// always carries filter_row, so clearing every row is distinguishable from a
// bare link.
func (s *Server) savedViewActiveFilters(userID int64, v store.SavedView, r *http.Request) listFilters {
	q := r.URL.Query()
	if q.Has("filter_row") || q.Has("f") || q.Has("tags") || q.Has("feed") {
		return s.savedViewFilters(userID, filtersFromRequest(r))
	}
	if q.Has("view") || q.Has("sort") {
		// An explicit tab/sort link with no filter params means "no filters".
		return nil
	}
	return s.savedViewFilters(userID, filtersFromParams(v.Filters))
}

// hasSavedViewListParams reports whether the request already carries a list
// state, so a bare view link can be redirected once to its canonical query.
func hasSavedViewListParams(r *http.Request) bool {
	q := r.URL.Query()
	for _, key := range []string{"view", "sort", "f", "filter_row", "tags", "feed", "before", "after", "offset"} {
		if q.Has(key) {
			return true
		}
	}
	return false
}

// savedViewCanonicalURL is the bare view link's redirect target.
func (s *Server) savedViewCanonicalURL(userID int64, v store.SavedView) string {
	filters := s.savedViewFilters(userID, filtersFromParams(v.Filters))
	base := "/views/" + strconv.FormatInt(v.ID, 10)
	return filterPageURL(base, normalizeSavedViewBase(v.BaseView), itemSortFromParam(v.Sort), filters)
}

// savedViewItemsBase builds the load-more URL for the active view state.
func (s *Server) savedViewItemsBase(v store.SavedView, view string, sort itemSort, filters listFilters) string {
	base := scopedViewQuery("/views/"+strconv.FormatInt(v.ID, 10)+"/items", view) + "&sort=" + sort.param()
	return filters.appendFilters(base)
}

// savedViewScopedData renders the view's scoped item list.
func (s *Server) savedViewScopedData(u store.User, v store.SavedView, view string, sort itemSort, filters listFilters) scopedItemsData {
	id := v.ID
	base := "/views/" + strconv.FormatInt(id, 10)
	items, more, _ := s.store.Items.ListPage(u.ID,
		scopedFilter(view, sort, 0, 0, 0, filters, u.Timezone))
	unread, _ := s.savedViewCount(u.ID, "unread", filters, u.Timezone)
	read, _ := s.savedViewCount(u.ID, "read", filters, u.Timezone)
	favs, _ := s.savedViewCount(u.ID, "favorites", filters, u.Timezone)
	filtered := 0
	if len(filters) > 0 {
		filtered, _ = s.savedViewCount(u.ID, view, filters, u.Timezone)
	}
	itemsBase := s.savedViewItemsBase(v, view, sort, filters)
	cats, _ := s.store.Items.ListCategoriesFilter(u.ID, store.ItemFilter{})
	feedOpts := s.savedViewFeedOptions(u.ID)
	return scopedItemsData{
		Path: base, ItemsPath: base + "/items", View: view, Sort: sort,
		UnreadCount: unread, ReadCount: read, Items: withTZ(u.Timezone, dedupItems(items)),
		More:   pageCursorFor(itemsBase, items, more, sort, 0),
		Mode:   s.store.ViewPrefs.Mode(u.ID, base),
		FavTab: favs > 0 || view == "favorites", FavCount: favs,
		Filters: filters, FilteredCount: filtered, TagOptions: tagItems(cats),
		FeedOptions: feedOpts,
	}
}

// savedViewFeedOptions maps the feeds the view's filter modal may offer.
func (s *Server) savedViewFeedOptions(userID int64) []comboItem {
	fs, _ := s.store.Feeds.ListWithUnread(userID)
	out := make([]comboItem, 0, len(fs))
	for _, f := range fs {
		out = append(out, comboItem{Value: strconv.FormatInt(f.Feed.ID, 10), Label: f.Feed.Title})
	}
	return out
}

// savedViewsForAuthor returns the views assigned to an author, for its page.
func (s *Server) savedViewsForAuthor(userID, authorID int64, tz string) []savedViewRow {
	views, _ := s.store.SavedViews.ListByAuthor(userID, authorID)
	rows := make([]savedViewRow, 0, len(views))
	for _, v := range views {
		if len(s.savedViewFilters(userID, filtersFromParams(v.Filters))) == 0 {
			continue
		}
		rows = append(rows, s.savedViewRow(userID, v, tz))
	}
	return rows
}

// storeViewFilter converts a saved view into the store's query predicate. An
// unfiltered view is skipped by callers: without a filter it would match every
// post, which is not a useful author source.
func (s *Server) storeViewFilter(userID int64, v store.SavedView, tz string) store.ViewFilter {
	filters := s.savedViewFilters(userID, filtersFromParams(v.Filters))
	tagGroups, feedGroups, _ := filters.storeGroups()
	return store.ViewFilter{
		TagGroups: tagGroups, FeedGroups: feedGroups, TimeGroups: filters.timeGroups(tz),
	}
}

// savedViewsByAuthor returns the usable view predicates and the total assigned
// view count per author, from one read of the user's views.
func (s *Server) savedViewsByAuthor(userID int64, tz string) (map[int64][]store.ViewFilter, map[int64]int) {
	views, _ := s.store.SavedViews.List(userID)
	filters := make(map[int64][]store.ViewFilter)
	counts := make(map[int64]int)
	for _, v := range views {
		if v.AuthorID <= 0 {
			continue
		}
		if len(s.savedViewFilters(userID, filtersFromParams(v.Filters))) == 0 {
			continue
		}
		counts[v.AuthorID]++
		filters[v.AuthorID] = append(filters[v.AuthorID], s.storeViewFilter(userID, v, tz))
	}
	return filters, counts
}

// authorStoreViewFilters returns the usable view predicates assigned to an
// author. Unfiltered views are omitted, so an old/blank recipe cannot pull the
// whole stream into the author by accident.
func (s *Server) authorStoreViewFilters(userID, authorID int64, tz string) []store.ViewFilter {
	views, _ := s.store.SavedViews.ListByAuthor(userID, authorID)
	out := make([]store.ViewFilter, 0, len(views))
	for _, v := range views {
		if len(s.savedViewFilters(userID, filtersFromParams(v.Filters))) == 0 {
			continue
		}
		out = append(out, s.storeViewFilter(userID, v, tz))
	}
	return out
}

// authorStoreViewFilter returns one assigned, filtered view as a predicate.
func (s *Server) authorStoreViewFilter(userID, authorID, viewID int64, tz string) (store.ViewFilter, bool) {
	v, err := s.store.SavedViews.ByID(userID, viewID)
	if err != nil || v.AuthorID != authorID {
		return store.ViewFilter{}, false
	}
	filters := s.savedViewFilters(userID, filtersFromParams(v.Filters))
	if len(filters) == 0 {
		return store.ViewFilter{}, false
	}
	return s.storeViewFilter(userID, v, tz), true
}

// savedViewFiltersURL encodes the active filters onto a POST action URL (the
// mark-all handler re-reads them from the query).
func savedViewFiltersURL(base string, filters listFilters) string {
	return filterActionURL(base, filters)
}
