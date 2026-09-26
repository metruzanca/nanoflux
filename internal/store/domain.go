package store

import (
	"net"
	"net/url"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// RegistrableDomain returns the registrable domain (eTLD+1) of a URL or bare
// hostname, ignoring subdomains: "https://www.youtube.com/feeds" and
// "https://m.youtube.com" both map to "youtube.com". It returns "" for empty
// or unparseable input.
func RegistrableDomain(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	host := hostname(s)
	if host == "" {
		return ""
	}
	if net.ParseIP(host) != nil {
		return host
	}
	if eTLD, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return eTLD
	}
	// Fall back to the last two labels for hosts the public suffix list does
	// not know (intranet hosts, single-label names).
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return host
	}
	return strings.Join(labels[len(labels)-2:], ".")
}

// hostname extracts a lowercase hostname from a URL or bare hostname, without
// a port or path.
func hostname(s string) string {
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// redditHosts are the reddit hostnames whose feeds nanoflux normalizes.
var redditHosts = map[string]bool{
	"reddit.com": true, "www.reddit.com": true,
	"old.reddit.com": true, "np.reddit.com": true,
	"m.reddit.com": true,
}

// CanonicalFeedURL rewrites a feed URL to the form nanoflux prefers, for hosts
// where it knows the canonical shape. It leaves other URLs untouched.
//
// Reddit: all of reddit.com/www./old./np./m. collapse to the bare reddit.com
// origin (old./np. redirect .rss to a login wall), the /user/{name} form is
// rewritten to /u/{name}, and a user's bare feed is rewritten to the
// posts-only /submitted.rss (their overview feed mixes posts and comments).
// Reddit serves the same feeds on reddit.com, and the shorter, stable shape
// matches what discover.Derive produces. An explicit /comments.rss or
// /submitted.rss is left as-is.
func CanonicalFeedURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return raw
	}
	host := strings.ToLower(u.Hostname())
	if !redditHosts[host] {
		return raw
	}
	// Canonical host: reddit.com, keeping any port.
	u.Scheme = "https"
	if port := u.Port(); port != "" {
		u.Host = "reddit.com:" + port
	} else {
		u.Host = "reddit.com"
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	// /user/{name} -> /u/{name}.
	if len(parts) >= 2 && parts[0] == "user" {
		parts[0] = "u"
	}
	// A user's bare feed (nothing after the name, or the ".rss" suffix on the
	// name) -> the posts-only /submitted.rss. Explicit /submitted.rss and
	// /comments.rss are preserved.
	if len(parts) >= 2 && parts[0] == "u" {
		if name, ok := strings.CutSuffix(parts[1], ".rss"); ok {
			parts = append([]string{parts[0], name}, parts[2:]...)
		}
		if len(parts) == 2 {
			parts = append(parts, "submitted.rss")
		}
	}
	if len(parts) >= 1 && parts[0] != "" {
		u.Path = "/" + strings.Join(parts, "/")
	}
	return u.String()
}
