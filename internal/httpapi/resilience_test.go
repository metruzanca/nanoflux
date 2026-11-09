package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthz(t *testing.T) {
	_, h := newTestServer(t)
	rr := doGetRaw(h, "/healthz")
	if rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Fatalf("healthz: got %d %q", rr.Code, rr.Body.String())
	}
}

func TestHealthzReportsDatabaseFailure(t *testing.T) {
	s, h := newTestServer(t)
	if err := s.store.DB().Close(); err != nil {
		t.Fatal(err)
	}
	if rr := doGetRaw(h, "/healthz"); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("healthz with a closed database: got %d, want 503", rr.Code)
	}
}

func TestRecoverPanicsRenders500(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("panic: got %d, want 500", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "something went wrong") {
		t.Fatalf("panic page missing message: %s", rr.Body.String())
	}
}
