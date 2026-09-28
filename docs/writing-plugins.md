# Writing a plugin

A plugin is an out-of-process feed integration: a small executable that
nanoflux runs and talks to over gRPC. Plugins let you add a site-specific
integration (a private API-backed feed, a bespoke scraper) without touching the
nanoflux repo.

Status: **v0.1 implemented.** The API is small and will change before v1.0.0.
This is the authoring guide for the system described in
[`plugin-architecture.md`](plugin-architecture.md); read that for the rationale
(host-mediated HTTP, host-owned rate limiting, why go-plugin).

A working example ships in `examples/`: `plugin-youtube` serves nanoflux's native YouTube integration
over gRPC and is deliberately **identical** to the native plugin
(`internal/plugin/native/youtube`) — it imports that very package rather than
copying it, so the native and external forms cannot drift. At the time of
writing they are the same code; only the packaging differs.

## Where plugins live

nanoflux scans a single directory, `NF_PLUGINS_DIR` (default `./plugins`, or
`/plugins` in the container), for executables. In the container that directory
is bind-mounted from the host, so you drop a compiled binary into `./plugins/`
next to the repo:

```yaml
# docker-compose.yml
volumes:
  - ./plugins:/plugins
```

A plugin is just an executable file — there is no manifest or registration step.
A name like `plugins/nanoflux-plugin-appc` is enough. On a host install, point
`NF_PLUGINS_DIR` at any directory.

## A minimal plugin

A plugin is a normal Go module that imports the `pluginapi` module (a nested
module, `github.com/metruzanca/nanoflux/pluginapi`; see D8 in the architecture
doc) and serves one or more `Fetcher` implementations.

```go
// plugins/nanoflux-plugin-appc/main.go
package main

import (
	"context"
	"net/url"

	goplugin "github.com/hashicorp/go-plugin"
	"github.com/metruzanca/nanoflux/pluginapi"
)

type AppC struct{}

// Meta describes the plugin; the host checks APIVersion before loading.
func (AppC) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: "appc", APIVersion: pluginapi.APIVersion}
}

// Match decides which URLs (and which capability) this plugin handles.
func (AppC) Match(u *url.URL, cap pluginapi.Capability) bool {
	return cap == pluginapi.Fetch && u.Hostname() == "appc.com"
}

// Fetch returns the feed's items. Use h.Do for every outbound request so the
// host can apply its User-Agent/timeout policy and per-host rate limiting.
func (AppC) Fetch(ctx context.Context, req pluginapi.FetchRequest, h pluginapi.Host) (pluginapi.Result, error) {
	resp, err := h.Do(ctx, pluginapi.HTTPRequest{
		Method: "POST",
		URL:    "https://api.appc.com/list-blog-activity",
		Body:   []byte(`{"blog_name":"example"}`),
	})
	if err != nil {
		return pluginapi.Result{}, err
	}
	_ = resp
	return pluginapi.Result{ /* Feed + Items */ }, nil
}

func main() {
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: pluginapi.Handshake,
		Plugins:         pluginapi.PluginSet(&AppC{}), // wraps the Fetcher as a gRPC plugin
		GRPCServer:      goplugin.DefaultGRPCServer,
	})
}
```

