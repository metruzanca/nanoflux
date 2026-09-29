package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCacheImageServesStoredBlob(t *testing.T) {
	s, h := newTestServer(t)
	key := "cache/pics/1/0.png"
	if err := s.files.Put(context.Background(), key, "image/png", []byte("\x89PNGdata")); err != nil {
		t.Fatal(err)
	}
	cookies := login(t, h).Result().Cookies()

	req := httptest.NewRequest(http.MethodGet, "/cache/"+key, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("content type = %q", got)
	}
	if rr.Body.String() != "\x89PNGdata" {
		t.Fatalf("body = %q", rr.Body.String())
	}
}

func TestCacheImageFallsBackToRemote(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("remote"))
	}))
	defer upstream.Close()

	s, h := newTestServer(t)
	s.client = upstream.Client()
	cookies := login(t, h).Result().Cookies()

	req := httptest.NewRequest(http.MethodGet, "/cache/cache/feeds/9/0.png?u="+upstream.URL+"/x.png", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || rr.Body.String() != "remote" {
		t.Fatalf("fallback: status %d body %q", rr.Code, rr.Body.String())
	}
}

func TestCacheImageRejectsTraversal(t *testing.T) {
	_, h := newTestServer(t)
	cookies := login(t, h).Result().Cookies()
	req := httptest.NewRequest(http.MethodGet, "/cache/cache/..%2fetc%2fpasswd", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}
