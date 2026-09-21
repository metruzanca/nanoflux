package httpapi

import (
	"context"
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
	return authorPreviewForm{Name: f.NewAuthorName, URL: f.HomeURL, AvatarURL: f.NewAuthorAvatar}
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
	URL       string
	AvatarURL string
}

// noFeedFoundData carries the URL that yielded no feed and the htmx container
// the scrape builder should swap into.
type noFeedFoundData struct {
	URL    string
	Target string
}

// scrapeSampleItem is one extracted item in the scrape builder's live preview.
type scrapeSampleItem struct {
	Title string
	Link  string
	Date  string
}

// scrapeBuilderData drives the scrape feed builder form: the scraped URL, the
// selector config, the live preview sample, and the standard add-feed form
// fields (author selection, htmx Action/Target/Swap).
type scrapeBuilderData struct {
	URL              string
	Title            string
	HomeURL          string
	Config           feedparse.ScrapeConfig
	Sample           []scrapeSampleItem
	Action           string
	Target           string
	Swap             string
	Authors          []store.Author
	SelectedAuthorID int64
	FixedAuthor      *store.Author
	NewAuthorName    string
	NewAuthorAvatar  string
	Timezone         string // user's IANA timezone for sample timestamps
}

// newAuthor returns the authorCreateFields payload for the default "create new
// author" selection in the scrape builder.
func (d scrapeBuilderData) newAuthor() authorPreviewForm {
	return authorPreviewForm{Name: d.NewAuthorName, URL: d.HomeURL, AvatarURL: d.NewAuthorAvatar}
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
	// swaps into it (chooser, scrape builder opt-in) must target it.
	previewTarget := "#feed-preview"
	if r.FormValue("scoped") == "1" {
		previewTarget = "#author-feed-preview"
	}
	redirect := r.FormValue("redirect") == "1"

	candidates, err := s.discoverCandidates(r.Context(), u.ID, pageURL)
	if err != nil {
		renderError(w, r, "could not inspect that url")
		return
	}
	if len(candidates) == 0 {
		web.Render(w, r, noFeedFound(noFeedFoundData{URL: pageURL, Target: previewTarget}))
		return
	}

	if chosen := strings.TrimSpace(r.FormValue("feed_url")); chosen != "" {
		for _, c := range candidates {
			if c.FeedURL == chosen {
				s.renderFeedPreviewForm(r, w, c, pageURL, previewHome(c, pageURL), authors, selectedAuthor, fixedAuthor, redirect)
				return
			}
		}
	}

	if len(candidates) == 1 {
		s.renderFeedPreviewForm(r, w, candidates[0], pageURL, previewHome(candidates[0], pageURL), authors, selectedAuthor, fixedAuthor, redirect)
		return
	}

	web.Render(w, r, feedChooser(feedChoose{URL: pageURL, Target: previewTarget, Candidates: candidates, Redirect: redirect}))
}

// previewHome returns the home page to pre-fill for a candidate: a direct feed
// URL carries its own discovered home, while a feed found on a page keeps the
// page the user entered as home (so a stale mapping never changes it).
func previewHome(c discover.Candidate, pageURL string) string {
	if c.Strategy == "direct" {
		return c.HomeURL
	}
	return pageURL
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
	// The URL itself may already be a feed; if so we can also derive the home page.
	if res, err := feedparse.Fetch(ctx, feedURL, s.client, "", ""); err == nil {
		home := res.Feed.HomeURL
		if home == "" || feedURL != pageURL {
			home = pageURL
		}
		return []discover.Candidate{{FeedURL: feedURL, Title: res.Feed.Title, HomeURL: home, Strategy: "direct"}}, nil
	}

	candidates, err := s.discoverer.Discover(ctx, feedURL)
	if err != nil {
		log.Error("feed preview discover", "err", err)
		if feedURL == pageURL {
			return nil, err
		}
	}
	if len(candidates) == 0 && feedURL != pageURL {
		// A mapping transformed the url but its feed is gone or the page is
		// not a feed; fall back to the original input.
		candidates, err = s.discoverer.Discover(ctx, pageURL)
		if err != nil {
			log.Error("feed preview discover fallback", "err", err)
			return nil, err
		}
	}
	return candidates, nil
}

