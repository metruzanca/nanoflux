package httpapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxProxyImageBytes caps how much of an upstream image is proxied. sniffLen is
// how many bytes are read up front to identify the format by content.
const (
	maxProxyImageBytes = 5 << 20
	sniffLen           = 512
)

// imgProxy fetches a remote image server-side so author avatars load without
// CORS or hotlink restrictions. Only http(s) URLs are allowed; responses are
// size-capped and cached by the browser.
func (s *Server) imgProxy(w http.ResponseWriter, r *http.Request) {
	u, err := url.Parse(r.URL.Query().Get("u"))
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
	ct := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if !strings.HasPrefix(ct, "image/") {
		if sniffed := http.DetectContentType(prefix); strings.HasPrefix(sniffed, "image/") {
			ct = sniffed
		}
	}
	if !strings.HasPrefix(ct, "image/") {
		http.Error(w, "not an image", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = io.Copy(w, io.LimitReader(io.MultiReader(bytes.NewReader(prefix), resp.Body), maxProxyImageBytes))
}
