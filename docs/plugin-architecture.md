# Plugin architecture — design plan

The plugin system lets feed integrations that don't belong in this repository
(site-specific scrapers, private API-backed feeds) live in separate, possibly
private repos. The current native integrations — YouTube, X, Instagram, Patreon,
and the CSS-selector scraper — become "native plugins" that double as the
example implementations.

Status: **design — not yet implemented.** Breaking changes are expected until
v1.0.0. See "Phases & status" and "Fast follow-ups" to track progress.

## Goals

- Load external feed plugins from a directory without rebuilding nanoflux.
- Keep the core app the source of truth for HTTP policy: User-Agent, timeouts,
  and per-host rate limiting / backoff.
- Turn the existing native integrations into plugins so they are examples, and
  remove the scattered special-casing (`kind='scrape'`, `isXProfileFeedURL`,
  per-host branches) in favor of one dispatch path.

## Non-goals (v0.1)

- A plugin marketplace, discovery, or version resolution.
- Plugin signing, sandboxing, or hot reload without restart.
- Cross-language plugins. The gRPC wire makes them possible; only the Go SDK is
  supported and tested for now.
- API stability. The interface is deliberately small and expected to churn.

## Decisions locked

| # | Decision | Choice |
| --- | --- | --- |
| D1 | Plugin mechanism | **hashicorp/go-plugin** (out-of-process, RPC). Verified to build with `CGO_ENABLED=0`, so the goreleaser build and Alpine image are unchanged — unlike Go's `plugin` package, which needs cgo and `-buildmode=plugin`. |
| D2 | RPC protocol | **gRPC**, for context propagation (timeouts/cancellation) and the broker used for plugin→host calls. |
| D3 | HTTP | **Host-mediated HTTP, with plugin-owned HTTP as an opt-in** (`RawNetwork`). |
| D4 | Rate limits | The host detects/parses limits on **every** mediated response and cools the **request host**. The **feed** is parked only when the plugin signals a rate limit (e.g. it did not recover from cache). |
| D5 | Caching | Plugin-internal for v0.1. A host KV/cache capability is a fast follow. |
| D6 | Native plugins | In-process Go implementations registered as built-ins (no subprocess), sharing the host HTTP path. |
| D7 | Versioning | An explicit `APIVersion` plus the go-plugin protocol version; a mismatch hard-fails with a clear message. |
| D8 | API module | A **nested Go module** at `pluginapi/` (`github.com/metruzanca/nanoflux/pluginapi`), tagged `pluginapi/vX.Y.Z`, so external repos can import the interface without importing the app. (Alt: standalone repo — open question 1.) |
| D9 | Preview metadata | A plugin's discovered `Candidate` carries `Title`/`IconURL`/`HomeURL`, and the host prefers them over `PageMeta`. Required so YouTube (and any avatar-bearing site) stays native as a plugin. |

## Architecture

```
                    ┌───────────────────────────────┐
                    │              host             │
  poller ──────────►│  plugin.Registry              │
  discover ────────►│   ├─ native Fetcher (in-proc) │──► shared *http.Client
  preview/api ─────►│   └─ rpcClientFetcher ────────┼──► go-plugin client (gRPC)
                    │  hostHTTP (UA, timeout,       │
                    │    limit detect, host cool)   │
                    └───────────────────────────────┘
                              │  spawn once, reuse
                              ▼
                    plugins/ (executables) ── gRPC ── Host.Do(ctx, req)
```

- **One `Fetcher` interface.** Native plugins implement it directly; external
  plugins are adapted from the go-plugin gRPC client. Callers never know which
  kind they hold.
- **One dispatch path.** `feedparse.Fetch` becomes "first matching plugin wins,
  else the generic feed parser (gofeed)". The poller, discovery, preview, and
  JSON API all go through it, so there is no parallel implementation to keep in
  sync.
- **Transport.** External plugins are subprocesses launched once and **kept
  alive** across polls. Plugin-internal caching (see the AppC example below)
  depends on this.

### Plugin API (v0.1 sketch)

The API lives in an importable module (D8) so private plugin repos can
`require` it.

