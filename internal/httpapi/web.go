package httpapi

import (
	"context"
	"encoding/json"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/feedparse"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

type homeData struct {
	Unread      []store.ItemWithFeed
	UnreadCount int
	More        *loadMoreData
}

// pageSize is the number of items rendered per page on every list.
const pageSize = 25

type readData struct {
	Read      []store.ItemWithFeed
	ReadCount int
	More      *loadMoreData
}

type favoritesData struct {
	Favorites  []store.ItemWithFeed
	FavCount   int
	ShareToken string // public share token for the favorites list, "" when unshared
	More       *loadMoreData
}

type feedRow struct {
	store.Feed
	AuthorName string
	Unread     int
	Timezone   string // user's IANA timezone, for relative timestamps in templates
}

type feedForm struct {
	ID              int64
	Title           string
	FeedURL         string
	HomeURL         string
	Description     string
	AuthorID        int64
	PollIntervalSec int
	CollectionIDs   []int64
	Enabled         bool
	Kind            string                 // "feed" or "scrape"
	ScrapeConfig    feedparse.ScrapeConfig // selector config for scrape feeds
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
	URL         string
	AvatarURL   string
	Description string
}

type authorsData struct {
	Rows       []authorRow
	Form       authorForm
	AvatarCard authorAvatarCardData // edit page avatar cache card
}

type authorData struct {
	Author store.Author
	Rows   []feedRow
	Scoped scopedItemsData
}

type feedPageData struct {
	Row    feedRow
	Scoped scopedItemsData
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
	UnreadCount int
	ReadCount   int
	Items       []store.ItemWithFeed
	More        *loadMoreData // "load more" cursor, nil when no next page
	SwapOOB     bool          // render with hx-swap-oob for the collection OOB fragment
	HideAuthor  bool          // drop the author link from item meta (author-scoped page)
}

// moreURL builds the load-more fragment URL preserving the current filters:
// base may already carry a query string, in which case before= is appended.
func moreURL(base string, id int64) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "before=" + strconv.FormatInt(id, 10)
}

// pageCursor returns the load-more cursor for a fetched page, or nil when the
// list is empty or there is no next page.
func pageCursor(base string, items []store.ItemWithFeed, hasMore bool) *loadMoreData {
	if !hasMore || len(items) == 0 {
		return nil
	}
	return &loadMoreData{URL: moreURL(base, items[len(items)-1].ID)}
}

// scopedFilter builds the item filter for a feed/author/collection read/unread
// list, honoring a keyset cursor.
func scopedFilter(view string, before int64, feedID, authorID, collectionID int64) store.ItemFilter {
	f := store.ItemFilter{FeedID: feedID, AuthorID: authorID, CollectionID: collectionID, BeforeID: before, Limit: pageSize}
	if view == "read" {
		f.ReadOnly = true
	} else {
		f.UnreadOnly = true
	}
	return f
}

// beforeID reads the ?before= keyset cursor from a load-more request.
func beforeID(r *http.Request) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	return n
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	items, more, _ := s.store.Items.ListPage(u.ID, store.ItemFilter{UnreadOnly: true, Limit: pageSize})
	unread, _ := s.store.Items.CountUnread(u.ID, 0)
	web.Render(w, r, basePage("unread", u, homePage(homeData{
		Unread: withTZ(u.Timezone, items), UnreadCount: unread, More: pageCursor("/items", items, more),
	})))
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

func (s *Server) readPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	items, more, _ := s.store.Items.ListPage(u.ID, store.ItemFilter{ReadOnly: true, Limit: pageSize})
	count, _ := s.store.Items.CountRead(u.ID, 0)
	web.Render(w, r, basePage("history", u, readPage(readData{
		Read: withTZ(u.Timezone, items), ReadCount: count, More: pageCursor("/items?read=1", items, more),
	})))
}

