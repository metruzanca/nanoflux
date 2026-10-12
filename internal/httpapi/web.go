package httpapi

import (
	"context"
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
	"github.com/metruzanca/nanoflux/pluginapi"
)

type homeData struct {
	Unread      []store.ItemWithFeed
	UnreadCount int
	Sort        itemSort
	More        *loadMoreData
	Mode        string // saved display mode for "/unread"
}

// pageSize is the number of items rendered per page on every list.
const pageSize = 25

type readData struct {
	Read      []store.ItemWithFeed
	ReadCount int
	More      *loadMoreData
	Mode      string // saved display mode for "/read"
}

type favoritesData struct {
	Favorites  []store.ItemWithFeed
	FavCount   int
	ShareToken string // public share token for the favorites list, "" when unshared
	Sort       itemSort
	More       *loadMoreData
	Mode       string // saved display mode for "/favorites"
}

// bookmarksData drives the native bookmarks list page.
type bookmarksData struct {
	Bookmarks  []store.ItemWithFeed
	BookCount  int
	ShareToken string // public share token for the bookmarks list, "" when unshared
	Sort       itemSort
	More       *loadMoreData
	Mode       string // saved display mode for "/bookmarks"
}

type feedRow struct {
	store.Feed
	AuthorName  string
	Unread      int
	Storage     int64              // cached-media bytes this feed lists (feed page only)
	Timezone    string             // user's IANA timezone, for relative timestamps in templates
	Collections []store.Collection // the collections this feed belongs to (author page)
}

// Cooling reports whether the feed is currently inside a rate-limit backoff, so
// the row can show a retry badge and disable its refresh button.
func (r feedRow) Cooling() bool { return cooling(r.NextPollAt) }

type feedForm struct {
	ID               int64
	Title            string
	FeedURL          string
	HomeURL          string
	Description      string
	AuthorID         int64
	PollIntervalSec  int
	PollIntervalAuto bool
	CollectionIDs    []int64
	Enabled          bool
	CacheImages      bool
	// CacheForced is true when the feed's plugin forces image caching on
	// (CapImageCache), in which case the user cannot turn it off.
	CacheForced bool
}

type feedsData struct {
	Collections []store.Collection
	Form        feedForm
	// DocsPlugin is the loaded plugin that documents this feed's URL (matched on
	// CapDocs, so a generic-parser feed still finds it), or "" when none does.
	// It drives the "plugin docs" button on the editor.
	DocsPlugin string
	// Panel is the "managed feed" card for a feed a plugin manages remotely
	// (a Kill the Newsletter inbox): its display fields and the title-sync
	// action, shown on the edit page. nil when no plugin manages the feed.
	Panel *feedPanelData
	// NextPageURL is the feed's older-history cursor; non-empty renders the
	// "load older items" control in the settings panel.
	NextPageURL string
	// Stats backs the settings panel's history card (unread/read/oldest post).
	Stats store.FeedItemStats
	// Timezone is the user's IANA timezone for relative dates in the card.
	Timezone string
	// LastError/LastPolledAt/PluginName back the settings panel's fetch-error
	// section, so a failed poll shows the underlying message (e.g. a plugin's
	// configuration error) instead of a bare "last poll failed".
	LastError    string
	LastPolledAt string
	PluginName   string
}

type authorRow struct {
	store.Author
	FeedCount int
	Unread    int
	// Taste is the magic-sort key: the author's rank tier (raised > neutral >
	// lowered), then total favorites across their feeds. Higher sorts first.
	Taste int
}

type authorForm struct {
	ID          int64
	Name        string
	AvatarURL   string
	Description string
}

type authorsData struct {
	Rows       []authorRow
	Form       authorForm
	Links      []store.AuthorLink   // edit page: the author's external links, editable
	Feeds      []feedRow            // edit page: the author's feeds, shown as a UX assist
	AvatarCard authorAvatarCardData // edit page avatar cache card
	// Editor is the combined page's feed editor, for the feed named by the
	// ?feed= query. nil when no feed is selected.
	Editor *feedsData
}

type authorData struct {
	Author store.Author
	Rows   []feedRow
	Links  []store.AuthorLink
	// Collections is every feed collection the author's feeds belong to,
	// combined and deduped. Shown as chips on the author card.
	Collections []store.Collection
	Scoped      scopedItemsData
	Stats       store.AuthorItemStats
	Frequency   string // approximate posting cadence, "" when unknown
	Timezone    string
	MarkAll     markAllReadData
}

// markAllReadData drives the "mark all as read" control on a feed or author
// page: the button, its confirmation dialog, and the POST that performs it.
type markAllReadData struct {
	Action string // POST endpoint that marks the scope read
	Unread int    // items the confirmation names and the button counts
}

type collectionData struct {
	Collection store.Collection
	Feeds      []store.Feed
	AllFeeds   []collectionFeedGroup
	Scoped     scopedItemsData
}

// collectionFeedGroup is one author's feeds for the collection add-feed
// dropdown. Collections hold feeds (not authors), but the picker presents them
// grouped by author so feeds are seen from the perspective of authors.
type collectionFeedGroup struct {
	AuthorName string
	Feeds      []store.FeedWithUnread
}

// scopedItemsData renders the unread/read tabs and an item list scoped to a
// single feed, author, or collection.
type scopedItemsData struct {
	Path        string // full page URL base, e.g. "/authors/1"
	ItemsPath   string // fragment URL base, e.g. "/authors/1/items"
	View        string // "unread", "read", "favorites", "bookmarks" or "feeds"
	Sort        itemSort
	NoMagic     bool // lists order by membership time, so the magic sort is not offered
	UnreadCount int
	ReadCount   int
	Items       []store.ItemWithFeed
	More        *loadMoreData // "load more" cursor, nil when no next page
	SwapOOB     bool          // render with hx-swap-oob for the collection OOB fragment
	HideAuthor  bool          // drop the author link from item meta (author-scoped page)
	Mode        string        // saved display mode for this scope ("list" or "grid")
	FeedsTab    bool          // collection scope only: render the "feeds" tab
	FeedCount   int           // feeds in the collection, for the "feeds (N)" tab
	Feeds       []feedRow     // feed cards for the "feeds" view
	FavTab      bool          // author scope only: render the "favorites" tab
	FavCount    int           // favorited items in scope, for the "favorites (N)" tab
	Tags        []string      // active tag filters (AND), from ?tags=
	TagOptions  []comboItem   // distinct tags in scope, for the tag-filter modal
	// FeedID/FeedTitle are the single-feed filter (author pages only): the item
	// list is scoped to one feed of the author, which mirrors the removed
	// /feeds/{id} page. FeedOptions are the author's feeds for the filter modal.
	FeedID      int64
	FeedTitle   string
	FeedOptions []comboItem
}

// itemSort is the ordering of an item list: newest first (time desc), oldest
// first (time asc), or magic (feed taste; see store.ItemFilter.Magic).
type itemSort string

const (
	sortNewest itemSort = "newest"
	sortOldest itemSort = "oldest"
	sortMagic  itemSort = "magic"
)

// itemSortFromParam maps a ?sort= value onto a sort. Anything unknown is the
// default newest.
func itemSortFromParam(v string) itemSort {
	switch v {
	case "magic":
		return sortMagic
	case "oldest":
		return sortOldest
	case "newest":
		return sortNewest
	}
	return sortNewest
}

// itemSortOf reads the list's sort from ?sort=. The legacy ?dir=asc spelling is
// still honored so an old bookmark or in-flight link keeps working.
func itemSortOf(r *http.Request) itemSort {
	s := r.URL.Query().Get("sort")
	if s == "" && r.URL.Query().Get("dir") == "asc" {
		return sortOldest
	}
	return itemSortFromParam(s)
}

// param is the canonical ?sort= value.
func (s itemSort) param() string { return string(s) }

func (s itemSort) asc() bool   { return s == sortOldest }
func (s itemSort) magic() bool { return s == sortMagic }

// itemsAsc reports whether the list is ordered oldest-first.
func itemsAsc(r *http.Request) bool { return itemSortOf(r).asc() }

func dirParam(asc bool) string {
	if asc {
		return "asc"
	}
	return "desc"
}

// moreURL builds the load-more fragment URL preserving the current filters:
// base may already carry a query string. The cursor key depends on the sort:
// before= (newest), after= (oldest), offset= (magic).
func moreURL(base string, id int64, asc bool) string {
	return moreURLFor(base, sortNewest, id)
}

// moreURLFor builds a load-more URL for a cursor value under a sort. For newest
// and oldest the cursor is the last item's id; for magic it is the next offset.
func moreURLFor(base string, sort itemSort, cursor int64) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	key := "before"
	switch sort {
	case sortOldest:
		key = "after"
	case sortMagic:
		key = "offset"
	}
	return base + sep + key + "=" + strconv.FormatInt(cursor, 10)
}

// pageCursor returns the load-more cursor for a fetched page, or nil when the
// list is empty or there is no next page. offset is the current magic-sort page
// offset (ignored for the time sorts).
func pageCursor(base string, items []store.ItemWithFeed, hasMore bool, asc bool) *loadMoreData {
	return pageCursorFor(base, items, hasMore, sortNewest, 0)
}

// pageCursorFor is pageCursor with an explicit sort. Magic pages advance by
// offset rather than a keyset id.
func pageCursorFor(base string, items []store.ItemWithFeed, hasMore bool, sort itemSort, offset int) *loadMoreData {
	if !hasMore || len(items) == 0 {
		return nil
	}
	if sort.magic() {
		return &loadMoreData{URL: moreURLFor(base, sort, int64(offset+pageSize))}
	}
	return &loadMoreData{URL: moreURLFor(base, sort, items[len(items)-1].ID)}
}

// scopedFilter builds the item filter for a feed/author/collection list,
// honoring the sort, a page cursor (a keyset id for the time sorts, an offset
// for magic), and any active tag filters (AND-ed; none means no tag filter).
// tags is variadic so the global lists (which never tag-filter) can omit it.
func scopedFilter(view string, sort itemSort, cursor int64, feedID, authorID, collectionID int64, tags ...string) store.ItemFilter {
	f := store.ItemFilter{FeedID: feedID, AuthorID: authorID, CollectionID: collectionID, Limit: pageSize, Tags: tags}
	switch sort {
	case sortOldest:
		f.Ascending = true
		f.AfterID = cursor
	case sortMagic:
		f.Magic = true
		f.Offset = int(cursor)
	default:
		f.BeforeID = cursor
	}
	switch view {
	case "read":
		f.ReadOnly = true
	case "favorites":
		f.FavoritesOnly = true
	case "bookmarks":
		f.BookmarksOnly = true
	default:
		f.UnreadOnly = true
	}
	return f
}

// maxTagFilters caps how many tags a list request may carry, so a hand-crafted
// URL cannot build an unbounded IN (...) predicate.
const maxTagFilters = 100