Build it like any Go binary and drop it in the directory:

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o plugins/nanoflux-plugin-appc .
```

Then restart nanoflux. Logs from the plugin (stdout/stderr, or `h.Logf`) are
forwarded to nanoflux's logs prefixed with the plugin name.

## The capabilities

A plugin implements `Fetcher`; it may also implement the optional `Renderer`,
`Enricher`, `Decoration`, `URLPolicy` and `Docser` interfaces. `Match` tells the
host which URL shapes (and which capability) each applies to. A plugin may own a
site's whole shape (fetch + discover + enrich + render + URL rules) or decorate a
feed it does not fetch at all.

- **`Fetch` (required)** — given a feed URL, return its `Feed` metadata and
  `[]Item`s. Runs in the poller and on manual refresh.
- **`Discover` (optional)** — given a page URL, return `[]Candidate`s (feeds
  found on that page). Runs in the add-feed "find feed" flow. Set
  `Candidate.Derived` when the feed URL follows from the page URL alone, so the
  host skips the validation page fetch.
- **`Render` (optional)** — given an item, resolve view-time media that a
  stored item cannot carry. Runs when the item modal opens.
- **`Enrich` (optional)** — given a feed's freshly parsed items, set each item's
  `SharedKey` (cross-feed identity). Runs at ingest after either fetch path.
- **`Decorate` (optional)** — given a page's stored items, return each item's
  source attribution and card kind. Runs at view time; must not do network I/O.
- **`URLPolicy` (optional)** — pure site URL rules: the canonical feed shape
  (`CanonicalizeFeedURL`) and the token a feed URL represents (`FeedToken`).
- **`Docs` (optional)** — return Markdown describing the plugin, shown from the
  admin plugin card and from a feed's edit page. `Match(u, CapDocs)` decides
  which URLs it documents.

```go
func (AppC) Discover(ctx context.Context, pageURL string, h pluginapi.Host) ([]pluginapi.Candidate, error) {
	return []pluginapi.Candidate{{
		FeedURL: "https://appc.com/example/rss",
		Title:   "Example",
		IconURL: "https://appc.com/avatar.png", // author avatar / site icon
		HomeURL: pageURL,
	}}, nil
}
```

A candidate's `Title`/`IconURL`/`HomeURL` are the **preview metadata** shown in
the add form; when set, they take precedence over nanoflux's generic page
metadata (`PageMeta`). `AuthorName` overrides the new-author prefill when the
site suggests a cleaner name than the feed title. This is how a plugin keeps a
site's real author name and avatar instead of a generic favicon.

### URL rules (`URLPolicy`)

A site whose feed URL has a known shape — and whose endpoints are rate-limited —
should own those rules, so the host never probes the site during discovery. The
reddit plugin is the reference: `Match` returns true for `CapURLPolicy` on reddit
hosts, `CanonicalizeFeedURL` rewrites every stored reddit URL to the shape reddit
answers without a redirect, `FeedToken` maps a feed URL to the `r/<sub>` /
`u/<name>` token its items carry, and `Discover` derives the feed from the page
URL with no request (`Candidate.Derived`).

```go
func (AppC) CanonicalizeFeedURL(raw string) string { /* redirect-free shape */ }
func (AppC) FeedToken(feedURL string) string        { /* "r/cats" | "u/sam" | "" */ }
```

`CanonicalizeFeedURL` runs on feed create/edit and on a startup pass.
`FeedToken` is matched against an item's categories/decoration tokens to link an
item to the user's subscribed feed for it, even before that feed has polled the
item.

### Ingest enrichment (`Enrich`)

`EnrichItems` runs on a feed's parsed items — whether the plugin fetched them or
the generic parser did — and returns per-item `SharedKey` (addressed by index, so
items are never reordered or dropped). The host stores one item per
`(user, SharedKey)`, so the same entry seen through two subscriptions shares
read/favorite/list state. reddit uses it for the post fullname `t3_<id>`.

### View-time decoration (`Decoration`)

`Decorate` runs on a page's stored items and returns each item's source
attribution and card kind. The parts carry tokens; the host resolves a token to
the user's subscribed feed (via the same plugin's `FeedToken`) for the internal
link, falling back to the part's URL (the external site). That is how reddit
renders "r/cats by u/sam": the sub and the poster link internally when
subscribed, else to reddit. `Kind` picks the row card
(`KindText`/`KindImage`/`KindGallery`/`KindLink`/`KindVideo`/`KindAudio`), and
`ThumbURL` overrides the row thumbnail (a gallery's full-res first image).

### View-time rendering (`Render`)

Some sites mark an item's real content only in its HTML — an external
destination, an embeddable player, or a multi-image gallery — and enumerating it
needs live lookups the stored `Item` cannot hold. A plugin implements `Renderer`
for those:

```go
func (AppC) Render(ctx context.Context, req pluginapi.RenderRequest, h pluginapi.Host) (pluginapi.Media, error) {
	// req.Link / req.Summary / req.ImageURL describe the stored item.
	return pluginapi.Media{
		SourceURL: "https://external.example/page", // "source" menu link
		EmbedSrc:  "https://player.example/embed/1", // iframe src
		Gallery:   []string{"https://cdn/1.jpg", "https://cdn/2.jpg"},
	}, nil
}
```

`Match(u, pluginapi.CapRender)` gates it: return true for the item links the
plugin can resolve. Every `Media` field is optional; empty fields leave the
item's stored content in place. Returning `pluginapi.ErrUnsupportedCapability`
(or an empty `Media`) means "nothing extra".

### Documentation (`Docs`)

A plugin can document itself in Markdown so its users know what to expect and
how to write filter rules against it. Implement `Docser`:

```go
//go:embed readme.md
var readme string

