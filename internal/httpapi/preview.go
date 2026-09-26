package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/discover"
	"github.com/metruzanca/nanoflux/internal/feedparse"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// feedPreviewForm is the combined add form shown once a feed is identified: an
// author and their first feed together. On an author page the author is fixed
// (FixedAuthor) and only the feed fields render.
type feedPreviewForm struct {
	Title            string
	FeedURL          string
	HomeURL          string
	Action           string // form submit endpoint ("/feeds" or "/authors/{id}/feeds")
	Target           string // htmx target ("#authors-list" or "#feeds-list")
	Swap             string // htmx swap for the response row
	Authors          []store.Author
	SelectedAuthorID int64
	FixedAuthor      *store.Author
	NewAuthorName    string // prefill for the create-new-author fields
	NewAuthorAvatar  string
	Redirect         bool // after saving, send the client to the author page
}

// newAuthor returns the authorCreateFields payload for the default "create new
// author" selection.
func (f feedPreviewForm) newAuthor() authorPreviewForm {
	return authorPreviewForm{Name: f.NewAuthorName, AvatarURL: f.NewAuthorAvatar}
}

// feedChoose is the dropdown shown when a page exposes multiple feeds.
type feedChoose struct {
	URL        string
	Target     string
	Candidates []discover.Candidate
	Redirect   bool // carry the "send to the author page after saving" flag
}

// authorPreviewForm pre-fills the new-author fields from a detected page.
type authorPreviewForm struct {
	Name      string
	AvatarURL string
}

// noFeedFoundData carries the URL that yielded no feed, the htmx container the
// manual form should swap into, and a user-facing reason when discovery failed
// for a detectable cause (e.g. an HTTP 429 rate limit).
type noFeedFoundData struct {
	URL    string
	Target string
	Reason string
}

// feedPreview inspects a URL (direct feed or page) and renders the combined
// author + feed form. When the URL yields several feeds, it renders a dropdown
// first; the chosen feed re-posts here with feed_url set and renders a single
// form.
//
// The user's url mappings are applied first: if a pattern matches the entered
// url, the mapped feed url is inspected instead and the original url becomes
// the feed's home page. When the mapped url yields nothing, discovery falls
// back to the original url so a stale mapping never blocks adding a feed.
func (s *Server) feedPreview(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	pageURL := normalizeURL(r.FormValue("url"))
	if pageURL == "" {
		renderError(w, r, "enter a url")
		return
	}
	if msg := invalidURL(pageURL); msg != "" {
		renderError(w, r, msg)
		return
	}
	authors, _ := s.store.Authors.List(u.ID)
	selectedAuthor, _ := strconv.ParseInt(r.FormValue("author_id"), 10, 64)
	var fixedAuthor *store.Author
	if r.FormValue("scoped") == "1" {
		if a, err := s.store.Authors.ByID(u.ID, selectedAuthor); err == nil {
			fixedAuthor = &a
		}
	}
	// The preview container that rendered the fragment. The author-scoped
	// dialog uses a different id than the global one, so every fragment that
	// swaps into it (chooser, manual form) must target it.
	previewTarget := "#feed-preview"
	if r.FormValue("scoped") == "1" {
		previewTarget = "#author-feed-preview"
	}

	candidates, err := s.discoverCandidates(r.Context(), u.ID, pageURL)
	if len(candidates) == 0 {
		// Surface the underlying failure when there is one (e.g. a rate limit)
		// so the user can tell "no feed here" from "couldn't check right now".
		web.Render(w, r, noFeedFound(noFeedFoundData{URL: pageURL, Target: previewTarget, Reason: feedPreviewError(err)}))
		return
	}
	if err != nil {
		renderError(w, r, "could not inspect that url")
		return
	}

	if chosen := strings.TrimSpace(r.FormValue("feed_url")); chosen != "" {
		for _, c := range candidates {
			if c.FeedURL == chosen {
				s.renderFeedPreviewForm(r, w, c, pageURL, previewHome(c, pageURL), authors, selectedAuthor, fixedAuthor)
				return
			}
		}
	}

	if len(candidates) == 1 {
		s.renderFeedPreviewForm(r, w, candidates[0], pageURL, previewHome(candidates[0], pageURL), authors, selectedAuthor, fixedAuthor)
		return
	}

	web.Render(w, r, feedChooser(feedChoose{URL: pageURL, Target: previewTarget, Candidates: candidates, Redirect: fixedAuthor == nil}))
}

