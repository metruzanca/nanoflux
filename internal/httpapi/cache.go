package httpapi

import (
	"net/http"
	"strings"

	"github.com/charmbracelet/log"
)

// cacheImage serves a feed image cached by the image cache from object storage.
// It is authenticated (like /img): cached images are the logged-in experience,
// and the original URL is carried as ?u= so a purged cache falls back to the
// remote image rather than a broken one.
func (s *Server) cacheImage(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" || strings.Contains(key, "..") {
		http.NotFound(w, r)
		return
	}
	ct, data, err := s.files.Get(r.Context(), key)
	if err == nil {
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "private, max-age=86400")
		w.Write(data)
		return
	}
	if u := r.URL.Query().Get("u"); u != "" {
		// The cache entry is gone (purged, or not yet written) but the original
		// remote URL is known: proxy it as /img would, rather than showing a
		// broken image.
		s.serveRemoteImage(w, r, u)
		return
	}
	log.Debug("cache image miss", "key", key, "err", err)
	http.NotFound(w, r)
}
