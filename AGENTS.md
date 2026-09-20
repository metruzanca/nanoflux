# AGENTS.md

Project-specific guidance for coding agents working in this repository.

## Links: internal by default, ↗ on every external link

The app's own pages are the primary navigation surface. This is a hard rule.

- **Authors/usernames and feed titles are always internal links.** Route them
  to `/authors/{id}` and `/feeds/{id}` respectively. Never link them to an
  external URL (e.g. the feed's RSS URL or the author's homepage) — the item
  listing meta, the item modal meta, and any feed/author listing follow this.
  See `item_row` in `items.html` and `item_view.html`.
- **Every link that leaves the app** (href points at an external `http(s)://`
  origin) MUST carry `class="external"`. `app.css` renders the `↗` marker via
  `a.external::after`, so external links are always visibly marked — e.g.
  "feed", "home", an author's homepage URL, and "open live" in the item modal.
  Do not emit an external link without this class, and do not hard-code a
  second `↗` character in link text.
- This applies to the webapp UI only (`internal/web/templates`); the
  `website/` Hugo marketing site is out of scope.

## Error handling in the web UI

The web UI is server-rendered with htmx. All user-facing failures must render a
visible error into the page. Never return a bare status or fail silently.

### htmx swap rules (load-bearing)

htmx 2.x does not swap `4xx`/`5xx` response bodies by default. `layout.html`
installs a global `htmx:beforeSwap` listener that sets `shouldSwap = true` for
any `status >= 400`, so error fragments actually render. Do not remove or narrow
that listener; every form/fragment handler below relies on it.

`event.detail.successful` remains `false` on `4xx`/`5xx`, so `hx-on::after-request`
handlers that close dialogs on success (`if (event.detail.successful) ...`)
leave the dialog open on error — which is what lets the user read the message.

### Two error response shapes

1. **Preview endpoints** (`/fragments/feed-preview`, `/fragments/author-preview`):
   the form's `hx-target` *is* the preview container, so errors replace it via a
   normal swap. Return `400` + the `form_error` fragment using `renderError(w, msg)`.
   Do NOT use OOB here — an out-of-band div targeting the same element as the
   normal target is fragile.

2. **Mutation forms** (`POST /feeds`, `/authors`, `/collections`): the target is
   the list; the error slot is a separate `#add-…-error` div inside the open
   dialog. Return `400` + an OOB swap into that slot using `writeFormError(w, target, msg)`.

Error messages are short, user-facing, and generic. Log the underlying cause
server-side with `log.Error(...)`; never leak internals (SQL, URLs, stack traces)
to the client. `5xx` responses currently render plain text into the target via
the global swap override; prefer returning a fragment there too.

### Error template

`form_error` lives in `internal/web/templates/fragments.html` and renders a
`role="alert"` banner. Keep the `role="alert"` so screen readers announce it.

### Tests

Every error path must assert both `rr.Code == http.StatusBadRequest` and that
the message (or the `form_error`/`role="alert"` fragment) appears in the body.
See `internal/httpapi/preview_test.go` and `internal/httpapi/web_test.go` for
the pattern.

## YouTube channel feeds

YouTube's public `feeds/videos.xml?channel_id=` endpoint intermittently serves
404 for active channels (a known upstream issue). `internal/feedparse/youtube.go`
falls back to the site's internal `youtubei/v1/browse` API whenever a YouTube
channel feed URL fails to fetch — the two live tests in the git history
(`TestLiveYouTubeFetch`, `TestLiveYouTubeHandleDiscover`) prove the flow, but
they are network-dependent and intentionally not committed.

- Feed URLs stay `https://www.youtube.com/feeds/videos.xml?channel_id=<id>`; the
  fallback is transparent inside `feedparse.Fetch`, so discovery, the poller,
  and preview all work unchanged.
- Synthesized item GUIDs use the `yt:video:` prefix so they dedup against the
  native feed when the endpoint recovers. Do not change that.
