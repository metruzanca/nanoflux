package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestImgProxy(t *testing.T) {
	_, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	img := []byte{0x89, 'P', 'N', 'G'}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(img)
	}))
	defer upstream.Close()

	// Proxied image is served through the backend.
	req := httptest.NewRequest(http.MethodGet, "/img?u="+url.QueryEscape(upstream.URL+"/x.png"), nil)
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("proxy: %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("content-type = %q", got)
	}
	if string(rr.Body.Bytes()) != string(img) {
		t.Fatalf("body mismatch: %x", rr.Body.Bytes())
	}

	// Non-image upstream content is rejected.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>"))
	}))
	defer bad.Close()
	req = httptest.NewRequest(http.MethodGet, "/img?u="+url.QueryEscape(bad.URL), nil)
	req.AddCookie(cookie)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("non-image: %d", rr.Code)
	}

	// Non-http schemes are rejected.
	req = httptest.NewRequest(http.MethodGet, "/img?u="+url.QueryEscape("file:///etc/passwd"), nil)
	req.AddCookie(cookie)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad scheme: %d", rr.Code)
	}

	// Auth required (web path -> redirect to login).
	req = httptest.NewRequest(http.MethodGet, "/img?u="+url.QueryEscape(upstream.URL), nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusFound || !strings.HasPrefix(rr.Header().Get("Location"), "/login") {
		t.Fatalf("unauthed proxy: %d %q", rr.Code, rr.Header().Get("Location"))
	}
}

func TestAuthorRowsShowAvatar(t *testing.T) {
	s, h := newTestServer(t)
	cookie := sessionCookie(t, h)

	u, _ := s.store.Users.ByUsername("alice")
	s.store.Authors.Create(u.ID, "Metru", "", "https://example.com/favicon.png", "")

	rr := doGet(h, "/authors", cookie)
	body := rr.Body.String()
	if !strings.Contains(body, `/img?u=`+url.QueryEscape("https://example.com/favicon.png")) {
		t.Fatalf("author row missing proxied avatar: %s", body)
	}
	if !strings.Contains(body, "avatar-sm") {
		t.Fatalf("author row missing avatar-sm class: %s", body)
	}
}
