package httpapi

import (
	"context"
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/discover"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
	"github.com/metruzanca/nanoflux/pluginapi"
)

type homeData struct {
	Unread      []store.ItemWithFeed
	UnreadCount int
	Dir         string
	More        *loadMoreData
	Mode        string // saved display mode for "/unread"
}

// pageSize is the number of items rendered per page on every list.
const pageSize = 25

type readData struct {
	Read      []store.ItemWithFeed
	ReadCount int
	Dir       string
	More      *loadMoreData
	Mode      string // saved display mode for "/read"
}

type favoritesData struct {
	Favorites  []store.ItemWithFeed
	FavCount   int
	ShareToken string // public share token for the favorites list, "" when unshared
	Dir        string
	More       *loadMoreData
	Mode       string // saved display mode for "/favorites"
}

type feedRow struct {
	store.Feed
	AuthorName  string
	Unread      int
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
}

type feedsData struct {
	Rows        []feedRow
	Authors     []store.Author
	Collections []store.Collection
	Form        feedForm
	Rules       feedRulesData
}

// feedRulesData drives the filter-rule section on the feed edit page.
type feedRulesData struct {
	FeedID int64
	Rows   []store.Filter
}

type authorRow struct {
	store.Author
	FeedCount int
	Unread    int
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
	AvatarCard authorAvatarCardData // edit page avatar cache card
}

type authorData struct {
	Author    store.Author
	Rows      []feedRow
	Links     []store.AuthorLink
	Scoped    scopedItemsData
	Stats     store.AuthorItemStats
	Frequency string // approximate posting cadence, "" when unknown
	Timezone  string
	MarkAll   markAllReadData
}

type feedPageData struct {
	Row     feedRow
	Scoped  scopedItemsData
	MarkAll markAllReadData
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
	Path        string // full page URL base, e.g. "/feeds/1"
	ItemsPath   string // fragment URL base, e.g. "/feeds/1/items"
	View        string // "unread" or "read"
	Dir         string // "asc" or "desc" (item order)
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
}

// itemsAsc reports whether the item list should be ordered oldest-first from
// the ?dir=asc param (default is newest-first).
func itemsAsc(r *http.Request) bool {
	return r.URL.Query().Get("dir") == "asc"
}

func dirParam(asc bool) string {
	if asc {
		return "asc"
	}
	return "desc"
}

// moreURL builds the load-more fragment URL preserving the current filters:
// base may already carry a query string; the keyset cursor is before= (desc) or
// after= (asc).
func moreURL(base string, id int64, asc bool) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	key := "before"
	if asc {
		key = "after"
	}
	return base + sep + key + "=" + strconv.FormatInt(id, 10)
}

// pageCursor returns the load-more cursor for a fetched page, or nil when the
// list is empty or there is no next page.
func pageCursor(base string, items []store.ItemWithFeed, hasMore bool, asc bool) *loadMoreData {
	if !hasMore || len(items) == 0 {
		return nil
	}
	return &loadMoreData{URL: moreURL(base, items[len(items)-1].ID, asc)}
}

// scopedFilter builds the item filter for a feed/author/collection read/unread
// list, honoring a keyset cursor and sort direction.
func scopedFilter(view string, cursor int64, asc bool, feedID, authorID, collectionID int64) store.ItemFilter {
	f := store.ItemFilter{FeedID: feedID, AuthorID: authorID, CollectionID: collectionID, Ascending: asc, Limit: pageSize}
	if asc {
		f.AfterID = cursor
	} else {
		f.BeforeID = cursor
	}
	if view == "read" {
		f.ReadOnly = true
	} else {
		f.UnreadOnly = true
	}
	return f
}

