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

// redditCanonicalHost is the reddit host nanoflux rewrites feed URLs to. The
// www host serves a feed in one request; the bare host 301-redirects to it, and
// that extra hop spends a request from reddit's tight anonymous rate-limit
// budget. The canonical shape must therefore be the one with no redirect.
const redditCanonicalHost = "www.reddit.com"

// CanonicalFeedURL rewrites a feed URL to the form nanoflux prefers, for hosts
// where it knows the canonical shape. It leaves other URLs untouched.
//
// Reddit: all of reddit.com/www./old./np./m. collapse to the www.reddit.com
// origin (old./np. redirect .rss to a login wall), the /u/{name} short form is
// rewritten to /user/{name}, and a user's bare feed is rewritten to the
// posts-only /submitted.rss (their overview feed mixes posts and comments).
// Reddit serves the same feeds at www.reddit.com with no redirect, and the
// /user/ form avoids reddit's /u/ -> /user/ redirect; both matter because each
// redirect hop consumes a request from the host's ~1/minute anonymous budget.
// An explicit /comments.rss or /submitted.rss is left as-is.
func CanonicalFeedURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return raw
	}
	host := strings.ToLower(u.Hostname())
	if !redditHosts[host] {
		return raw
	}
	// Canonical host: www.reddit.com, keeping any port.
	u.Scheme = "https"
	if port := u.Port(); port != "" {
		u.Host = redditCanonicalHost + ":" + port
	} else {
		u.Host = redditCanonicalHost
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	// /u/{name} -> /user/{name}: reddit 301-redirects the short form, and the
	// extra hop costs a request from the host's tight anonymous budget.
	if len(parts) >= 2 && parts[0] == "u" {
		parts[0] = "user"
	}
	// A user's bare feed -> the posts-only /submitted.rss. Both bare forms are
	// handled: the ".rss" suffix on the name (/user/{name}.rss) and a trailing
	// ".rss" segment (/user/{name}/.rss). Explicit /submitted.rss and
	// /comments.rss are preserved.
	if len(parts) >= 2 && parts[0] == "user" {
		if name, ok := strings.CutSuffix(parts[1], ".rss"); ok {
			parts = append([]string{parts[0], name}, parts[2:]...)
		}
		if len(parts) >= 3 && parts[2] == ".rss" {
			parts = append([]string{parts[0], parts[1]}, parts[3:]...)
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