func (AppC) Docs() string { return readme }
```

Two things make `Docs` different from the other optional capabilities:

- **It is keyed on `CapDocs`, not on ownership.** A plugin that does not own a
  feed's fetch can still document it. reddit is the case that matters: reddit
  feeds are plain RSS fetched by the generic parser, so `feeds.plugin_name` is
  empty, yet the reddit plugin documents the subreddit/author categories it
  adds. Its `Match` returns true for `CapDocs` on reddit hosts, so the feed
  edit page finds it by URL:

  ```go
  func (*Plugin) Match(u *url.URL, cap pluginapi.Capability) bool {
      switch cap {
      case pluginapi.CapRender, pluginapi.CapDocs:
          return isRedditHost(u.Hostname())
      }
      return false
  }
  ```

- **It must not do network I/O.** The host calls `Docs()` while rendering a
  modal, so return Markdown from an embedded string. Raw HTML in it is escaped
  by the renderer, so a readme cannot inject markup.

`Meta().Summary` is a separate one-line description shown on the plugin's card;
keep it short.

### Item identity (`Identity`)

The host deduplicates a feed's items on `Item.Identity` when you set it, and on
`Item.GUID` when you do not. **Set it whenever your `GUID` may change shape for
the same entry over time.** `GUID` is the display/feed identity and you are free
to regenerate it; `Identity` is the durable key and must never change for the
same entry:

```go
it := pluginapi.Item{
	GUID:     "appc:" + post.ID,   // display identity, may evolve
	Identity: "appc:" + post.ID,   // durable: the site's stable post id
	// ...
}
```

The failure this prevents: a plugin that once emitted a post URL as `GUID` and
later switched to `"scheme:id"` gets the same post stored **twice** — the host
cannot know the two GUIDs mean one entry. Derive `Identity` from the site's own
immutable identifier (a numeric post/gallery id), never from a URL or a
timestamp. A common real change is a plugin whose `GUID` starts as the post URL
and later becomes `"scheme:<id>"`; setting `Identity` to `"scheme:<id>"` from the
start keeps the identity stable across that change.

If you shipped a plugin before setting `Identity` and it split entries, the
duplicates can be merged with `nanoflux item dedup --apply` (see below). The host
also keeps a one-time fallback: when `Identity` is empty it uses `GUID`, so a
plugin that never sets it behaves exactly as before the field existed.

Adding `Identity` to an existing plugin needs a repair pass: the host backfills
existing rows' dedup key from their GUID, so a row already stored under the old
GUID scheme does not match the new `Identity` until the split is merged. After
updating the plugin, run `nanoflux item dedup --apply`. It is idempotent and
safe to re-run; a pair that only appears after the feed's next poll merges on the
following run. The repair keys on link + published time, not GUID, so it does not
matter whether the new identity has been polled yet.

### Media duration (`DurationSec`)

Set `Item.DurationSec` to a media entry's runtime in **seconds** when the site
reports one (a video's length). The host stores it (as `items.duration_sec`) and
renders it as a pill on the item card. Leave it `0` when unknown: a live stream,
a text post, a site that does not expose it. It is part of the item snapshot, so
a later re-poll can fill it in on an existing row without changing identity or
read state.

The native YouTube plugin is the reference: its channel RSS carries no duration,
so it fetches through YouTube's browse API, which returns the runtime alongside
each entry.

### Categories (`Categories`)

Set `Item.Categories` to feed-provided labels when the site carries context
worth filtering on: a subreddit, an author, a post kind. The host stores them
(`items.categories`) and matches a filter rule's `field = "category"` against
**any one** category (contains, case-insensitive; regex per category). So a
plugin that marks, say, reblogs with a single `"reblog"` label lets the user
delete them all with one rule (`action: delete, field: category, pattern:
reblog`) without any site-specific host code.

The generic RSS/Atom parser fills the equivalent from an entry's `<category>`
values and author names, which is how reddit's `r/<sub>` and `u/<name>` work.
A plugin that reads a site's own API sets the slice directly. Keep values clean
(no leading slash, no surrounding whitespace) so a filter matches predictably.
Leave it `nil` when the site offers nothing to filter on.

### Media and enclosures (`Enclosures`, `ImageURL`)

`Item.Enclosures` attaches media to an entry; set `Enclosures` for anything the
reader can play or open (images, audio, video). The host stores them per item and
renders images inline, audio/video in a player, and other files as download
links. `Item.ImageURL` is the listing thumbnail. Because enclosure storage is
refreshed on every poll (not only on insert), a plugin that starts returning
enclosures later fills them in on an existing feed without an identity change.

An image enclosure's URL is also the image the host renders inline; a video
enclosure is played with `<video controls>`. Prefer a directly playable file
(e.g. an `mp4`) over a streaming manifest: a manifest needs a player the
frontend does not ship. The native Bluesky plugin is the reference for
reconstructing media from a site's own records: its profile RSS carries no
media, so it reads `app.bsky.feed.post` records and resolves each blob to a
full-size image or an `mp4` via `com.atproto.sync.getBlob`.

### Repairing duplicate items

A GUID-scheme change stores the same entry under two rows. The host provides a
conservative repair:

```bash
nanoflux item dedup            # report the groups that would merge
nanoflux item dedup --apply    # merge them
```

It groups only rows in the **same feed with the same link and same published
time** — a triple a GUID change leaves untouched, so distinct posts are never
merged. The row most recently confirmed by the feed (the current scheme, which
future polls will match) is kept; the others' read/favorite state, enclosures,
list memberships and share links carry over. Run it inside the container
(`make shell`) or on the host against the same `NF_DB`.

### Cross-feed items (`SharedKey` across feeds)

Identity dedup is per feed: two subscriptions that both see the same post store
two rows even when `Identity` matches. A plugin's `Enricher` can collapse a post
seen through a user's feeds by setting `Item.SharedKey` (an `Enrichment` return
addressed by index) to an identity the feeds share. reddit sets it to the post
fullname `reddit:t3_<id>`, identical in a subreddit feed and the user feed, so
the host stores one row both feeds are members of (shared read/favorite/list/
share state).

`SharedKey` is the plugin-owned generalization of the old core "cross-feed key":
the plugin decides what a shared identity is, and the host only deduplicates on
it. `nanoflux item cross-dedup` reports/merges any rows stored before this (and
the server runs the same merge at startup, deriving the reddit key for legacy
rows). See the "Cross-feed items" note in `AGENTS.md`.

`Render` runs in the item-view path with a 4-second timeout, once per modal
open. Use `h.Do` for any lookup (oEmbed discovery, an embed page) so the host
still paces and inspects the requests. `Decorate` is view-time and pure: it must
not use `h.Do`. The native reddit plugin
(`internal/plugin/native/reddit`) is the reference implementation for all of
these.

## HTTP: `Host.Do` vs `RawNetwork`

By default (`RawNetwork == false`) the plugin must make every outbound request
through `h.Do`:

```go
type Host interface {
	Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error)
	Now() time.Time
	Logf(format string, args ...any)
}
type HTTPRequest  struct{ Method, URL string; Headers map[string]string; Body []byte }
type HTTPResponse struct {
	Status      int
	Headers     map[string]string
	Body        []byte
	RateLimited bool
	RetryAfter  time.Duration
}
```

This is what keeps nanoflux the source of truth for:

- **User-Agent and timeouts** — applied by the host, not the plugin.
- **Per-host rate limiting** — the host inspects every response for `429` (or
  `503` with a retry hint) and `Retry-After` / `x-ratelimit-reset`, and cools
  the **actual request host** (e.g. `api.appc.com`, not `appc.com`). A cooled
  host is not hit again until its window passes.

`h.Do` returns the raw status, headers, and body so the plugin still owns its
logic: it can inspect a 404 and try another endpoint, or serve from its own
cache. When the host rate-limits a request it cools the host **and** returns the
response normally with `RateLimited: true` (not as an error) — this inline
signal is deliberate so a native and an external plugin behave identically. A
plugin that cannot avoid the limit returns a `RateLimit` so nanoflux parks the
feed; a plugin that recovered from its cache returns its result as usual.

```go
resp, _ := h.Do(ctx, pluginapi.HTTPRequest{Method: "GET", URL: url})
if resp.RateLimited {
	return pluginapi.Result{}, &pluginapi.RateLimit{URL: url, Status: resp.Status, RetryAfter: resp.RetryAfter}
}
```

A plugin that sets `RawNetwork: true` in `Meta` uses its own HTTP client
instead, and must surface rate limits itself (return a `RateLimit`). Reserve
this for plugins that need a specialized HTTP stack.

### Multi-step fetches and caching

Plugins may make several calls and keep state between polls. The host keeps the
plugin subprocess alive across polls, so in-memory caches persist:

```go
type AppC struct {
	blogID  string    // cached ~1h
	meta    Metadata  // cached ~24h
	metaExp time.Time
}
```

A cold `Fetch` might resolve an ID, then fetch metadata + activity; subsequent
polls hit the cache and make only the uncached call. A plugin that cannot avoid
a limit returns `RateLimit{RetryAfter: d}` so nanoflux parks the feed and cools
the host, exactly like the built-in integrations.

## How nanoflux loads a plugin

At startup, and only when `NF_PLUGINS_DIR` exists:

1. **Scan** the directory for executables.
2. **Handshake** each one: nanoflux starts the subprocess and verifies the
   shared magic cookie and protocol version. A mismatch or crash is logged and
   that plugin is **skipped** — a broken plugin cannot crash nanoflux.
3. **Dispense** the `Fetcher` over gRPC, and keep the subprocess alive across
   polls.
4. **Register** it alongside the native plugins.

At **add time**, `Discover` runs for matching plugins to propose feeds. At
**poll time**, the poller asks the registry which plugin matches the feed URL;
the first match fetches it, otherwise the generic feed parser runs. If two
plugins match, the host orders them (native before external, then by name) and
logs the conflict.

## Versioning

The host and plugin share `pluginapi.Handshake` — a magic cookie plus a protocol
version — and every plugin reports `Meta().APIVersion`. A mismatch makes the host
refuse the plugin with a clear message rather than speaking a stale protocol.
Until v1.0.0, expect the API to change and bump `APIVersion`; rebuild your plugin
against the matching `pluginapi` release when you upgrade nanoflux.

## Notes and limits (v0.1)

- **Restart to load.** There is no hot reload yet.
- **Toolchain and module versions do not need to match** the host — the gRPC
  boundary decouples them (unlike Go's `plugin` package, which needs cgo and
  exact-version builds; nanoflux deliberately does not use it).
- **View-time rendering is opt-in.** A plugin may resolve an item's view-time
  media through the optional `Render` capability (see above); a plugin that
  does not implement it still just provides data, and nanoflux renders the item.
  Brand chrome (the YouTube embed player and icon) remains core.
- The plugin API is intended to be iterated on; breaking changes are expected
  until v1.0.0.
