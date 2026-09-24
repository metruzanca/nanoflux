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

nanoflux scans a single directory, `NF_PLUGINS_DIR` (default `/plugins`), for
executables. In the container that directory is bind-mounted from the host, so
you drop a compiled binary into `./plugins/` next to the repo:

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

## The two capabilities

A `Fetcher` can implement either or both. `Match` tells the host which URL
shapes (and which capability) it applies to.

- **`Fetch` (required)** — given a feed URL, return its `Feed` metadata and
  `[]Item`s. Runs in the poller and on manual refresh.
- **`Discover` (optional)** — given a page URL, return `[]Candidate`s (feeds
  found on that page). Runs in the add-feed "find feed" flow.

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
metadata (`PageMeta`). This is how a plugin keeps a site's real author name and
avatar instead of a generic favicon.

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
- **View-time rendering stays in core.** A plugin provides data; nanoflux still
  renders items. For example, the YouTube embed player and brand icon are core,
  not plugin, concerns.
- The plugin API is intended to be iterated on; breaking changes are expected
  until v1.0.0.
