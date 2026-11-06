package httpapi

import (
	"net/http"
	"net/url"
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
	ID          int64
	Title       string
	AuthorID    int64
	AuthorName  string
	Filters     listFilters
	Collections []store.Collection
	Count       int
	CreatedAt   string
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
	ID                int64
	Title             string
	Filters           listFilters
	AuthorID          int64
	CollectionIDs     []int64
	AuthorOptions     []comboItem
	CollectionOptions []comboItem
	TagOptions        []comboItem
	FeedOptions       []comboItem
	Error             string
}

type savedViewPageData struct {
	Row     savedViewRow
	Scoped  scopedItemsData
	MarkAll markAllReadData
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
	rows := s.savedViewRows(u.ID, views, u.Timezone)
	web.Render(w, r, basePage("views", u, savedViewsPage(savedViewsPageData{
		Groups: groupSavedViews(rows), Total: len(rows),
	})))
}

// savedViewRows resolves a batch of views, attaching their collection
// memberships in one read.
func (s *Server) savedViewRows(userID int64, views []store.SavedView, tz string) []savedViewRow {
	memberships, _ := s.store.SavedViews.CollectionsByView(userID)
	rows := make([]savedViewRow, 0, len(views))
	for _, v := range views {
		row := s.savedViewRow(userID, v, tz)
		row.Collections = memberships[v.ID]
		rows = append(rows, row)
	}
	return rows
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
	count, _ := s.savedViewCount(userID, v.AuthorID, "unread", filters, tz)
	row := savedViewRow{
		ID: v.ID, Title: v.Title, AuthorID: v.AuthorID,
		Filters: filters, Count: count, CreatedAt: v.CreatedAt,
	}
	if v.AuthorID > 0 {
		if a, err := s.store.Authors.ByID(userID, v.AuthorID); err == nil {
			row.AuthorName = a.Name
		}
	}
	return row
}

// savedViewCount counts the matching items for a view's base tab and filters.
// authorID is the author the view is assigned to; the view follows that
// author's rules (0 means unassigned and unfiltered).
func (s *Server) savedViewCount(userID, authorID int64, view string, filters listFilters, tz string) (int, error) {
	f := savedViewItemFilter(view, sortNewest, 0, authorID, filters, tz)
	f.Limit = 0
	return s.store.Items.CountFiltered(userID, f)
}

// savedViewItemFilter builds the item scope of a saved view's own page: the
// view's current filter rows as its view predicate, so the page is unfiltered by
// the feed owners but still follows the view's assigned author.
func savedViewItemFilter(view string, sort itemSort, cursor, authorID int64, filters listFilters, tz string) store.ItemFilter {
	f := scopedFilter(view, sort, cursor, 0, 0, nil, tz)
	tagGroups, feedGroups, _ := filters.storeGroups()
	f.Views = []store.ViewFilter{{
		AuthorID:   authorID,
		TagGroups:  tagGroups,
		FeedGroups: feedGroups,
		TimeGroups: filters.timeGroups(tz),
	}}
	return f
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

// savedViewCollectionValues renders selected collection ids as combo values.
func savedViewCollectionValues(ids []int64) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, strconv.FormatInt(id, 10))
	}
	return out
}

// savedViewFormData builds the create/edit form's option lists. A view always
// spans all of the user's feeds.
func (s *Server) savedViewFormData(userID int64, v savedViewForm) savedViewForm {
	authors, _ := s.store.Authors.List(userID)
	v.AuthorOptions = savedViewAuthorItems(authors)
	collections, _ := s.store.Collections.List(userID)
	v.CollectionOptions = nonAutoCollections(collections)
	cats, _ := s.store.Items.ListCategoriesFilter(userID, store.ItemFilter{
		Views: []store.ViewFilter{{AuthorID: v.AuthorID}},
	})
	v.TagOptions = tagItems(cats)
	fs, _ := s.store.Feeds.List(userID)
	feeds := make([]comboItem, 0, len(fs))
	for _, f := range fs {
		feeds = append(feeds, comboItem{Value: strconv.FormatInt(f.ID, 10), Label: f.Title})
	}
	v.FeedOptions = feeds
	return v
}

