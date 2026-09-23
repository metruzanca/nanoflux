package plugin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// TestHostDoRateLimitedNative proves the in-process host returns the rate-limit
// signal inline (not as an error), which is what lets it survive the gRPC
// boundary for external plugins.
func TestHostDoRateLimitedNative(t *testing.T) {
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