// previewHome returns the home page to pre-fill for a candidate: a direct or
// derived feed URL carries its own home (a derived reddit feed's canonical
// page, not the possibly-old./np. URL the user entered), while a feed found on
// a page keeps the page the user entered as home (so a stale mapping never
// changes it).
func previewHome(c discover.Candidate, pageURL string) string {
	if c.Strategy == "direct" || c.Strategy == "derived" {
		return c.HomeURL
	}
	return pageURL
}

// toDiscoverCandidates converts plugin candidates into discover candidates,
// tagged with the "plugin" strategy so the preview form prefers their metadata.
func toDiscoverCandidates(cs []pluginapi.Candidate) []discover.Candidate {
	out := make([]discover.Candidate, 0, len(cs))
	seen := map[string]bool{}
	for _, c := range cs {
		if c.FeedURL == "" || seen[c.FeedURL] {
			continue
		}
		seen[c.FeedURL] = true
		out = append(out, discover.Candidate{
			FeedURL:  c.FeedURL,
			Title:    c.Title,
			IconURL:  c.IconURL,
			HomeURL:  c.HomeURL,
			Strategy: "plugin",
		})
	}
	return out
}

// mergeCandidates prepends the plugin-supplied candidates (which carry their own
// preview metadata) to the generic ones, de-duplicating by FeedURL so a plugin's
// richer entry wins.
func mergeCandidates(plugin, generic []discover.Candidate) []discover.Candidate {
	seen := map[string]bool{}
	out := make([]discover.Candidate, 0, len(plugin)+len(generic))
	for _, c := range plugin {
		if c.FeedURL == "" || seen[c.FeedURL] {
			continue
		}
		seen[c.FeedURL] = true
		out = append(out, c)
	}
	for _, c := range generic {
		if seen[c.FeedURL] {
			continue
		}
		seen[c.FeedURL] = true
		out = append(out, c)
	}
	return out
}

// candidateForURL returns the candidate whose feed URL matches url (ignoring a
// leading www. and a trailing slash), or nil. It is how the direct-fetch path
// finds the plugin candidate that describes the very URL it just fetched.
func candidateForURL(cs []discover.Candidate, url string) *discover.Candidate {
	key := normExtKey(url)
	if key == "" {
		return nil
	}
	for i := range cs {
		if cs[i].FeedURL != "" && normExtKey(cs[i].FeedURL) == key {
			return &cs[i]
		}
	}
	return nil
}

// fillCandidate fills a plugin candidate's blank preview fields from the feed
// the direct fetch just confirmed, so the add form always has a title and home
// page even when a plugin omits them.
func fillCandidate(c discover.Candidate, feedTitle, pageURL string) discover.Candidate {
	if c.Title == "" {
		c.Title = feedTitle
	}
	if c.HomeURL == "" {
		c.HomeURL = pageURL
	}
	return c
}

