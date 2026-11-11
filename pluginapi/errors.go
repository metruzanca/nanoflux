package pluginapi

import (
	"errors"
	"time"
)

// ErrUnsupportedCapability is returned by Discover when a plugin does not
// implement discovery.
var ErrUnsupportedCapability = errors.New("capability unsupported")

// StatusError reports an HTTP status >= 400 from a fetch. It carries the code
// separately so the host can map it to a user-facing reason without parsing the
// message.
type StatusError struct {
	Code int
	URL  string
}

func (e *StatusError) Error() string {
	return "status " + itoa(e.Code) + " for " + e.URL
}

// PermanentError reports a fetch failure that retrying cannot fix, such as a
// banned community or a deleted profile. The host maps it to its own permanent
// error and pauses the feed, recording Reason as the user-facing explanation.
// An older host that does not know the permanent flag treats it as a normal
// status error and keeps retrying, so this is safe to add without a version
// bump.
type PermanentError struct {
	Code   int
	Reason string
}

func (e *PermanentError) Error() string {
	reason := e.Reason
	if reason == "" {
		reason = "feed is no longer available"
	}
	if e.Code == 0 {
		return reason
	}
	return reason + " (status " + itoa(e.Code) + ")"
}

// RateLimit reports that a fetch hit a rate limit it could not avoid. The host
// parks the feed for RetryAfter and cools the host. A plugin that recovers from
// its own cache should not return this.
type RateLimit struct {
	URL        string
	Status     int
	RetryAfter time.Duration
}

func (e *RateLimit) Error() string {
	return "rate limited (status " + itoa(e.Status) + "), retry after " + e.RetryAfter.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
