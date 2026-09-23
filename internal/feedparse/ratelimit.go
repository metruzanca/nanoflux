package feedparse

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RateLimitError reports that a host refused the fetch for rate-limiting
// reasons (HTTP 429, or 503 with a retry hint). It carries how long the caller
// should wait before fetching from that host again, derived from the response's
// Retry-After or x-ratelimit-reset headers, or a fallback when neither is
// present.
type RateLimitError struct {
	URL        string
	Status     int
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("get %s: rate limited (status %d), retry after %s", e.URL, e.Status, e.RetryAfter)
}

// Rate-limit backoff bounds. A header with no usable value falls back to
// defaultRateLimitBackoff; any value is clamped to maxRateLimitBackoff so a
// bogus header can't park a feed for an unbounded time.
const (
	defaultRateLimitBackoff = 5 * time.Minute
	maxRateLimitBackoff     = time.Hour
)

// isRateLimited reports whether a response status is a rate-limit signal. 429 is
// the standard; 503 is sometimes used with a Retry-After during overload.
func isRateLimited(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	return resp.StatusCode == http.StatusServiceUnavailable && retryHint(resp) > 0
}

// IsRateLimited reports whether a response is a rate-limit signal. Exported for
// the plugin host, which inspects every mediated response.
func IsRateLimited(resp *http.Response) bool { return isRateLimited(resp) }

// RateLimitBackoff returns the retry delay for a rate-limited response.
// Exported for the plugin host.
func RateLimitBackoff(resp *http.Response) time.Duration { return rateLimitBackoff(resp) }

// rateLimitBackoff returns how long to wait before retrying, from Retry-After
// (seconds or HTTP-date) or x-ratelimit-reset (seconds), clamped to the bounds.
func rateLimitBackoff(resp *http.Response) time.Duration {
	d := retryHint(resp)
	if d <= 0 {
		d = defaultRateLimitBackoff
	}
	if d > maxRateLimitBackoff {
		d = maxRateLimitBackoff
	}
	return d
}

// retryHint reads the retry delay from standard rate-limit headers, or 0 when
// none is present/unparseable. Retry-After is preferred (it is the HTTP
// standard); x-ratelimit-reset (seconds until the window resets) is the common
// fallback.
func retryHint(resp *http.Response) time.Duration {
	if v := strings.TrimSpace(resp.Header.Get("Retry-After")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
		if t, err := http.ParseTime(v); err == nil {
			if d := time.Until(t); d > 0 {
				return d
			}
		}
	}
	if v := strings.TrimSpace(resp.Header.Get("x-ratelimit-reset")); v != "" {
		// Reddit sends a float (e.g. "18.0") or an integer number of seconds.
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return time.Duration(f * float64(time.Second))
		}
	}
	return 0
}
