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
const APIVersion = "0.5"

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
	// CapSharedKey asks whether the plugin assigns a cross-feed SharedKey to a
	// feed's freshly parsed items at ingest time. It runs after either fetch
	// path (plugin or generic parser), so a plugin can give items it does not
	// fetch an identity shared across a user's feeds — reddit sets
	// "reddit:t3_<id>" on the generic parser's items.
	CapSharedKey
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
	// CapEnrich asks whether the plugin can enrich an item's body at ingest
	// time: full-text extraction, a translation, a transcript. It runs on newly
	// stored items after either fetch path, matched on the item's link, and may
	// use Host.Do for a live fetch. The enriched body is stored separately from
	// the feed's own summary, so it survives re-polling.
	//
	// Capabilities are transmitted as integers across the gRPC boundary, so new
	// ones are appended: never insert one between existing values.
	CapEnrich
	// CapProvision asks whether the plugin can create a new feed on the remote
	// service on the user's behalf (a Kill the Newsletter inbox, say), returning
	// its feed URL plus display-only fields. There is no URL to match on: the
	// registry offers a plugin whose Meta.ProvisionLabel is non-empty.
	CapProvision
	// CapFeedAdmin asks whether the plugin can manage an already-subscribed
	// feed's remote settings and lifecycle (sync a title, delete it upstream).
	// It is matched on the feed URL, like the other URL-gated capabilities.
	CapFeedAdmin
	// CapImageCache asks whether a feed's images need host-side caching because
	// the site serves them with short-lived, signed URLs that expire before the
	// next poll. It is matched on the feed URL. A plugin that claims it makes
	// caching mandatory for its feeds (the user cannot turn it off); the host
	// downloads each item's image and image enclosures at poll time into its own
	// storage and renders the cached copy. Default (no plugin) is remote loading,
	// and a user may opt an individual feed in without a plugin.
	CapImageCache
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
	// ProvisionLabel, when non-empty, advertises the plugin in the add-feed
	// flow's "create" menu: the label is the menu entry ("newsletter (Kill the
	// Newsletter)"). A plugin that implements Provisioner but leaves this empty
	// is never offered as a create option, so provisioning stays opt-in.
	ProvisionLabel string
	// HasFeedAdmin reports whether the plugin implements FeedAdmin. It is filled
	// in by the host (from a type assertion for native plugins, or from the wire
	// for external ones) — a plugin does not set it. Like HasDocs, it exists so
	// a UI can advertise remote feed management without probing.
	HasFeedAdmin bool
	// HasSettings reports whether the plugin implements Configurable. It is
	// filled in by the host (a type assertion for native plugins, the wire for
	// external ones) — a plugin does not set it. It lets the admin page offer a
	// settings form without asking every plugin for its schema.
	HasSettings bool
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

// Enclosure render kinds. Kind is a hint: the host renders the enclosure with
// its built-in player for the kind, and it wins over what the host would infer
// from MIMEType/extension. This is how a site whose media is mislabeled (or has
// no useful extension) still renders correctly without a host change. The
// values mirror the host's own resolution; keep them stable.
const (
	// EnclosureKindAuto (the zero value) lets the host infer the kind from
	// MIMEType and the URL's extension.
	EnclosureKindAuto = ""
	// EnclosureKindImage renders inline as an image.
	EnclosureKindImage = "image"
	// EnclosureKindAudio renders as an audio player.
	EnclosureKindAudio = "audio"
	// EnclosureKindVideo renders as a native video player (a progressive file).
	EnclosureKindVideo = "video"
	// EnclosureKindHLS renders as a video player backed by an HLS manifest
	// (.m3u8); the host plays it with its bundled HLS player.
	EnclosureKindHLS = "hls"
	// EnclosureKindLink is an attachment that is only a link (a download or a
	// non-inline file), never embedded as media.
	EnclosureKindLink = "link"
)

