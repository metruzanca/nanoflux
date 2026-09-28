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
const APIVersion = "0.3"

// Capability selects which operation a Match call is about. A Fetcher may
// support either or both.
type Capability int

const (
	// CapDiscover asks whether the plugin can find feeds on a page URL.
	CapDiscover Capability = iota
	// CapFetch asks whether the plugin can fetch a feed URL into items.
	CapFetch
	// CapRender asks whether the plugin can resolve view-time media for an
	// item (an embed player, gallery images, or an external source link) that
	// cannot be represented on a stored Item.
	CapRender
	// CapDocs asks whether the plugin documents u. It is how a docs affordance
	// reaches a feed the plugin does not own the fetch of: a reddit feed is
	// fetched by the generic parser (so feeds.plugin_name is empty), yet the
	// reddit plugin still documents its category filters for reddit URLs.
	CapDocs
	// CapEnrich asks whether the plugin decorates a feed's freshly parsed items
	// at ingest time. It runs after either fetch path (plugin or generic
	// parser), so a plugin can add per-item identity to items it does not fetch
	// — reddit sets the cross-feed SharedKey on the generic parser's items.
	CapEnrich
	// CapDecorate asks whether the plugin decorates stored items for display
	// (source attribution, card kind, thumbnail). It is view-time and batched,
	// and may not perform network I/O.
	CapDecorate
	// CapURLPolicy asks whether the plugin knows URL rules for a site: the
	// canonical feed shape, the token a feed URL represents, and a feed
	// derivable from a page URL with no request. It lets a site's URL handling
	// live in the plugin instead of the core (reddit is the case that matters:
	// its .rss sits behind a tight rate limit, so discovery must not probe it).
	CapURLPolicy
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
	// UserAgent overrides the User-Agent the host sends for this plugin's
	// mediated requests. Empty uses the app default. Some sites require a
	// specific identity (e.g. Instagram serves the logged-out post grid only to
	// crawler agents), so a plugin that needs one sets it here.
	UserAgent string
	// Summary is a one-line description of what the plugin does, shown on its
	// card in the admin panel. Empty hides the line.
	Summary string
	// HasDocs reports whether the plugin implements Docser. It is filled in by
	// the host (from a type assertion for native plugins, or from the wire for
	// external ones) — a plugin does not set it. It lets a UI offer a docs
	// button without calling Docs() just to find out there is nothing.
	HasDocs bool
}

// Candidate is one feed a plugin discovered on a page. Title/IconURL/HomeURL are
// preview metadata shown in the add-feed form; when set they take precedence
// over the host's generic page metadata.
type Candidate struct {
	FeedURL string
	Title   string // feed display title
	IconURL string // author avatar / site icon, resolved to an absolute URL
	HomeURL string
	// AuthorName is the preferred name for a newly created author when the site
	// suggests one that differs from the feed title (reddit users: "spez"
	// rather than "u/spez"). Empty means the host falls back to Title.
	AuthorName string
	// Derived means the feed URL follows from the page URL alone, so the host
	// must not fetch the page to validate the feed or gather its metadata.
	// reddit is the case: its .rss sits behind a tight anonymous rate limit,
	// and probing it would spend the host's request budget on discovery.
	Derived bool
}

// Enclosure is one media attachment on an item.
type Enclosure struct {
	URL      string
	MIMEType string
	Length   int64
}