// tagParams reads the active tag filters from a list request: repeated ?tags=
// values, trimmed, empties dropped, deduped preserving order, and capped.
func tagParams(r *http.Request) []string {
	return tagsFromValues(r.URL.Query())
}

// tagsFromValues is tagParams over an already-parsed query, so callers holding a
// URL's values (e.g. the htmx current-URL) can reuse it.
func tagsFromValues(vals url.Values) []string {
	raw := vals["tags"]
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
		if len(out) >= maxTagFilters {
			break
		}
	}
	return out
}

// appendTags adds the active tags to a list URL base as repeated ?tags= params
// (the cursor/pagination keys are appended after it), preserving any existing
// query string.
func appendTags(base string, tags []string) string {
	if len(tags) == 0 {
		return base
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	var b strings.Builder
	b.WriteString(base)
	for _, t := range tags {
		b.WriteString(sep)
		b.WriteString("tags=")
		b.WriteString(url.QueryEscape(t))
		sep = "&"
	}
	return b.String()
}

// scopedViewQuery builds the "path?view=<view>[&feed=<id>]" prefix shared by a
// scoped list's tab, sort, filter and pagination links. The sort, tags and the
// load-more cursor are appended by the caller (see scopedTabURL, sortPicker and
// moreURLFor).
func scopedViewQuery(path, view string, feed int64) string {
	q := path + "?view=" + url.QueryEscape(view)
	if feed != 0 {
		q += "&feed=" + strconv.FormatInt(feed, 10)
	}
	return q
}

// tagPageURL builds a scoped page's URL with its view/sort and the given tags,
// used by the active-filter chips (clicking one drops it) and their links. feed
// preserves an active single-feed filter.
func tagPageURL(path, view string, sort itemSort, tags []string, feed int64) string {
	u := scopedViewQuery(path, view, feed) + "&sort=" + sort.param()
	return appendTags(u, tags)
}

// withoutTag returns tags with the entry at index i removed, for a chip's
// remove link. An out-of-range index returns tags unchanged.
func withoutTag(tags []string, i int) []string {
	if i < 0 || i >= len(tags) {
		return tags
	}
	out := make([]string, 0, len(tags)-1)
	out = append(out, tags[:i]...)
	out = append(out, tags[i+1:]...)
	return out
}

// cursorID reads the page cursor from a load-more request: before= (newest),
// after= (oldest), offset= (magic).
func cursorID(r *http.Request, sort itemSort) int64 {
	key := "before"
	switch sort {
	case sortOldest:
		key = "after"
	case sortMagic:
		key = "offset"
	}
	n, _ := strconv.ParseInt(r.URL.Query().Get(key), 10, 64)
	return n
}

func (s *Server) itemsReadAll(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	if err := s.store.Items.MarkAllRead(u.ID, 0); err != nil {
		log.Error("mark all read", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderItemsList(w, r, u.ID, u.Timezone)
}

// authorReadAll marks every item across all of an author's feeds read ("mark all
// as read" on the author page). The client reloads the page on success.
func (s *Server) authorReadAll(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := s.store.Authors.ByID(u.ID, id); err != nil {
		http.NotFound(w, r)
		return
	}
	// The author page's mark-all honors the active feed/tag filter, so a click
	// marks exactly the items the filtered list shows (unread ones).
	feedID := s.authorFeedFilter(u.ID, id, r)
	tags := tagParams(r)
	if _, err := s.store.Items.MarkAllReadFiltered(u.ID, store.ItemFilter{AuthorID: id, FeedID: feedID, Tags: tags}); err != nil {
		log.Error("mark author read", "author_id", id, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// authorReadAllURL is the author mark-all action, carrying the active feed/tag
// filter (as query params the POST re-reads) so it marks the shown items only.
func authorReadAllURL(authorID, feedID int64, tags []string) string {
	action := "/authors/" + strconv.FormatInt(authorID, 10) + "/read-all"
	q := url.Values{}
	if feedID != 0 {
		q.Set("feed", strconv.FormatInt(feedID, 10))
	}
	for _, t := range tags {
		q.Add("tags", t)
	}
	if len(q) > 0 {
		action += "?" + q.Encode()
	}
	return action
}

// readPage is the history page: the user's read items, most recently read
// first. It has no sort control (the order is fixed to read_at), so it pages by
// offset rather than a sort-keyed cursor.
func (s *Server) readPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	offset := historyOffset(r)
	items, more, _ := s.store.Items.ListReadHistory(u.ID, offset, pageSize)
	count, _ := s.store.Items.CountRead(u.ID, 0)
	web.Render(w, r, basePage("history", u, readPage(readData{
		Read: withTZ(u.Timezone, items), ReadCount: count,
		More: historyMore("/items?read=1", more, offset), Mode: s.store.ViewPrefs.Mode(u.ID, "/read"),
	})))
}

// historyOffset reads the history page's offset cursor.
func historyOffset(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if n < 0 {
		return 0
	}
	return n
}

// historyMore builds the load-more cursor for the offset-paged history list.
func historyMore(base string, hasMore bool, offset int) *loadMoreData {
	if !hasMore {
		return nil
	}
	return &loadMoreData{URL: moreURLFor(base, sortMagic, int64(offset+pageSize))}
}

func (s *Server) favoritesPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	sort := itemSortOf(r)
	items, more, _ := s.store.Items.ListPage(u.ID, scopedFilter("favorites", sort, cursorID(r, sort), 0, 0, 0))
	count, _ := s.store.Items.CountFavorites(u.ID, 0)
	tok, _ := s.store.Users.FavoritesShareToken(u.ID)
	mode := s.store.ViewPrefs.Mode(u.ID, "/favorites")
	base := "/items?fav=1&sort=" + sort.param()
	web.Render(w, r, basePage("favorites", u, favoritesPage(favoritesData{
		Favorites: withTZ(u.Timezone, items), FavCount: count, ShareToken: tok, Sort: sort,
		More: pageCursorFor(base, items, more, sort, int(cursorID(r, sort))), Mode: mode,
	})))
}

// bookmarksPage is the native bookmarks list (save-for-later), the sibling of
// favorites.
func (s *Server) bookmarksPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	sort := itemSortOf(r)
	items, more, _ := s.store.Items.ListPage(u.ID, scopedFilter("bookmarks", sort, cursorID(r, sort), 0, 0, 0))
	count, _ := s.store.Items.CountBookmarks(u.ID, 0)
	tok, _ := s.store.Users.BookmarksShareToken(u.ID)
	mode := s.store.ViewPrefs.Mode(u.ID, "/bookmarks")
	base := "/items?bookmark=1&sort=" + sort.param()
	web.Render(w, r, basePage("bookmarks", u, bookmarksPage(bookmarksData{
		Bookmarks: withTZ(u.Timezone, items), BookCount: count, ShareToken: tok, Sort: sort,
		More: pageCursorFor(base, items, more, sort, int(cursorID(r, sort))), Mode: mode,
	})))
}

func (s *Server) itemsMarkAllUnread(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	if err := s.store.Items.MarkAllUnread(u.ID, 0); err != nil {
		log.Error("mark all unread", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderReadItemsList(w, r, u.ID, u.Timezone)
}

// displayPrefSet persists the user's list/grid choice for a page scope. The
// client applies the mode immediately; this only records it so the choice
// follows the account across devices. Only known scope shapes are accepted so
// a malformed client can't write arbitrary keys.
func (s *Server) displayPrefSet(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	scope := r.FormValue("scope")
	mode := store.NormalizeViewMode(r.FormValue("mode"))
	if !validViewScope(scope) {
		http.Error(w, "invalid scope", http.StatusBadRequest)
		return
	}
	if err := s.store.ViewPrefs.Set(u.ID, scope, mode); err != nil {
		log.Error("set display pref", "scope", scope, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validViewScope reports whether scope is one of the display-preference page
// keys: a fixed view ("/unread", "/read", "/favorites", "/bookmarks") or an
// entity page ("/authors/{id}", "/collections/{id}", "/lists/{id}"). A query
// string or fragment is rejected.
func validViewScope(scope string) bool {
	switch scope {
	case "/unread", "/read", "/favorites", "/bookmarks":
		return true
	}
	for _, prefix := range []string{"/authors/", "/collections/", "/lists/"} {
		if id, ok := strings.CutPrefix(scope, prefix); ok {
			if id == "" || strings.ContainsAny(id, "/?#") {
				return false
			}
			_, err := strconv.ParseInt(id, 10, 64)
			return err == nil
		}
	}
	return false
}

func (s *Server) renderReadItemsList(w http.ResponseWriter, r *http.Request, userID int64, tz string) {
	offset := historyOffset(r)
	items, more, _ := s.store.Items.ListReadHistory(userID, offset, pageSize)
	web.Render(w, r, ItemsSection(withTZ(tz, items), historyMore("/items?read=1", more, offset), false, s.store.ViewPrefs.Mode(userID, "/read")))
}

func (s *Server) renderItemsList(w http.ResponseWriter, r *http.Request, userID int64, tz string) {
	sort := itemSortOf(r)
	items, more, _ := s.store.Items.ListPage(userID, scopedFilter("unread", sort, cursorID(r, sort), 0, 0, 0))
	base := "/items?sort=" + sort.param()
	web.Render(w, r, ItemsSection(withTZ(tz, items), pageCursorFor(base, items, more, sort, int(cursorID(r, sort))), false, s.store.ViewPrefs.Mode(userID, "/unread")))
}

// itemsFragment serves a "load more" page of rows for the home/read/favorites/
// bookmarks lists. The fragment targets the existing #items-list element.
func (s *Server) itemsFragment(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	// The history list is ordered by read_at and offset-paged, so it does not
	// share the sort-keyed cursor path below.
	if r.URL.Query().Get("read") == "1" {
		offset := historyOffset(r)
		items, more, err := s.store.Items.ListReadHistory(u.ID, offset, pageSize)
		if err != nil {
			log.Error("history fragment", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		web.Render(w, r, ItemsPage(withTZ(u.Timezone, items), historyMore("/items?read=1", more, offset), false))
		return
	}
	sort := itemSortOf(r)
	base := "/items"
	view := "unread"
	switch {
	case r.URL.Query().Get("fav") == "1":
		view, base = "favorites", "/items?fav=1"
	case r.URL.Query().Get("bookmark") == "1":
		view, base = "bookmarks", "/items?bookmark=1"
	}
	filter := scopedFilter(view, sort, cursorID(r, sort), 0, 0, 0)
	base += "&sort=" + sort.param()
	items, more, err := s.store.Items.ListPage(u.ID, filter)
	if err != nil {
		log.Error("items fragment", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	web.Render(w, r, ItemsPage(withTZ(u.Timezone, items), pageCursorFor(base, items, more, sort, int(cursorID(r, sort))), false))
}

// withTZ stamps the user's timezone onto each item so templates can render
// relative timestamps.
func withTZ(tz string, items []store.ItemWithFeed) []store.ItemWithFeed {
	for i := range items {
		items[i].Timezone = tz
	}
	return items
}

type itemViewData struct {
	ID          int64
	GUID        string
	Title       string
	AuthorName  string
	AuthorID    int64
	FeedID      int64
	PublishedAt string
	Summary     string
	ImageURL    string
	Categories  []string
	// ImageCacheKey is the object-storage key of the cached primary image, "" when
	// not cached. When set (and this view may use the authenticated /cache route)
	// the image renders from cache instead of the remote URL.
	ImageCacheKey string
	Kind          store.ItemKind // plugin-supplied card kind (KindText default)
	DurationSec   int
	Link          string
	Body          template.HTML
	SourceURL     string                  // external destination of a link post
	EmbedSrc      string                  // iframe src from the destination's oEmbed
	Gallery       []string                // full-res images of a gallery post
	FeedIsSystem  bool                    // true for a saved page (hidden feed/author, no internal links)
	Attribution   []store.AttributionPart // source line from the item's decoration; author/feed when empty
	Enclosures    []store.Enclosure
	ShareToken    string // public share token, "" when the item is not shared
	Timezone      string // user's IANA timezone, for relative timestamps in templates
	Favorite      bool   // drives the modal's favorite toggle
	Bookmark      bool   // drives the modal's bookmark toggle
	Read          bool   // drives the modal's read/unread toggle (true after auto-mark)
	ProxyImages   bool   // route remote images through /img (authenticated modal only)
}

// imgSrc returns a remote image URL as it should be rendered: proxied through
// /img when the view may use that authenticated route, unchanged otherwise.
func (d itemViewData) imgSrc(u string) string {
	if d.ProxyImages {
		return web.ProxiedImageURL(u)
	}
	return u
}

// cachedImageSrc returns the cached primary image when this item has one and the
// view may use the authenticated /cache route; else the normal proxy/remote path.
// It only applies when remote is the item's own ImageURL (a body image picked by
// BestImageURL is not what was cached).
func (d itemViewData) cachedImageSrc(remote string) string {
	if d.ImageCacheKey != "" && d.ProxyImages && remote == d.ImageURL {
		return web.CachedImageURL(d.ImageCacheKey, remote)
	}
	return d.imgSrc(remote)
}

// enclosureSrc returns an image enclosure's source: the cached copy when this item
// has one for it and the view may use /cache, else the remote URL through the
// usual proxy rule.
func (d itemViewData) enclosureSrc(e store.Enclosure) string {
	if e.CacheKey != "" && d.ProxyImages {
		return web.CachedImageURL(e.CacheKey, e.URL)
	}
	return d.imgSrc(e.URL)
}

// enclosurePoster returns a video/audio enclosure's poster frame: the
// enclosure's own poster when set, else the item's thumbnail (cached when
// available).
func (d itemViewData) enclosurePoster(e store.Enclosure) string {
	if e.Poster != "" {
		return d.imgSrc(e.Poster)
	}
	return d.cachedImageSrc(d.ImageURL)
}

// itemBody returns the HTML an item's modal renders: a plugin-enriched body
// when present, else the feed's own summary. Classification (image posts) still
// uses the stored summary, so enrichment never changes a card's kind.
func itemBody(it store.ItemWithFeed) string {
	if it.Content != "" {
		return it.Content
	}
	return it.Summary
}

// bodyHTML returns the item body ready for rendering, proxying its images when
// the view may use /img.
func (d itemViewData) bodyHTML() string {
	if d.ProxyImages {
		return web.ProxyImageSrcs(string(d.Body))
	}
	return web.UpgradeImageSrcs(string(d.Body))
}

// itemView renders an item's stored content as a fragment, injected into the
// modal by the frontend.
func (s *Server) itemView(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	it, err := s.store.Items.OneWithFeed(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !it.Read {
		if err := s.store.Items.SetRead(u.ID, id, true); err != nil {
			log.Error("auto mark read on view", "item_id", id, "err", err)
		} else {
			it.Read = true
		}
	}
	data := itemViewData{
		ID:            it.ID,
		GUID:          it.GUID,
		Title:         it.Title,
		AuthorName:    it.AuthorName,
		AuthorID:      it.AuthorID,
		FeedID:        it.FeedID,
		PublishedAt:   it.PublishedAt,
		Summary:       it.Summary,
		ImageURL:      it.ImageURL,
		ImageCacheKey: it.ImageCacheKey,
		Categories:    it.Categories,
		Kind:          it.Kind,
		DurationSec:   it.DurationSec,
		Link:          it.Link,
		Body:          template.HTML(itemBody(it)),
		Timezone:      u.Timezone,
		Favorite:      it.Favorite,
		Bookmark:      it.Bookmark,
		Read:          it.Read,
		FeedIsSystem:  it.FeedIsSystem,
		Attribution:   it.Attribution,
		ProxyImages:   true,
	}
	// Enclosures are loaded before the render plugins so a renderer can see the
	// item's stored media.
	data.Enclosures, _ = s.store.Items.Enclosures(it.ID)
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	data.SourceURL, data.EmbedSrc, data.Gallery = s.resolveItemPlugins(ctx, data)
	if data.EmbedSrc == "" {
		data.EmbedSrc = s.resolveMediaEmbed(ctx, data)
	}
	if sh, err := s.store.Shares.ByItem(u.ID, it.ID); err == nil {
		data.ShareToken = sh.Token
	}
	web.Render(w, r, ItemView(data))
}

// resolveMediaEmbed returns an embeddable player for an item whose own link is
// a media page that publishes oEmbed. It runs only when the item carries a
// playable enclosure, so ordinary article links never trigger the fetch, and it
// skips a link that is itself a direct media file (nothing to discover there).
// An empty result means the item keeps its native media rendering.
func (s *Server) resolveMediaEmbed(ctx context.Context, it itemViewData) string {
	playable := false
	for _, e := range it.Enclosures {
		if k := web.EnclosureKind(e); k == "video" || k == "audio" {
			playable = true
			break
		}
	}
	if !playable || it.Link == "" {
		return ""
	}
	if web.EnclosureKind(store.Enclosure{URL: it.Link}) != "" {
		return ""
	}
	if e, err := s.oembed.Resolve(ctx, it.Link); err == nil && e.Src != "" {
		return e.Src
	}
	return ""
}

// itemShare creates a public share link for an item and re-renders the share
// control in the modal.
func (s *Server) itemShare(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := s.store.Items.ByID(u.ID, id); err != nil {
		http.NotFound(w, r)
		return
	}
	sh, err := s.store.Shares.Create(u.ID, id)
	if err != nil {
		log.Error("share item", "item_id", id, "err", err)
		http.Error(w, "share failed", http.StatusInternalServerError)
		return
	}
	web.Render(w, r, shareControl(id, sh.Token))
}

// itemRevokeShare removes an item's public share link.
func (s *Server) itemRevokeShare(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.Shares.Delete(u.ID, id); err != nil {
		log.Error("revoke share", "item_id", id, "err", err)
		http.Error(w, "revoke failed", http.StatusInternalServerError)
		return
	}
	web.Render(w, r, shareControl(id, ""))
}

// sharedPage serves an item publicly by its share token. It is intentionally
// unauthenticated, never marks the item read, and links only to external
// content — the author/feed pages require a login.
func (s *Server) sharedPage(w http.ResponseWriter, r *http.Request) {
	sh, err := s.store.Shares.ByToken(r.PathValue("token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	it, err := s.store.Items.OneWithFeedAny(sh.ItemID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data := itemViewData{
		ID:            it.ID,
		GUID:          it.GUID,
		Title:         it.Title,
		PublishedAt:   it.PublishedAt,
		Summary:       it.Summary,
		ImageURL:      it.ImageURL,
		ImageCacheKey: it.ImageCacheKey,
		Categories:    it.Categories,
		Kind:          it.Kind,
		DurationSec:   it.DurationSec,
		Link:          it.Link,
		Body:          template.HTML(itemBody(it)),
	}
	data.Enclosures, _ = s.store.Items.Enclosures(it.ID)
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	data.SourceURL, data.EmbedSrc, data.Gallery = s.resolveItemPlugins(ctx, data)
	if data.EmbedSrc == "" {
		data.EmbedSrc = s.resolveMediaEmbed(ctx, data)
	}
	web.Render(w, r, sharedItemPage(it, data))
}

func (s *Server) itemRead(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	it, err := s.store.Items.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.Items.SetRead(u.ID, id, !it.Read); err != nil {
		log.Error("set read", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// From the item modal there is no row to swap; return the fresh toggle pair
	// plus an out-of-band update of the row behind the dialog (if the list is
	// author-scoped, keep it author-scoped) so the two never disagree.
	if r.FormValue("view") == "1" {
		row, _ := s.store.Items.OneWithFeed(u.ID, id)
		row.Timezone = u.Timezone
		hideAuthor := isAuthorPageURL(r.Header.Get("HX-Current-URL"))
		web.Render(w, r, templ.Join(
			itemViewControls(id, it.Favorite, it.Bookmark, !it.Read),
			ItemRowOOB(row, hideAuthor),
		))
		return
	}
	row, err := s.store.Items.OneWithFeed(u.ID, id)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	row.Timezone = u.Timezone
	// Author-scoped pages suppress the (self-referential) author link; the row's
	// toggle buttons carry hideAuthor=1 so a swapped row stays consistent.
	web.Render(w, r, ItemRow(row, r.FormValue("hideAuthor") == "1"))
}

// itemReadBefore marks every unread item newer than this one (in the same feed)
// as read, then lets the client reload the list.
func (s *Server) itemReadBefore(w http.ResponseWriter, r *http.Request) {
	s.markRangeRead(w, r, true)
}

// itemReadAfter marks every unread item older than this one (in the same feed)
// as read, then lets the client reload the list.
func (s *Server) itemReadAfter(w http.ResponseWriter, r *http.Request) {
	s.markRangeRead(w, r, false)
}

// itemUnread marks a single item unread and re-renders its row so the list
// reflects the change, keeping the modal open. Used by the item modal's "mark
// as not read". The author link is suppressed when the request came from an
// author page, mirroring the read toggle.
func (s *Server) itemUnread(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := s.store.Items.ByID(u.ID, id); err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.Items.SetRead(u.ID, id, false); err != nil {
		log.Error("set unread", "item_id", id, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	row, err := s.store.Items.OneWithFeed(u.ID, id)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	row.Timezone = u.Timezone
	hideAuthor := r.FormValue("hideAuthor") == "1" || isAuthorPageURL(r.Header.Get("HX-Current-URL"))
	web.Render(w, r, ItemRow(row, hideAuthor))
}

// markRangeRead marks items newer (before) or older (after) than the target
// item as read. The target item itself is left alone. On an author page with no
// feed filter the range spans every feed owned by the item's author; on an
// author page filtered to one feed (?feed=) it is limited to that feed, and
// elsewhere to the item's own feed. The page is read from htmx's
// HX-Current-URL so no extra state is threaded through the menu.
func (s *Server) markRangeRead(w http.ResponseWriter, r *http.Request, before bool) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := s.store.Items.ByID(u.ID, id); err != nil {
		http.NotFound(w, r)
		return
	}
	currentURL := r.Header.Get("HX-Current-URL")
	authorScoped := isAuthorPageURL(currentURL)
	feedID := int64(0)
	if authorScoped {
		feedID = feedFilterID(currentURL)
	}
	var derr error
	switch {
	case authorScoped && feedID == 0 && before:
		derr = s.store.Items.MarkAuthorBeforeRead(u.ID, id)
	case authorScoped && feedID == 0:
		derr = s.store.Items.MarkAuthorAfterRead(u.ID, id)
	case before:
		derr = s.store.Items.MarkBeforeRead(u.ID, feedID, id)
	default:
		derr = s.store.Items.MarkAfterRead(u.ID, feedID, id)
	}
	if derr != nil {
		log.Error("mark range read", "item_id", id, "before", before, "author_scoped", authorScoped, "err", derr)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// isAuthorPageURL reports whether a page URL (htmx's HX-Current-URL) is an
// author page, e.g. "/authors/65". Used to broaden the bulk "mark all
// before/after as read" action from one feed to every feed of that author.
func isAuthorPageURL(rawurl string) bool {
	if rawurl == "" {
		return false
	}
	u, err := url.Parse(rawurl)
	if err != nil {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "authors" {
		return false
	}
	_, err = strconv.ParseInt(parts[1], 10, 64)
	return err == nil
}

// feedFilterID parses a single-feed filter (?feed=) out of a page URL, or 0
// when absent. Used to scope a range mark to the page's feed filter rather than
// the item's owner feed.
func feedFilterID(rawurl string) int64 {
	if rawurl == "" {
		return 0
	}
	u, err := url.Parse(rawurl)
	if err != nil {
		return 0
	}
	id, _ := strconv.ParseInt(u.Query().Get("feed"), 10, 64)
	return id
}

func (s *Server) itemFavorite(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	it, err := s.store.Items.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.Items.SetFavorite(u.ID, id, !it.Favorite); err != nil {
		log.Error("set favorite", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// From the item modal there is no row to swap; return the fresh toggle pair
	// plus an out-of-band update of the row behind the dialog (if the list is
	// author-scoped, keep it author-scoped) so the two never disagree.
	if r.FormValue("view") == "1" {
		row, _ := s.store.Items.OneWithFeed(u.ID, id)
		row.Timezone = u.Timezone
		hideAuthor := isAuthorPageURL(r.Header.Get("HX-Current-URL"))
		web.Render(w, r, templ.Join(
			itemViewControls(id, !it.Favorite, it.Bookmark, it.Read),
			ItemRowOOB(row, hideAuthor),
		))
		return
	}
	row, err := s.store.Items.OneWithFeed(u.ID, id)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	row.Timezone = u.Timezone
	web.Render(w, r, ItemRow(row, r.FormValue("hideAuthor") == "1"))
}

// itemBookmark toggles an item's bookmark state (save-for-later). It mirrors
// itemFavorite: from the modal it returns the fresh toggle set plus an OOB row
// update, otherwise it re-renders the row.
func (s *Server) itemBookmark(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	it, err := s.store.Items.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.Items.SetBookmark(u.ID, id, !it.Bookmark); err != nil {
		log.Error("set bookmark", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if r.FormValue("view") == "1" {
		row, _ := s.store.Items.OneWithFeed(u.ID, id)
		row.Timezone = u.Timezone
		hideAuthor := isAuthorPageURL(r.Header.Get("HX-Current-URL"))
		web.Render(w, r, templ.Join(
			itemViewControls(id, !it.Bookmark, it.Favorite, it.Read),
			ItemRowOOB(row, hideAuthor),
		))
		return
	}
	row, err := s.store.Items.OneWithFeed(u.ID, id)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	row.Timezone = u.Timezone
	web.Render(w, r, ItemRow(row, r.FormValue("hideAuthor") == "1"))
}

// itemDeleteSaved hard-deletes a saved page. It responds 204 and signals the
// client with HX-Trigger: item-deleted (carrying the id), so app.js drops the
// row and closes the modal; no swap body is needed.
func (s *Server) itemDeleteSaved(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.Items.DeleteSaved(u.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		log.Error("delete saved item", "item_id", id, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Trigger", `{"item-deleted":{"id":`+strconv.FormatInt(id, 10)+`}}`)
	w.WriteHeader(http.StatusNoContent)
}

// feedCreate is the global add flow: the URL auto-detect result is an author
// with their first feed attached. The author selection is required — creating
// one when "new" is chosen — and the response is the author's row, not the
// feed's: a new author is appended to the authors list, an existing one has
// its row (and feed count) refreshed via an HX-Retarget.
func (s *Server) feedCreate(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)

	if s.demoFeedLimitReached(u.ID) {
		writeFormError(w, r, "add-feed-error", demoAddFeedMessage())
		return
	}
	if errMsg := validateFeedFields(r); errMsg != "" {
		writeFormError(w, r, "add-feed-error", errMsg)
		return
	}
	if s.feedURLExists(u.ID, normalizeURL(r.FormValue("feed_url"))) {
		writeFormError(w, r, "add-feed-error", "you already have this feed")
		return
	}
	authorID, isNew, errMsg := s.resolveAuthor(r, u.ID)
	if errMsg != "" {
		writeFormError(w, r, "add-feed-error", errMsg)
		return
	}
	if _, errMsg = s.createFeed(r, u.ID, authorID); errMsg != "" {
		writeFormError(w, r, "add-feed-error", errMsg)
		return
	}
	// The share-target flow asks to land on the new author's page.
	if r.FormValue("redirect") == "1" {
		w.Header().Set("HX-Redirect", "/authors/"+strconv.FormatInt(authorID, 10))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	author, _ := s.store.Authors.ByID(u.ID, authorID)
	feeds, _ := s.store.Feeds.ListByAuthor(u.ID, authorID)
	unread, _ := s.store.Items.CountUnreadAuthor(u.ID, authorID)
	row := authorRow{Author: author, FeedCount: len(feeds), Unread: unread}
	if isNew {
		w.Header().Set("HX-Retarget", "#authors-list")
		w.Header().Set("HX-Reswap", "beforeend")
	} else {
		w.Header().Set("HX-Retarget", "#author-"+strconv.FormatInt(authorID, 10))
		w.Header().Set("HX-Reswap", "outerHTML")
	}
	web.Render(w, r, AuthorRow(row))
}

// authorFeedCreate adds a feed to a specific author from the author page. The
// author is fixed by the route, so the response is the new feed row appended
// to the author's feeds list.
func (s *Server) authorFeedCreate(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	authorID, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	author, err := s.store.Authors.ByID(u.ID, authorID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if s.demoFeedLimitReached(u.ID) {
		writeFormError(w, r, "add-feed-error", demoAddFeedMessage())
		return
	}
	if errMsg := validateFeedFields(r); errMsg != "" {
		writeFormError(w, r, "add-feed-error", errMsg)
		return
	}
	if s.feedURLExists(u.ID, normalizeURL(r.FormValue("feed_url"))) {
		writeFormError(w, r, "add-feed-error", "you already have this feed")
		return
	}
	f, errMsg := s.createFeed(r, u.ID, authorID)
	if errMsg != "" {
		writeFormError(w, r, "add-feed-error", errMsg)
		return
	}
	unread, _ := s.store.Items.CountUnread(u.ID, f.ID)
	web.Render(w, r, FeedRow(s.feedRowFor(u.ID, f, author.Name, u.Timezone, unread)))
}

// validateFeedFields checks the required title and feed url fields of the add
// form, returning a user-facing error message when either is missing.
func validateFeedFields(r *http.Request) string {
	title := strings.TrimSpace(r.FormValue("title"))
	feedURL := normalizeURL(r.FormValue("feed_url"))
	if title == "" || feedURL == "" {
		return "title and feed url are required"
	}
	return ""
}

// createFeed validates and persists a new feed under an author, attaching it
// to any selected collections, its site's auto collection, and caching the
// site icon. Returns the created feed, or a non-empty user-facing error
// message on failure.
func (s *Server) createFeed(r *http.Request, userID, authorID int64) (store.Feed, string) {
	title := strings.TrimSpace(r.FormValue("title"))
	feedURL := normalizeURL(r.FormValue("feed_url"))
	homeURL := r.FormValue("home_url")
	interval, _ := strconv.Atoi(r.FormValue("poll_interval_sec"))
	if interval <= 0 {
		interval = 900
	}
	if title == "" || feedURL == "" {
		return store.Feed{}, "title and feed url are required"
	}
	f, err := s.store.Feeds.CreateWithPlugin(userID, authorID, title, feedURL, homeURL, "",
		s.pluginNameFor(feedURL), interval)
	if err != nil {
		log.Error("create feed", "err", err)
		return store.Feed{}, "could not create feed"
	}
	// A plugin whose images expire forces caching on; the user cannot disable it
	// later. A feed with no forcing plugin starts with caching off (the user may
	// enable it from the feed's edit page).
	if s.imageCacheForced(feedURL) {
		if err := s.store.Feeds.SetCacheImages(userID, f.ID, true); err != nil {
			log.Error("force feed image cache", "feed_id", f.ID, "err", err)
		} else {
			f.CacheImages = true
		}
	}
	for _, cid := range r.Form["collections"] {
		if n, err := strconv.ParseInt(cid, 10, 64); err == nil {
			s.store.Collections.AddFeed(userID, n, f.ID)
		}
	}
	if err := s.store.Collections.AssignAuto(userID, f.ID, homeURL, feedURL); err != nil {
		log.Error("assign auto collection", "feed_id", f.ID, "err", err)
	}
	// Auto-cache the site's favicon when the form supplied a home url. Best
	// effort and bounded so a slow site can't stall the create response. A
	// plugin-owned page is skipped: a page fetch shares the host's request budget
	// with the feed (a tight anonymous limit), and the immediate poll needs that
	// budget more.
	if homeURL != "" && !s.urlPolicyOwned(homeURL) {
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		s.autoCacheFeedIcon(ctx, userID, homeURL)
		cancel()
	}
	// Kick off the first poll right away so the feed's items show up without
	// waiting for the next poller tick. Fire-and-forget: the poller's http
	// client bounds the fetch, and a slow feed can't stall the add response.
	s.pollFeedNow(f)
	return f, ""
}

// pluginNameFor reports the name of the plugin that owns feedURL for fetching,
// or "" when the generic parser does. It is recorded on the feed so the startup
// reconciler can detect a missing plugin later.
func (s *Server) pluginNameFor(feedURL string) string {
	if s.plugins == nil || s.plugins.Empty() {
		return ""
	}
	u, err := url.Parse(feedURL)
	if err != nil {
		return ""
	}
	if f := s.plugins.Match(u, pluginapi.CapFetch); f != nil {
		return f.Meta().Name
	}
	return ""
}

// imageCacheForced reports whether a feed's plugin forces host-side image
// caching on (it claims CapImageCache for the URL), so the user cannot disable
// it. It is evaluated against the plugin registry, never stored, so it tracks
// which plugins are loaded.
func (s *Server) imageCacheForced(feedURL string) bool {
	if s.plugins == nil || s.plugins.Empty() {
		return false
	}
	return s.plugins.CachesImages(feedURL)
}

// pollFeedNow triggers an immediate first poll of a newly added feed. It is
// best-effort and detached from the request: when no poller is attached (e.g.
// in tests) or the feed is disabled, it does nothing.
func (s *Server) pollFeedNow(f store.Feed) {
	if s.poller == nil || !f.Enabled {
		return
	}
	go s.poller.PollOne(context.Background(), f)
}

// resolveAuthor maps the feed form's author selection to an author id, creating
// a new author when the selection is "new". An author is required on every feed,
// so an empty selection is an error. It returns a non-empty error message when
// the selection is invalid.
func (s *Server) resolveAuthor(r *http.Request, userID int64) (int64, bool, string) {
	switch r.FormValue("author_id") {
	case "new":
		name := strings.TrimSpace(r.FormValue("author_name"))
		if name == "" {
			return 0, false, "new author needs a name"
		}
		a, err := s.store.Authors.Create(
			userID, name, r.FormValue("avatar_url"), "",
		)
		if err != nil {
			log.Error("create author", "err", err)
			return 0, false, "could not create author"
		}
		if a.AvatarURL != "" {
			ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
			s.autoCacheAuthorAvatar(ctx, a)
			cancel()
		}
		return a.ID, true, ""
	default:
		id, err := strconv.ParseInt(r.FormValue("author_id"), 10, 64)
		if err != nil || id == 0 {
			return 0, false, "a feed needs an author — create one or pick an existing author"
		}
		if _, err := s.store.Authors.ByID(userID, id); err != nil {
			return 0, false, "author not found"
		}
		return id, false, ""
	}
}

// feedEdit is the legacy feed edit URL. Editing now lives on the combined
// author edit page, so this redirects there with the feed selected.
func (s *Server) feedEdit(w http.ResponseWriter, r *http.Request) {
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
	http.Redirect(w, r, "/authors/"+strconv.FormatInt(f.AuthorID, 10)+"/edit?feed="+strconv.FormatInt(f.ID, 10), http.StatusFound)
}

// feedEditFragment returns one feed's editor body for the author edit page's
// lazy feed accordion. Ownership is checked through Feeds.ByID.
func (s *Server) feedEditFragment(w http.ResponseWriter, r *http.Request) {
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
	web.Render(w, r, feedEditorBody(*s.feedEditorData(u, f)))
}

// feedEditorData assembles the editor for a single feed: its form fields, the
// collections it belongs to, its plugin docs link and its plugin panel. It backs
// the combined author edit page's ?feed= editor.
func (s *Server) feedEditorData(u store.User, f store.Feed) *feedsData {
	form := feedForm{
		ID: f.ID, Title: f.Title, FeedURL: f.FeedURL, HomeURL: f.HomeURL,
		Description: f.Description, AuthorID: f.AuthorID,
		PollIntervalSec: f.PollIntervalSec, PollIntervalAuto: f.PollIntervalAuto, Enabled: f.Enabled,
		CacheImages: f.CacheImages, CacheForced: s.imageCacheForced(f.FeedURL),
	}
	form.CollectionIDs = s.collectionIDsForFeed(u.ID, f.ID)
	collections, _ := s.store.Collections.List(u.ID)

	var panel *feedPanelData
	if p, ok := s.feedPanel(f, ""); ok {
		panel = &p
	}
	stats, _ := s.store.Items.StatsFeed(u.ID, f.ID)
	return &feedsData{
		Collections: collections, Form: form, DocsPlugin: s.pluginDocNameFor(f.FeedURL), Panel: panel,
		NextPageURL: f.NextPageURL, Stats: stats, Timezone: u.Timezone,
		LastError: f.LastError, LastPolledAt: f.LastPolledAt, PluginName: f.PluginName,
	}
}

func (s *Server) collectionIDsForFeed(userID, feedID int64) []int64 {
	ids, _ := s.store.Collections.FeedCollectionIDs(userID, feedID)
	return ids
}

func (s *Server) feedUpdate(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	old, err := s.store.Feeds.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	title := r.FormValue("title")
	feedURL := normalizeURL(r.FormValue("feed_url"))
	interval, _ := strconv.Atoi(r.FormValue("poll_interval_sec"))
	if interval <= 0 {
		interval = 900
	}
	auto := r.FormValue("poll_interval_auto") == "1"
	if auto {
		// Adaptive polling owns the interval; keep the stored value.
		interval = old.PollIntervalSec
	}
	back := "/authors/" + strconv.FormatInt(old.AuthorID, 10) + "/edit?feed=" + strconv.FormatInt(id, 10)
	if title == "" || feedURL == "" {
		http.Redirect(w, r, back, http.StatusFound)
		return
	}
	// Editing happens on the feed's author's page, so the feed keeps its author.
	authorID := old.AuthorID
	cacheImages := r.FormValue("cache_images") == "1" || s.imageCacheForced(feedURL)
	if err := s.store.Feeds.Update(u.ID, id, authorID, title, feedURL,
		r.FormValue("home_url"), "", interval, auto, r.FormValue("enabled") == "1", cacheImages); err != nil {
		log.Error("update feed", "err", err)
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
	// Auto collections track the feed's website; re-assign when its host
	// changes, and drop the feed from its old site collection.
	if err := s.store.Collections.UnassignAuto(u.ID, id, old.HomeURL, old.FeedURL); err != nil {
		log.Error("unassign auto collection", "feed_id", id, "err", err)
	}
	if err := s.store.Collections.AssignAuto(u.ID, id, r.FormValue("home_url"), feedURL); err != nil {
		log.Error("assign auto collection", "feed_id", id, "err", err)
	}
	// Sync collection memberships.
	form := feedForm{}
	collections, _ := s.store.Collections.List(u.ID)
	for _, cid := range r.Form["collections"] {
		if n, err := strconv.ParseInt(cid, 10, 64); err == nil {
			form.CollectionIDs = append(form.CollectionIDs, n)
		}
	}
	form.CollectionIDs = unique(form.CollectionIDs)
	for _, c := range collections {
		if c.IsAuto {
			continue
		}
		has := false
		for _, want := range form.CollectionIDs {
			if want == c.ID {
				has = true
				break
			}
		}
		feeds, _ := s.store.Collections.Feeds(u.ID, c.ID)
		contains := false
		for _, f := range feeds {
			if f.ID == id {
				contains = true
				break
			}
		}
		switch {
		case has && !contains:
			s.store.Collections.AddFeed(u.ID, c.ID, id)
		case !has && contains:
			s.store.Collections.RemoveFeed(u.ID, c.ID, id)
		}
	}
	http.Redirect(w, r, "/authors/"+strconv.FormatInt(authorID, 10)+"/edit?feed="+strconv.FormatInt(id, 10), http.StatusFound)
}

func unique(ids []int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// normalizeURL trims s and prepends https:// when no scheme is present, so
// user-entered URLs like "x.com/metruzanca" and "https://x.com/metruzanca"
// are equivalent.
func normalizeURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return s
	}
	return "https://" + s
}

// stripWWW removes a leading "www." subdomain from a URL's host, preserving the
// rest of the host's case and any port. Auto-filled urls (the find-author
// preview form's feed/home fields, new-author prefill) are
// cleaned through this so "https://www.example.com" shows up as
// "https://example.com". A URL a plugin owns is exempt: its canonical shape
// (which serves a feed without a redirect) must be preserved, so rewriting it
// would spend a request from the host's tight rate-limit budget. Returns s
// unchanged when it can't be parsed.
func (s *Server) stripWWW(raw string) string {
	if s.urlPolicyKeepsHost(raw) {
		return raw
	}
	return stripWWW(raw)
}

// urlPolicyKeepsHost reports whether a plugin's URL policy keeps the URL's host
// as-is (its canonical form differs from the generic www-stripping). It is how
// the preview form knows not to rewrite a plugin-owned URL.
func (s *Server) urlPolicyKeepsHost(raw string) bool {
	return s.urlPolicyOwned(raw)
}

// urlPolicyOwned reports whether a plugin owns URL rules for raw. A plugin-owned
// page shares its request budget with the feed (a tight anonymous limit), so
// page metadata fetches for icon/avatar discovery must be skipped.
func (s *Server) urlPolicyOwned(raw string) bool {
	if s.plugins == nil || s.plugins.Empty() {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return s.plugins.URLPolicy(u) != nil
}

// stripWWW is the plugin-agnostic www-stripper (see Server.stripWWW).
func stripWWW(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	host := u.Hostname()
	if !strings.HasPrefix(strings.ToLower(host), "www.") || len(host) <= 4 {
		return s
	}
	rest := host[4:]
	if p := u.Port(); p != "" {
		u.Host = net.JoinHostPort(rest, p)
	} else {
		u.Host = rest
	}
	return u.String()
}

// purgeObjects deletes the given object-storage keys, best-effort. A flaky
// store must never block a feed/account mutation that already committed.
func (s *Server) purgeObjects(ctx context.Context, keys []string) {
	for _, k := range keys {
		if err := s.files.Delete(ctx, k); err != nil {
			log.Warn("purge object", "key", k, "err", err)
		}
	}
}

func (s *Server) feedDelete(w http.ResponseWriter, r *http.Request) {
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
	cacheKeys, err := s.store.Feeds.Delete(u.ID, id)
	if err != nil {
		log.Error("delete feed", "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	s.purgeObjects(r.Context(), cacheKeys)
	// When the user opted in and a plugin manages this feed, delete the remote
	// feed too. Best-effort, after the local delete: a remote failure must not
	// block removing the feed from nanoflux, so it is only logged.
	if deleteRemoteRequested(r) {
		if fa, pf := s.feedAdmin(f.FeedURL); fa != nil {
			if _, err := fa.Action(r.Context(), pluginapi.FeedActionRequest{
				FeedURL: f.FeedURL, Action: "delete",
			}, s.pluginHosts.For(pf)); err != nil {
				log.Error("delete remote feed", "feed_id", id, "err", err)
			}
		}
	}
	http.Redirect(w, r, "/authors/"+strconv.FormatInt(f.AuthorID, 10), http.StatusSeeOther)
}

func (s *Server) feedRefresh(w http.ResponseWriter, r *http.Request) {
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
	// Respect a rate-limit backoff: don't re-hit a host that asked us to wait.
	// The row already shows the retry deadline, so a click during the window is
	// a no-op rather than another 429.
	newItems := 0
	if !cooling(f.NextPollAt) && s.poller != nil {
		if n, err := s.poller.PollOne(r.Context(), f); err != nil {
			log.Error("refresh feed", "feed_id", id, "err", err)
		} else {
			newItems = n
		}
		f, _ = s.store.Feeds.ByID(u.ID, id)
	}
	unread, _ := s.store.Items.CountUnread(u.ID, id)
	author, _ := s.store.Authors.ByID(u.ID, f.AuthorID)
	row := s.feedRowFor(u.ID, f, author.Name, u.Timezone, unread)
	// A refresh that stored new posts also updates the author page's item list
	// (the feed "on the author"), so the new posts appear without a reload. Only
	// when the visitor is on this feed's author page: the list is rebuilt from
	// htmx's HX-Current-URL so its view, sort, feed filter and tag filters are
	// preserved rather than reset. The row is the primary swap; the list rides
	// along as an out-of-band swap.
	if newItems > 0 {
		if scoped, ok := s.authorScopedFromCurrentURL(u.ID, u.Timezone, r, f.AuthorID); ok {
			web.Render(w, r, templ.Join(FeedRow(row), ScopedItems(scoped)))
			return
		}
	}
	web.Render(w, r, FeedRow(row))
}

// authorScopedFromCurrentURL rebuilds the author page's current scoped item list
// from htmx's HX-Current-URL, so a manual refresh can out-of-band replace the
// list the visitor is looking at (honoring its view, sort, feed filter and tag
// filters) instead of resetting it to the default. ok is false when the current
// page is not this author's page, so no list is swapped (e.g. a refresh from the
// feed settings page or a collection).
func (s *Server) authorScopedFromCurrentURL(userID int64, tz string, r *http.Request, authorID int64) (scopedItemsData, bool) {
	raw := r.Header.Get("HX-Current-URL")
	if raw == "" {
		return scopedItemsData{}, false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return scopedItemsData{}, false
	}
	if u.Path != "/authors/"+strconv.FormatInt(authorID, 10) {
		return scopedItemsData{}, false
	}
	view := normalizeAuthorView(u.Query().Get("view"))
	sort := itemSortFromParam(u.Query().Get("sort"))
	feedID := feedFilterID(raw)
	scoped := s.authorScopedItems(userID, authorID, feedID, view, sort, tz, tagsFromValues(u.Query()))
	scoped.SwapOOB = true
	return scoped, true
}

// cooling reports whether a feed's stored NextPollAt is in the future, i.e. the
// host asked us to wait after a rate limit. An empty or unparseable value means
// not cooling.
func cooling(nextPollAt string) bool {
	if nextPollAt == "" {
		return false
	}
	t, err := db.ParseTime(nextPollAt)
	if err != nil {
		return false
	}
	return time.Now().Before(t)
}

// feedOlder fetches the next page of a paginated feed ("load older items") and
// re-renders the control in the feed's settings panel. The response reports how
// many items and unread the load produced, or an error when the fetch fails, so
// a click always gives visible feedback.
func (s *Server) feedOlder(w http.ResponseWriter, r *http.Request) {
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
	data := s.feedHistoryData(u.ID, f, u.Timezone)
	renderCard := func(status int) {
		if status != http.StatusOK {
			w.WriteHeader(status)
		}
		web.Render(w, r, feedHistoryCard(data))
	}
	if f.NextPageURL == "" {
		// History already exhausted: keep "full history loaded" without an error.
		renderCard(http.StatusOK)
		return
	}
	if s.poller == nil {
		data.Err = "could not load older items"
		renderCard(http.StatusBadRequest)
		return
	}
	newItems, _, err := s.poller.PollOlder(r.Context(), f)
	if err != nil {
		log.Error("load older items", "feed_id", id, "err", err)
		data.Err = "could not load older items"
		renderCard(http.StatusBadRequest)
		return
	}
	if newItems > 0 {
		log.Info("loaded older items", "feed_id", id, "new_items", newItems)
	}
	// Re-read the feed and its stats: PollOlder advanced (or cleared) the
	// next-page cursor and the load changed the counts and oldest post.
	f, err = s.store.Feeds.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data = s.feedHistoryData(u.ID, f, u.Timezone)
	data.Message = "loaded " + strconv.Itoa(newItems) + " items · " + strconv.Itoa(data.Unread) + " unread"
	renderCard(http.StatusOK)
}

// feedHistoryData assembles a feed's history card data (counts, oldest post and
// the older-history cursor).
func (s *Server) feedHistoryData(userID int64, f store.Feed, tz string) feedHistoryData {
	stats, _ := s.store.Items.StatsFeed(userID, f.ID)
	return feedHistoryData{
		FeedID: f.ID, Unread: stats.Unread, Read: stats.Read,
		OldestAt: stats.FirstAt, Timezone: tz, NextPageURL: f.NextPageURL,
	}
}

// feedToggle pauses or resumes a feed's polling and re-renders its row.
func (s *Server) feedToggle(w http.ResponseWriter, r *http.Request) {
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
	if err := s.store.Feeds.SetEnabled(u.ID, id, !f.Enabled); err != nil {
		log.Error("toggle feed", "feed_id", id, "err", err)
		http.Error(w, "toggle failed", http.StatusInternalServerError)
		return
	}
	f.Enabled = !f.Enabled
	unread, _ := s.store.Items.CountUnread(u.ID, id)
	author, _ := s.store.Authors.ByID(u.ID, f.AuthorID)
	web.Render(w, r, FeedRow(s.feedRowFor(u.ID, f, author.Name, u.Timezone, unread)))
}

// feedRank sets a feed's magic-sort manual lever (-1 lowered, 0 neutral, +1
// raised) from the algorithm editor and re-renders just its control.
func (s *Server) feedRank(w http.ResponseWriter, r *http.Request) {
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
	rank, _ := strconv.Atoi(r.FormValue("rank"))
	if rank < -1 {
		rank = -1
	} else if rank > 1 {
		rank = 1
	}
	if err := s.store.Feeds.SetRank(u.ID, id, rank); err != nil {
		log.Error("set feed rank", "feed_id", id, "err", err)
		http.Error(w, "could not set rank", http.StatusInternalServerError)
		return
	}
	web.Render(w, r, rankControl(f.ID, rank))
}

// favoritesAlgorithmPage is the magic-sort editor: every feed with its favorite
// count (the taste signal) and a manual raise/lower control. It makes the
// ranking transparent and adjustable, so the "magic" never feels arbitrary.
func (s *Server) favoritesAlgorithmPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	rows, _ := s.store.Feeds.ListWithFavorites(u.ID)
	web.Render(w, r, basePage("favorites algorithm", u, favoritesAlgorithmPage(u, algorithmData{Rows: rows})))
}

func (s *Server) authors(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	rows := s.authorRows(u.ID)
	// Default sort is most unread first (the sort picker's default); app.js
	// reorders client-side once the user picks another mode. Ties break by name
	// so the order is stable.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Unread != rows[j].Unread {
			return rows[i].Unread > rows[j].Unread
		}
		return rows[i].Name < rows[j].Name
	})
	web.Render(w, r, basePage("authors", u, authorsPage(u, authorsData{Rows: rows})))
}

func (s *Server) authorRows(userID int64) []authorRow {
	rows, _ := s.store.Authors.ListWithFeedCount(userID)
	out := make([]authorRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, authorRow{
			Author: r.Author, FeedCount: r.FeedCount, Unread: r.UnreadCount,
			Taste: authorTaste(r.MaxRank, r.FavCount),
		})
	}
	return out
}

// authorTaste builds the author index's magic-sort key: raised authors rank
// above neutral above lowered, and within a tier the author with more
// favorites ranks higher. Kept in one place so the client sort and any server
// tie-break agree.
func authorTaste(maxRank, favCount int) int {
	tier := 1 // neutral
	if maxRank > 0 {
		tier = 2 // raised: the author has at least one raised feed
	} else if maxRank < 0 {
		tier = 0 // lowered: every feed is lowered
	}
	return tier*1_000_000_000 + favCount
}

func (s *Server) authorCreate(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		writeFormError(w, r, "add-author-error", "name is required")
		return
	}
	a, err := s.store.Authors.Create(u.ID, name, r.FormValue("avatar_url"), r.FormValue("description"))
	if err != nil {
		log.Error("create author", "err", err)
		writeFormError(w, r, "add-author-error", "could not create author")
		return
	}
	if a.AvatarURL != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		s.autoCacheAuthorAvatar(ctx, a)
		cancel()
	}
	// A standalone author has no feeds yet, so land on the new author's page
	// where feeds can be added.
	w.Header().Set("HX-Redirect", "/authors/"+strconv.FormatInt(a.ID, 10))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) authorPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a, err := s.store.Authors.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rows, err := s.feedRowsForAuthor(u.ID, id, u.Timezone)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	links, _ := s.store.AuthorLinks.ListByAuthor(u.ID, id)
	feedID := s.authorFeedFilter(u.ID, id, r)
	tags := tagParams(r)
	scoped := s.authorScopedItems(u.ID, id, feedID, authorView(r), itemSortOf(r), u.Timezone, tags)
	stats, _ := s.store.Items.StatsAuthor(u.ID, id)
	frequency := ""
	if times, err := s.store.Items.AuthorRecentTimes(u.ID, id, 30); err == nil {
		if gap, ok := web.AverageGapSeconds(times); ok {
			frequency = web.PostFrequency(gap)
		}
	}
	// The header's "mark all read" is always shown and honors the active
	// feed/tag filter, so it marks exactly the unread items the list shows.
	unread, _ := s.store.Items.CountUnreadFiltered(u.ID, store.ItemFilter{AuthorID: id, FeedID: feedID, Tags: tags})
	markAll := markAllReadData{Action: authorReadAllURL(id, feedID, tags), Unread: unread}
	web.Render(w, r, basePage(a.Name, u, authorPage(u, authorData{
		Author: a, Rows: rows, Links: links, Scoped: scoped, Collections: authorCollections(rows),
		Stats: stats, Frequency: frequency, Timezone: u.Timezone,
		MarkAll: markAll,
	})))
}

// authorLinks loads an author's external bookmarks for the edit form.
func (s *Server) authorLinks(userID, authorID int64) []store.AuthorLink {
	links, _ := s.store.AuthorLinks.ListByAuthor(userID, authorID)
	return links
}

// reconcileAuthorLinks applies the edit form's link rows: each row is a
// link_id plus link_label/link_url. A row with an id updates the existing link;
// a row with no id creates a new one; a row whose url is blank/invalid deletes
// its link. Any existing link whose id is not submitted was removed from the
// form (its ✕ took it out of the DOM), so it is deleted too. A "links_present"
// marker guards the lot: a post without the link fields (e.g. an API client)
// leaves the links untouched.
func (s *Server) reconcileAuthorLinks(userID, authorID int64, r *http.Request) {
	if r.FormValue("links_present") == "" {
		return
	}
	existing, _ := s.store.AuthorLinks.ListByAuthor(userID, authorID)
	ids := r.Form["link_id"]
	labels := r.Form["link_label"]
	urls := r.Form["link_url"]
	kept := make(map[int64]bool, len(ids))
	for i := range urls {
		var id int64
		if i < len(ids) {
			id, _ = strconv.ParseInt(ids[i], 10, 64)
		}
		var label string
		if i < len(labels) {
			label = strings.TrimSpace(labels[i])
		}
		linkURL := normalizeURL(urls[i])
		valid := linkURL != "" && invalidURL(linkURL) == ""
		if id == 0 {
			if valid {
				if _, err := s.store.AuthorLinks.Create(userID, authorID, label, linkURL); err != nil {
					log.Error("create author link", "err", err)
				}
			}
			continue
		}
		kept[id] = true
		if !valid {
			if err := s.store.AuthorLinks.Delete(userID, id); err != nil && err != store.ErrNotFound {
				log.Error("delete author link", "link_id", id, "err", err)
			}
			continue
		}
		if err := s.store.AuthorLinks.Update(userID, id, label, linkURL); err != nil && err != store.ErrNotFound {
			log.Error("update author link", "link_id", id, "err", err)
		}
	}
	for _, e := range existing {
		if kept[e.ID] {
			continue
		}
		if err := s.store.AuthorLinks.Delete(userID, e.ID); err != nil && err != store.ErrNotFound {
			log.Error("delete removed author link", "link_id", e.ID, "err", err)
		}
	}
}

// authorFeedFilter reads ?feed= and returns it only when it names a feed owned
// by the author, else 0. It scopes the author's item list to one feed, the
// replacement for the removed /feeds/{id} page.
func (s *Server) authorFeedFilter(userID, authorID int64, r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.URL.Query().Get("feed"), 10, 64)
	if id == 0 {
		return 0
	}
	f, err := s.store.Feeds.ByID(userID, id)
	if err != nil || f.AuthorID != authorID {
		return 0
	}
	return id
}

// authorScopedItems loads one item list (unread/read/favorites) for an author
// plus the counts that drive the tabs. When feedID is non-zero the list and
// counts are scoped to that single feed instead of the whole author.
func (s *Server) authorScopedItems(userID, authorID, feedID int64, view string, sort itemSort, tz string, tags []string) scopedItemsData {
	items, more, _ := s.store.Items.ListPage(userID, scopedFilter(view, sort, 0, feedID, authorID, 0, tags...))
	var unread, read, favs int
	var feedTitle string
	tagFilter := store.ItemFilter{AuthorID: authorID}
	if feedID != 0 {
		unread, _ = s.store.Items.CountUnread(userID, feedID)
		read, _ = s.store.Items.CountRead(userID, feedID)
		favs, _ = s.store.Items.CountFavorites(userID, feedID)
		tagFilter = store.ItemFilter{FeedID: feedID}
		if f, err := s.store.Feeds.ByID(userID, feedID); err == nil {
			feedTitle = f.Title
		}
	} else {
		unread, _ = s.store.Items.CountUnreadAuthor(userID, authorID)
		read, _ = s.store.Items.CountReadAuthor(userID, authorID)
		favs, _ = s.store.Items.CountFavoritesAuthor(userID, authorID)
	}
	base := "/authors/" + strconv.FormatInt(authorID, 10)
	itemsBase := appendTags(scopedViewQuery(base+"/items", view, feedID)+"&sort="+sort.param(), tags)
	cats, _ := s.store.Items.ListCategories(userID, tagFilter)
	authorFeeds, _ := s.store.Feeds.ListByAuthorWithUnread(userID, authorID)
	feedOpts := make([]comboItem, 0, len(authorFeeds))
	for _, f := range authorFeeds {
		feedOpts = append(feedOpts, comboItem{Value: strconv.FormatInt(f.Feed.ID, 10), Label: f.Feed.Title})
	}
	return scopedItemsData{
		Path: base, ItemsPath: base + "/items", View: view, Sort: sort,
		UnreadCount: unread, ReadCount: read, Items: withTZ(tz, dedupItems(items)),
		More: pageCursorFor(itemsBase, items, more, sort, 0), HideAuthor: true,
		Mode: s.store.ViewPrefs.Mode(userID, base), FavTab: favs > 0 || view == "favorites", FavCount: favs,
		Tags: tags, TagOptions: tagItems(cats),
		FeedID: feedID, FeedTitle: feedTitle, FeedOptions: feedOpts,
	}
}

func (s *Server) authorItems(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	view := authorView(r)
	sort := itemSortOf(r)
	tags := tagParams(r)
	feedID := s.authorFeedFilter(u.ID, id, r)
	if cursor := cursorID(r, sort); cursor > 0 {
		items, more, err := s.store.Items.ListPage(u.ID, scopedFilter(view, sort, cursor, feedID, id, 0, tags...))
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		base := appendTags(scopedViewQuery("/authors/"+strconv.FormatInt(id, 10)+"/items", view, feedID)+"&sort="+sort.param(), tags)
		web.Render(w, r, ItemsPage(withTZ(u.Timezone, dedupItems(items)), pageCursorFor(base, items, more, sort, int(cursor)), true))
		return
	}
	web.Render(w, r, ScopedItems(s.authorScopedItems(u.ID, id, feedID, view, sort, u.Timezone, tags)))
}

// feedRowFor builds a single feed card row with its collection tags and cached
// media, for the feed row fragments the add/refresh/toggle handlers render.
func (s *Server) feedRowFor(userID int64, f store.Feed, authorName, tz string, unread int) feedRow {
	byFeed, _ := s.store.Collections.CollectionsByAuthorFeed(userID, f.AuthorID)
	storage, _ := s.store.Items.StorageByFeed(f.ID)
	return feedRow{
		Feed: f, AuthorName: authorName, Unread: unread, Timezone: tz,
		Storage:     storage,
		Collections: byFeed[f.ID],
	}
}

// authorFeedURL is the author page scoped to a single feed, the replacement for
// the removed /feeds/{id} page. It returns "" when the author is unknown.
func authorFeedURL(authorID, feedID int64) string {
	if authorID == 0 {
		return ""
	}
	return "/authors/" + strconv.FormatInt(authorID, 10) + "?feed=" + strconv.FormatInt(feedID, 10)
}

// authorTagHref is the author page filtered to one tag, for the item modal's
// tag chips. It returns "" when the author is unknown (a saved page).
func authorTagHref(authorID int64, tag string) string {
	if authorID == 0 {
		return ""
	}
	return "/authors/" + strconv.FormatInt(authorID, 10) + "?tags=" + url.QueryEscape(tag)
}

// feedIconURL is the URL a feed's source icon is looked up from: its home page,
// falling back to the feed URL. Some feed URLs (API endpoints, scrape targets,
// ...) have no favicon while the home page does.
func feedIconURL(f store.Feed) string {
	if strings.TrimSpace(f.HomeURL) != "" {
		return f.HomeURL
	}
	return f.FeedURL
}

// itemIconURL is feedIconURL for an item's joined feed.
func itemIconURL(it store.ItemWithFeed) string {
	if strings.TrimSpace(it.FeedHomeURL) != "" {
		return it.FeedHomeURL
	}
	return it.FeedURL
}

func (s *Server) feedRowsForAuthor(userID, authorID int64, tz string) ([]feedRow, error) {
	rows, err := s.store.Feeds.ListByAuthorWithUnread(userID, authorID)
	if err != nil {
		return nil, err
	}
	byFeed, _ := s.store.Collections.CollectionsByAuthorFeed(userID, authorID)
	out := make([]feedRow, 0, len(rows))
	for _, r := range rows {
		storage, _ := s.store.Items.StorageByFeed(r.Feed.ID)
		out = append(out, feedRow{
			Feed: r.Feed, AuthorName: r.AuthorName, Unread: r.Unread, Timezone: tz,
			Storage: storage, Collections: byFeed[r.Feed.ID],
		})
	}
	return out, nil
}

// authorCollections returns the distinct collections the author's feeds belong
// to, combined and deduped, ordered by name, for the author card's chips.
func authorCollections(rows []feedRow) []store.Collection {
	seen := make(map[int64]store.Collection)
	for _, r := range rows {
		for _, c := range r.Collections {
			seen[c.ID] = c
		}
	}
	out := make([]store.Collection, 0, len(seen))
	for _, c := range seen {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Server) authorEdit(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a, err := s.store.Authors.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	links, _ := s.store.AuthorLinks.ListByAuthor(u.ID, a.ID)
	feeds, _ := s.feedRowsForAuthor(u.ID, a.ID, u.Timezone)
	var editor *feedsData
	if fid, err := strconv.ParseInt(r.URL.Query().Get("feed"), 10, 64); err == nil && fid != 0 {
		if f, err := s.store.Feeds.ByID(u.ID, fid); err == nil && f.AuthorID == a.ID {
			editor = s.feedEditorData(u, f)
		}
	}
	web.Render(w, r, basePage("edit "+a.Name, u, authorEditPage(u, authorsData{
		Form:       authorForm{ID: a.ID, Name: a.Name, AvatarURL: a.AvatarURL, Description: a.Description},
		Links:      links,
		Feeds:      feeds,
		AvatarCard: s.authorAvatarCardData(a, u.Timezone, ""),
		Editor:     editor,
	})))
}

func (s *Server) authorUpdate(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	old, err := s.store.Authors.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	name := r.FormValue("name")
	if name == "" {
		http.Redirect(w, r, "/authors", http.StatusFound)
		return
	}
	avatarURL := r.FormValue("avatar_url")
	if avatarURL != old.AvatarURL {
		// The source changed (or was cleared): drop the stale cache before the
		// row changes so neither an orphaned object nor a stale key survives.
		s.clearAuthorAvatar(r.Context(), old)
	}
	if err := s.store.Authors.Update(u.ID, id, name, avatarURL, r.FormValue("description")); err != nil {
		log.Error("update author", "err", err)
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
	s.reconcileAuthorLinks(u.ID, id, r)
	if avatarURL != "" {
		a, _ := s.store.Authors.ByID(u.ID, id)
		if a.ID != 0 {
			ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
			s.autoCacheAuthorAvatar(ctx, a)
			cancel()
		}
	}
	http.Redirect(w, r, "/authors/"+strconv.FormatInt(id, 10), http.StatusFound)
}

func (s *Server) authorDelete(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a, err := s.store.Authors.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.clearAuthorAvatar(r.Context(), a)
	if err := s.store.Authors.Delete(u.ID, id); err != nil {
		log.Error("delete author", "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/authors", http.StatusSeeOther)
}

func (s *Server) authorFormFragment(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("author_id") != "new" {
		// An existing author is selected: clear the new-author fields. An empty
		// 200 (not 204 — htmx doesn't swap on 204) makes htmx empty the target.
		w.WriteHeader(http.StatusOK)
		return
	}
	// When adding a feed, the fragment is asked with the feed's home url so the
	// new author's name/avatar can be derived from it.
	var name, avatar string
	if home := strings.TrimSpace(r.FormValue("home_url")); home != "" {
		if meta, err := s.discoverer.PageMeta(r.Context(), home); err == nil {
			name, avatar = meta.Title, meta.IconURL
		}
	}
	web.Render(w, r, authorCreateFields(authorPreviewForm{
		Name: name, AvatarURL: avatar,
	}))
}

func (s *Server) collections(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	// Refresh the synthetic "no-collection" so its card lists the feeds that
	// are still ungrouped.
	if _, err := s.store.Collections.SyncNoCollection(u.ID); err != nil {
		log.Error("sync no-collection", "err", err)
	}
	rows, _ := s.store.Collections.ListWithCounts(u.ID)
	web.Render(w, r, basePage("collections", u, collectionsPage(u, rows)))
}

// userCollections returns the non-auto collections, preserving the query's
// order (most unread first).
func userCollections(rows []store.CollectionWithCounts) []store.CollectionWithCounts {
	out := make([]store.CollectionWithCounts, 0, len(rows))
	for _, c := range rows {
		if !c.IsAuto {
			out = append(out, c)
		}
	}
	return out
}

// autoCollections returns the auto collections, preserving the query's order
// (most unread first).
func autoCollections(rows []store.CollectionWithCounts) []store.CollectionWithCounts {
	out := make([]store.CollectionWithCounts, 0, len(rows))
	for _, c := range rows {
		if c.IsAuto {
			out = append(out, c)
		}
	}
	return out
}

func (s *Server) collectionCreate(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		writeFormError(w, r, "add-collection-error", "name is required")
		return
	}
	c, err := s.store.Collections.Create(u.ID, name)
	if err != nil {
		log.Error("create collection", "err", err)
		writeFormError(w, r, "add-collection-error", "could not create collection")
		return
	}
	web.Render(w, r, collectionCreated(store.CollectionWithCounts{Collection: c}))
}

func (s *Server) collectionPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d, err := s.collectionDataFor(u.ID, id, collectionView(r), itemSortOf(r), u.Timezone, tagParams(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	web.Render(w, r, basePage(d.Collection.Name, u, collectionPage(u, d)))
}

func (s *Server) collectionDataFor(userID, id int64, view string, sort itemSort, tz string, tags []string) (collectionData, error) {
	c, err := s.store.Collections.ByID(userID, id)
	if err != nil {
		return collectionData{}, err
	}
	// The no-collection is computed, not user-managed: refresh it before reading
	// its feeds so the page matches the current grouping.
	if c.IsNoCollection() {
		s.store.Collections.SyncNoCollection(userID)
	}
	feeds, _ := s.store.Collections.Feeds(userID, id)
	allFeeds, _ := s.store.Feeds.ListWithUnread(userID)
	scoped := s.collectionScopedItems(userID, id, view, sort, tz, tags)
	return collectionData{Collection: c, Feeds: feeds, AllFeeds: groupFeedsByAuthor(allFeeds), Scoped: scoped}, nil
}

// groupFeedsByAuthor groups a user's feeds by author name for the collection
// add-feed dropdown, ordering feed titles within each author.
func groupFeedsByAuthor(rows []store.FeedWithUnread) []collectionFeedGroup {
	var groups []collectionFeedGroup
	index := map[string]int{}
	for _, r := range rows {
		name := r.AuthorName
		if name == "" {
			name = "unassigned"
		}
		i, ok := index[name]
		if !ok {
			index[name] = len(groups)
			groups = append(groups, collectionFeedGroup{AuthorName: name})
			i = len(groups) - 1
		}
		groups[i].Feeds = append(groups[i].Feeds, r)
	}
	return groups
}

// collectionScopedItems loads one read/unread item list for a collection plus
// the counts that drive the tabs. The collection scope also gets a "feeds" tab
// listing the collection's feeds as cards; when that view is active no items
// are loaded.
func (s *Server) collectionScopedItems(userID, collectionID int64, view string, sort itemSort, tz string, tags []string) scopedItemsData {
	memberFeeds, _ := s.store.Collections.Feeds(userID, collectionID)
	unread, _ := s.store.Items.CountUnreadCollection(userID, collectionID)
	read, _ := s.store.Items.CountReadCollection(userID, collectionID)
	base := "/collections/" + strconv.FormatInt(collectionID, 10)
	d := scopedItemsData{
		Path: base, ItemsPath: base + "/items", View: view, Sort: sort,
		UnreadCount: unread, ReadCount: read, FeedsTab: true, FeedCount: len(memberFeeds),
		Mode: s.store.ViewPrefs.Mode(userID, base), Tags: tags,
	}
	if view == "feeds" {
		d.Feeds = s.collectionFeedRows(userID, memberFeeds, tz)
		return d
	}
	items, more, _ := s.store.Items.ListPage(userID, scopedFilter(view, sort, 0, 0, 0, collectionID, tags...))
	itemsBase := appendTags(base+"/items?view="+view+"&sort="+sort.param(), tags)
	cats, _ := s.store.Items.ListCategories(userID, store.ItemFilter{CollectionID: collectionID})
	d.TagOptions = tagItems(cats)
	d.Items = withTZ(tz, dedupItems(items))
	d.More = pageCursorFor(itemsBase, items, more, sort, 0)
	return d
}

// collectionFeedRows joins a collection's member feeds with their author name
// and unread count (the same join the add-feed picker uses), preserving the
// title order Collections.Feeds returns.
func (s *Server) collectionFeedRows(userID int64, memberFeeds []store.Feed, tz string) []feedRow {
	withUnread, _ := s.store.Feeds.ListWithUnread(userID)
	byID := make(map[int64]store.FeedWithUnread, len(withUnread))
	for _, f := range withUnread {
		byID[f.ID] = f
	}
	out := make([]feedRow, 0, len(memberFeeds))
	for _, f := range memberFeeds {
		row := feedRow{Feed: f, Timezone: tz}
		if w, ok := byID[f.ID]; ok {
			row.AuthorName = w.AuthorName
			row.Unread = w.Unread
		}
		out = append(out, row)
	}
	return out
}

func (s *Server) collectionItems(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	view := collectionView(r)
	sort := itemSortOf(r)
	tags := tagParams(r)
	if view != "feeds" {
		if cursor := cursorID(r, sort); cursor > 0 {
			items, more, err := s.store.Items.ListPage(u.ID, scopedFilter(view, sort, cursor, 0, 0, id, tags...))
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			base := appendTags("/collections/"+strconv.FormatInt(id, 10)+"/items?view="+view+"&sort="+sort.param(), tags)
			web.Render(w, r, ItemsPage(withTZ(u.Timezone, dedupItems(items)), pageCursorFor(base, items, more, sort, int(cursor)), false))
			return
		}
	}
	web.Render(w, r, ScopedItems(s.collectionScopedItems(u.ID, id, view, sort, u.Timezone, tags)))
}

func (s *Server) collectionDelete(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := s.store.Collections.ByID(u.ID, id); err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.Collections.Delete(u.ID, id); err != nil {
		log.Error("delete collection", "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/collections", http.StatusSeeOther)
}

// collectionEdit renders the collection's edit form (name + feeds + delete).
// Auto collections render the same screen but read-only: their name can't be
// changed and their feeds can't be removed, so it doubles as the place to see
// every feed in the collection.
func (s *Server) collectionEdit(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	c, err := s.store.Collections.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	feeds, _ := s.store.Collections.Feeds(u.ID, id)
	web.Render(w, r, basePage("edit "+c.Name, u, collectionEditPage(u, collectionData{Collection: c, Feeds: feeds})))
}

// collectionUpdate renames a collection.
func (s *Server) collectionUpdate(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	c, err := s.store.Collections.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if c.IsAuto {
		renderError(w, r, "auto collections are managed automatically")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Redirect(w, r, "/collections/"+strconv.FormatInt(id, 10)+"/edit", http.StatusSeeOther)
		return
	}
	if err := s.store.Collections.Rename(u.ID, id, name); err != nil {
		log.Error("rename collection", "err", err)
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/collections/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (s *Server) collectionAddFeed(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if c, err := s.store.Collections.ByID(u.ID, id); err != nil {
		http.NotFound(w, r)
		return
	} else if c.IsAuto {
		renderError(w, r, "auto collections are managed automatically")
		return
	}
	feedID, _ := strconv.ParseInt(r.FormValue("feed_id"), 10, 64)
	if feedID != 0 {
		s.store.Collections.AddFeed(u.ID, id, feedID)
	}
	web.Render(w, r, ScopedItems(s.collectionScopedItems(u.ID, id, normalizeCollectionView(r.FormValue("view")), itemSortOf(r), u.Timezone, tagParams(r))))
}

func (s *Server) collectionRemoveFeed(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	c, err := s.store.Collections.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if c.IsAuto {
		renderError(w, r, "auto collections are managed automatically")
		return
	}
	feedID, err := strconv.ParseInt(r.PathValue("feed_id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.store.Collections.RemoveFeed(u.ID, id, feedID)
	// The feed list is a client-side multi-select: the chip is already gone, so
	// just confirm. A failure (non-2xx) makes the client restore the chip.
	w.WriteHeader(http.StatusNoContent)
}

// itemsView reads the ?view= query param and normalizes it to "unread" or "read".
func itemsView(r *http.Request) string {
	return normalizeItemsView(r.URL.Query().Get("view"))
}

func normalizeItemsView(v string) string {
	if v == "read" {
		return "read"
	}
	return "unread"
}

// collectionView reads the collection page's ?view= param. It understands the
// collection-only "feeds" tab in addition to the shared unread/read views.
func collectionView(r *http.Request) string {
	return normalizeCollectionView(r.URL.Query().Get("view"))
}

func normalizeCollectionView(v string) string {
	if v == "feeds" {
		return "feeds"
	}
	return normalizeItemsView(v)
}

// authorView reads the author page's ?view= param. It understands the
// author-only "favorites" tab in addition to the shared unread/read views.
func authorView(r *http.Request) string {
	return normalizeAuthorView(r.URL.Query().Get("view"))
}

func normalizeAuthorView(v string) string {
	if v == "favorites" {
		return v
	}
	return normalizeItemsView(v)
}

// writeFormError responds to an htmx add-form submit with an out-of-band swap
// that renders msg into the modal's error div without disturbing the form. The
// markup matches the form_error fragment (role="alert" banner).
func writeFormError(w http.ResponseWriter, r *http.Request, target, msg string) {
	w.WriteHeader(http.StatusBadRequest)
	web.Render(w, r, FormErrorOOB(target, msg))
}

// renderError responds to an htmx request whose target is the preview
// container itself with a 400 and a styled error fragment swapped in via the
// normal target swap. The global htmx:beforeSwap listener allows 4xx content
// to render.
func renderError(w http.ResponseWriter, r *http.Request, msg string) {
	w.WriteHeader(http.StatusBadRequest)
	web.Render(w, r, FormError(msg))
}
