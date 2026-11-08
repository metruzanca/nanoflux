// Package urlcanon canonicalizes user-entered URLs that are stored as
// bookmarks rather than fetched as feeds. Feed URLs are owned by the plugin
// URL policy; this package only tightens the parts of a URL that are
// case-insensitive by specification.
package urlcanon

import (
	"net/url"
	"strings"
)

// Link canonicalizes an author-link URL. It trims the input, adds an https
// scheme when the user omitted one, lowercases the scheme and host, and drops
// a port only when it is the scheme's default. The path, query and fragment are
// preserved exactly: they are case-sensitive and may be meaningful, and a
// bookmark must keep pointing at the same resource.
//
// It deliberately keeps single-label hosts (a LAN or tailnet MagicDNS name such
// as "nixos") and non-default ports (for example "http://nixos:8090/"). It does
// not validate the URL; callers keep using their own validity check and reject
// what cannot be parsed.
func Link(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return s
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = canonicalHost(u)
	return u.String()
}

// canonicalHost lowercases the host, preserves a non-default port, and rebrackets
// an IPv6 literal. It returns the authority form (host[:port]).
func canonicalHost(u *url.URL) string {
	host, port := u.Hostname(), u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	host = strings.ToLower(host)
	if strings.Contains(host, ":") {
		// IPv6 literal: url.Hostname strips the brackets, so add them back.
		host = "[" + host + "]"
	}
	if port != "" {
		return host + ":" + port
	}
	return host
}
