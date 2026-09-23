// Package pluginapi defines the interface between nanoflux (the host) and feed
// plugins. A plugin may be built into nanoflux ("native") or distributed as an
// out-of-process executable that nanoflux loads at runtime ("external"). Both
// implement the same Fetcher interface; the host adapts external plugins over
// gRPC.
//
// The API is versioned: APIVersion is reported by every plugin and checked by
// the host. Breaking changes are expected until v1.0.0.
package pluginapi

import (
	"context"
	"encoding/json"
	"net/url"
	"time"
)

// APIVersion is the plugin API version. The host refuses a plugin whose
// Meta().APIVersion differs.
const APIVersion = "0.1"

// Capability selects which operation a Match call is about. A Fetcher may
// support either or both.
type Capability int

const (
	// CapDiscover asks whether the plugin can find feeds on a page URL.
	CapDiscover Capability = iota
	// CapFetch asks whether the plugin can fetch a feed URL into items.
	CapFetch
)

// Meta describes a plugin to the host.
type Meta struct {
	// Name identifies the plugin (also the value stored in feeds.plugin_name).
	Name string
	// APIVersion must equal APIVersion for the host to load the plugin.
	APIVersion string
	// RawNetwork opts the plugin out of host-mediated HTTP: it may use its own
	// client, and must surface rate limits itself. Default false is strongly
	// preferred so the host can pace and inspect every request.
	RawNetwork bool
}

// Candidate is one feed a plugin discovered on a page. Title/IconURL/HomeURL are
// preview metadata shown in the add-feed form; when set they take precedence
// over the host's generic page metadata.
type Candidate struct {
	FeedURL string
	Title   string // feed display title
	IconURL string // author avatar / site icon, resolved to an absolute URL
	HomeURL string
}

// Enclosure is one media attachment on an item.
type Enclosure struct {
	URL      string
	MIMEType string
	Length   int64
}

// Item is one normalized feed entry.
type Item struct {
	GUID        string
	Title       string
	Link        string
	Summary     string
	ImageURL    string
	PublishedAt string // "" when unknown; host format is "2006-01-02 15:04:05" UTC
	Enclosures  []Enclosure
}

// Feed carries feed-level metadata.
type Feed struct {
	Title       string
	HomeURL     string
	Description string
	ImageURL    string
}

// Result is a plugin's answer to a Fetch: the feed metadata and its items, plus
// the HTTP cache validators and pagination cursor when the feed supplies them.
type Result struct {
	Feed         Feed
	Items        []Item
	ETag         string
	LastModified string
	NextPageURL  string
}

// FetchRequest is one fetch of a feed.
type FetchRequest struct {
	URL          string
	ETag         string
	LastModified string
	Config       json.RawMessage // per-feed plugin config (JSON), may be nil
}

// HTTPRequest is an outbound request a plugin makes through Host.Do.
type HTTPRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// HTTPResponse is the raw response from Host.Do: the plugin sees the status,
// headers, and body and owns its own interpretation.
//
// A rate-limited response carries the limit inline (RateLimited/RetryAfter)
// rather than as an error, so it transports identically whether the plugin is
// native (in-process) or external (over gRPC). The plugin decides how to react;
// if it cannot avoid the limit it returns a RateLimit error so the host parks
// the feed.
type HTTPResponse struct {
	Status      int
	Headers     map[string]string
	Body        []byte
	RateLimited bool
	RetryAfter  time.Duration // meaningful when RateLimited
}

// Host is what the plugin uses to talk back to nanoflux. It is fulfilled
// in-process for native plugins and over gRPC for external ones.
type Host interface {
	// Do performs an HTTP request on the plugin's behalf. The host applies the
	// shared User-Agent and timeout policy and inspects every response for rate
	// limits, cooling the request host as needed. A rate-limited response is
	// returned normally with RateLimited set (not as an error), so behavior is
	// identical for native and external plugins.
	Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error)
	// Now returns the current time (UTC).
	Now() time.Time
	// Logf logs a message to the host's log, prefixed with the plugin name.
	Logf(format string, args ...any)
}

// Fetcher is the interface a plugin implements. Match gates Discover and Fetch
// by URL shape; Discover is optional (return ErrUnsupportedCapability).
type Fetcher interface {
	// Meta describes the plugin.
	Meta() Meta
	// Match reports whether the plugin handles u for the given capability.
	Match(u *url.URL, cap Capability) bool
	// Discover returns feeds found on pageURL, or ErrUnsupportedCapability.
	Discover(ctx context.Context, pageURL string, h Host) ([]Candidate, error)
	// Fetch returns the feed's metadata and items for req.
	Fetch(ctx context.Context, req FetchRequest, h Host) (Result, error)
}
