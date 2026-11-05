package feedparse

import (
	"regexp"
	"strings"
)

// FailureKind categorizes why a feed's last poll failed. It exists for read-side
// surfaces (the admin errors card) that only have the stored feeds.last_error
// text: the typed errors below are gone by the time a later request renders the
// row, so the category is recovered from the message the poller recorded.
type FailureKind string

const (
	// FailureRateLimit is a 429 (or 503 carrying a retry hint): waiting, not a
	// broken feed.
	FailureRateLimit FailureKind = "rate-limit"
	// FailureServer is an HTTP 5xx response.
	FailureServer FailureKind = "http-5xx"
	// FailureClient is an HTTP 4xx response other than a rate limit.
	FailureClient FailureKind = "http-4xx"
	// FailureNetwork is a transport-level failure: DNS, TCP dial, timeout, TLS,
	// or a truncated/closed connection.
	FailureNetwork FailureKind = "network"
	// FailureOther is anything else (a parse error, a read error, an
	// unrecognized plugin error).
	FailureOther FailureKind = "other"
)

// statusCodePattern matches the status code shape both error producers use:
// feedparse's "get <url>: status 500" and pluginapi's "status 500 for <url>".
// It deliberately requires the word "status" so a URL containing digits is not
// misread as a response code.
var statusCodePattern = regexp.MustCompile(`\bstatus\s+(\d{3})\b`)

// networkSignatures are the transport-level phrases a stored error can carry.
// They come from net/http and the dialer (which the SSRF-safe client wraps), so
// they are stable Go runtime strings rather than site-specific messages.
var networkSignatures = []string{
	"dial tcp",
	"dial udp",
	"no such host",
	"i/o timeout",
	"connection refused",
	"connection reset",
	"connection timed out",
	"tls handshake",
	"tls: ",
	"x509:",
	"network is unreachable",
	"temporary failure in name resolution",
	"server misbehaving",
	"context deadline exceeded",
}

// eofPattern matches a bare EOF, which a truncated read reports. It is matched
// separately (with word boundaries) so a message that merely contains the
// letters (a name, a token) is not mistaken for a transport failure.
var eofPattern = regexp.MustCompile(`\beof\b`)

// ClassifyFailureText categorizes a stored poll error. It is best-effort: the
// original typed error is not persisted, so a plugin that words its failure
// unusually falls through to FailureOther.
func ClassifyFailureText(msg string) FailureKind {
	lower := strings.ToLower(msg)
	if lower == "" {
		return FailureOther
	}
	// Rate limits come first: a 429 is both a 4xx status and pacing, and a 503
	// with a retry hint is recorded as "rate limited", not as a server error.
	if strings.Contains(lower, "rate limited") || strings.Contains(lower, "too many requests") {
		return FailureRateLimit
	}
	if m := statusCodePattern.FindStringSubmatch(lower); m != nil {
		switch m[1][0] {
		case '4':
			if m[1] == "429" {
				return FailureRateLimit
			}
			return FailureClient
		case '5':
			return FailureServer
		}
	}
	for _, sig := range networkSignatures {
		if strings.Contains(lower, sig) {
			return FailureNetwork
		}
	}
	if eofPattern.MatchString(lower) {
		return FailureNetwork
	}
	return FailureOther
}
