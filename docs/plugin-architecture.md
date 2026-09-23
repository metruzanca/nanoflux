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

The API lives in an importable module (see D8 below) so private plugin repos can
`require` it.

```go
const APIVersion = "0.1"

type Capability int // Discover | Fetch

type Meta struct {
	Name       string
	APIVersion string
	RawNetwork bool // opt in to plugin-owned HTTP (D3)
}

type Candidate struct{ FeedURL, Title, HomeURL string }

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
- **D8 (proposed):** `pluginapi` is a **nested Go module** at `pluginapi/`
  (`module github.com/metruzanca/nanoflux/pluginapi`), tagged
  `pluginapi/vX.Y.Z`, so external repos can import the interface without
  importing the app. (Alternative: a standalone `nanoflux-plugin-api` repo.)
- A skeleton plugin repo layout and a `mise run plugin:build` helper (dev
  tooling belongs in mise, per the mise/make split).

## Migration of the native implementations

Order, self-contained to shared: **youtube → instagram → patreon → x → scrape**.
Each keeps its existing unit tests, re-pointed at the `Fetcher` interface.

- **Reddit (open):** its *feed* is plain subreddit RSS (a discovery rule), while
  `internal/httpapi/reddit.go` is **view-time** link/gallery resolution, not
  fetching. Proposed: reddit stays a discovery rule and view-time resolution
  stays in core.
- **Discovery (open):** the `discover/host.go` rules (reddit `r/X.rss`, GitHub
  `.atom`, Bluesky, YouTube channel ID) move behind the optional `Discover`
  capability — or stay in core with only fetch exposed to plugins.

## Phases & status

| Phase | Deliverable | Status |
| --- | --- | --- |
| 0 | This design doc | done |
| 1 | `pluginapi` module: types, `Fetcher`, `Host`, `APIVersion`, errors | not started |
| 2 | `internal/plugin`: registry, built-ins, `NF_PLUGINS_DIR` loader, gRPC broker, in-process host | not started |
| 3 | Port natives: youtube, instagram, patreon, x, scrape | not started |
| 4 | Unify dispatch (`feedparse.Fetch`), migrate `kind`/config (schemaV30) | not started |
| 5 | Discovery capability | not started |
| 6 | Authoring story: skeleton repo, `docs/plugins.md`, env/compose docs | not started |
| 7 | Tests: registry, fake in-process plugin, end-to-end build + load a plugin | not started |

## Open questions

1. **API module location** — nested module `pluginapi/` (proposed, D8) or a
   standalone repo.
2. **Discovery scope** — plugins expose **fetch only**, or **fetch + discover**
   (which moves reddit/GitHub/Bluesky discovery out of core)?
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
