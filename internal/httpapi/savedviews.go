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

// savedViewRow is one view on an author card, with its resolved display fields
// and matching-item count.
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

// savedViewForm is the create page's data and the inline editor's data.
type savedViewForm struct {
	ID                int64
	Title             string
	Filters           listFilters
	AuthorID          int64
	NewAuthorName     string
	NewAuthorAvatar   string
	CollectionIDs     []int64
	AuthorOptions     []comboItem
	CollectionOptions []comboItem
	TagOptions        []comboItem
	FeedOptions       []comboItem
	Error             string
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

// savedViewRow resolves one view's display fields and matching count.
func (s *Server) savedViewRow(userID int64, v store.SavedView, tz string) savedViewRow {
	filters := s.savedViewFilters(userID, filtersFromParams(v.Filters))
	count, _ := s.savedViewCount(userID, v.AuthorID, "unread", filters, tz)
	row := savedViewRow{
		ID: v.ID, Title: v.Title, AuthorID: v.AuthorID,
		Filters: filters, Count: count, CreatedAt: v.CreatedAt,
	}
	if a, err := s.store.Authors.ByID(userID, v.AuthorID); err == nil {
		row.AuthorName = a.Name
	}
	return row
}

// savedViewCount counts the matching items for a view's base tab and filters.
// authorID is the author the view is assigned to; the view follows that
// author's rules.
func (s *Server) savedViewCount(userID, authorID int64, view string, filters listFilters, tz string) (int, error) {
	f := savedViewItemFilter(view, sortNewest, 0, authorID, filters, tz)
	f.Limit = 0
	return s.store.Items.CountFiltered(userID, f)
}

// savedViewItemFilter builds the item scope of a saved view: its filter rows as
// the view predicate, so it is unfiltered by feed owners but still follows the
// view's assigned author's rules.
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

// savedViewPrefillAuthor validates the optional ?author_id= prefill on the
// create page. 0 means "no author selected yet", which the form shows as the
// create-new-author fields.
func (s *Server) savedViewPrefillAuthor(userID int64, raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
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
	v.AuthorOptions = authorItems(authors)
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
	authorID, ok := s.savedViewPrefillAuthor(u.ID, r.URL.Query().Get("author_id"))
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
	form := s.savedViewFormFromRequest(u.ID, r)
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
	http.Redirect(w, r, savedViewAuthorURL(created.AuthorID, created.ID), http.StatusSeeOther)
}

// savedViewAuthorURL is the canonical view location: the view as the selected
// source on its author's page.
func savedViewAuthorURL(authorID, viewID int64) string {
	return "/authors/" + strconv.FormatInt(authorID, 10) + "?view=" + strconv.FormatInt(viewID, 10)
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
	old, err := s.store.SavedViews.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	form := s.savedViewFormFromRequest(u.ID, r)
	form.ID = id
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
		s.renderViewPanel(w, r, u, id, form, http.StatusBadRequest)
		return
	}
	if err := s.store.SavedViews.SetCollections(u.ID, id, form.CollectionIDs); err != nil {
		log.Error("set view collections", "view_id", id, "err", err)
		form.Error = "could not save this view's collections"
		s.renderViewPanel(w, r, u, id, form, http.StatusBadRequest)
		return
	}
	// Changing the assigned author moves the view to that author's page. Send
	// the browser there instead of re-rendering a panel that no longer belongs
	// to the page it was opened on.
	if form.AuthorID != old.AuthorID {
		target := "/authors/" + strconv.FormatInt(form.AuthorID, 10) + "/edit?view=" + strconv.FormatInt(id, 10)
		if htmx {
			w.Header().Set("HX-Redirect", target)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
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
	http.Redirect(w, r, savedViewAuthorURL(form.AuthorID, id), http.StatusSeeOther)
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

// renderSavedViewForm re-renders the create page with a validation or
// persistence error.
func (s *Server) renderSavedViewForm(w http.ResponseWriter, r *http.Request, u store.User, form savedViewForm, status int) {
	w.WriteHeader(status)
	web.Render(w, r, basePage("new view", u, savedViewFormPage(u, s.savedViewFormData(u.ID, form))))
}

// savedViewFormFromRequest reads and validates the create/edit fields. The
// author is required; "new" creates one from the embedded author fields, so a
// view is always assigned.
func (s *Server) savedViewFormFromRequest(userID int64, r *http.Request) savedViewForm {
	parsed := savedViewForm{
		Title:           strings.TrimSpace(r.FormValue("title")),
		NewAuthorName:   strings.TrimSpace(r.FormValue("author_name")),
		NewAuthorAvatar: strings.TrimSpace(r.FormValue("avatar_url")),
		CollectionIDs:   s.savedViewCollectionIDs(userID, r.Form["collections"]),
		Filters:         s.savedViewFilters(userID, filtersFromValues(r.Form)),
	}
	switch {
	case parsed.Title == "":
		parsed.Error = "title is required"
	case len(parsed.Title) > maxSavedViewTitle:
		parsed.Error = "title is too long"
	case len(parsed.Filters) == 0:
		parsed.Error = "add at least one filter"
	}
	if parsed.Error != "" {
		return parsed
	}
	authorID, _, msg := s.resolveAuthor(r, userID, "view")
	if msg != "" {
		parsed.Error = msg
		return parsed
	}
	parsed.AuthorID = authorID
	return parsed
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

// savedViewDelete removes a view and returns to its author's edit page.
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
	http.Redirect(w, r, "/authors/"+strconv.FormatInt(v.AuthorID, 10)+"/edit", http.StatusSeeOther)
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
	AuthorID  int64
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
			ID: v.ID, Title: v.Title, AuthorID: v.AuthorID,
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
				ID: matches[vi].ID, Title: matches[vi].Title, AuthorID: matches[vi].AuthorID,
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