```go
const APIVersion = "0.1"

type Capability int // Discover | Fetch

type Meta struct {
	Name       string
	APIVersion string
	RawNetwork bool // opt in to plugin-owned HTTP (D3)
}

// Candidate is one feed a plugin discovered on a page. Title/IconURL/HomeURL
// are the *preview* metadata shown in the add-feed form; a plugin that supplies
// them overrides the host's generic page metadata (see "Preview metadata").
type Candidate struct {
	FeedURL string
	Title   string // feed display title
	IconURL string // author avatar / site icon, resolved to an absolute URL
	HomeURL string
}

type Item struct {
	GUID, Title, Link, Summary, ImageURL, PublishedAt string
	Enclosures []Enclosure
}

type Result struct {
	Feed                     Feed
	Items                    []Item
	ETag, LastModified       string
	NextPageURL              string
}

type FetchRequest struct {
	URL, ETag, LastModified string
	Config                  json.RawMessage // per-feed plugin config
}

// RateLimit is returned by a plugin that could not avoid a rate limit, so the
// poller can park the feed. The host cools the host independently.
type RateLimit struct{ RetryAfter time.Duration }

type Fetcher interface {
	Meta() Meta
	Match(u *url.URL, cap Capability) bool
	Discover(ctx context.Context, pageURL string, h Host) ([]Candidate, error) // optional
	Fetch(ctx context.Context, req FetchRequest, h Host) (Result, error)       // required
}
```

### Host interface

Fulfilled **in-process** for native plugins (a direct call) and **over gRPC**
for external plugins (a broker callback). Same interface, different plumbing.

```go
type Host interface {
	Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error) // limit-aware
	Now() time.Time
	Logf(format string, args ...any)
	// fast-follow: KV Get/Put for shared caching
}

type HTTPRequest  struct{ Method, URL string; Headers map[string]string; Body []byte }
type HTTPResponse struct{ Status int; Headers map[string]string; Body []byte }

```

- `RawNetwork == false` (default): the plugin must use `Host.Do`. The host
  applies the shared User-Agent and timeout policy, and handles rate limits.
- `RawNetwork == true`: the plugin uses its own client and must surface limits
  itself (a `RateLimit` result / error). Only for plugins that need a
  specialized HTTP stack.

### Preview metadata (why this is required, not a nicety)

When a feed is added, the add form is pre-filled with an **author name and
avatar** derived from the entered page. Today the host does this generically in
`discover.PageMeta` (page `<title>` + `<link rel=icon>`), with per-site special
cases (YouTube strips " - YouTube" and uses the channel's `og:image`; Patreon
reads its JSON-LD avatar). If those special cases move into plugins, the host
must be able to get the metadata back from the plugin.

So a plugin's `Discover` returns `Candidate`s that **carry** `Title`, `IconURL`,
and `HomeURL`, and the host prefers them over `PageMeta`. Precedence in the add
form:

1. The candidate's own `Title` / `IconURL` (plugin-supplied), when set.
2. Else the host's `PageMeta` (generic `<title>` / favicon).
3. Else the feed title / page host.

This is the one API addition that makes YouTube (and any avatar-bearing site)
feel native as a plugin rather than regressing to a favicon. `PageMeta` stays in
core as the generic fallback; its per-site special cases become plugin logic.

### Discovery and dispatch precedence

- **Discovery** (add-time): after the host's own generic discovery, matching
  plugins' `Discover` runs; the host merges candidates, preferring plugin
  metadata (above) and de-duplicating by `FeedURL`.
- **Fetch** (poll-time): a plugin whose `Match(u, Fetch)` returns true takes
  precedence over the generic gofeed parse. If two plugins match, the host
  orders them deterministically (native before external, then by name) and
  logs the conflict; v0.1 does not treat overlapping matches as fatal.