- Published times come from relative text ("1 month ago") parsed by
  `parseRelativeTime`; they are approximate.
- `youtubeBrowseBaseURL` is a package var so tests can point it at a mock.

## X (Twitter) profile feeds

X removed RSS in 2013 and offers no public guest API in 2026; Nitter is
DMCA'd. `internal/feedparse/x.go` therefore scrapes the profile page
(`x.com/<handle>`, `twitter.com/<handle>`) and extracts the posts embedded in
the web client's Relay payload. `fetchXProfile` runs as a short-circuit inside
`feedparse.Fetch` when the URL is an X profile, so discovery, the poller, and
preview all work unchanged.

- The Relay payload is minified, unofficial, and split across `$R[n]` refs;
  parsing is best-effort. `isXProfileURL` only accepts single-segment profile
  paths (rejects `/home`, `/search`, status URLs, etc.).
- Item GUIDs are `tweet:<id>`; publish times come from the tweet's snowflake
  ID (`(id >> 22) + 1288834974657` ms), not the page's scattered timestamp refs.
- `xProfileHosts` is a package var so tests can inject a mock host. If X starts
  serving a login wall, `fetchXProfile` returns an error and the feed fails
  gracefully.

## Reddit link posts

Reddit "link posts" point at an external site (imgur, a news article,
...). Their items are identifiable from stored data alone: the thumbnail host
is `external-preview.redd.it` (vs `preview.redd.it`/`i.redd.it` for in-post
images) and the summary is a bare link wrapper. The modal resolves the chain
lazily and generically — no destination site is hardcoded:

- The destination comes from the `[link]` anchor reddit embeds in its feed
  content (`extractLinkAnchor` in `internal/httpapi/reddit.go`). Feeds whose
  content was stripped (e.g. a content-stripping proxy) fall back to a lookup in
  the post's subreddit RSS (`/r/{sub}/.rss`), keyed by the `t3_{id}` from the
  item link. Subreddit RSS works unauthenticated; reddit's JSON/HTML APIs are
  login-walled and must not be used.
- Embedding uses generic oEmbed discovery (`internal/oembed`): fetch the
  destination page, find its `application/json+oembed` alternate link, and
  extract the iframe `src` — the provider's raw html is never injected. Results
  are cached; the whole resolution is timeboxed in `itemView` and degrades to
  today's behavior on any failure.
- `redditRSSBaseURL` is a package var so tests can inject a mock host.
- Link-post thumbnails are not treated as image posts (`isImagePost` in
  `internal/web/templates.go` rejects `external-preview.redd.it`).

### Reddit galleries

Gallery posts are identified in the listing by their stored thumbnail alone:
reddit's RSS gives galleries a small square cover (`preview.redd.it` +
`crop=1:1,smart`) instead of a natural-aspect image-post crop (`isGallery` in
`internal/web/templates.go`), and the card thumb is upgraded to the full-res
`i.redd.it/{id}.{ext}` original (`galleryThumb`), which needs no signed params.

In the modal, a gallery's `[link]` anchor points at `reddit.com/gallery/{id}`
(recognized by `redditGalleryID`), and the images are enumerated from the
post's embed page on `embed.reddit.com/r/{sub}/comments/{id}/` — an
unauthenticated render of the post's full media, unlike reddit's login-walled
JSON/HTML APIs. File ids are rewritten to `i.redd.it` URLs and cached per post
id; `redditEmbedBaseURL` is a package var so tests can inject a mock host. The
whole resolution is timeboxed in `itemView` and degrades to today's behavior
on any failure.

## Settings and custom source icons

`/settings` lets users set a profile-picture URL and add custom per-domain brand
icons that override the built-in X/YouTube/globe set.

- Source icons render as `<img src="/icons/{hostname}">` (see `sourceIcon` in
  `internal/web/templates.go`); the auth-required `GET /icons/{domain}` handler
  serves the user's **cached** custom icon bytes, else a built-in SVG. This is
  what makes per-user icons work without threading the user into every fragment.