// cursorID reads the keyset cursor from a load-more request: before= (desc) or
// after= (asc).
func cursorID(r *http.Request, asc bool) int64 {
	key := "before"
	if asc {
		key = "after"
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

// feedReadAll marks every item in one feed read ("mark all as read" on the feed
// page). The client reloads the page on success.
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
	if err := s.store.Items.MarkAllRead(u.ID, id); err != nil {
		log.Error("mark feed read", "feed_id", id, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	if err := s.store.Items.MarkAuthorRead(u.ID, id); err != nil {
		log.Error("mark author read", "author_id", id, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) readPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	asc := itemsAsc(r)
	items, more, _ := s.store.Items.ListPage(u.ID, store.ItemFilter{ReadOnly: true, Ascending: asc, Limit: pageSize})
	count, _ := s.store.Items.CountRead(u.ID, 0)
	mode := s.store.ViewPrefs.Mode(u.ID, "/read")
	web.Render(w, r, basePage("history", u, readPage(readData{
		Read: withTZ(u.Timezone, items), ReadCount: count, Dir: dirParam(asc),
		More: pageCursor("/items?read=1&dir="+dirParam(asc), items, more, asc), Mode: mode,
	})))
}

func (s *Server) favoritesPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	asc := itemsAsc(r)
	items, more, _ := s.store.Items.ListPage(u.ID, store.ItemFilter{FavoritesOnly: true, Ascending: asc, Limit: pageSize})
	count, _ := s.store.Items.CountFavorites(u.ID, 0)
	tok, _ := s.store.Users.FavoritesShareToken(u.ID)
	mode := s.store.ViewPrefs.Mode(u.ID, "/favorites")
	web.Render(w, r, basePage("favorites", u, favoritesPage(favoritesData{
		Favorites: withTZ(u.Timezone, items), FavCount: count, ShareToken: tok, Dir: dirParam(asc),
		More: pageCursor("/items?fav=1&dir="+dirParam(asc), items, more, asc), Mode: mode,
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
// keys: a fixed view ("/unread", "/read", "/favorites") or an entity page
// ("/feeds/{id}", "/authors/{id}", "/collections/{id}", "/lists/{id}"). A
// query string or fragment is rejected.
func validViewScope(scope string) bool {
	switch scope {
	case "/unread", "/read", "/favorites":
		return true
	}
	for _, prefix := range []string{"/feeds/", "/authors/", "/collections/", "/lists/"} {
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
	asc := itemsAsc(r)
	items, more, _ := s.store.Items.ListPage(userID, store.ItemFilter{ReadOnly: true, Ascending: asc, Limit: pageSize})
	web.Render(w, r, ItemsSection(withTZ(tz, items), pageCursor("/items?read=1&dir="+dirParam(asc), items, more, asc), false, s.store.ViewPrefs.Mode(userID, "/read")))
}

func (s *Server) renderItemsList(w http.ResponseWriter, r *http.Request, userID int64, tz string) {
	asc := itemsAsc(r)
	items, more, _ := s.store.Items.ListPage(userID, store.ItemFilter{UnreadOnly: true, Ascending: asc, Limit: pageSize})
	web.Render(w, r, ItemsSection(withTZ(tz, items), pageCursor("/items?dir="+dirParam(asc), items, more, asc), false, s.store.ViewPrefs.Mode(userID, "/unread")))
}

// itemsFragment serves a "load more" page of rows for the home/read/favorites
// lists. The fragment targets the existing #items-list element.
func (s *Server) itemsFragment(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	asc := itemsAsc(r)
	base := "/items"
	filter := store.ItemFilter{Limit: pageSize, Ascending: asc}
	if asc {
		filter.AfterID = cursorID(r, true)
	} else {
		filter.BeforeID = cursorID(r, false)
	}
	switch {
	case r.URL.Query().Get("fav") == "1":
		filter.FavoritesOnly = true
		base = "/items?fav=1"
	case r.URL.Query().Get("read") == "1":
		filter.ReadOnly = true
		base = "/items?read=1"
	default:
		filter.UnreadOnly = true
	}
	base += "&dir=" + dirParam(asc)
	items, more, err := s.store.Items.ListPage(u.ID, filter)
	if err != nil {
		log.Error("items fragment", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	web.Render(w, r, ItemsPage(withTZ(u.Timezone, items), pageCursor(base, items, more, asc), false))
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
	ID           int64
	Title        string
	AuthorName   string
	AuthorID     int64
	FeedID       int64
	PublishedAt  string
	Summary      string
	ImageURL     string
	Link         string
	Body         template.HTML
	EmbedURL     string
	SourceURL    string   // external destination of a reddit link post
	EmbedSrc     string   // iframe src from the destination's oEmbed
	Gallery      []string // full-res images of a reddit gallery post
	FeedIsSystem bool     // true for a saved page (hidden feed/author, no internal links)
	Enclosures   []store.Enclosure
	ShareToken   string // public share token, "" when the item is not shared
	Timezone     string // user's IANA timezone, for relative timestamps in templates
	Favorite     bool   // drives the modal's favorite toggle
	Read         bool   // drives the modal's read/unread toggle (true after auto-mark)
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
		ID:           it.ID,
		Title:        it.Title,
		AuthorName:   it.AuthorName,
		AuthorID:     it.AuthorID,
		FeedID:       it.FeedID,
		PublishedAt:  it.PublishedAt,
		Summary:      it.Summary,
		ImageURL:     it.ImageURL,
		Link:         it.Link,
		Body:         template.HTML(it.Summary),
		EmbedURL:     web.YoutubeEmbedURL(it.Link),
		Timezone:     u.Timezone,
		Favorite:     it.Favorite,
		Read:         it.Read,
		FeedIsSystem: it.FeedIsSystem,
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	data.SourceURL, data.EmbedSrc, data.Gallery = s.resolveItemPlugins(ctx, data)
	data.Enclosures, _ = s.store.Items.Enclosures(it.ID)
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
		ID:          it.ID,
		Title:       it.Title,
		PublishedAt: it.PublishedAt,
		Summary:     it.Summary,
		ImageURL:    it.ImageURL,
		Link:        it.Link,
		Body:        template.HTML(it.Summary),
		EmbedURL:    web.YoutubeEmbedURL(it.Link),
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
			itemViewControls(id, it.Favorite, !it.Read),
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
// item as read. The target item itself is left alone. On an author page the
// range spans every feed owned by the item's author; elsewhere (a feed page)
// it is limited to the item's own feed. The page is read from htmx's
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
	authorScoped := isAuthorPageURL(r.Header.Get("HX-Current-URL"))
	var derr error
	switch {
	case authorScoped && before:
		derr = s.store.Items.MarkAuthorBeforeRead(u.ID, id)
	case authorScoped:
		derr = s.store.Items.MarkAuthorAfterRead(u.ID, id)
	case before:
		derr = s.store.Items.MarkBeforeRead(u.ID, id)
	default:
		derr = s.store.Items.MarkAfterRead(u.ID, id)
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
			itemViewControls(id, !it.Favorite, it.Read),
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
	for _, cid := range r.Form["collections"] {
		if n, err := strconv.ParseInt(cid, 10, 64); err == nil {
			s.store.Collections.AddFeed(userID, n, f.ID)
		}
	}
	if err := s.store.Collections.AssignAuto(userID, f.ID, homeURL, feedURL); err != nil {
		log.Error("assign auto collection", "feed_id", f.ID, "err", err)
	}
	// Auto-cache the site's favicon when the form supplied a home url. Best
	// effort and bounded so a slow site can't stall the create response. Reddit
	// is skipped: a page fetch shares the host's tight anonymous rate limit with
	// the feed's .rss, and the immediate poll needs that budget more.
	if homeURL != "" && !discover.IsRedditHost(homeURL) {
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
	form := feedForm{
		ID: f.ID, Title: f.Title, FeedURL: f.FeedURL, HomeURL: f.HomeURL,
		Description: f.Description, AuthorID: f.AuthorID,
		PollIntervalSec: f.PollIntervalSec, PollIntervalAuto: f.PollIntervalAuto, Enabled: f.Enabled,
	}
	collections, _ := s.store.Collections.List(u.ID)
	form.CollectionIDs = s.collectionIDsForFeed(u.ID, f.ID)
	authors, _ := s.store.Authors.List(u.ID)

	web.Render(w, r, basePage("edit "+f.Title, u, feedEditPage(u, feedsData{
		Authors: authors, Collections: collections, Form: form,
		Rules: s.feedRules(u.ID, f.ID),
	})))
}

// feedRules returns a feed's filter rules (its own plus feed-wide rules).
func (s *Server) feedRules(userID, feedID int64) feedRulesData {
	rows, _ := s.store.Filters.ListByFeed(userID, feedID)
	return feedRulesData{FeedID: feedID, Rows: rows}
}

// feedRuleCreate adds a filter rule from the feed edit page.
func (s *Server) feedRuleCreate(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	pattern := strings.TrimSpace(r.FormValue("pattern"))
	if pattern == "" {
		writeFormError(w, r, "feed-rules-error", "a pattern is required")
		return
	}
	action := r.FormValue("action")
	if action != "hide" && action != "mark_read" {
		action = "hide"
	}
	field := r.FormValue("field")
	switch field {
	case "title", "summary", "link", "category":
	default:
		field = "title"
	}
	isRegex := r.FormValue("is_regex") == "1"
	if isRegex {
		if _, err := regexp.Compile(pattern); err != nil {
			writeFormError(w, r, "feed-rules-error", "invalid regex")
			return
		}
	}
	if _, err := s.store.Filters.Create(u.ID, id, action, field, pattern, isRegex); err != nil {
		log.Error("create filter", "err", err)
		writeFormError(w, r, "feed-rules-error", "could not add rule")
		return
	}
	web.Render(w, r, feedRulesSection(s.feedRules(u.ID, id)))
}

// filterDelete removes a filter rule and re-renders the list.
func (s *Server) filterDelete(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	fid, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := s.store.Filters.ByID(u.ID, fid)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.Filters.Delete(u.ID, fid); err != nil {
		log.Error("delete filter", "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	rows, _ := s.store.Filters.ListByFeed(u.ID, f.FeedID)
	web.Render(w, r, FilterList(f.FeedID, rows))
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
	back := "/authors/" + strconv.FormatInt(old.AuthorID, 10)
	if title == "" || feedURL == "" {
		http.Redirect(w, r, back, http.StatusFound)
		return
	}
	authorID, _, errMsg := s.resolveAuthor(r, u.ID)
	if errMsg != "" {
		http.Redirect(w, r, back, http.StatusFound)
		return
	}
	if err := s.store.Feeds.Update(u.ID, id, authorID, title, feedURL,
		r.FormValue("home_url"), "", interval, auto, r.FormValue("enabled") == "1"); err != nil {
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
	http.Redirect(w, r, "/authors/"+strconv.FormatInt(authorID, 10), http.StatusFound)
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
// "https://example.com". Returns s unchanged when it can't be parsed.
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
	if err := s.store.Feeds.Delete(u.ID, id); err != nil {
		log.Error("delete feed", "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
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
	if !cooling(f.NextPollAt) && s.poller != nil {
		if _, err := s.poller.PollOne(r.Context(), f); err != nil {
			log.Error("refresh feed", "feed_id", id, "err", err)
		}
		f, _ = s.store.Feeds.ByID(u.ID, id)
	}
	unread, _ := s.store.Items.CountUnread(u.ID, id)
	author, _ := s.store.Authors.ByID(u.ID, f.AuthorID)
	row := s.feedRowFor(u.ID, f, author.Name, u.Timezone, unread)
	web.Render(w, r, FeedRow(row))
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

// feedOlder fetches the next page of a paginated feed ("load older items"),
// stores its items, and re-renders the feed page's item list and the
// load-older control. The control is the htmx target (preview-style swap);
// the refreshed list arrives as an out-of-band swap so both update in one
// round trip.
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
	if f.NextPageURL == "" {
		renderError(w, r, "no more items to load")
		return
	}
	if s.poller == nil {
		renderError(w, r, "could not load older items")
		return
	}
	newItems, _, err := s.poller.PollOlder(r.Context(), f)
	if err != nil {
		log.Error("load older items", "feed_id", id, "err", err)
		renderError(w, r, "could not load older items")
		return
	}
	if newItems > 0 {
		log.Info("loaded older items", "feed_id", id, "new_items", newItems)
	}
	// Re-read the feed: PollOlder advanced (or cleared) the next-page cursor.
	f, err = s.store.Feeds.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	author, _ := s.store.Authors.ByID(u.ID, f.AuthorID)
	row := feedRow{Feed: f, AuthorName: author.Name, Timezone: u.Timezone}
	scoped := s.feedScopedItems(u.ID, id, itemsView(r), u.Timezone, itemsAsc(r))
	scoped.SwapOOB = true
	web.Render(w, r, templ.Join(feedOlderControl(row), ScopedItems(scoped)))
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
		out = append(out, authorRow{Author: r.Author, FeedCount: r.FeedCount, Unread: r.UnreadCount})
	}
	return out
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
	web.Render(w, r, AuthorRow(authorRow{Author: a}))
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
	scoped := s.authorScopedItems(u.ID, id, itemsView(r), u.Timezone, itemsAsc(r))
	stats, _ := s.store.Items.StatsAuthor(u.ID, id)
	frequency := ""
	if times, err := s.store.Items.AuthorRecentTimes(u.ID, id, 30); err == nil {
		if gap, ok := web.AverageGapSeconds(times); ok {
			frequency = web.PostFrequency(gap)
		}
	}
	web.Render(w, r, basePage(a.Name, u, authorPage(u, authorData{
		Author: a, Rows: rows, Links: links, Scoped: scoped,
		Stats: stats, Frequency: frequency, Timezone: u.Timezone,
		MarkAll: markAllReadData{
			Action: "/authors/" + strconv.FormatInt(id, 10) + "/read-all",
			Unread: stats.Unread,
		},
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

// authorScopedItems loads one read/unread item list for an author plus the
// counts that drive the tabs.
func (s *Server) authorScopedItems(userID, authorID int64, view, tz string, asc bool) scopedItemsData {
	items, more, _ := s.store.Items.ListPage(userID, scopedFilter(view, 0, asc, 0, authorID, 0))
	unread, _ := s.store.Items.CountUnreadAuthor(userID, authorID)
	read, _ := s.store.Items.CountReadAuthor(userID, authorID)
	base := "/authors/" + strconv.FormatInt(authorID, 10)
	itemsBase := base + "/items?view=" + view + "&dir=" + dirParam(asc)
	return scopedItemsData{
		Path: base, ItemsPath: base + "/items", View: view, Dir: dirParam(asc),
		UnreadCount: unread, ReadCount: read, Items: withTZ(tz, dedupItems(items)),
		More: pageCursor(itemsBase, items, more, asc), HideAuthor: true,
		Mode: s.store.ViewPrefs.Mode(userID, base),
	}
}

func (s *Server) authorItems(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	view := itemsView(r)
	asc := itemsAsc(r)
	if cursor := cursorID(r, asc); cursor > 0 {
		items, more, err := s.store.Items.ListPage(u.ID, scopedFilter(view, cursor, asc, 0, id, 0))
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		base := "/authors/" + strconv.FormatInt(id, 10) + "/items?view=" + view + "&dir=" + dirParam(asc)
		web.Render(w, r, ItemsPage(withTZ(u.Timezone, dedupItems(items)), pageCursor(base, items, more, asc), true))
		return
	}
	web.Render(w, r, ScopedItems(s.authorScopedItems(u.ID, id, view, u.Timezone, asc)))
}

func (s *Server) feedPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	feed, err := s.store.Feeds.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	authorName := ""
	if a, err := s.store.Authors.ByID(u.ID, feed.AuthorID); err == nil {
		authorName = a.Name
	}
	unread, _ := s.store.Items.CountUnread(u.ID, id)
	scoped := s.feedScopedItems(u.ID, id, itemsView(r), u.Timezone, itemsAsc(r))
	web.Render(w, r, basePage(feed.Title, u, feedPage(u, feedPageData{
		Row: feedRow{Feed: feed, AuthorName: authorName, Unread: unread, Timezone: u.Timezone}, Scoped: scoped,
		MarkAll: markAllReadData{
			Action: "/feeds/" + strconv.FormatInt(id, 10) + "/read-all",
			Unread: unread,
		},
	})))
}

// feedScopedItems loads one read/unread item list for a feed plus the counts
// that drive the tabs.
func (s *Server) feedScopedItems(userID, feedID int64, view, tz string, asc bool) scopedItemsData {
	items, more, _ := s.store.Items.ListPage(userID, scopedFilter(view, 0, asc, feedID, 0, 0))
	unread, _ := s.store.Items.CountUnread(userID, feedID)
	read, _ := s.store.Items.CountRead(userID, feedID)
	base := "/feeds/" + strconv.FormatInt(feedID, 10)
	itemsBase := base + "/items?view=" + view + "&dir=" + dirParam(asc)
	return scopedItemsData{
		Path: base, ItemsPath: base + "/items", View: view, Dir: dirParam(asc),
		UnreadCount: unread, ReadCount: read, Items: withTZ(tz, items),
		More: pageCursor(itemsBase, items, more, asc),
		Mode: s.store.ViewPrefs.Mode(userID, base),
	}
}

func (s *Server) feedItems(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	view := itemsView(r)
	asc := itemsAsc(r)
	if cursor := cursorID(r, asc); cursor > 0 {
		items, more, err := s.store.Items.ListPage(u.ID, scopedFilter(view, cursor, asc, id, 0, 0))
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		base := "/feeds/" + strconv.FormatInt(id, 10) + "/items?view=" + view + "&dir=" + dirParam(asc)
		web.Render(w, r, ItemsPage(withTZ(u.Timezone, items), pageCursor(base, items, more, asc), false))
		return
	}
	web.Render(w, r, ScopedItems(s.feedScopedItems(u.ID, id, view, u.Timezone, asc)))
}

// feedRowFor builds a single feed row with its collection tags, for the feed
// row fragments the add/refresh handlers render.
func (s *Server) feedRowFor(userID int64, f store.Feed, authorName, tz string, unread int) feedRow {
	byFeed, _ := s.store.Collections.CollectionsByAuthorFeed(userID, f.AuthorID)
	return feedRow{
		Feed: f, AuthorName: authorName, Unread: unread, Timezone: tz,
		Collections: byFeed[f.ID],
	}
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
		out = append(out, feedRow{
			Feed: r.Feed, AuthorName: r.AuthorName, Unread: r.Unread, Timezone: tz,
			Collections: byFeed[r.Feed.ID],
		})
	}
	return out, nil
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
	web.Render(w, r, basePage("edit "+a.Name, u, authorEditPage(u, authorsData{
		Form:       authorForm{ID: a.ID, Name: a.Name, AvatarURL: a.AvatarURL, Description: a.Description},
		Links:      links,
		AvatarCard: s.authorAvatarCardData(a, u.Timezone, ""),
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
	d, err := s.collectionDataFor(u.ID, id, collectionView(r), u.Timezone, itemsAsc(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	web.Render(w, r, basePage(d.Collection.Name, u, collectionPage(u, d)))
}

func (s *Server) collectionDataFor(userID, id int64, view, tz string, asc bool) (collectionData, error) {
	c, err := s.store.Collections.ByID(userID, id)
	if err != nil {
		return collectionData{}, err
	}
	feeds, _ := s.store.Collections.Feeds(userID, id)
	allFeeds, _ := s.store.Feeds.ListWithUnread(userID)
	scoped := s.collectionScopedItems(userID, id, view, tz, asc)
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
func (s *Server) collectionScopedItems(userID, collectionID int64, view, tz string, asc bool) scopedItemsData {
	memberFeeds, _ := s.store.Collections.Feeds(userID, collectionID)
	unread, _ := s.store.Items.CountUnreadCollection(userID, collectionID)
	read, _ := s.store.Items.CountReadCollection(userID, collectionID)
	base := "/collections/" + strconv.FormatInt(collectionID, 10)
	d := scopedItemsData{
		Path: base, ItemsPath: base + "/items", View: view, Dir: dirParam(asc),
		UnreadCount: unread, ReadCount: read, FeedsTab: true, FeedCount: len(memberFeeds),
		Mode: s.store.ViewPrefs.Mode(userID, base),
	}
	if view == "feeds" {
		d.Feeds = s.collectionFeedRows(userID, memberFeeds, tz)
		return d
	}
	items, more, _ := s.store.Items.ListPage(userID, scopedFilter(view, 0, asc, 0, 0, collectionID))
	itemsBase := base + "/items?view=" + view + "&dir=" + dirParam(asc)
	d.Items = withTZ(tz, dedupItems(items))
	d.More = pageCursor(itemsBase, items, more, asc)
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
	asc := itemsAsc(r)
	if view != "feeds" {
		if cursor := cursorID(r, asc); cursor > 0 {
			items, more, err := s.store.Items.ListPage(u.ID, scopedFilter(view, cursor, asc, 0, 0, id))
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			base := "/collections/" + strconv.FormatInt(id, 10) + "/items?view=" + view + "&dir=" + dirParam(asc)
			web.Render(w, r, ItemsPage(withTZ(u.Timezone, dedupItems(items)), pageCursor(base, items, more, asc), false))
			return
		}
	}
	web.Render(w, r, ScopedItems(s.collectionScopedItems(u.ID, id, view, u.Timezone, asc)))
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
	web.Render(w, r, ScopedItems(s.collectionScopedItems(u.ID, id, normalizeCollectionView(r.FormValue("view")), u.Timezone, itemsAsc(r))))
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