// renderFeedPreviewForm renders the combined add form for one discovered feed.
// homeURL is the page the user entered (the feed's home page); the default
// new-author name is derived from that page's <title>, falling back to the
// feed title, then the page host; the avatar comes from the site icon. When
// redirect is set the saved feed sends the client to its author page.
func (s *Server) renderFeedPreviewForm(r *http.Request, w http.ResponseWriter, c discover.Candidate, pageURL, homeURL string, authors []store.Author, selectedAuthor int64, fixedAuthor *store.Author, redirect bool) {
	meta, _ := s.discoverer.PageMeta(r.Context(), pageURL)
	name := meta.Title
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
		SelectedAuthorID: selectedAuthor, FixedAuthor: fixedAuthor, Redirect: redirect,
		NewAuthorName: name, NewAuthorAvatar: meta.IconURL,
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

// scrapeBuilder renders the CSS-selector feed builder for a URL that has no
// feed. On first open it auto-detects an item selector (best effort); when the
// auto-detect button re-posts, the submitted selectors are preserved and the
// builder is re-rendered with a fresh sample.
func (s *Server) scrapeBuilder(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	pageURL := normalizeURL(scrapeURLFromForm(r))
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

	cfg := scrapeConfigFromForm(r)
	if r.FormValue("detect") == "1" || strings.TrimSpace(cfg.Item) == "" {
		if auto, err := feedparse.AutoDetect(r.Context(), pageURL, s.client); err == nil {
			cfg = auto
		}
	}

	sample := s.scrapeSample(r.Context(), pageURL, cfg, u.Timezone)

	// Preserve what the user typed on a re-post (auto-detect); derive from the
	// page only on first open.
	title := strings.TrimSpace(r.FormValue("title"))
	homeURL := strings.TrimSpace(r.FormValue("home_url"))
	name, avatar := s.authorPrefill(r.Context(), pageURL, pageURL)
	if title == "" {
		title = name
	}
	if homeURL == "" {
		homeURL = pageURL
	}
	form := scrapeBuilderData{
		URL: stripWWW(pageURL), Title: title, HomeURL: stripWWW(homeURL), Config: cfg, Sample: sample,
		Authors: authors, SelectedAuthorID: selectedAuthor, FixedAuthor: fixedAuthor,
		NewAuthorName: name, NewAuthorAvatar: avatar, Timezone: u.Timezone,
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
	web.Render(w, r, scrapeBuilder(form))
}

// scrapePreview runs the submitted selector config against the url and renders
// the extracted sample. It returns the standard form_error on any failure so
// the user sees why nothing matched.
func (s *Server) scrapePreview(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	pageURL := normalizeURL(scrapeURLFromForm(r))
	if pageURL == "" {
		renderError(w, r, "enter a url")
		return
	}
	cfg := scrapeConfigFromForm(r)
	if strings.TrimSpace(cfg.Item) == "" {
		renderError(w, r, "an item selector is required")
		return
	}
	sample := s.scrapeSample(r.Context(), pageURL, cfg, u.Timezone)
	if sample == nil {
		renderError(w, r, "no items matched those selectors")
		return
	}
	web.Render(w, r, scrapeSample(sample))
}

// scrapeSample extracts up to 5 items from pageURL using cfg, returning nil
// when the scrape fails or yields nothing.
func (s *Server) scrapeSample(ctx context.Context, pageURL string, cfg feedparse.ScrapeConfig, tz string) []scrapeSampleItem {
	res, err := feedparse.Scrape(ctx, pageURL, s.client, cfg, "", "")
	if err != nil || len(res.Items) == 0 {
		return nil
	}
	n := min(len(res.Items), 5)
	out := make([]scrapeSampleItem, 0, n)
	for _, it := range res.Items[:n] {
		date := ""
		if it.PublishedAt != "" {
			date = web.TimeFmt(tz, it.PublishedAt)
		}
		out = append(out, scrapeSampleItem{Title: it.Title, Link: it.Link, Date: date})
	}
	return out
}

// scrapeConfigFromForm reads the six selector fields from a scrape builder
// form submission.
func scrapeConfigFromForm(r *http.Request) feedparse.ScrapeConfig {
	return feedparse.ScrapeConfig{
		Item:    strings.TrimSpace(r.FormValue("scrape_item")),
		Title:   strings.TrimSpace(r.FormValue("scrape_title")),
		Link:    strings.TrimSpace(r.FormValue("scrape_link")),
		Summary: strings.TrimSpace(r.FormValue("scrape_summary")),
		Date:    strings.TrimSpace(r.FormValue("scrape_date")),
		Image:   strings.TrimSpace(r.FormValue("scrape_image")),
	}
}

// scrapeURLFromForm returns the page to scrape from the builder form: the
// initial open posts `url`, while re-posts (auto-detect, preview) include the
// form's hidden `feed_url` field.
func scrapeURLFromForm(r *http.Request) string {
	if u := r.FormValue("url"); u != "" {
		return u
	}
	return r.FormValue("feed_url")
}

// authorPrefill returns a default name and avatar for the create-new-author
// fields, derived from the page's <title> and icon. Falls back to the host.
func (s *Server) authorPrefill(ctx context.Context, pageURL, fallback string) (string, string) {
	meta, _ := s.discoverer.PageMeta(ctx, pageURL)
	name := meta.Title
	if name == "" {
		if u, err := url.Parse(stripWWW(fallback)); err == nil && u.Host != "" {
			name = u.Host
		}
	}
	return name, meta.IconURL
}