- A plugin that matches a **plain RSS URL** (like YouTube's `videos.xml`) may
  call `Host.Do` itself and fall back to another endpoint when the response is
  not usable (e.g. a 404), because `Host.Do` returns the raw status/body. The
  host does not need a YouTube-specific branch; the plugin owns that fallback.
- **View-time** concerns stay in core and are *not* plugin capabilities: e.g.
  YouTube's embed player (`web.YoutubeEmbedURL`, the modal iframe) and the
  built-in YouTube brand icon. A plugin provides *data*; the reader still
  renders it.

### Rate limiting (D4)

Every `Host.Do` response runs the existing detection and backoff logic
(`feedparse.isRateLimited` / `rateLimitBackoff`: HTTP 429, or 503 with a retry
hint; `Retry-After` then `x-ratelimit-reset`) and cools the **actual request
host** — e.g. `api.appc.com`, not the feed's `appc.com`.

The plugin still owns its multi-step logic and may recover from its own cache
after a 429. The host does not override a plugin that recovered; it only refuses
future requests to a cooling host. When the plugin cannot avoid the limit, the
poller persists `feeds.next_poll_at` and cools the host for the cycle exactly as
it does today (`Poller.PollOne`, `coolHost`).

### Worked example: an AppC-backed plugin

A plugin for an AppC-style API performs, on a cold `Fetch`:

1. `POST /resolve-identifier {blog_name}` → blog ID (cached ~1h).
2. In parallel, `POST /get-blog {blogId}` (cached ~24h) and
   `POST /list-blog-activity` (never cached, paged ~20 posts).

All three calls go through `Host.Do` (default `RawNetwork=false`), so every
response's headers are inspected for limits and the host is paced. The plugin
keeps the blog ID and metadata in memory, which is why the host keeps the
subprocess alive between polls — so only `list-blog-activity` remains per poll.
A missing blog ID is a 404 (recorded as `last_error`, no backoff); a failure in
the metadata/activity step is reported as 403 "private". Feed XML generation
itself needs no network.

### Worked example: a YouTube plugin (the reference plugin)

YouTube is the hardest native integration and the proof that the API is
sufficient: if it fits, arbitrary third-party plugins fit. It exercises **both**
capabilities and a non-RSS endpoint.

- **Discover** (`Match(u, Discover)` for YouTube hosts): resolve the channel ID
  from the `/channel/UC…` path, or from the `@handle` page's canonical link /
  embedded JSON, all via `Host.Do`. Return a `Candidate` whose `FeedURL` is
  `https://www.youtube.com/feeds/videos.xml?channel_id=UC…`, with the channel
  `Title` and `IconURL` (the `yt3.googleusercontent.com` `og:image`) so the add
  form shows the real avatar — replacing today's `PageMeta` YouTube special case.
- **Fetch** (`Match(u, Fetch)` for the `videos.xml` URL): request the RSS
  endpoint through `Host.Do`. If it returns 200 with a parseable feed, normalize
  it. YouTube's endpoint intermittently 404s for live channels, so on a
  non-usable response the plugin falls back to `POST
  /youtubei/v1/browse?key=…` (the web client's channel-tab API) through
  `Host.Do` and normalizes the `lockupViewModel` entries. Item GUIDs keep the
  `yt:video:<id>` prefix so they dedup against the native feed if it recovers.
- **Rate limits / errors**: handled by the host on every `Host.Do` (429 → host
  cooled; 404/403 → `StatusError` on the feed), exactly as the current native
  code does inline today.
- **Stays in core** (view-time, not fetch): the embeddable player in the item
  modal and the built-in YouTube brand icon.

The plugin's own code is then the `youtubeChannelID` resolution, the two
response normalizers, the relative-time parser, and the fallback decision — all
of which already exist in `internal/feedparse/youtube.go` and
`internal/discover/host.go`, moved behind the interface.

## Storage

- `feeds.kind`: `'feed' | 'scrape'` → add `'plugin'`.
- New `feeds.plugin_name TEXT` (which plugin handles the feed).
- `feeds.scrape_config` generalizes to `feeds.plugin_config TEXT` (JSON; the
  CSS-scrape plugin's config is the existing `ScrapeConfig`).
- Migration **schemaV30**: backfill `kind='scrape'`, `plugin_name='scrape'` from
  existing scrape feeds; keep `schema.sql` in sync.
- The feed create/edit UI grows a plugin selector for `kind='plugin'`. The
  scrape builder remains, as the config UI for the scrape plugin.

## Configuration & distribution

- `NF_PLUGINS_DIR` (default `/plugins`) is scanned for plugin executables.
  Absent or empty ⇒ no behavior change from today. Documented in the README
  config table, `.env.example`, and AGENTS.md (the repo's env-var rule).
- `docker-compose.yml` bind-mounts `./plugins:/plugins` (mirroring `./backups`),
  off by default.
- Plugins are plain executables; drop them in the directory and restart.

## Build & tooling

- No goreleaser or Docker change: go-plugin is pure Go and the build stays
  `CGO_ENABLED=0` / static (verified in a scratch module).
- **D8:** `pluginapi` is a **nested Go module** at `pluginapi/`
  (`module github.com/metruzanca/nanoflux/pluginapi`), tagged
  `pluginapi/vX.Y.Z`, so external repos can import the interface without
  importing the app. (Alternative: a standalone `nanoflux-plugin-api` repo.)
- A skeleton plugin repo layout and a `mise run plugin:build` helper (dev
  tooling belongs in mise, per the mise/make split).

## Migration of the native implementations

Order, self-contained to shared, with **YouTube first** as the reference plugin
(it exercises discover + fetch + preview metadata + a non-RSS endpoint): **youtube
→ instagram → patreon → x → scrape**. Each keeps its existing unit tests,
re-pointed at the `Fetcher` interface.

- **YouTube is the gate.** If the API carries YouTube — including plugin-supplied
  preview metadata and the RSS→browse fallback — it can carry arbitrary
  third-party plugins; treat it as the acceptance test for v0.1.
- **Preview metadata:** `discover.PageMeta`'s per-site special cases (YouTube
  suffix/og:image, Patreon JSON-LD) move into the respective plugins'
  `Discover`; `PageMeta` stays as the generic fallback.
- **Reddit (open):** its *feed* is plain subreddit RSS (a discovery rule), while
  `internal/httpapi/reddit.go` is **view-time** link/gallery resolution, not
  fetching. Proposed: reddit stays a discovery rule and view-time resolution
  stays in core.
- **Discovery (open):** the remaining `discover/host.go` rules (reddit `r/X.rss`,
  GitHub `.atom`, Bluesky) move behind the optional `Discover` capability — or
  stay in core with only fetch exposed to plugins.

## Phases & status

| Phase | Deliverable | Status |
| --- | --- | --- |
| 0 | This design doc | done |
| 1 | `pluginapi` module: types (incl. `Candidate.IconURL`), `Fetcher`, `Host`, `APIVersion`, errors | not started |
| 2 | `internal/plugin`: registry, built-ins, `NF_PLUGINS_DIR` loader, gRPC broker, in-process host | not started |
| 3 | Port natives, **YouTube first** (reference), then instagram, patreon, x, scrape | not started |
| 4 | Unify dispatch (`feedparse.Fetch`), migrate `kind`/config (schemaV30) | not started |
| 5 | Discovery capability | not started |
| 6 | Authoring story: skeleton repo, `docs/plugins.md`, env/compose docs | not started |
| 7 | Tests: registry, fake in-process plugin, end-to-end build + load a plugin | not started |

## Open questions

1. **API module location** — nested module `pluginapi/` per D8, or a standalone
   `nanoflux-plugin-api` repo.
2. **Discovery scope** — plugins expose **fetch only**, or **fetch + discover**
   (which moves the remaining reddit/GitHub/Bluesky discovery out of core)?
   YouTube needs both, so **fetch + discover is now assumed**; this question is
   only about moving the *other* host rules.
3. **Reddit** — discovery rule + core view-time resolution (proposed), or a full
   plugin?
4. **CSS scraper** — one generic native `scrape` plugin preserving current
   behavior (proposed), or drop it for per-site plugins?
5. **Per-plugin credentials** — per-feed JSON only for v0.1, plugin-level env
   vars, or a config file in the plugins directory?

## Fast follow-ups

- Host **KV/cache** capability (shared caching across plugins and polls).
- Per-plugin credential/config surface (secret-safe).
- Plugin **signing/checksum** (`SecureConfig`) and TLS for the RPC channel.
- Cross-language examples (e.g. Python) once the gRPC API settles.
- Hot reload / reattach without a restart.
- A richer `Host` (streaming HTTP, proxy controls).
- Per-plugin metrics and health on `/admin`.
- **Per-site preview metadata beyond a candidate** — if a plugin needs richer
  add-form enrichment than `Title`/`IconURL`/`HomeURL` (e.g. a description), a
  dedicated optional `Meta`/`Enrich` capability rather than growing `Candidate`.
