package httpapi

import (
	"github.com/charmbracelet/log"
	"net/http"
	"strconv"
	"strings"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/discover"
	"github.com/metruzanca/nanoflux/internal/feedparse"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

// feedPreviewForm is the quick-add form shown once a feed is identified.
type feedPreviewForm struct {
	Title            string
	FeedURL          string
	HomeURL          string
	Authors          []store.Author
	SelectedAuthorID int64
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

// feedPreview inspects a URL (direct feed or page) and renders the quick-add
// form. When the URL yields several feeds, it renders a dropdown first; the
// chosen feed re-posts here with feed_url set and renders a single form.
func (s *Server) feedPreview(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	pageURL := normalizeURL(r.FormValue("url"))
	if pageURL == "" {
		renderError(w, r, "enter a url")
		return
	}
	authors, _ := s.store.Authors.List(u.ID)
	selectedAuthor, _ := strconv.ParseInt(r.FormValue("author_id"), 10, 64)

	// The URL itself may already be a feed; if so we can also derive the home page.
	if res, err := feedparse.Fetch(r.Context(), pageURL, s.client, "", ""); err == nil {
		home := res.Feed.HomeURL
		if home == "" {
			home = pageURL
		}
		web.Render(w, r, feedPreviewFields(feedPreviewForm{
			Title: res.Feed.Title, FeedURL: pageURL, HomeURL: home, Authors: authors,
			SelectedAuthorID: selectedAuthor,
		}))
		return
	}

	candidates, err := s.discoverer.Discover(r.Context(), pageURL)
	if err != nil {
		log.Error("feed preview discover", "err", err)
		renderError(w, r, "could not inspect that url")
		return
	}
	if len(candidates) == 0 {
		renderError(w, r, "no feed found at that url")
		return
	}

	if chosen := strings.TrimSpace(r.FormValue("feed_url")); chosen != "" {
		for _, c := range candidates {
			if c.FeedURL == chosen {
				s.renderFeedPreviewForm(r, w, c, pageURL, authors, selectedAuthor)
				return
			}
		}
	}

	if len(candidates) == 1 {
		s.renderFeedPreviewForm(r, w, candidates[0], pageURL, authors, selectedAuthor)
		return
	}

	web.Render(w, r, feedChooser(feedChoose{URL: pageURL, Candidates: candidates}))
}

func (s *Server) renderFeedPreviewForm(r *http.Request, w http.ResponseWriter, c discover.Candidate, pageURL string, authors []store.Author, selectedAuthor int64) {
	if c.Title == "" {
		if meta, err := s.discoverer.PageMeta(r.Context(), pageURL); err == nil {
			c.Title = meta.Title
		}
	}
	web.Render(w, r, feedPreviewFields(feedPreviewForm{
		Title: c.Title, FeedURL: c.FeedURL, HomeURL: pageURL, Authors: authors,
		SelectedAuthorID: selectedAuthor,
	}))
}

// authorPreview inspects a URL and pre-fills name (from <title>) and avatar
// (favicon) for the new-author form.
func (s *Server) authorPreview(w http.ResponseWriter, r *http.Request) {
	pageURL := normalizeURL(r.FormValue("url"))
	if pageURL == "" {
		renderError(w, r, "enter a url")
		return
	}
	meta, err := s.discoverer.PageMeta(r.Context(), pageURL)
	if err != nil {
		log.Error("author preview", "err", err)
		renderError(w, r, "could not inspect that url")
		return
	}
	name := meta.Title
	if name == "" {
		name = pageURL
	}
	web.Render(w, r, authorPreviewFields(authorPreviewForm{
		Name: name, URL: pageURL, AvatarURL: meta.IconURL,
	}))
}
