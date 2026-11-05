package feedparse

import "testing"

func TestClassifyFailureText(t *testing.T) {
	cases := []struct {
		msg  string
		want FailureKind
	}{
		// Rate limits: the generic error text, a plugin-shaped one, and a bare
		// 429 status.
		{"get https://example.com/feed: rate limited (status 429), retry after 5m0s", FailureRateLimit},
		{"rate limited (status 429), retry after 1m0s", FailureRateLimit},
		{"get https://example.com/feed: status 429", FailureRateLimit},
		{"get https://example.com/feed: too many requests", FailureRateLimit},

		// HTTP status faults, in both producer shapes.
		{"get https://example.com/feed: status 500", FailureServer},
		{"get https://example.com/feed: status 503", FailureServer},
		{"status 502 for https://example.com/feed", FailureServer},
		{"get https://example.com/feed: status 404", FailureClient},
		{"status 403 for https://example.com/feed", FailureClient},

		// Transport failures.
		{`get https://example.com/feed: Get "https://example.com/feed": dial tcp 93.184.216.34:443: connect: connection refused`, FailureNetwork},
		{`get https://example.com/feed: Get "https://example.com/feed": dial tcp: lookup example.com: no such host`, FailureNetwork},
		{`get https://example.com/feed: Get "https://example.com/feed": net/http: TLS handshake timeout`, FailureNetwork},
		{`get https://example.com/feed: Get "https://example.com/feed": context deadline exceeded`, FailureNetwork},
		{`get https://example.com/feed: read https://example.com/feed: unexpected EOF`, FailureNetwork},

		// Everything else, including a name that merely contains "eof".
		{"parse https://example.com/feed: invalid character '<' looking for beginning of value", FailureOther},
		{"empty feed", FailureOther},
		{"user geoff is banned", FailureOther},
		{"", FailureOther},
	}
	for _, c := range cases {
		if got := ClassifyFailureText(c.msg); got != c.want {
			t.Errorf("ClassifyFailureText(%q) = %q, want %q", c.msg, got, c.want)
		}
	}
}
