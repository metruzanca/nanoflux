package httpapi

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/discover"
	"github.com/metruzanca/nanoflux/internal/feedparse"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

// feedPreviewForm is the quick-add form shown once a feed is identified.
type feedPreviewForm struct {
	Title   string
	FeedURL string
	HomeURL string
	Authors []store.Author
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
	pageURL := strings.TrimSpace(r.FormValue("url"))
	if pageURL == "" {
		writeFormError(w, "feed-preview", "enter a url")
		return
	}
	authors, _ := s.store.Authors.List(u.ID)

	// The URL itself may already be a feed; if so we can also derive the home page.
	if res, err := feedparse.Fetch(r.Context(), pageURL, s.client, "", ""); err == nil {
		home := res.Feed.HomeURL
		if home == "" {
			home = pageURL
		}
		web.RenderFragment(w, "feed_preview", feedPreviewForm{
			Title: res.Feed.Title, FeedURL: pageURL, HomeURL: home, Authors: authors,
		})
		return
	}

	candidates, err := s.discoverer.Discover(r.Context(), pageURL)
	if err != nil {
		log.Printf("feed preview discover: %v", err)
		writeFormError(w, "feed-preview", "could not inspect that url")
		return
	}
	if len(candidates) == 0 {
		writeFormError(w, "feed-preview", "no feed found at that url")
		return
	}

	if chosen := strings.TrimSpace(r.FormValue("feed_url")); chosen != "" {
		for _, c := range candidates {
			if c.FeedURL == chosen {
				s.renderFeedPreviewForm(r.Context(), w, c, pageURL, authors)
				return
			}
		}
	}

	if len(candidates) == 1 {
		s.renderFeedPreviewForm(r.Context(), w, candidates[0], pageURL, authors)
		return
	}

	web.RenderFragment(w, "feed_choose", feedChoose{URL: pageURL, Candidates: candidates})
}

func (s *Server) renderFeedPreviewForm(ctx context.Context, w http.ResponseWriter, c discover.Candidate, pageURL string, authors []store.Author) {
	if c.Title == "" {
		if meta, err := s.discoverer.PageMeta(ctx, pageURL); err == nil {
			c.Title = meta.Title
		}
	}
	web.RenderFragment(w, "feed_preview", feedPreviewForm{
		Title: c.Title, FeedURL: c.FeedURL, HomeURL: pageURL, Authors: authors,
	})
}

// authorPreview inspects a URL and pre-fills name (from <title>) and avatar
// (favicon) for the new-author form.
func (s *Server) authorPreview(w http.ResponseWriter, r *http.Request) {
	pageURL := strings.TrimSpace(r.FormValue("url"))
	if pageURL == "" {
		writeFormError(w, "author-preview", "enter a url")
		return
	}
	meta, err := s.discoverer.PageMeta(r.Context(), pageURL)
	if err != nil {
		log.Printf("author preview: %v", err)
		writeFormError(w, "author-preview", "could not inspect that url")
		return
	}
	name := meta.Title
	if name == "" {
		name = pageURL
	}
	web.RenderFragment(w, "author_preview", authorPreviewForm{
		Name: name, URL: pageURL, AvatarURL: meta.IconURL,
	})
}
