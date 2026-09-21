package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLogoColorParam(t *testing.T) {
	cases := []struct{ in, want string }{
		{"#ff0000", "ff0000"},
		{"ff0000", "ff0000"},
		{"", "5b8cff"},
		{"bogus", "5b8cff"},
	}
	for _, tc := range cases {
		if got := logoColorParam(tc.in); got != tc.want {
			t.Errorf("logoColorParam(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLogo(t *testing.T) {
	_, h := newTestServer(t)

	cases := []struct {
		name  string
		query string
		want  string // substring expected in the SVG body
	}{
		{"with hash", "?c=%23ff0000", `fill="#ff0000"`},
		{"without hash", "?c=ff0000", `fill="#ff0000"`},
		{"uppercase normalized", "?c=%23FF0000", `fill="#ff0000"`},
		{"missing falls back to default", "", `fill="#5b8cff"`},
		{"invalid falls back to default", "?c=notacolor", `fill="#5b8cff"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/logo.svg"+tc.query, nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status: %d", rr.Code)
			}
			if got := rr.Header().Get("Content-Type"); got != "image/svg+xml" {
				t.Fatalf("content type: %q", got)
			}
			if cc := rr.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
				t.Fatalf("cache control: %q", cc)
			}
			if !strings.Contains(rr.Body.String(), tc.want) {
				t.Fatalf("body missing %q: %s", tc.want, rr.Body.String())
			}
		})
	}

	// Favicon serves the same mark, color-addressed like the logo.
	fav := httptest.NewRequest(http.MethodGet, "/favicon.svg?c=%23ff0000", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, fav)
	if rr.Code != http.StatusOK {
		t.Fatalf("favicon status: %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Fatalf("favicon content type: %q", got)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Fatalf("favicon cache control: %q", cc)
	}
	if !strings.Contains(rr.Body.String(), `fill="#ff0000"`) {
		t.Fatalf("favicon body missing accent: %s", rr.Body.String())
	}
}