package httpapi

import (
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
}

// newAuthor returns the authorCreateFields payload for the default "create new
// author" selection.
func (f feedPreviewForm) newAuthor() authorPreviewForm {
	return authorPreviewForm{Name: f.NewAuthorName, URL: f.HomeURL, AvatarURL: f.NewAuthorAvatar}
}

// feedChoose is the dropdown shown when a page exposes multiple feeds.
type feedChoose struct {
	URL        string
	Candidates []discover.Candidate
}

// authorPreviewForm pre-fills the new-author fields from a detected page.
type authorPreviewForm struct {
	Name      string
	URL       string
	AvatarURL string
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
	authors, _ := s.store.Authors.List(u.ID)
	selectedAuthor, _ := strconv.ParseInt(r.FormValue("author_id"), 10, 64)
	var fixedAuthor *store.Author
	if r.FormValue("scoped") == "1" {
		if a, err := s.store.Authors.ByID(u.ID, selectedAuthor); err == nil {
			fixedAuthor = &a
		}
	}

	feedURL := pageURL
	if mapped, ok := s.mappedFeedURL(u.ID, pageURL); ok {
		feedURL = mapped
	}

	// The URL itself may already be a feed; if so we can also derive the home page.
	if res, err := feedparse.Fetch(r.Context(), feedURL, s.client, "", ""); err == nil {
		home := res.Feed.HomeURL
		if home == "" || feedURL != pageURL {
			home = pageURL
		}
		s.renderFeedPreviewForm(r, w, discover.Candidate{FeedURL: feedURL, Title: res.Feed.Title, HomeURL: home}, pageURL, home, authors, selectedAuthor, fixedAuthor)
		return
	}

	candidates, err := s.discoverer.Discover(r.Context(), feedURL)
	if err != nil {
		log.Error("feed preview discover", "err", err)
	}
	if len(candidates) == 0 && feedURL != pageURL {
		// A mapping transformed the url but its feed is gone or the page is
		// not a feed; fall back to the original input.
		if candidates, err = s.discoverer.Discover(r.Context(), pageURL); err != nil {
			log.Error("feed preview discover fallback", "err", err)
		}
	}
	if len(candidates) == 0 {
		renderError(w, r, "no feed found at that url")
		return
	}

	if chosen := strings.TrimSpace(r.FormValue("feed_url")); chosen != "" {
		for _, c := range candidates {
			if c.FeedURL == chosen {
				s.renderFeedPreviewForm(r, w, c, pageURL, pageURL, authors, selectedAuthor, fixedAuthor)
				return
			}
		}
	}

	if len(candidates) == 1 {
		s.renderFeedPreviewForm(r, w, candidates[0], pageURL, pageURL, authors, selectedAuthor, fixedAuthor)
		return
	}

	web.Render(w, r, feedChooser(feedChoose{URL: pageURL, Candidates: candidates}))
}

// renderFeedPreviewForm renders the combined add form for one discovered feed.
// homeURL is the page the user entered (the feed's home page); the default
// new-author name is derived from that page's <title>, falling back to the
// feed title, then the page host; the avatar comes from the site icon.
func (s *Server) renderFeedPreviewForm(r *http.Request, w http.ResponseWriter, c discover.Candidate, pageURL, homeURL string, authors []store.Author, selectedAuthor int64, fixedAuthor *store.Author) {
	meta, _ := s.discoverer.PageMeta(r.Context(), pageURL)
	name := meta.Title
	if name == "" {
		name = c.Title
	}
	if name == "" {
		if u, err := url.Parse(pageURL); err == nil && u.Host != "" {
			name = u.Host
		}
	}
	form := feedPreviewForm{
		Title: c.Title, FeedURL: c.FeedURL, HomeURL: homeURL, Authors: authors,
		SelectedAuthorID: selectedAuthor, FixedAuthor: fixedAuthor,
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