// viewEditorData assembles one view's editor: its form values, collection
// memberships and the option lists for the filters it may edit.
func (s *Server) viewEditorData(u store.User, v store.SavedView) savedViewForm {
	form := savedViewForm{
		ID:       v.ID,
		Title:    v.Title,
		Filters:  s.savedViewFilters(u.ID, filtersFromParams(v.Filters)),
		AuthorID: v.AuthorID,
	}
	form.CollectionIDs, _ = s.store.SavedViews.CollectionIDs(u.ID, v.ID)
	return s.savedViewFormData(u.ID, form)
}

// savedViewNew renders the create form prefilled from the list the user saved
// from (?author_id= and the filter fields).
func (s *Server) savedViewNew(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	authorID, ok := s.savedViewAssignedAuthor(u.ID, r.URL.Query().Get("author_id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	form := savedViewForm{
		Title:    "",
		Filters:  s.savedViewFilters(u.ID, filtersFromRequest(r)),
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
		AuthorID: form.AuthorID, Title: form.Title, Filters: form.Filters.params(),
	})
	if err != nil {
		log.Error("create saved view", "err", err)
		form.Error = "could not save this view"
		s.renderSavedViewForm(w, r, u, form, http.StatusInternalServerError)
		return
	}
	if err := s.store.SavedViews.SetCollections(u.ID, created.ID, form.CollectionIDs); err != nil {
		log.Error("set view collections", "view_id", created.ID, "err", err)
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
	form := s.viewEditorData(u, v)
	web.Render(w, r, basePage("edit view", u, savedViewFormPage(u, form)))
}

// savedViewEditFragment returns one view's inline editor body for the author
// edit page's lazy accordion.
func (s *Server) savedViewEditFragment(w http.ResponseWriter, r *http.Request) {
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
	web.Render(w, r, viewEditorPanel(u, s.viewEditorData(u, v)))
}

// savedViewUpdate persists the edit form. On htmx requests (the author edit
// page's auto-saving view panel) it re-renders that accordion item instead of
// redirecting.
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
	form.Filters = s.savedViewFilters(u.ID, form.Filters)
	htmx := r.Header.Get("HX-Request") != ""
	if form.Error != "" {
		if htmx {
			s.renderViewPanel(w, r, u, id, form, http.StatusBadRequest)
			return
		}
		s.renderSavedViewForm(w, r, u, form, http.StatusBadRequest)
		return
	}
	if err := s.store.SavedViews.Update(u.ID, id, store.SavedView{
		AuthorID: form.AuthorID, Title: form.Title, Filters: form.Filters.params(),
	}); err != nil {
		log.Error("update saved view", "err", err)
		form.Error = "could not save this view"
		if htmx {
			s.renderViewPanel(w, r, u, id, form, http.StatusBadRequest)
			return
		}
		s.renderSavedViewForm(w, r, u, form, http.StatusInternalServerError)
		return
	}
	if err := s.store.SavedViews.SetCollections(u.ID, id, form.CollectionIDs); err != nil {
		log.Error("set view collections", "view_id", id, "err", err)
		form.Error = "could not save this view's collections"
		if htmx {
			s.renderViewPanel(w, r, u, id, form, http.StatusBadRequest)
			return
		}
		s.renderSavedViewForm(w, r, u, form, http.StatusInternalServerError)
		return
	}
	if htmx {
		v, err := s.store.SavedViews.ByID(u.ID, id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		rows := s.savedViewRows(u.ID, []store.SavedView{v}, u.Timezone)
		if len(rows) == 0 {
			http.NotFound(w, r)
			return
		}
		editor := s.viewEditorData(u, v)
		web.Render(w, r, viewAccordionBody(u, rows[0], &editor))
		return
	}
	http.Redirect(w, r, "/views/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// renderViewPanel re-renders one auto-saving view panel with a validation or
// persistence error.
func (s *Server) renderViewPanel(w http.ResponseWriter, r *http.Request, u store.User, id int64, form savedViewForm, status int) {
	v, err := s.store.SavedViews.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rows := s.savedViewRows(u.ID, []store.SavedView{v}, u.Timezone)
	if len(rows) == 0 {
		http.NotFound(w, r)
		return
	}
	form = s.savedViewFormData(u.ID, form)
	w.WriteHeader(status)
	web.Render(w, r, viewAccordionBody(u, rows[0], &form))
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

// savedViewFormFromRequest reads and validates the create/edit fields.
func (s *Server) savedViewFormFromRequest(userID int64, r *http.Request) (savedViewForm, bool) {
	return s.savedViewFormFromRequestValues(userID, r.Form)
}

// savedViewCollectionIDs validates a view form's collection selections against
// the user's non-auto collections, silently dropping stale values.
func (s *Server) savedViewCollectionIDs(userID int64, raw []string) []int64 {
	collections, _ := s.store.Collections.List(userID)
	allowed := make(map[int64]bool, len(collections))
	for _, c := range collections {
		if !c.IsAuto {
			allowed[c.ID] = true
		}
	}
	out := make([]int64, 0, len(raw))
	seen := make(map[int64]bool, len(raw))
	for _, value := range raw {
		id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || !allowed[id] || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// savedViewFormFromRequestValues is savedViewFormFromRequest for an already
// parsed form.
func (s *Server) savedViewFormFromRequestValues(userID int64, form url.Values) (savedViewForm, bool) {
	parsed := savedViewForm{
		Title:         strings.TrimSpace(form.Get("title")),
		CollectionIDs: s.savedViewCollectionIDs(userID, form["collections"]),
		Filters:       filtersFromValues(form),
	}
	var ok bool
	parsed.Filters = s.savedViewFilters(userID, parsed.Filters)
	parsed.AuthorID, ok = s.savedViewAssignedAuthor(userID, form.Get("author_id"))
	if !ok {
		return parsed, false
	}
	if parsed.Title == "" {
		parsed.Error = "title is required"
	} else if len(parsed.Title) > maxSavedViewTitle {
		parsed.Error = "title is too long"
	} else if len(parsed.Filters) == 0 {
		parsed.Error = "add at least one filter"
	}
	return parsed, true
}

// savedViewDelete removes a view and returns to the index.
func (s *Server) savedViewDelete(w http.ResponseWriter, r *http.Request) {
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
	if err := s.store.SavedViews.Delete(u.ID, id); err != nil && err != store.ErrNotFound {
		log.Error("delete saved view", "id", id, "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	if r.URL.Query().Get("back") == "author" && v.AuthorID > 0 {
		http.Redirect(w, r, "/authors/"+strconv.FormatInt(v.AuthorID, 10)+"/edit", http.StatusSeeOther)
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
	// A legacy unfiltered view would otherwise render every feed under the
	// view's title. Send it to the editor to give it a recipe.
	if len(s.storedViewFilters(u.ID, v)) == 0 {
		http.Redirect(w, r, "/views/"+strconv.FormatInt(v.ID, 10)+"/edit", http.StatusSeeOther)
		return
	}
	// An assigned view is part of its author's page now; the standalone page is
	// only for unassigned views.
	if v.AuthorID > 0 {
		http.Redirect(w, r, "/authors/"+strconv.FormatInt(v.AuthorID, 10)+"?view="+strconv.FormatInt(v.ID, 10), http.StatusSeeOther)
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
	view := savedViewTab(r)
	sort := savedViewSort(r)
	filters := s.storedViewFilters(u.ID, v)
	if cursor := cursorID(r, sort); cursor > 0 {
		items, more, err := s.store.Items.ListPage(u.ID,
			savedViewItemFilter(view, sort, cursor, v.AuthorID, filters, u.Timezone))
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		base := s.savedViewItemsBase(v, view, sort)
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
	filters := s.storedViewFilters(u.ID, v)
	tagGroups, feedGroups, _ := filters.storeGroups()
	if _, err := s.store.Items.MarkAllReadFiltered(u.ID, store.ItemFilter{
		Views: []store.ViewFilter{{
			AuthorID:   v.AuthorID,
			TagGroups:  tagGroups,
			FeedGroups: feedGroups,
			TimeGroups: filters.timeGroups(u.Timezone),
		}},
	}); err != nil {
		log.Error("mark saved view read", "view_id", id, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// savedViewRecipeLabel renders one stored recipe row for the view page.
func savedViewRecipeLabel(f listFilter) string {
	action := "show "
	if f.hidden() {
		action = "hide "
	}
	return action + f.label()
}

// storedViewFilters returns a view's own recipe, validated against the user's
// feeds. The saved view page always uses this recipe; it has no transient
// filter layer of its own.
func (s *Server) storedViewFilters(userID int64, v store.SavedView) listFilters {
	return s.savedViewFilters(userID, filtersFromParams(v.Filters))
}

// savedViewPageData assembles the view page header and its scoped item list.
func (s *Server) savedViewPageData(u store.User, v store.SavedView, r *http.Request) savedViewPageData {
	view := savedViewTab(r)
	sort := savedViewSort(r)
	filters := s.storedViewFilters(u.ID, v)
	unread, _ := s.savedViewCount(u.ID, v.AuthorID, "unread", filters, u.Timezone)
	markAll := markAllReadData{
		Action: "/views/" + strconv.FormatInt(v.ID, 10) + "/read-all",
		Unread: unread,
	}
	return savedViewPageData{
		Row:     s.savedViewRow(u.ID, v, u.Timezone),
		Scoped:  s.savedViewScopedData(u, v, view, sort, filters),
		MarkAll: markAll,
	}
}

// savedViewTab reads the page tab, defaulting to unread. A legacy non-numeric
// ?view= is accepted as a tab fallback.
func savedViewTab(r *http.Request) string {
	tab := tabParam(r.URL.Query())
	switch tab {
	case "read", "favorites", "bookmarks", "unread":
		return tab
	}
	return "unread"
}

// savedViewSort reads ?sort=, defaulting to newest.
func savedViewSort(r *http.Request) itemSort {
	if raw := r.URL.Query().Get("sort"); raw != "" {
		return itemSortFromParam(raw)
	}
	return sortNewest
}

// savedViewItemsBase builds the load-more URL for the active view state.
func (s *Server) savedViewItemsBase(v store.SavedView, view string, sort itemSort) string {
	base := scopedViewQuery("/views/"+strconv.FormatInt(v.ID, 10)+"/items", view) + "&sort=" + sort.param()
	return base
}

// savedViewScopedData renders the view's scoped item list. The view's stored
// recipe is fixed, so the generic filter toolbar is intentionally absent.
func (s *Server) savedViewScopedData(u store.User, v store.SavedView, view string, sort itemSort, filters listFilters) scopedItemsData {
	id := v.ID
	base := "/views/" + strconv.FormatInt(id, 10)
	items, more, _ := s.store.Items.ListPage(u.ID,
		savedViewItemFilter(view, sort, 0, v.AuthorID, filters, u.Timezone))
	unread, _ := s.savedViewCount(u.ID, v.AuthorID, "unread", filters, u.Timezone)
	read, _ := s.savedViewCount(u.ID, v.AuthorID, "read", filters, u.Timezone)
	favs, _ := s.savedViewCount(u.ID, v.AuthorID, "favorites", filters, u.Timezone)
	itemsBase := s.savedViewItemsBase(v, view, sort)
	return scopedItemsData{
		Path: base, ItemsPath: base + "/items", View: view, Sort: sort,
		UnreadCount: unread, ReadCount: read, Items: withTZ(u.Timezone, dedupItems(items)),
		More:   pageCursorFor(itemsBase, items, more, sort, 0),
		Mode:   s.store.ViewPrefs.Mode(u.ID, base),
		FavTab: favs > 0 || view == "favorites", FavCount: favs,
		NoFilters: true,
	}
}

// savedViewsForAuthor returns the views assigned to an author, for its page.
func (s *Server) savedViewsForAuthor(userID, authorID int64, tz string) []savedViewRow {
	views, _ := s.store.SavedViews.ListByAuthor(userID, authorID)
	usable := make([]store.SavedView, 0, len(views))
	for _, v := range views {
		if len(s.savedViewFilters(userID, filtersFromParams(v.Filters))) == 0 {
			continue
		}
		usable = append(usable, v)
	}
	return s.savedViewRows(userID, usable, tz)
}

// viewFiltersFromViews converts a set of saved views into item predicates,
// skipping unfiltered legacy rows (they would match every post).
func (s *Server) viewFiltersFromViews(userID int64, views []store.SavedView, tz string) []store.ViewFilter {
	out := make([]store.ViewFilter, 0, len(views))
	for _, v := range views {
		if len(s.savedViewFilters(userID, filtersFromParams(v.Filters))) == 0 {
			continue
		}
		out = append(out, s.storeViewFilter(userID, v, tz))
	}
	return out
}

// collectionViewMatch is one usable saved view in a collection: its item
// predicate plus the id and title used by a view-source link.
type collectionViewMatch struct {
	ID        int64
	Title     string
	Predicate store.ViewFilter
}

// collectionViewFilters returns the item predicates of a collection's member
// views, for the collection's item scope.
func (s *Server) collectionViewFilters(userID, collectionID int64, tz string) []store.ViewFilter {
	matches := s.collectionViewMatches(userID, collectionID, tz)
	out := make([]store.ViewFilter, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.Predicate)
	}
	return out
}

// collectionViewMatches resolves a collection's member views into item
// predicates. Unfiltered legacy views are skipped, matching the collection
// scope. The id and title ride along for view-source attribution.
func (s *Server) collectionViewMatches(userID, collectionID int64, tz string) []collectionViewMatch {
	views, _ := s.store.SavedViews.ListByCollection(userID, collectionID)
	out := make([]collectionViewMatch, 0, len(views))
	for _, v := range views {
		filters := s.savedViewFilters(userID, filtersFromParams(v.Filters))
		if len(filters) == 0 {
			continue
		}
		tagGroups, feedGroups, _ := filters.storeGroups()
		out = append(out, collectionViewMatch{
			ID: v.ID, Title: v.Title,
			Predicate: store.ViewFilter{
				AuthorID:  v.AuthorID,
				TagGroups: tagGroups, FeedGroups: feedGroups,
				TimeGroups: filters.timeGroups(tz),
			},
		})
	}
	return out
}

// annotateCollectionViews marks the items a collection reaches only through a
// member view, meaning neither their owner feed nor any cross-feed membership
// is a collection member feed. Those items carry a link to the matching view so
// the reason they are here is visible, and CollectionID lets a row toggle
// preserve the context through its swap.
func (s *Server) annotateCollectionViews(userID, collectionID int64, tz string, items []store.ItemWithFeed) {
	if collectionID <= 0 || len(items) == 0 {
		return
	}
	matches := s.collectionViewMatches(userID, collectionID, tz)
	if len(matches) == 0 {
		return
	}
	memberFeeds, _ := s.store.Collections.Feeds(userID, collectionID)
	member := make(map[int64]bool, len(memberFeeds))
	for _, f := range memberFeeds {
		member[f.ID] = true
	}
	index := make(map[int64]int, len(items))
	ids := make([]int64, 0, len(items))
	for i := range items {
		items[i].CollectionID = collectionID
		if collectionHasFeed(items[i], member) {
			continue
		}
		index[items[i].ID] = i
		ids = append(ids, items[i].ID)
	}
	if len(ids) == 0 {
		return
	}
	predicates := make([]store.ViewFilter, len(matches))
	for i, m := range matches {
		predicates[i] = m.Predicate
	}
	matched, err := s.store.Items.MatchingViews(userID, ids, predicates)
	if err != nil {
		log.Error("match collection views", "collection_id", collectionID, "err", err)
		return
	}
	for id, viewIndexes := range matched {
		i, ok := index[id]
		if !ok {
			continue
		}
		for _, vi := range viewIndexes {
			if vi < 0 || vi >= len(matches) {
				continue
			}
			items[i].ViewSources = append(items[i].ViewSources, store.ItemViewSource{
				ID: matches[vi].ID, Title: matches[vi].Title,
			})
		}
	}
}

// collectionHasFeed reports whether an item belongs to any of a collection's
// member feeds (its owner feed or a cross-feed membership).
func collectionHasFeed(it store.ItemWithFeed, member map[int64]bool) bool {
	if member[it.FeedID] {
		return true
	}
	for _, src := range it.Sources {
		if member[src.FeedID] {
			return true
		}
	}
	return false
}

// storeViewFilter converts a saved view into the store's query predicate. An
// unfiltered view is skipped by callers: without a filter it would match every
// post, which is not a useful author source.
func (s *Server) storeViewFilter(userID int64, v store.SavedView, tz string) store.ViewFilter {
	filters := s.savedViewFilters(userID, filtersFromParams(v.Filters))
	tagGroups, feedGroups, _ := filters.storeGroups()
	return store.ViewFilter{
		AuthorID:  v.AuthorID,
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
	return s.viewFiltersFromViews(userID, views, tz)
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
