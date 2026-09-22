package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/feedparse"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

// saveFeed validates and stores a feed for the extension's add flow: the feed
// is fetched to confirm it exists, the title falls back to the feed's own
// title, and the author is the selected author_id when given (verified against
// the user) or auto-created from the feed otherwise (every feed needs one). It
// reuses the same store/collection/poll paths as the web and JSON API.
//
// homeURL is the page the user was on when adding the feed (what the web flow
// calls the home page) and takes precedence over the feed's own advertised
// home — e.g. a YouTube @handle page rather than the feed's /channel/UC...
// URL. When empty it falls back to res.Feed.HomeURL.
func (s *Server) saveFeed(ctx context.Context, u store.User, feedURL, homeURL, title string, authorID int64) (apiFeed, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	res, err := feedparse.Fetch(ctx, feedURL, client, "", "")
	if err != nil {
		return apiFeed{}, err
	}
	pageURL := homeURL // the page the user was on, before the feed-home fallback
	if homeURL == "" {
		homeURL = res.Feed.HomeURL
	}
	if title == "" {
		title = res.Feed.Title
	}
	if title == "" {
		title = feedURL
	}
	authorName := title
	if authorID != 0 {
		a, err := s.store.Authors.ByID(u.ID, authorID)
		if err != nil {
			return apiFeed{}, err
		}
		authorName = a.Name
	} else {
		a, err := s.store.Authors.Create(u.ID, title, s.pageIconURL(ctx, pageURL), "")
		if err != nil {
			return apiFeed{}, err
		}
		if a.AvatarURL != "" {
			cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			s.autoCacheAuthorAvatar(cctx, a)
			cancel()
		}
		authorID = a.ID
	}
	f, err := s.store.Feeds.Create(u.ID, authorID, title, feedURL, homeURL, "", 900)
	if err != nil {
		return apiFeed{}, err
	}
	if err := s.store.Collections.AssignAuto(u.ID, f.ID, homeURL, feedURL); err != nil {
		log.Error("assign auto collection", "feed_id", f.ID, "err", err)
	}
	s.pollFeedNow(f)
	return apiFeed{
		ID: f.ID, Title: f.Title, FeedURL: f.FeedURL, HomeURL: f.HomeURL,
		AuthorID: f.AuthorID, AuthorName: authorName,
	}, nil
}

// pageIconURL derives an author avatar URL from the page the user was on when
// adding the feed — the site favicon, or the channel's og:image on YouTube. It
// returns "" when no page URL is known or the fetch fails, so a feed added
// without a page is still created.
func (s *Server) pageIconURL(ctx context.Context, pageURL string) string {
	if pageURL == "" {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	meta, err := s.discoverer.PageMeta(cctx, pageURL)
	if err != nil {
		return ""
	}
	return meta.IconURL
}

// apiExtFeedForm returns the add-feed form as an HTML fragment for the browser
// extension. The popup loads it with htmx and submits it to /api/ext/save.
func (s *Server) apiExtFeedForm(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	feedURL := normalizeURL(r.FormValue("feed_url"))
	if feedURL == "" {
		web.Render(w, r, extError("feed url required"))
		return
	}
	homeURL := normalizeURL(r.FormValue("url"))
	title := strings.TrimSpace(r.FormValue("title"))
	authors, _ := s.store.Authors.List(u.ID)
	web.Render(w, r, extFeedForm(feedURL, homeURL, title, authors))
}

// apiExtSave stores a feed for the browser extension and returns an HTML
// fragment (success or error) that htmx swaps into the popup.
func (s *Server) apiExtSave(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	feedURL := normalizeURL(r.FormValue("feed_url"))
	if feedURL == "" {
		web.Render(w, r, extError("feed url required"))
		return
	}
	authorID, _ := strconv.ParseInt(r.FormValue("author_id"), 10, 64)
	f, err := s.saveFeed(r.Context(), u, feedURL, normalizeURL(r.FormValue("home_url")), strings.TrimSpace(r.FormValue("title")), authorID)
	if err != nil {
		log.Error("extension save feed", "url", feedURL, "err", err)
		web.Render(w, r, extError("could not add that feed"))
		return
	}
	web.Render(w, r, extSaved(f.Title))
}

// savedFeeds holds the normalized feed URLs a user already has (mapped to the
// saved feed's id), so apiDiscover can report which discovered candidates are
// already saved and link back to them. Only the candidate's own feed URL
// counts — matching the page URL against a saved feed's home/feed URL would
// falsely mark unrelated feeds on the page as saved.
type savedFeeds struct {
	feedURLs map[string]int64
}

// savedFeedsFor builds the normalized feed-url -> id map of the user's feeds.
func (s *Server) savedFeedsFor(userID int64) *savedFeeds {
	feeds, _ := s.store.Feeds.List(userID)
	sf := &savedFeeds{feedURLs: map[string]int64{}}
	for _, f := range feeds {
		if f.FeedURL != "" {
			sf.feedURLs[normExtKey(f.FeedURL)] = f.ID
		}
	}
	return sf
}

// saved returns the id of the user's feed matching feedURL, or 0 when it isn't
// saved.
func (sf *savedFeeds) saved(feedURL string) int64 {
	return sf.feedURLs[normExtKey(feedURL)]
}

// feedURLExists reports whether the user already has a feed with this exact
// feed URL (normalized). It is the duplicate check for adding a feed: two feeds
// may share a title or home URL, but not their feed URL.
func (s *Server) feedURLExists(userID int64, feedURL string) bool {
	return s.savedFeedsFor(userID).saved(feedURL) != 0
}

// normExtKey normalizes a URL for "already saved" matching: lowercase host
// without a leading www., the path with a trailing slash trimmed, and the query
// string (feeds like YouTube's differ only by channel_id=…).
func normExtKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(host, "www.")
	p := strings.TrimSuffix(u.EscapedPath(), "/")
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		return host + p + "?" + u.RawQuery
	}
	return host + p
}
