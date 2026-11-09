package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestClientIPTrustProxy(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 198.51.100.7")

	if got := clientIP(r, false); got != "10.0.0.1" {
		t.Fatalf("untrusted client IP = %q, want RemoteAddr", got)
	}
	if got := clientIP(r, true); got != "198.51.100.7" {
		t.Fatalf("trusted client IP = %q, want the last forwarded entry", got)
	}
}
