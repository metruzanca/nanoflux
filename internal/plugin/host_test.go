package plugin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// TestHostDoRateLimited proves the host returns the rate-limit signal inline
// (not as an error), which is what lets it survive the gRPC boundary.
func TestHostDoRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	cool := NewCooldown()
	h := NewHost(srv.Client(), cool, "test", "")
	resp, err := h.Do(context.Background(), pluginapi.HTTPRequest{Method: "GET", URL: srv.URL})
	if err != nil {
		t.Fatalf("Do returned an error instead of an inline limit: %v", err)
	}
	if !resp.RateLimited || resp.RetryAfter <= 0 {
		t.Fatalf("response should carry the rate limit inline: %+v", resp)
	}
	if resp.Status != http.StatusTooManyRequests {
		t.Fatalf("status = %d", resp.Status)
	}

	// The host is now cooling: a second request is refused inline, not errored.
	resp2, err := h.Do(context.Background(), pluginapi.HTTPRequest{Method: "GET", URL: srv.URL})
	if err != nil {
		t.Fatalf("cooled request errored: %v", err)
	}
	if !resp2.RateLimited {
		t.Fatalf("a cooled host should report RateLimited inline: %+v", resp2)
	}
}

// TestHostDoNoRedirectAndSetCookies proves a plugin can stop a redirect and
// read every Set-Cookie value, which a login form flow needs.
func TestHostDoNoRedirectAndSetCookies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "first", Value: "1"})
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc"})
			http.Redirect(w, r, "/feed", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := NewHost(srv.Client(), NewCooldown(), "test", "")
	resp, err := h.Do(context.Background(), pluginapi.HTTPRequest{
		Method: "GET", URL: srv.URL + "/login", NoRedirect: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != http.StatusFound {
		t.Fatalf("status = %d, want the 302 instead of following it", resp.Status)
	}
	if len(resp.SetCookies) != 2 {
		t.Fatalf("SetCookies = %v", resp.SetCookies)
	}

	// Without NoRedirect the client follows, so the login hop is not visible.
	resp, err = h.Do(context.Background(), pluginapi.HTTPRequest{Method: "GET", URL: srv.URL + "/login"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("followed status = %d", resp.Status)
	}
}
