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
