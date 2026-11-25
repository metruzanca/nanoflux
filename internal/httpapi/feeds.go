package httpapi

import (
	"net/http"
	"strconv"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

// feeds is the feed library: every feed the user owns, including feeds with no
// author. It replaced the old implicit ownership model where a feed could only
// be reached through an author page.
func (s *Server) feeds(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	rows, err := s.store.Feeds.ListWithUnread(u.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	feedRows := make([]feedRow, 0, len(rows))
	for _, row := range rows {
		feedRows = append(feedRows, s.feedRowFor(u.ID, row.Feed, row.AuthorName, u.Timezone, row.Unread))
	}
	web.Render(w, r, basePage("feeds", u, feedsPage(u, feedRows)))
}

// feedPage is one feed's items, reachable whether or not an author owns it.
func (s *Server) feedPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := s.store.Feeds.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	authorName := ""
	if f.AuthorID != 0 {
		if a, err := s.store.Authors.ByID(u.ID, f.AuthorID); err == nil {
			authorName = a.Name
		}
	}
	unread, _ := s.store.Items.CountUnread(u.ID, f.ID)
	row := s.feedRowFor(u.ID, f, authorName, u.Timezone, unread)
	scoped := s.feedScopedItems(u.ID, f, itemsView(r), sortsTime.coerce(itemSortOf(r)), u.Timezone, filtersFromRequest(r))
	history := s.feedHistoryData(u.ID, f, u.Timezone)
	history.Query = "?tab=" + scoped.View + "&sort=" + scoped.Sort.param()
	history.Query = scoped.Filters.appendFilters(history.Query)
	web.Render(w, r, basePage(f.Title, u, feedPage(u, row, scoped, history)))
}

// feedItems returns the feed page's list fragment for paging and tab changes.
func (s *Server) feedItems(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := s.store.Feeds.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	view := itemsView(r)
	sort := sortsTime.coerce(itemSortOf(r))
	filters := filtersFromRequest(r)
	if cursor := cursorID(r, sort); cursor > 0 {
		filter := scopedFilter(view, sort, cursor, 0, 0, filters, u.Timezone)
		filter.FeedID = f.ID
		items, more, err := s.store.Items.ListPage(u.ID, filter)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		base := filters.appendFilters("/feeds/" + strconv.FormatInt(f.ID, 10) + "/items?tab=" + view + "&sort=" + sort.param())
		web.Render(w, r, ItemsPage(withTZ(u.Timezone, dedupItems(items)), pageCursorFor(base, items, more, sort, int(cursor)), false, view == "unread"))
		return
	}
	web.Render(w, r, ScopedItems(s.feedScopedItems(u.ID, f, view, sort, u.Timezone, filters)))
}

// feedReadAll marks every unread item in a feed read, honoring the page's
// active show/hide filters.
func (s *Server) feedReadAll(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := s.store.Feeds.ByID(u.ID, id); err != nil {
		http.NotFound(w, r)
		return
	}
	filters := filtersFromRequest(r)
	tagGroups, feedGroups, authorGroups, _ := filters.storeGroups()
	if _, err := s.store.Items.MarkAllReadFiltered(u.ID, store.ItemFilter{
		FeedID:       id,
		Library:      true,
		TagGroups:    tagGroups,
		FeedGroups:   feedGroups,
		AuthorGroups: authorGroups,
		TimeGroups:   filters.timeGroups(u.Timezone),
	}); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// feedScopedItems builds the unread/read list for a feed page.
func (s *Server) feedScopedItems(userID int64, f store.Feed, view string, sort itemSort, tz string, filters listFilters) scopedItemsData {
	sorts := sortsTime
	sort = sorts.coerce(sort)
	base := "/feeds/" + strconv.FormatInt(f.ID, 10)
	d := scopedItemsData{
		Path: base, ItemsPath: base + "/items", View: view, Sort: sort, Sorts: sorts,
		Mode: s.store.ViewPrefs.Mode(userID, base), Filters: filters,
	}
	d.AuthorOptions = s.authorOptions(userID)
	filter := scopedFilter(view, sort, 0, 0, 0, filters, tz)
	filter.FeedID = f.ID
	filter.Library = true
	items, more, _ := s.store.Items.ListPage(userID, filter)
	// /feeds is the library view: its source line names the feed, not an author.
	for i := range items {
		items[i].AuthorID = 0
		items[i].AuthorName = ""
	}
	d.UnreadCount, _ = s.store.Items.CountFiltered(userID, store.ItemFilter{FeedID: f.ID, UnreadOnly: true, Library: true})
	d.ReadCount, _ = s.store.Items.CountFiltered(userID, store.ItemFilter{FeedID: f.ID, ReadOnly: true, Library: true})
	cats, _ := s.store.Items.ListCategories(userID, store.ItemFilter{FeedID: f.ID})
	d.TagOptions = tagItems(cats)
	d.Items = withTZ(tz, dedupItems(items))
	itemsBase := filters.appendFilters(base + "/items?tab=" + view + "&sort=" + sort.param())
	d.More = pageCursorFor(itemsBase, items, more, sort, 0)
	if len(filters) > 0 {
		countFilter := scopedFilter(view, sortNewest, 0, 0, 0, filters, tz)
		countFilter.FeedID = f.ID
		countFilter.Library = true
		d.FilteredCount, _ = s.store.Items.CountFiltered(userID, countFilter)
	}
	return d
}
