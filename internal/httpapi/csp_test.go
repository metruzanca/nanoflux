package httpapi

import (
	"strings"
	"testing"
)

// TestContentSecurityPolicyFrames proves third-party player iframes are not
// blocked: the app resolves oEmbed providers and plugin Render embeds at view
// time, so frame-src must allow any http(s) origin while scripts stay locked.
func TestContentSecurityPolicyFrames(t *testing.T) {
	s, _ := newTestServer(t)
	csp := s.contentSecurityPolicy()

	if !strings.Contains(csp, "frame-src https: http:") {
		t.Fatalf("frame-src should allow http(s) players: %s", csp)
	}
	// Inbound framing stays locked, and scripts are still inline/eval-free.
	for _, want := range []string{
		"frame-ancestors 'none'",
		"object-src 'none'",
		"script-src 'self'",
	} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP missing %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") || strings.Contains(csp, "unsafe-eval") {
		t.Fatalf("script-src must not allow inline/eval: %s", csp)
	}
}

// TestContentSecurityPolicyHeader proves the relaxed frame-src reaches the
// browser: every response carries it.
func TestContentSecurityPolicyHeader(t *testing.T) {
	_, h := newTestServer(t)
	got := doGetRaw(h, "/login").Header().Get("Content-Security-Policy")
	if !strings.Contains(got, "frame-src https: http:") {
		t.Fatalf("response CSP should allow http(s) frames: %s", got)
	}
}
