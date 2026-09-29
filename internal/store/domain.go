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

// URLPolicy is the host's per-URL site rules, supplied by the plugin layer so
// site-specific URL handling lives in a plugin instead of the store. It is
// dispatched by URL (the plugin layer matches the URL to the owning plugin), so
// the store holds one value that already knows every loaded site's rules.
type URLPolicy interface {
	// CanonicalizeFeedURL returns the preferred stored shape of a feed URL
	// (reddit: www host, /user/{name}, posts-only /submitted.rss). It returns
	// the input unchanged when no plugin has a rule for it.
	CanonicalizeFeedURL(raw string) string
	// FeedToken returns the token a feed URL represents ("r/cats", "u/sam"),
	// used to match an item's tokens back to the user's subscribed feed, or ""
	// when no plugin claims the URL.
	FeedToken(feedURL string) string
}

// identityPolicy is the no-plugin policy: it leaves every URL untouched and
// yields no tokens. It is the default until the plugin layer installs one.
type identityPolicy struct{}

func (identityPolicy) CanonicalizeFeedURL(raw string) string { return raw }
func (identityPolicy) FeedToken(string) string               { return "" }

// ItemDecorator supplies view-time decoration for stored items (source
// attribution, card kind, thumbnail), computed by the plugin layer. It is
// injected by the plugin layer and called in one batch per page load. A nil
// decorator leaves every item with its stored author/feed rendering.
//
// The decorator returns raw parts (with tokens), not resolved links: the store
// resolves each token to the user's subscribed feed (it owns the DB and the URL
// policy), so no site-specific link logic lives in the core.
type ItemDecorator interface {
	// Decorate returns decoration keyed by item id. An item absent from the map
	// keeps its stored rendering.
	Decorate(items []ItemWithFeed) (map[int64]RawDecoration, error)
}

// ItemKind classifies an item's primary content for card rendering and media.
type ItemKind int

const (
	// KindText is the default: a text post, or an item no plugin classified.
	KindText ItemKind = iota
	// KindImage is a single-image post.
	KindImage
	// KindGallery is a multi-image post.
	KindGallery
	// KindLink is a post whose primary content is an external site.
	KindLink
	// KindVideo is a video post.
	KindVideo
	// KindAudio is an audio/podcast post.
	KindAudio
)

// RawDecoration is a plugin's view-time decoration for an item, with its
// attribution tokens unresolved. The store turns it into an ItemDecoration by
// resolving tokens against the user's subscriptions.
type RawDecoration struct {
	Kind        ItemKind
	Attribution []RawAttributionPart
	ThumbURL    string
	// DedupeKey is a plugin-supplied view-time content identity, used by the
	// list layer to collapse the same content reposted under different titles
	// (see pluginapi.Decorated.DedupeKey). Empty disables it.
	DedupeKey string
}

// RawAttributionPart is one unresolved piece of a source line.
type RawAttributionPart struct {
	// Text is the display text ("r/cats", "by", "u/sam").
	Text string
	// Token, when set, is resolved to the user's subscribed feed (internal
	// link); when it does not resolve, URL is used (external).
	Token string
	// URL is the external destination when Token has no subscription. Empty
	// with no Token means plain text ("by").
	URL string
}

// AttributionPart is one resolved piece of an item's source line ("r/cats",
// "by", "u/sam"). URL is always the final destination (internal or external).
type AttributionPart struct {
	Text     string
	URL      string
	External bool
}

// ItemDecoration is an item's view-time decoration, already resolved by the
// store (no further lookups needed).
type ItemDecoration struct {
	Kind        ItemKind
	Attribution []AttributionPart
	ThumbURL    string
}