// Enclosure is one media attachment on an item.
type Enclosure struct {
	URL      string
	MIMEType string
	Length   int64
	// Kind is the declared render kind (see EnclosureKind*). Empty means the
	// host infers it from MIMEType/extension.
	Kind string
	// Poster is a thumbnail for a video or audio enclosure. Empty falls back to
	// the item's ImageURL.
	Poster string
	// Title is a human label for the enclosure's download link. Empty falls back
	// to the URL's filename.
	Title string
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
	// read/favorite/list state. It is set by a SharedKeyer (not authored on a
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

// SharedKeyer is an optional capability a plugin may implement to assign a
// cross-feed SharedKey to a feed's freshly parsed items at ingest time. It runs
// after either fetch path (the plugin's own Fetch, or the generic parser for a
// feed the plugin does not own), matched on the feed URL via CapSharedKey. It
// lets a plugin give per-item identity to items it does not fetch: reddit sets
// each post's cross-feed key so the same post seen through a subreddit feed and
// the poster's user feed is stored once.
//
// SharedKeys must be pure with respect to the items it returns and must not
// reorder or drop them: it returns entries addressed by Index, and the host
// leaves any item without an entry untouched. It may use Host.Do, but a pure
// function of the parsed items is strongly preferred since it runs on every poll.
type SharedKeyer interface {
	// SharedKeys returns the cross-feed key for the items it can resolve, each
	// addressed by its index in the request.
	SharedKeys(ctx context.Context, req SharedKeyRequest, h Host) ([]ItemSharedKey, error)
}

// SharedKeyRequest is one feed's parsed output, offered to a SharedKeyer.
type SharedKeyRequest struct {
	// FeedURL is the feed the items came from.
	FeedURL string
	// Feed is the feed-level metadata.
	Feed Feed
	// Items are the freshly parsed items, in order.
	Items []Item
}

// ItemSharedKey is a cross-feed key for one item in a SharedKeyRequest,
// addressed by its position in SharedKeyRequest.Items. An empty SharedKey means
// "no shared identity" (the host leaves the item as-is).
type ItemSharedKey struct {
	Index     int
	SharedKey string
}

// Enricher is an optional capability a plugin may implement to enrich an item's
// body at ingest time: full-text extraction, a translation, a transcript. It
// runs on newly stored items (not on every poll) and is matched on the item's
// link via CapEnrich, so a plugin can enrich items from a feed it does not
// fetch. It may use Host.Do for a live fetch (the page, a translation API).
//
// Enrich must not change an item's identity; it only produces a body. The host
// stores it as items.content, separate from the feed's own summary, so a
// re-poll's snapshot refresh never clobbers it. An empty Content means "no
// enrichment" (the item keeps rendering its summary).
type Enricher interface {
	// Enrich returns the enriched body for each item, addressed by its index in
	// the request. Returning fewer entries than items is fine.
	Enrich(ctx context.Context, req EnrichRequest, h Host) ([]Enriched, error)
}

// EnrichRequest is a batch of newly stored items offered for enrichment.
type EnrichRequest struct {
	Items []Item
}

// Enriched is one item's enrichment, addressed by its position in
// EnrichRequest.Items.
type Enriched struct {
	Index int
	// Content is the enriched body (HTML). Empty leaves the item's summary in
	// place.
	Content string
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
	// DedupeKey is a view-time content identity for cross-feed deduplication:
	// two items with the same key, seen in different feeds, collapse into one
	// row. Use it for the same content reposted under different titles (reddit
	// link posts crossposted to several subreddits share their external URL),
	// keyed by both the content and its author so unrelated posts that merely
	// share a URL are not merged. Empty keeps the default title-only dedupe.
	DedupeKey string
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
	// GUID is the item's stored GUID.
	GUID string
	// Link is the item's stored link (the post permalink).
	Link string
	// Summary is the item's stored HTML content.
	Summary string
	// ImageURL is the item's stored thumbnail, if any.
	ImageURL string
	// Kind is the item's display classification (see ItemKind).
	Kind ItemKind
	// Categories are the item's stored categories.
	Categories []string
	// Enclosures are the item's stored media attachments, so a renderer can
	// decide how to render (or replace) media the host already stored.
	Enclosures []Enclosure
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

// Field is one display-only key/value a plugin surfaces for a feed it manages,
// e.g. the inbox address a newsletter feed is subscribed with. It is rendered
// read-only so a user can copy it; a plugin that wants an editable value
// describes it in FeedAdmin.Settings and reads it back from the action's
// Fields.
type Field struct {
	Name  string // stable key, e.g. "email"
	Label string // human label, e.g. "subscribe this address"
	Value string
	// Kind hints the widget: "text" (default), "url", or "email". The host may
	// ignore it and render plain text.
	Kind string
}

// Provisioner is an optional capability a plugin may implement to create a feed
// on the remote service on the user's behalf. It is advertised by a non-empty
// Meta.ProvisionLabel (there is no URL to match), and runs when a user picks the
// plugin from the add-feed create menu.
type Provisioner interface {
	// Provision creates a remote feed and returns its feed URL and metadata.
	// It may use Host.Do for the create request. The host then stores the feed
	// with the generic parser reading the returned FeedURL.
	Provision(ctx context.Context, req ProvisionRequest, h Host) (Provisioned, error)
}

// ProvisionRequest is the input to Provision.
type ProvisionRequest struct {
	// Title is the user-supplied feed title. A plugin may send it to the site
	// as the remote feed's title and/or use it locally.
	Title string
}

// Provisioned is a newly created remote feed.
type Provisioned struct {
	// FeedURL is the URL the host stores and polls (fetched by the generic
	// parser unless the plugin also claims CapFetch).
	FeedURL string
	// Title overrides the user's title when the site returns its own.
	Title string
	// HomeURL is the feed's web page, when it has one.
	HomeURL string
	// Fields are display-only values to show after creation (the inbox address).
	Fields []Field
}

// FeedAdmin is an optional capability a plugin may implement to manage an
// already-subscribed feed's remote settings and lifecycle. It is gated by
// CapFeedAdmin and matched on the feed URL.
//
// Settings must be pure (no network I/O): it is called while rendering the
// feed's page. Action runs on a user's explicit request and may use Host.Do.
type FeedAdmin interface {
	// FeedFields returns the display-only fields for a feed URL, or nil when
	// the URL is not one the plugin manages. It must not perform network I/O.
	// (Named FeedFields, not Settings, so a plugin can also implement
	// Configurable without a method-name collision.)
	FeedFields(feedURL string) []Field
	// Action performs a remote management action and returns a message and any
	// refreshed fields. Deleted reports that the remote feed was deleted, so
	// the host can drop the local feed too when the user asked for it.
	Action(ctx context.Context, req FeedActionRequest, h Host) (FeedActionResult, error)
}

// FeedActionRequest is one management action on a feed.
type FeedActionRequest struct {
	FeedURL string
	// Action is the requested operation. The host sends "save" (sync the local
	// title/icon to the site) and "delete" (remove the remote feed). A plugin
	// may define more.
	Action string
	// Fields carries the submitted values, keyed by Field.Name.
	Fields map[string]string
}

// FeedActionResult is a completed management action.
type FeedActionResult struct {
	// Message is a short user-facing confirmation ("feed deleted on Kill the
	// Newsletter").
	Message string
	// Deleted reports that the remote feed no longer exists.
	Deleted bool
	// Fields, when non-empty, replaces the feed's displayed fields.
	Fields []Field
}

// SettingField describes one configuration value a plugin needs (a service
// base URL, an API token, a session cookie). The host renders a form from the
// schema in the admin panel and pushes the stored values back through
// Configure; a plugin never writes the value itself.
type SettingField struct {
	// Name is the stable key, e.g. "base_url". Configure receives values under
	// this name.
	Name string
	// Label is the human label shown in the form.
	Label string
	// Kind hints the widget: "text" (default), "password", "url", or "bool".
	// A "password" value is write-only in the UI and is never rendered back.
	// A "bool" renders as a checkbox and reaches Configure as "1" (on) or "0"
	// (off), so a plugin gets a stable value whether or not the box was ticked.
	Kind string
	// Placeholder is optional example text.
	Placeholder string
	// Help is optional explanatory text shown under the field.
	Help string
	// Required rejects an empty value on save, when the plugin needs one.
	Required bool
}

// Configurable is an optional capability a plugin may implement to declare the
// settings it needs and receive their values. The host calls Configure at load
// with the stored values (so Match-time configuration, like a service host, is
// available before the first request) and again whenever an admin saves the
// form. It is not matched on a URL: every configurable plugin is configured.
//
// Configure must be safe to call with an incomplete map (missing fields arrive
// as empty strings) so a plugin can start unconfigured and report what is
// missing when it is used.
type Configurable interface {
	// Settings returns the fields this plugin needs, in display order.
	Settings() []SettingField
	// Configure receives the stored values, keyed by SettingField.Name.
	Configure(values map[string]string)
}

// ProxyBypasser is an optional capability a plugin may implement to have its
// image URLs loaded directly by the browser instead of through the host's image
// proxy. Some sites serve images with permissive hotlinking but block the
// server-side requests the proxy makes (a browser User-Agent is fine, a
// datacenter one is refused), so the proxied request fails while a direct one
// works. A plugin whose site behaves that way returns true for its image hosts.
//
// It is not matched on a URL capability: the host asks every plugin that
// implements it, and any true answer wins. BypassProxy must be pure (no network
// I/O) and cheap; it runs for every rendered image URL.
type ProxyBypasser interface {
	// BypassProxy reports whether rawurl should be loaded directly rather than
	// through the host's image proxy.
	BypassProxy(rawurl string) bool
}
