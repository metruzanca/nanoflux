package httpapi

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/web"
)

// shareAdd is the PWA share-target landing page (Android shares a URL to the
// installed app, which opens GET /add?url=…). It runs the normal add-feed flow
// for the shared URL; the saved feed redirects to its author page.
func (s *Server) shareAdd(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	pageURL := normalizeURL(sharedURL(r))
	if pageURL == "" {
		// Nothing shareable — fall back to adding manually.
		http.Redirect(w, r, "/authors", http.StatusFound)
		return
	}
	web.Render(w, r, basePage("add feed", u, shareAddPage(pageURL)))
}

// sharedURL reads the shared page URL from the share-target params: the url
// field when present, else the first http(s) URL in text.
func sharedURL(r *http.Request) string {
	q := r.URL.Query()
	if u := strings.TrimSpace(q.Get("url")); u != "" {
		return u
	}
	return firstURL(q.Get("text"))
}

var firstURLRe = regexp.MustCompile(`https?://[^\s]+`)

// firstURL returns the first http(s) URL in s, with trailing punctuation
// trimmed.
func firstURL(s string) string {
	u := firstURLRe.FindString(s)
	return strings.TrimRight(u, ".,;:!?)\"'")
}