// discoverCandidates resolves the feeds for a page URL: the user's url mappings
// are applied first (the original url becomes the home page), the direct URL is
// tried as a feed, then discovery runs, falling back to the original url when a
// mapping yields nothing so a stale mapping never blocks adding a feed.
func (s *Server) discoverCandidates(ctx context.Context, userID int64, pageURL string) ([]discover.Candidate, error) {
	feedURL := pageURL
	if mapped, ok := s.mappedFeedURL(userID, pageURL); ok {
		feedURL = mapped
	}
	// Plugin discovery (native + external) runs first and contributes candidates
	// with their own preview metadata. It is independent of the generic page
	// crawl, so a page that cannot be fetched (or a host the plugin knows
	// without a live page) still yields the plugin's feeds. It runs before the
	// direct-fetch shortcut because a site-specific plugin's feed URL is often
	// the page URL itself (an X profile): the direct fetch would
	// otherwise return a bare candidate and discard the plugin's real author
	// name/avatar in favour of the page's generic SEO metadata.
	var pluginCandidates []discover.Candidate
	if s.plugins != nil && !s.plugins.Empty() {
		if pcs := s.plugins.Discover(ctx, feedURL, s.pluginHosts.For); len(pcs) > 0 {
			pluginCandidates = toDiscoverCandidates(pcs)
		}
	}

	// Hosts with a known, fixed feed shape are derived without a request. Reddit
	// is the case that matters: probing its .rss would spend the host's tight
	// anonymous rate limit on discovery, so the candidate is built from the URL.
	if c, ok := discover.Derive(feedURL); ok {
		return []discover.Candidate{c}, nil
	}

	var directErr error
	// The URL itself may already be a feed; if so we can also derive the home page.
	// Remember the fetch error so a rate limit / server error can be surfaced
	// when discovery ultimately finds nothing.
	if res, err := feedparse.Fetch(ctx, feedURL, s.client, "", ""); err == nil {
		// When a plugin both serves the URL as a feed and describes it, its
		// preview metadata wins over the generic page metadata the direct
		// branch would otherwise fall back to.
		if pc := candidateForURL(pluginCandidates, feedURL); pc != nil {
			return []discover.Candidate{fillCandidate(*pc, res.Feed.Title, pageURL)}, nil
		}
		home := res.Feed.HomeURL
		if home == "" || feedURL != pageURL {
			home = pageURL
		}
		return []discover.Candidate{{FeedURL: feedURL, Title: res.Feed.Title, HomeURL: home, Strategy: "direct"}}, nil
	} else {
		directErr = err
	}

	candidates, err := s.discoverer.Discover(ctx, feedURL)
	if err != nil {
		log.Error("feed preview discover", "err", err)
		if feedURL == pageURL && len(pluginCandidates) == 0 {
			return nil, err
		}
	}
	candidates = mergeCandidates(pluginCandidates, candidates)
	if len(candidates) == 0 && feedURL != pageURL {
		// A mapping transformed the url but its feed is gone or the page is
		// not a feed; fall back to the original input.
		candidates, err = s.discoverer.Discover(ctx, pageURL)
		if err != nil {
			log.Error("feed preview discover fallback", "err", err)
			return nil, err
		}
	}
	if len(candidates) == 0 {
		// Nothing found: prefer Discover's error, else the direct-fetch one.
		// Only a real fetch error (an HTTP status) is worth surfacing — a plain
		// "not a feed" parse error just means the page has no feed, so we keep
		// the bare banner.
		if err != nil {
			return nil, err
		}
		var se *feedparse.StatusError
		var rl *feedparse.RateLimitError
		if errors.As(directErr, &se) || errors.As(directErr, &rl) {
			return nil, directErr
		}
		return nil, nil
	}
	return candidates, nil
}

// feedPreviewError maps a discovery/fetch error to a short, user-facing reason
// (or "" for an unknown cause). It never includes the URL, since errors carry
// the raw fetch URL and the client must not see internals.
func feedPreviewError(err error) string {
	if err == nil {
		return ""
	}
	var rl *feedparse.RateLimitError
	if errors.As(err, &rl) {
		return "the site is rate-limiting requests (HTTP " + strconv.Itoa(rl.Status) + ") — wait a bit and try again"
	}
	var se *feedparse.StatusError
	if errors.As(err, &se) {
		switch se.Code {
		case http.StatusTooManyRequests:
			return "the site is rate-limiting requests (HTTP 429) — wait a bit and try again"
		case http.StatusForbidden:
			return "the site refused the request (HTTP 403) — it may block automated access"
		case http.StatusNotFound:
			return "the page could not be found (HTTP 404) — check the url"
		default:
			if se.Code >= 500 {
				return "the site had a server error (HTTP " + strconv.Itoa(se.Code) + ") — try again later"
			}
			return "the site returned an error (HTTP " + strconv.Itoa(se.Code) + ")"
		}
	}
	return "could not inspect that url"
}

