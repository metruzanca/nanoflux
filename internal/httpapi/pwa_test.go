package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestPWAManifest(t *testing.T) {
	_, h := newTestServer(t)
	rr := doGetRaw(h, "/manifest.webmanifest")
	if rr.Code != http.StatusOK {
		t.Fatalf("manifest: %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/manifest+json") {
		t.Fatalf("manifest content type: %q", ct)
	}
	body := rr.Body.String()
	for _, want := range []string{`"short_name": "nanoflux"`, `"start_url": "/"`, `"display": "standalone"`, `"/static/pwa-192.png"`, `"/static/pwa-512.png"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("manifest missing %q: %s", want, body)
		}
	}
}

func TestPWAServiceWorker(t *testing.T) {
	_, h := newTestServer(t)
	rr := doGetRaw(h, "/sw.js")
	if rr.Code != http.StatusOK {
		t.Fatalf("service worker: %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
		t.Fatalf("sw content type: %q", ct)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("sw cache control should be no-cache, got %q", cc)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "addEventListener('fetch'") {
		t.Fatalf("service worker missing fetch handler: %s", body)
	}
}

func TestPWAHeadTags(t *testing.T) {
	_, h := newTestServer(t)
	rr := doGetRaw(h, "/login")
	if rr.Code != http.StatusOK {
		t.Fatalf("login page: %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{
		`rel="manifest" href="/manifest.webmanifest"`,
		`rel="apple-touch-icon" href="/static/apple-touch-icon.png"`,
		`name="theme-color" content="#111318"`,
		`name="mobile-web-app-capable" content="yes"`,
		`name="apple-mobile-web-app-capable" content="yes"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("login page missing %q", want)
		}
	}
}