func (s *Server) favoritesPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	items, more, _ := s.store.Items.ListPage(u.ID, store.ItemFilter{FavoritesOnly: true, Limit: pageSize})
	count, _ := s.store.Items.CountFavorites(u.ID, 0)
	tok, _ := s.store.Users.FavoritesShareToken(u.ID)
	web.Render(w, r, basePage("favorites", u, favoritesPage(favoritesData{
		Favorites: withTZ(u.Timezone, items), FavCount: count, ShareToken: tok,
		More: pageCursor("/items?fav=1", items, more),
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

func (s *Server) renderReadItemsList(w http.ResponseWriter, r *http.Request, userID int64, tz string) {
	items, more, _ := s.store.Items.ListPage(userID, store.ItemFilter{ReadOnly: true, Limit: pageSize})
	web.Render(w, r, ItemsSection(withTZ(tz, items), pageCursor("/items?read=1", items, more), false))
}

func (s *Server) renderItemsList(w http.ResponseWriter, r *http.Request, userID int64, tz string) {
	items, more, _ := s.store.Items.ListPage(userID, store.ItemFilter{UnreadOnly: true, Limit: pageSize})
	web.Render(w, r, ItemsSection(withTZ(tz, items), pageCursor("/items", items, more), false))
}

// itemsFragment serves a "load more" page of rows for the home/read/favorites
// lists. The fragment targets the existing #items-list element.
func (s *Server) itemsFragment(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	base := "/items"
	filter := store.ItemFilter{Limit: pageSize, BeforeID: beforeID(r)}
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
	items, more, err := s.store.Items.ListPage(u.ID, filter)
	if err != nil {
		log.Error("items fragment", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	web.Render(w, r, ItemsPage(withTZ(u.Timezone, items), pageCursor(base, items, more), false))
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
	Title       string
	AuthorName  string
	AuthorID    int64
	FeedTitle   string
	FeedID      int64
	PublishedAt string
	Summary     string
	ImageURL    string
	Link        string
	Body        template.HTML
	EmbedURL    string
	SourceURL   string   // external destination of a reddit link post
	EmbedSrc    string   // iframe src from the destination's oEmbed
	Gallery     []string // full-res images of a reddit gallery post
	Enclosures  []store.Enclosure
	ShareToken  string       // public share token, "" when the item is not shared
	Favorite    bool         // whether the item is in the special favorites list
	Lists       []store.List // the user's lists, for the add-to-list picker
	ItemListIDs []int64      // ids of the user's lists that already contain this item
	Timezone    string       // user's IANA timezone, for relative timestamps in templates
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
		}
	}
	data := itemViewData{
		ID:          it.ID,
		Title:       it.Title,
		AuthorName:  it.AuthorName,
		AuthorID:    it.AuthorID,
		FeedTitle:   it.FeedTitle,
		FeedID:      it.FeedID,
		PublishedAt: it.PublishedAt,
		Summary:     it.Summary,
		ImageURL:    it.ImageURL,
		Link:        it.Link,
		Body:        template.HTML(it.Summary),
		EmbedURL:    web.YoutubeEmbedURL(it.Link),
		Timezone:    u.Timezone,
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	data.SourceURL, data.EmbedSrc, data.Gallery = s.resolveItemSource(ctx, data)
	data.Enclosures, _ = s.store.Items.Enclosures(it.ID)
	data.Favorite = it.Favorite
	if rows, err := s.store.Lists.List(u.ID); err == nil {
		data.Lists = listsOnly(rows)
	}
	if ids, err := s.store.Lists.ItemListIDs(u.ID, it.ID); err == nil {
		data.ItemListIDs = ids
	}
	if sh, err := s.store.Shares.ByItem(u.ID, it.ID); err == nil {
		data.ShareToken = sh.Token
	}
	web.Render(w, r, ItemView(data))
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
	data.SourceURL, data.EmbedSrc, data.Gallery = s.resolveItemSource(ctx, data)
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
	row, err := s.store.Items.OneWithFeed(u.ID, id)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	row.Timezone = u.Timezone
	web.Render(w, r, ItemRow(row, r.FormValue("hideAuthor") == "1"))
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
	authorID, isNew, errMsg := s.resolveAuthor(r, u.ID)
	if errMsg != "" {
		writeFormError(w, r, "add-feed-error", errMsg)
		return
	}
	if _, errMsg = s.createFeed(r, u.ID, authorID); errMsg != "" {
		writeFormError(w, r, "add-feed-error", errMsg)
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
	f, errMsg := s.createFeed(r, u.ID, authorID)
	if errMsg != "" {
		writeFormError(w, r, "add-feed-error", errMsg)
		return
	}
	unread, _ := s.store.Items.CountUnread(u.ID, f.ID)
	web.Render(w, r, FeedRow(feedRow{Feed: f, AuthorName: author.Name, Unread: unread, Timezone: u.Timezone}))
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
	var (
		f   store.Feed
		err error
	)
	if r.FormValue("kind") == store.ScrapeKind {
		cfg := scrapeConfigFromForm(r)
		if strings.TrimSpace(cfg.Item) == "" {
			return store.Feed{}, "an item selector is required"
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			log.Error("marshal scrape config", "err", err)
			return store.Feed{}, "could not create feed"
		}
		f, err = s.store.Feeds.CreateScrape(userID, authorID, title, feedURL, homeURL, "", string(raw), interval)
	} else {
		f, err = s.store.Feeds.Create(userID, authorID, title, feedURL, homeURL, "", interval)
	}
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
	// effort and bounded so a slow site can't stall the create response.
	if homeURL != "" {
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
			userID, name, r.FormValue("author_url"), r.FormValue("avatar_url"), "",
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
		PollIntervalSec: f.PollIntervalSec, Enabled: f.Enabled,
		Kind: f.Kind,
	}
	if f.Kind == store.ScrapeKind {
		if cfg, err := feedparse.ParseScrapeConfig(f.ScrapeConfig); err == nil {
			form.ScrapeConfig = cfg
		}
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
	case "title", "summary", "link":
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
	if old.Kind == store.ScrapeKind {
		cfg := scrapeConfigFromForm(r)
		if strings.TrimSpace(cfg.Item) == "" {
			http.Redirect(w, r, back, http.StatusFound)
			return
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			log.Error("marshal scrape config", "err", err)
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}
		if err := s.store.Feeds.UpdateScrape(u.ID, id, authorID, title, feedURL,
			r.FormValue("home_url"), "", string(raw), interval, r.FormValue("enabled") == "1"); err != nil {
			log.Error("update scrape feed", "err", err)
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}
	} else if err := s.store.Feeds.Update(u.ID, id, authorID, title, feedURL,
		r.FormValue("home_url"), "", interval, r.FormValue("enabled") == "1"); err != nil {
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
// preview form's feed/home fields, the scrape builder, new-author prefill) are
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
	if err := s.store.Feeds.Delete(u.ID, id); err != nil {
		log.Error("delete feed", "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	if s.poller != nil {
		if _, err := s.poller.PollOne(r.Context(), f); err != nil {
			log.Error("refresh feed", "feed_id", id, "err", err)
		}
	}
	unread, _ := s.store.Items.CountUnread(u.ID, id)
	author, _ := s.store.Authors.ByID(u.ID, f.AuthorID)
	web.Render(w, r, FeedRow(feedRow{Feed: f, AuthorName: author.Name, Unread: unread, Timezone: u.Timezone}))
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
	scoped := s.feedScopedItems(u.ID, id, itemsView(r), u.Timezone)
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
	web.Render(w, r, FeedRow(feedRow{Feed: f, AuthorName: author.Name, Unread: unread, Timezone: u.Timezone}))
}

func (s *Server) authors(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	rows := s.authorRows(u.ID)
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
	a, err := s.store.Authors.Create(u.ID, name, r.FormValue("url"), r.FormValue("avatar_url"), r.FormValue("description"))
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
	scoped := s.authorScopedItems(u.ID, id, itemsView(r), u.Timezone)
	web.Render(w, r, basePage(a.Name, u, authorPage(u, authorData{
		Author: a, Rows: rows, Scoped: scoped,
	})))
}

// authorScopedItems loads one read/unread item list for an author plus the
// counts that drive the tabs.
func (s *Server) authorScopedItems(userID, authorID int64, view, tz string) scopedItemsData {
	items, more, _ := s.store.Items.ListPage(userID, scopedFilter(view, 0, 0, authorID, 0))
	unread, _ := s.store.Items.CountUnreadAuthor(userID, authorID)
	read, _ := s.store.Items.CountReadAuthor(userID, authorID)
	base := "/authors/" + strconv.FormatInt(authorID, 10)
	return scopedItemsData{
		Path: base, ItemsPath: base + "/items", View: view,
		UnreadCount: unread, ReadCount: read, Items: withTZ(tz, dedupItems(items)),
		More: pageCursor(base+"/items?view="+view, items, more), HideAuthor: true,
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
	if before := beforeID(r); before > 0 {
		items, more, err := s.store.Items.ListPage(u.ID, scopedFilter(view, before, 0, id, 0))
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		base := "/authors/" + strconv.FormatInt(id, 10) + "/items?view=" + view
		web.Render(w, r, ItemsPage(withTZ(u.Timezone, dedupItems(items)), pageCursor(base, items, more), true))
		return
	}
	web.Render(w, r, ScopedItems(s.authorScopedItems(u.ID, id, view, u.Timezone)))
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
	scoped := s.feedScopedItems(u.ID, id, itemsView(r), u.Timezone)
	web.Render(w, r, basePage(feed.Title, u, feedPage(u, feedPageData{
		Row: feedRow{Feed: feed, AuthorName: authorName, Unread: unread, Timezone: u.Timezone}, Scoped: scoped,
	})))
}

// feedScopedItems loads one read/unread item list for a feed plus the counts
// that drive the tabs.
func (s *Server) feedScopedItems(userID, feedID int64, view, tz string) scopedItemsData {
	items, more, _ := s.store.Items.ListPage(userID, scopedFilter(view, 0, feedID, 0, 0))
	unread, _ := s.store.Items.CountUnread(userID, feedID)
	read, _ := s.store.Items.CountRead(userID, feedID)
	base := "/feeds/" + strconv.FormatInt(feedID, 10)
	return scopedItemsData{
		Path: base, ItemsPath: base + "/items", View: view,
		UnreadCount: unread, ReadCount: read, Items: withTZ(tz, items),
		More: pageCursor(base+"/items?view="+view, items, more),
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
	if before := beforeID(r); before > 0 {
		items, more, err := s.store.Items.ListPage(u.ID, scopedFilter(view, before, id, 0, 0))
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		base := "/feeds/" + strconv.FormatInt(id, 10) + "/items?view=" + view
		web.Render(w, r, ItemsPage(withTZ(u.Timezone, items), pageCursor(base, items, more), false))
		return
	}
	web.Render(w, r, ScopedItems(s.feedScopedItems(u.ID, id, view, u.Timezone)))
}

func (s *Server) feedRowsForAuthor(userID, authorID int64, tz string) ([]feedRow, error) {
	rows, err := s.store.Feeds.ListByAuthorWithUnread(userID, authorID)
	if err != nil {
		return nil, err
	}
	out := make([]feedRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, feedRow{Feed: r.Feed, AuthorName: r.AuthorName, Unread: r.Unread, Timezone: tz})
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
	web.Render(w, r, basePage("edit "+a.Name, u, authorEditPage(u, authorsData{
		Form:       authorForm{ID: a.ID, Name: a.Name, URL: a.URL, AvatarURL: a.AvatarURL, Description: a.Description},
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
	if err := s.store.Authors.Update(u.ID, id, name, r.FormValue("url"), avatarURL, r.FormValue("description")); err != nil {
		log.Error("update author", "err", err)
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
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
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) authorFormFragment(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("author_id") != "new" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// When adding a feed, the fragment is asked with the feed's home url so the
	// new author's name/avatar can be derived from it.
	var name, homeURL, avatar string
	if home := strings.TrimSpace(r.FormValue("home_url")); home != "" {
		if meta, err := s.discoverer.PageMeta(r.Context(), home); err == nil {
			name, homeURL, avatar = meta.Title, stripWWW(meta.HomeURL), meta.IconURL
		}
	}
	web.Render(w, r, authorCreateFields(authorPreviewForm{
		Name: name, URL: homeURL, AvatarURL: avatar,
	}))
}

func (s *Server) collections(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	rows, _ := s.store.Collections.List(u.ID)
	web.Render(w, r, basePage("collections", u, collectionsPage(u, rows)))
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
	web.Render(w, r, CollectionRow(c))
}

func (s *Server) collectionPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d, err := s.collectionDataFor(u.ID, id, itemsView(r), u.Timezone)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	web.Render(w, r, basePage(d.Collection.Name, u, collectionPage(u, d)))
}

func (s *Server) collectionDataFor(userID, id int64, view, tz string) (collectionData, error) {
	c, err := s.store.Collections.ByID(userID, id)
	if err != nil {
		return collectionData{}, err
	}
	feeds, _ := s.store.Collections.Feeds(userID, id)
	allFeeds, _ := s.store.Feeds.ListWithUnread(userID)
	scoped := s.collectionScopedItems(userID, id, view, tz)
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
// the counts that drive the tabs.
func (s *Server) collectionScopedItems(userID, collectionID int64, view, tz string) scopedItemsData {
	items, more, _ := s.store.Items.ListPage(userID, scopedFilter(view, 0, 0, 0, collectionID))
	unread, _ := s.store.Items.CountUnreadCollection(userID, collectionID)
	read, _ := s.store.Items.CountReadCollection(userID, collectionID)
	base := "/collections/" + strconv.FormatInt(collectionID, 10)
	return scopedItemsData{
		Path: base, ItemsPath: base + "/items", View: view,
		UnreadCount: unread, ReadCount: read, Items: withTZ(tz, dedupItems(items)),
		More: pageCursor(base+"/items?view="+view, items, more),
	}
}

func (s *Server) collectionItems(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	view := itemsView(r)
	if before := beforeID(r); before > 0 {
		items, more, err := s.store.Items.ListPage(u.ID, scopedFilter(view, before, 0, 0, id))
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		base := "/collections/" + strconv.FormatInt(id, 10) + "/items?view=" + view
		web.Render(w, r, ItemsPage(withTZ(u.Timezone, dedupItems(items)), pageCursor(base, items, more), false))
		return
	}
	web.Render(w, r, ScopedItems(s.collectionScopedItems(u.ID, id, view, u.Timezone)))
}

func (s *Server) collectionDelete(w http.ResponseWriter, r *http.Request) {
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
		renderError(w, r, "auto collections cannot be deleted")
		return
	}
	if err := s.store.Collections.Delete(u.ID, id); err != nil {
		log.Error("delete collection", "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	s.renderCollectionFeeds(w, r, u.ID, id, normalizeItemsView(r.FormValue("view")), u.Timezone)
}

func (s *Server) collectionRemoveFeed(w http.ResponseWriter, r *http.Request) {
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
	feedID, err := strconv.ParseInt(r.PathValue("feed_id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.store.Collections.RemoveFeed(u.ID, id, feedID)
	s.renderCollectionFeeds(w, r, u.ID, id, normalizeItemsView(r.FormValue("view")), u.Timezone)
}

func (s *Server) renderCollectionFeeds(w http.ResponseWriter, r *http.Request, userID, id int64, view, tz string) {
	d, err := s.collectionDataFor(userID, id, view, tz)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	d.Scoped.SwapOOB = true
	web.Render(w, r, collectionUpdated(d))
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