- Custom icons are stored in the `source_icons` table (domain unique per user,
  `icon_data` BLOB holds the cached bytes). Added/refreshed by fetching the
  user's `icon_url` server-side (`fetchAndCacheIcon`, capped at 1MB, must be
  `image/*`); failures keep the row with a "not cached" note and a refresh button.
- Domain matching is an exact lowercase hostname match (no subdomain
  wildcards). `normalizeDomain` accepts bare hostnames or URLs.
- The avatar is a **file upload**: bytes go to object storage (`avatars/<userID>`)
  and are served at the auth-required `GET /avatar` (`Cache-Control: private,
  no-cache`); `User.HasAvatar` (from `avatar_key IS NOT NULL`) decides whether
  the topbar shows it. The avatar form re-renders itself (error inside the
  swapped card), unlike the create-form OOB pattern.

## Object storage (S3 / SeaweedFS)

Avatars and custom-icon bytes live in S3-compatible object storage, not the DB.
The DB stores object **keys** (`users.avatar_key`, `source_icons.icon_key`).
`internal/filestore` exposes the `Store` interface (`Put`/`Get`/`Delete`/
`EnsureBucket`) over minio-go; tests use `filestore.NewMemory()`.

- Config is `S3_*` env vars (`S3_ENDPOINT`, `S3_BUCKET`, `S3_ACCESS_KEY`,
  `S3_SECRET_KEY`, `S3_REGION`). When `S3_ENDPOINT` is unset, the server
  **embeds** a SeaweedFS mini cluster (master+volume+filer+S3) in-process on
  127.0.0.1 (`filestore.StartEmbedded` in `internal/filestore/embed.go`,
  using `weed/command`'s `mini` command + `command.MiniClusterCtx`; data under
  `<db dir>/seaweedfs`). No external weed binary or container is needed; the
  cluster tears down on the returned stop func (wired to shutdown in main).
- A configured-but-unreachable endpoint fails fast at startup (see
  `newFileStore` in `cmd/server/main.go`); the message points at `S3_ENDPOINT`.
- The `mini` flags are set best-effort (`dir`, `master.port`, `volume.port`,
  `filer.port`, `s3.port`, `webdav/admin.ui/iceberg/lance=false`); unknown
  flags are ignored so a renamed upstream flag doesn't break boot.
- Importing `weed/command` pulls the full SeaweedFS module (rclone, FUSE, all
  filer stores) — the binary is ~230MB and builds are slow. The charmbracelet
  stack (lipgloss/cellbuf/ansi/colorprofile) is upgraded to versions
  compatible with the ansi v0.11.x that rclone forces; do not downgrade those.
- On SIGINT/SIGTERM SeaweedFS's `weed/util/grace` handler also runs; verified
  the app's own graceful shutdown still runs first and frees the ports.
- `StartEmbedded` redirects the cluster's stdout (the mini banner/status rows)
  to `/dev/null` and pipes its stderr (glog) through the app's charmbracelet
  logger (`routeOutput` in `embed.go`, glog severity mapped to the matching
  level, tagged `component=seaweedfs`). The global stdout/stderr swap is safe
  because charmbracelet captured `os.Stderr` at init; on the normal stop path
  the globals are intentionally left redirected so shutdown rows don't leak.
- `Store.MigrateLegacyFiles` moves pre-object-storage DB blobs to objects once,
  at startup; the legacy `avatar_data`/`icon_data` columns are left in place but
  cleared.
- Object keys: `avatars/<userID>`, `icons/<userID>/<domain>`.

## Running the dev server

`mise dev` runs the server and auto-watches `.go` files, rebuilding and
restarting on change. If a port is already taken, it's likely a `mise dev`
instance is still running — no need to kill it, just edit the `.go` files and
it will rebuild and rerun.