// renderFeedPreviewForm renders the combined add form for one discovered feed.
// homeURL is the page the user entered (the feed's home page); the default
// new-author name is derived from that page's <title>, falling back to the
// feed title, then the page host; the avatar comes from the site icon. The
// global add flow (no fixed author) sends the saved feed to its author page;
// the author-scoped flow appends the new feed row in place.
func (s *Server) renderFeedPreviewForm(r *http.Request, w http.ResponseWriter, c discover.Candidate, pageURL, homeURL string, authors []store.Author, selectedAuthor int64, fixedAuthor *store.Author) {
	// A derived candidate (reddit) already carries a clean title and home URL,
	// and the page must not be fetched: that request shares the host's tight
	// anonymous rate limit with the feed's .rss (which the immediate poll needs).
	var meta discover.PageMeta
	if c.Strategy != "derived" {
		meta, _ = s.discoverer.PageMeta(r.Context(), pageURL)
	}
	// A plugin-discovered candidate carries its own preview metadata, which wins
	// over the generic page metadata. A generic candidate's Title is the feed
	// title, so only use it as a fallback (below), never over the page title.
	name := meta.Title
	avatar := meta.IconURL
	if c.Strategy == "plugin" || c.Strategy == "derived" {
		if c.Title != "" {
			name = c.Title
		}
		if c.IconURL != "" {
			avatar = c.IconURL
		}
	}
	// A derived candidate may suggest a cleaner author name than the feed title
	// (a reddit user is "spez", not "u/spez").
	if c.AuthorName != "" {
		name = c.AuthorName
	}
	if name == "" {
		name = c.Title
	}
	if name == "" {
		if u, err := url.Parse(stripWWW(pageURL)); err == nil && u.Host != "" {
			name = u.Host
		}
	}
	form := feedPreviewForm{
		Title: c.Title, FeedURL: stripWWW(c.FeedURL), HomeURL: stripWWW(homeURL), Authors: authors,
		SelectedAuthorID: selectedAuthor, FixedAuthor: fixedAuthor, Redirect: fixedAuthor == nil,
		NewAuthorName: name, NewAuthorAvatar: avatar,
	}
	if fixedAuthor != nil {
		form.Action = "/authors/" + strconv.FormatInt(fixedAuthor.ID, 10) + "/feeds"
		form.Target = "#feeds-list"
		form.Swap = "beforeend"
	} else {
		form.Action = "/feeds"
		form.Target = "#authors-list"
		form.Swap = "beforeend"
	}
	web.Render(w, r, feedPreviewFields(form))
}

// invalidURL returns a user-facing message when raw doesn't parse into a URL
// with a usable host, or "" when it does.
func invalidURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "enter a valid url"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "enter a valid url"
	}
	host := u.Hostname()
	if host == "" || !strings.Contains(host, ".") && host != "localhost" {
		return "enter a valid url"
	}
	return ""
}

// manualFeedForm renders the combined add form pre-filled with the entered url
// as the feed url, for when discovery finds nothing. The user pastes a real
// feed url (or any url) and fills in the rest themselves.
func (s *Server) manualFeedForm(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	pageURL := normalizeURL(pageURLFromForm(r))
	if pageURL == "" {
		renderError(w, r, "enter a url")
		return
	}
	authors, _ := s.store.Authors.List(u.ID)
	selectedAuthor, _ := strconv.ParseInt(r.FormValue("author_id"), 10, 64)
	var fixedAuthor *store.Author
	if r.FormValue("scoped") == "1" {
		if a, err := s.store.Authors.ByID(u.ID, selectedAuthor); err == nil {
			fixedAuthor = &a
		}
	}
	// No PageMeta fetch here: "add manually" is the fast path for a user who
	// already knows the feed url, so it must render the form without a network
	// round-trip. The new-author name falls back to the url's host and the user
	// fills in the rest.
	form := feedPreviewForm{
		FeedURL: stripWWW(pageURL), HomeURL: stripWWW(pageURL), Authors: authors,
		SelectedAuthorID: selectedAuthor, FixedAuthor: fixedAuthor, Redirect: fixedAuthor == nil,
		NewAuthorName: authorNameFallback(pageURL),
	}
	if fixedAuthor != nil {
		form.Action = "/authors/" + strconv.FormatInt(fixedAuthor.ID, 10) + "/feeds"
		form.Target = "#feeds-list"
		form.Swap = "beforeend"
	} else {
		form.Action = "/feeds"
		form.Target = "#authors-list"
		form.Swap = "beforeend"
	}
	web.Render(w, r, feedPreviewFields(form))
}

// pageURLFromForm returns the page URL from a form: the url field, else the
// hidden feed_url field.
func pageURLFromForm(r *http.Request) string {
	if u := r.FormValue("url"); u != "" {
		return u
	}
	return r.FormValue("feed_url")
}

// authorNameFallback derives a default new-author name from a page url's host,
// for flows that must not fetch the page. Returns "" when the url has no host.
func authorNameFallback(pageURL string) string {
	if u, err := url.Parse(stripWWW(pageURL)); err == nil && u.Host != "" {
		return u.Host
	}
	return ""
}
