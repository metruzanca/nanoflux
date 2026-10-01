package httpapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/metruzanca/nanoflux/internal/imageutil"
)

// maxProxyImageBytes caps how much of an upstream image is proxied. sniffLen is
// how many bytes are read up front to identify the format by content.
const (
	maxProxyImageBytes = 5 << 20
	sniffLen           = 512
)

// setImageHeaders hardens a response that serves fetched image bytes. The
// sandbox CSP neutralizes any residual active content (a parser gap, an SVG
// that slipped through a mislabeled type) if a browser opens the URL directly.
func setImageHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
}

// imgProxy fetches a remote image server-side so author avatars load without
// CORS or hotlink restrictions. Only http(s) URLs are allowed; responses are
// size-capped and cached by the browser.
func (s *Server) imgProxy(w http.ResponseWriter, r *http.Request) {
	s.serveRemoteImage(w, r, r.URL.Query().Get("u"))
}

// serveRemoteImage fetches and streams one remote image, applying the proxy's
// URL, size and content-type rules. It backs both /img and the /cache fallback
// (a cached blob that has been purged falls back to its original URL).
func (s *Server) serveRemoteImage(w http.ResponseWriter, r *http.Request, rawURL string) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		http.Error(w, "bad url", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		http.Error(w, "bad url", http.StatusBadRequest)
		return
	}
	req.Header.Set("User-Agent", "nanoflux/0.1")

	resp, err := s.client.Do(req)
	if err != nil {
		http.Error(w, "upstream fetch failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}

	// Trust the upstream type when it is an image; otherwise sniff the bytes.
	// Many CDNs mislabel images (Bluesky's video thumbnails come back as
	// application/octet-stream), which would otherwise show as a broken image.
	// Sniffing also lets us echo the real type, so the browser renders the
	// image instead of downloading it.
	prefix, _ := io.ReadAll(io.LimitReader(resp.Body, sniffLen))
	ct, ok := imageutil.Sniff(resp.Header.Get("Content-Type"), prefix)
	if !ok {
		http.Error(w, "not an image", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", ct)
	setImageHeaders(w)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = io.Copy(w, io.LimitReader(io.MultiReader(bytes.NewReader(prefix), resp.Body), maxProxyImageBytes))
}