// Item is one normalized feed entry.
type Item struct {
	GUID string
	// Identity is the stable per-feed key the host deduplicates on. When empty
	// the host falls back to GUID, so a plugin that never sets it behaves
	// exactly as before.
	//
	// Set it when GUID may change shape for the same underlying entry — e.g. a
	// plugin that once emitted a post URL and now emits "scheme:id". GUID is
	// the display/feed identity and may be regenerated; Identity is the durable
	// one. For a site with numeric post ids, Identity is that id (or a stable
	// prefix like "post:<id>"), never the URL.
	Identity string
	// SharedKey is the identity an item shares across a user's feeds, so the
	// same entry seen through two subscriptions is stored once with shared
	// read/favorite/list state. It is set by an Enricher (not authored on a
	// fetched Item), e.g. reddit's post fullname "reddit:t3_<id>". Empty means
	// the item is not cross-deduplicated.
	SharedKey   string
	Title       string
	Link        string
	Summary     string
	ImageURL    string
	PublishedAt string // "" when unknown; host format is "2006-01-02 15:04:05" UTC
	// DurationSec is the media's runtime in seconds (a video's length). 0 means
	// unknown. Stored so a card can show it without a view-time lookup.
	DurationSec int
	// Categories are feed-provided labels the host stores (items.categories) and
	// matches filter rules against (field = "category"). Set them when the site
	// carries context worth filtering on, e.g. a subreddit, an author, or a
	// reblog marker. The generic RSS/Atom parser fills the equivalent from
	// <category> values; a plugin that reads a site's own API sets them directly.
	Categories []string
	Enclosures []Enclosure
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

// Renderer is an optional capability a plugin may implement to resolve
// view-time media for an item. Some sites (reddit) mark a post's real content —
// an external destination, an embeddable player, a multi-image gallery — only in
// the item's HTML, and enumerating it needs live lookups the stored Item cannot
// carry. Such a plugin answers Match(u, CapRender) for the item's link, and
// Render resolves the media when the item modal opens.
//
// Render returning ErrUnsupportedCapability (or an empty Media) means "nothing
// extra"; the host falls back to the item's stored fields.
type Renderer interface {
	// Render resolves an item's view-time media. req describes the item; the
	// returned Media is merged into the modal.
	Render(ctx context.Context, req RenderRequest, h Host) (Media, error)
}

// Docser is an optional capability a plugin may implement to document itself.
// Docs returns Markdown describing the plugin — typically how it maps a site's
// context into item categories, so a user can write filter rules against them
// (reddit, for example, exposes each post's subreddit and author as categories).
//
// Docs must return promptly and must not perform network I/O: the host calls it
// while rendering a modal. Returning "" means "no documentation".
type Docser interface {
	Docs() string
}

// Enricher is an optional capability a plugin may implement to decorate a
// feed's freshly parsed items at ingest time. It runs after either fetch path
// (the plugin's own Fetch, or the generic parser for a feed the plugin does not
// own), matched on the feed URL via CapEnrich. It lets a plugin attach per-item
// identity to items it does not fetch: reddit sets each post's cross-feed
// SharedKey so the same post seen through a subreddit feed and the poster's
// user feed is stored once.
//
// EnrichItems must be pure with respect to the items it returns and must not
// reorder or drop them: it returns Enrichments addressed by Index, and the host
// leaves any item without an Enrichment untouched. It may use Host.Do, but a
// pure function of the parsed items is strongly preferred since it runs on
// every poll.
type Enricher interface {
	// EnrichItems returns per-item enrichment for a feed's parsed items.
	EnrichItems(ctx context.Context, req EnrichRequest, h Host) ([]Enrichment, error)
}

// EnrichRequest is one feed's parsed output, offered for enrichment.
type EnrichRequest struct {
	// FeedURL is the feed the items came from.
	FeedURL string
	// Feed is the feed-level metadata.
	Feed Feed
	// Items are the freshly parsed items, in order.
	Items []Item
}

// Enrichment is enrichment for one item in an EnrichRequest, addressed by its
// position in EnrichRequest.Items.
type Enrichment struct {
	Index     int
	SharedKey string
}

// URLPolicy is an optional capability a plugin may implement to own a site's
// URL rules, so site-specific URL handling lives in the plugin instead of the
// core. It is gated by CapURLPolicy and matched on the URL in question, and none
// of its methods may perform network I/O.
type URLPolicy interface {
	// CanonicalizeFeedURL returns the preferred stored shape of a feed URL
	// (reddit: the www host, /user/{name}, a user's bare feed -> /submitted.rss).
	// It is idempotent and returns the input unchanged when it has no rule.
	CanonicalizeFeedURL(raw string) string
	// FeedToken returns the token a feed URL represents ("r/cats" for
	// /r/cats.rss, "u/sam" for /user/sam/submitted.rss), or "" when the URL is
	// not one of the plugin's feeds. The host matches it against an item's
	// Tokens to link an item to the user's subscribed feed for it.
	FeedToken(feedURL string) string
}

// Decoration is an optional capability a plugin may implement to render a
// stored item's source attribution at view time (reddit's "r/cats by u/sam").
// It is gated by CapDecorate and matched on the item's link, is batched per
// page, and must not perform network I/O.
//
// The plugin returns the parts and their tokens; the host resolves a token to
// the user's subscribed feed (via URLPolicy.FeedToken) and supplies the internal
// link, falling back to the part's URL (an external site) when unsubscribed. So
// no site-specific link logic lives in the core.
type Decoration interface {
	// Decorate returns the source attribution for each item, addressed by its
	// index in DecorateRequest.Items. An item with no Decoration keeps its
	// author/feed source line.
	Decorate(ctx context.Context, req DecorateRequest) ([]Decorated, error)
}

// DecorateRequest is a batch of stored items offered for view-time decoration.
type DecorateRequest struct {
	Items []Item
}

// Decorated is one item's view-time decoration, addressed by Index.
type Decorated struct {
	Index int
	// Kind is the item's display classification (see ItemKind). KindText (0) is
	// the safe default and leaves the stored rendering in place.
	Kind ItemKind
	// Attribution is the ordered source line ("r/cats", "by", "u/sam"). Empty
	// keeps the item's author/feed source.
	Attribution []SourcePart
	// ThumbURL overrides the row thumbnail when the stored ImageURL is not the
	// right one to show (e.g. a gallery's cover is a tiny crop). Empty keeps the
	// stored ImageURL.
	ThumbURL string
}

// SourcePart is one piece of a source attribution line.
type SourcePart struct {
	// Text is the display text ("r/cats", "by", "u/sam").
	Text string
	// Token, when set, is a site token ("r/cats") the host resolves to the
	// user's subscribed feed via URLPolicy.FeedToken. When it resolves, the part
	// links internally; otherwise the host uses URL.
	Token string
	// URL is the external destination when Token has no matching subscription
	// (a site profile page). Empty means the part is plain text ("by").
	URL string
}

// ItemKind classifies an item's primary content for card rendering and media.
type ItemKind int

const (
	// KindText is the default: a text post, or an item no plugin classified.
	KindText ItemKind = iota
	// KindImage is a single-image post (rendered as a thumbnail card).
	KindImage
	// KindGallery is a multi-image post (thumbnail is its first image).
	KindGallery
	// KindLink is a post whose primary content is an external site.
	KindLink
	// KindVideo is a video post with a playable embed.
	KindVideo
	// KindAudio is an audio/podcast post.
	KindAudio
)

// RenderRequest describes the item whose view-time media is being resolved.
type RenderRequest struct {
	// Link is the item's stored link (the post permalink).
	Link string
	// Summary is the item's stored HTML content.
	Summary string
	// ImageURL is the item's stored thumbnail, if any.
	ImageURL string
	// Config is the per-feed plugin config (JSON), may be nil.
	Config json.RawMessage
}

// Media is a plugin's view-time rendering for an item. Every field is optional;
// empty fields leave the item's stored content in place.
type Media struct {
	// SourceURL is an external destination of a link post (rendered as a
	// "source" entry in the item menu).
	SourceURL string
	// EmbedSrc is an iframe src for an embeddable player.
	EmbedSrc string
	// Gallery is a list of full-res image URLs to render instead of the single
	// stored thumbnail.
	Gallery []string
}
