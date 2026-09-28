## Project Rules

- Commit messages must follow the Conventional Commits spec.
- Document all environment variables in `.env.example`.
- App links are internal by default, ↗ on every external link.
- mise is for development, make is for selfhosting an instance
- Do not mention any external plugins in internal code, comments or docs
- Do not run `make` or `podman` or `docker` commands without user's approval. The container is likely the user's production deployment. Use go to run the app locally instead e.g. go run cmd/server/main.go which runs on 8080. You may kill port 8080 if necessary.
- we're pre v1 so breaking changes are allowed/expected if they make v1 better.

# Notes

htmx 2.x does not swap `4xx`/`5xx` response bodies by default. `app.js`
(loaded by `views_layout.templ`) installs a global `htmx:beforeSwap` listener
that sets `shouldSwap = true` for any `status >= 400`, so error fragments
actually render. Do not remove or narrow that listener; every form/fragment
handler below relies on it.

`event.detail.successful` remains `false` on `4xx`/`5xx`, so `hx-on::after-request`
handlers that close dialogs on success (`if (event.detail.successful) ...`)
leave the dialog open on error — which is what lets the user read the message.

## Rate limiting

Some hosts (notably reddit's anonymous `.rss`, ~1 request/IP/minute) reject
polling with 429. The app treats this as pacing, not failure:

- `feedparse` returns a typed `RateLimitError` for 429 / 503-with-hint,
  deriving the window from `Retry-After` / `x-ratelimit-reset`.
- `feeds.next_poll_at` (schemaV29) stores that deadline. `ListFeedsDue` gates on
  it **independently of `poll_interval_sec`** — a passed backoff makes a feed due
  even inside a 1-day interval, so the host's real window is honored instead of
  being masked. Do not recombine those conditions with `AND`.
- `poller.pollDue` groups feeds by registrable host and processes each group
  least-recently-polled-first (fair rotation). A host that 429s is paced by its
  learned window (`hostWindow`/`hostNextHit`); its remaining feeds stay **due**
  and are picked up when the window clears, rather than skipped for the cycle.
  `Run` wakes at the earliest of the base interval or a host's next-hit time, so
  rotating hosts are revisited promptly (floored at `minWake`).
- Even a host that never rate-limits is spaced: after one fetch the group stops
  and wakes after `hostSpacing` (`NF_POLL_HOST_SPACING`, default 30s; learned
  window overrides when larger; 0 disables). `pollGroup` only stops early when
  another feed is still waiting (`i < len(group)-1`), so a single-feed host is
  never delayed. The default spacing is poller-only and must not pace plugin
  `Host.Do` calls.
- The poller and plugin host share **one** `plugin.Cooldown` (wired in
  `cmd/server/main.go`, poller holds it via the local `hostCooler` interface):
  a limit seen by either paces both. Do not give `plugin.Setup` or `poller.New`
  their own instance.
- The manual refresh button and `feedRefresh` respect `next_poll_at`: a cooling
  feed shows a "rate limited · retry in …" badge with the button disabled, and a
  click during the window is a no-op.
- `discover` surfaces the host's fetch error when a rule's probe fails, so
  adding a reddit feed at limit says "rate-limiting requests (HTTP 429)" rather
  than a bare "no feed found".

## Plugins own site-specific behavior

Site-specific logic lives in a plugin, not the core, through URL-matched
capabilities (`pluginapi.Capability`, added additively over the single `Fetcher`
gRPC service and `APIVersion`-gated). A site whose feed URL shape is known and
rate-limited (reddit) owns its rules in its plugin while its `.rss` is still
fetched by the generic parser, so `feeds.plugin_name` stays empty:

- **`URLPolicy`** (`CapURLPolicy`): `CanonicalizeFeedURL(raw)` returns the
  redirect-free stored shape, and `FeedToken(feedURL)` the `r/<sub>` / `u/<name>`
  token its items carry. `cmd/server/main.go` installs the registry's
  `StoreURLPolicy` on the store (`Store.SetURLPolicy`); `FeedStore` create/edit
  and `CanonicalizeFeedURLs` call it, and the preview form's `stripWWW` is
  skipped for a URL a plugin owns (`Server.urlPolicyOwned`). The reddit plugin
  holds the former `store.CanonicalFeedURL` logic (`urlpolicy.go`).
- **`Discover` derived candidates** (`Candidate.Derived`): the plugin derives the
  feed from the page URL with no request. The add/discovery flows return it
  without fetching the page (`Server.derivedCandidate`, `preview.go`), replacing
  the deleted `discover.Derive`. A plugin-owned page also skips the icon/avatar
  page fetch (`pageIconURL`, `createFeed`, `Server.urlPolicyOwned`).
- **`SharedKeyer`** (`CapSharedKey`): `SharedKeys` assigns a feed's freshly
  parsed items a cross-feed `SharedKey` after either fetch path (index-addressed
  entries). `feedparse.FetchFeed` runs it via `runSharedKeys`; `poller.ingest`
  copies `SharedKey` into `store.Item.SharedKey`, and `Upsert` stores it as
  `cross_key` (`crossFeedKey` now just trims the plugin-supplied key). reddit
  sets `reddit:t3_<id>`; `MergeCrossFeedDuplicates` remains as the one-time
  legacy backfill.
- **`Decoration`** (`CapDecorate`): `Decorate` returns each item's source
  attribution parts (with tokens) and card `Kind`. It is view-time and pure (no
  network). `cmd/server/main.go` installs the registry's `StoreDecorator`
  (`Store.SetItemDecorator`); the store's `attachSources` calls `decorate`,
  resolving each token to the user's subscribed feed via the URL policy's
  `FeedToken` (internal link) or the part's URL (external). This replaces the
  deleted `httpapi.attribution.go` and `store.attachRedditLinks`/`RedditLink`.

The reddit plugin (`internal/plugin/native/reddit`) is the reference for all of
these: `Match` returns true for `CapSharedKey`, `CapDecorate`, `CapURLPolicy`,
`CapDiscover`, `CapRender` and `CapDocs` on reddit hosts; it does **not** claim
`CapFetch`. `ItemWithFeed.Kind` drives the row card (`KindText` default,
`KindImage`, `KindGallery`, `KindLink`, `KindVideo`, `KindAudio`); a generic
single-image baseline (`web.IsSingleImagePost`) still applies when no plugin
classified the item, and `ThumbURL` overrides the row thumbnail.

## Item categories (ingest filters)

Feeds carry context that should be filterable without any site-specific code:
reddit, for example, tags every entry with its subreddit (`<category
label="r/golang"/>`) and its author (`<name>/u/poster</name>`). A subreddit feed
gives the poster as the item author; a user feed gives the destination sub as the
category.

- `feedparse.normalizeItem` maps an entry's `<category>` values plus its author
  name(s) into `Item.Categories` (leading `/` stripped, deduped). This is the
  generic parser path reddit uses. Plugin-fetched items set `pluginapi.Item.
  Categories` directly; `plugin/dispatch.go` copies them into `feedparse.Item`,
  so both paths reach the same store and filter (a plugin can mark a post kind,
  e.g. a reblog, with a single label).
- `items.categories` (schemaV36) stores them newline-joined (denormalized);
  `Upsert`/`UpdateItemSnapshot` write them, so an already-stored item gains
  categories on re-poll without a re-fetch. `poller.ingest` copies them through.
- A filter rule's `field = "category"` matches if **any one** category matches
  (contains, case-insensitive; regex per-category), so
  `action: hide, field: category, pattern: r/golang` works. Choices live in
  `filterFieldItems`; `feedRuleCreate` accepts the field.

## Auto-read

- `users.auto_read_after_days` (schemaV32) is a per-user retention window:
  unread items older than it (by `COALESCE(published_at, fetched_at)`) are
  marked read. `0` = off; default 30. Set via the settings auto-read card
  (`POST /settings/auto-read`, options 3/7/30/60/off).
- `ItemStore.MarkOlderThanRead(userID, days)` is the sweep. It ignores favorites
  and is user-scoped through `feeds.user_id`. `days <= 0` is a no-op.
- `maintenance.Sweeper` runs it at startup and every 24h
  (`maintenance.DefaultInterval`), started in `cmd/server/main.go`; the settings
  handler also sweeps that user immediately on save so the change is visible.
- The sweep only flips `read`/`read_at` — nothing is deleted, so it is
  reversible with "mark all unread".

## Saved pages

The extension can save an arbitrary page ("watch later") when the current page
has no feed. A saved page is a normal `items` row under a hidden per-user system
feed, so lists, favorites, FTS search and share pages all work unchanged.

- `authors.is_system` / `feeds.is_system` (schemaV34) mark the hidden pair.
  `Store.EnsureSystemFeed`/`EnsureSystemAuthor` create them lazily;
  `Store.SavePage` upserts by `guid` (`page:<normalized-url>`, so re-saving is
  idempotent) and adds list membership.
- The system feed is `enabled = 0`, never polled, and excluded from every feed
  and author listing (`ListFeeds*`, `ListAuthors*`, `ListAllFeeds`, collections,
  OPML, plugin reconcile). `FeedStore.ByID`/`AuthorStore.ByID` return
  `ErrNotFound` for them, so their pages 404 instead of rendering.
- Saved pages surface **only** in lists, favorites and search. The `ListItems`
  queries keep them out of the unread/read/feed/author/collection streams
  (`AND (f.is_system = 0 OR favorites = 1)`); `CountUnread`/`MarkAllItemsRead`/
  `MarkAllItemsUnread` exclude them. They stay favoriteable, searchable and
  subject to the auto-read sweep.
- `ItemWithFeed.FeedIsSystem` drives rendering: the row/modal show "saved" as
  plain text (no link to the hidden feed/author) and `dedupItems` never merges a
  saved page into a feed item.
- Default list is "watch later", created on demand by
  `Server.defaultSavedListID`; the extension form can pick another list or type
  a new name. Routes: `POST /api/ext/page-form` and `/api/ext/page-save`.

## Plugins

- `feeds.plugin_name` (schemaV31) records which plugin owns a feed; empty means
  the generic parser. Set on create via `FeedStore.CreateWithPlugin` (callers
  use `Server.pluginNameFor`) and adopted by `plugin.ReconcileFeeds` at startup.
- `plugin.ReconcileFeeds` runs after plugins load: it adopts feeds whose plugin
  is present, auto-disables feeds whose plugin is missing
  (`feeds.disabled_reason = 'plugin not loaded: <name>'`), and auto-re-enables
  those exact feeds when the plugin returns. Re-enable is keyed to the reason
  (`store.IsPluginMissingReason`), so a **user-paused** feed (no reason) is never
  resumed, and to the owner adopted *this boot*, so a renamed/swapped plugin
  resumes its feeds. `UpdateFeed` clears `disabled_reason`, so a user save takes
  ownership back.
- Escape hatch: `POST /admin/plugins/reset` (admin only) takes a `domain`,
  clears `plugin_name` for that registrable domain's feeds
  (`FeedStore.ResetPluginForDomain`, un-parking auto-disabled ones), then
  re-runs `ReconcileFeeds` so the loaded registry re-derives the owner. The
  plugins card lists owned domains with a reset button.
- See `docs/fetching.md` (how fetching works) and
  `docs/writing-plugins.md` (author guide).

### Plugin docs

- A plugin may implement `pluginapi.Docser` (`Docs() string`, Markdown) to
  document itself; `Meta.Summary` is its one-line admin-card description.
  `HasDocs` on `Meta` is host-filled (native: type assertion; external: the
  wire), not authored.
- Docs are matched on `CapDocs`, **not** on feed ownership, so a plugin that
  does not own a feed's fetch can still document it. reddit is the reason:
  reddit `.rss` feeds are fetched by the generic parser (`plugin_name` empty),
  yet reddit's plugin documents the subreddit/author categories its parser
  adds. The same URL-matched pattern carries `CapSharedKey`, `CapDecorate`,
  `CapURLPolicy` and `CapDiscover` (see "Plugins own site-specific behavior").
- `Registry.Docs(name)` / `Registry.MatchDocs(url)` / `Registry.ByName(name)`
  back it; `Registry.ErrNotFound` distinguishes "no such plugin" from
  "plugin has no docs" (`pluginapi.ErrUnsupportedCapability`).
- `Registry.Docs` is called lazily by `GET /fragments/plugin-docs?plugin=<name>`
  (auth-only, **not** admin-only: the feed edit page offers the same docs next
  to the filter rules). The readme must not do network I/O.
- The docs modal is the shared `#plugin-docs-dialog` in `views_layout.templ`,
  filled by `openPluginDocs(name)` in `app.js`. Entry points: the admin plugin
  card and the feed edit filter section.
- Markdown is rendered by `web.Markdown` (goldmark, raw HTML disabled; anchors
  get `class="external"` + `target/rel`). goldmark is a root-module dep only;
  `pluginapi` stays dependency-free.
- Native readmes live beside their plugin code (`readme.md`, `//go:embed`):
  reddit (categories), youtube, bluesky.

### The native YouTube plugin

- The plugin fetches a channel's recent videos through YouTube's internal
  browse API (`youtubei/v1/browse`), not the channel RSS. The RSS carries no
  video duration; browse returns duration (and views) for every entry in the
  same request. Browse reports publish times only as relative text ("3 days
  ago"), which the plugin converts to an absolute UTC timestamp at fetch time,
  so a stored item never shows a frozen relative date.
- The plugin derives the browse origin from the feed URL's host, so it talks to
  the same origin the feed names and a test can point it at a mock host with no
  env plumbing.

### The native Bluesky plugin

- The plugin (`internal/plugin/native/bluesky`) replaces the profile RSS feed
  (`bsky.app/profile/{handle}/rss`), which is text-only: no item titles, no
  media, no author, and it drops an image/video post down to its caption. It
  reads the raw `app.bsky.feed.post` records through `com.atproto.repo.listRecords`
  and rebuilds entries from them. The stored feed URL stays the `/rss` shape
  (what discovery has always produced) and is never parsed; its handle/DID only
  selects the repository to read.
- Two origins are involved and they are **not** interchangeable:
  `public.api.bsky.app` answers `app.bsky.actor.getProfile` but returns **501**
  for `com.atproto.repo.listRecords`; `bsky.social` answers `listRecords` and
  `com.atproto.sync.getBlob` but **401**s `getProfile`. The plugin's `apiBase`,
  `repoBase`, `syncBase`, `imgBase` and `videoBase` vars encode this so a test
  can point them at a mock host.
- Media: images use `cdn.bsky.app/img/feed_fullsize/plain/{did}/{cid}` (direct,
  cacheable). Video is attached as a `video/mp4` enclosure via
  `com.atproto.sync.getBlob?did={did}&cid={cid}` (redirects to the account's PDS;
  returns the whole MP4 and ignores `Range`, so seeking is limited until cached),
  with its poster frame (`video.bsky.app/watch/.../thumbnail.jpg`) as the
  thumbnail. An HLS alternative (`video.cdn.bsky.app` playlists, Range-capable,
  needs a frontend player) is documented in a comment on `blobURL` in case the
  MP4 enclosure proves too limiting.
- Replies are skipped, matching the RSS feed's original-posts-only behavior.
  Quotes are rendered as a link to the quoted post (not expanded: hydrating each
  quoted record would cost one request per quote). Facets (mentions, links,
  tags) are rendered as anchors from the record's byte offsets.
- Existing stored bsky feeds are adopted by `ReconcileFeeds` at startup (the URL
  shape matches), and `poller.ingest` now stores enclosures on **every** poll
  (not only on insert), so an already-stored feed gains its media on the next
  poll. The generic `discover.hostSpecificURLs` bsky rule was removed so
  discovery no longer spends an extra `/rss` fetch; the plugin's `Discover`
  supplies the candidate and its preview metadata.

### Item media duration

- `items.duration_sec` (schemaV37) is a media item's runtime in seconds, NULL
  when unknown. `pluginapi.Item.DurationSec` carries it across native and gRPC
  plugin boundaries; `store.Item.DurationSec` and `poller.ingest` pass it through.
- It is part of the upsert snapshot, so re-polling refreshes it on an existing
  row (`UpdateItemSnapshot`). Cards show it as a bottom-right pill
  (`web.FormatDuration`); 0/unknown renders nothing.

### Item identity and dedup

- Items are deduplicated on `(feed_id, dedup_key)` (schemaV35). `dedup_key` is
  the plugin's `Item.Identity` when set, else its `GUID` (`store.dedupKey`). The
  legacy `UNIQUE(feed_id, guid)` remains but is no longer the conflict target.
- `Identity` exists so a plugin whose `GUID` changes shape for the same entry
  (e.g. a post URL → `"scheme:<id>"`) does not store it twice. Existing rows
  were backfilled `dedup_key = guid`. New plugins should set `Identity` from the
  site's immutable id; the generic parser leaves it empty (identity == GUID).
- A GUID-scheme change already split rows can be repaired with `nanoflux item
  dedup` (report) / `--apply` (merge). `ItemStore.FindDedupGroups` /
  `DeduplicateItems` group only same-feed + same-link + same-published-time rows
  (conservative), keep the most recently fetched row (the current scheme), and
  carry over read/favorite state, enclosures, list memberships and shares. The
  merge runs in one transaction; verify integrity + FK + FTS after a run.

### Cross-feed items (one item-id per post)

The same reddit post can be subscribed twice: through the subreddit feed and
through the poster's user feed. It is stored as one row, not two, so read/
favorite/list/share state is shared and the combined streams count it once.

- `items.user_id` (schemaV38) denormalizes the owner; `items.cross_key` is the
  per-user cross-feed identity (`reddit:t3_<id>`, empty when not dedupable).
  `items.feed_id` remains the owner/display feed. `item_feeds(item_id, feed_id)`
  is the membership set; feeds list items through it, while the display join
  still uses the owner feed.
- `store.crossFeedKey` normalizes the plugin-supplied `SharedKey` into the stored
  key (it now just trims). The identity is the plugin's: reddit's `SharedKeyer` sets
  the post fullname `reddit:t3_<id>`, identical in both feeds. Nothing else is
  cross-deduped unless a plugin supplies a `SharedKey`.
- `ItemStore.Upsert` resolves by `(user_id, cross_key)` first: a post already
  stored through another feed gains a membership (and its snapshot is refreshed)
  instead of a second row. Its `inserted` return means "new to this feed" (new
  row or new membership), which the poller uses for history exhaustion.
- A partial unique index `idx_items_cross ON items(user_id, cross_key) WHERE
  cross_key <> ''` enforces it. It is created by
  `ItemStore.MergeCrossFeedDuplicates` **after** merging pre-existing duplicate
  rows (the migration cannot backfill `cross_key`, since old duplicates would
  violate the index). The server runs that merge at startup, next to
  `CanonicalizeFeedURLs`; `nanoflux item cross-dedup` is the manual/report form.
  The merge carries read/favorite state, memberships, enclosures, lists and
  shares, and is idempotent.
- `FeedStore.Delete` re-homes items the deleted feed owns that are also members
  of another feed, so deleting the sub feed does not delete a post still
  reachable via the user feed.
- Attribution comes from the plugin's `Decoration` (see "Plugins own
  site-specific behavior"): `itemAttribution`/`redditAttribution` are gone, and
  the row/modal render `ItemWithFeed.Attribution` (already resolved by the store
  from the decoration's tokens). `ItemWithFeed.Sources` is populated from
  `item_feeds`; the title-based `httpapi.dedupItems` remains only for non-reddit
  near-duplicate titles and skips items with a `CrossKey`.
- A poster (or sub) link must not depend on the item's `item_feeds` membership:
  a cross-feed post seen through the subreddit feed only gains a membership in
  the poster's user feed once *that* feed has polled the post, so resolving the
  poster from `Sources` alone left it externally linked until then (the "same
  feed, same poster, only one linked" bug). The store's `decorate` resolves each
  decoration token against the user's **subscribed** feeds via the URL policy's
  `FeedToken` (derived from the feed URL, not its title, so a rename still
  resolves). It runs inside `attachSources` (user-scoped) on every list/detail
  path; the public list page passes user 0 and skips the resolution.

## Combo boxes (Vaadin)

Single- and multi-select form fields use vendored Vaadin v25 web components
(`vaadin-combo-box`, `vaadin-multi-select-combo-box`), so long option lists are
searchable. See [`docs/vendored-web-components.md`](docs/vendored-web-components.md)
for the full guide (regenerating the bundle, adding more components, gotchas).
The invariants that bite:

- The committed bundle (`internal/web/static/vaadin.bundle.js`) is regenerated
  with `mise run vendor:vaadin` (needs node once; runtime does not). It is an
  **IIFE**, not ESM: a plain `<script defer>` containing `export` fails to parse,
  so the elements never register and render as zero-size unknown elements.
- The components are **not form-associated**, so htmx's `FormData` would submit
  the label (single-select) or nothing (multi-select). Each wrapper in
  `views_combo.templ` pairs the component with hidden native mirrors, synced by
  `static/vaadin.js`. Options ride inline as JSON attributes (`items`,
  `selected-items` as full item objects, not bare values).
- Some fragments are injected with `fetch` + `innerHTML` (the add-to-list
  dialog), which fires no htmx swap event; `static/app.js` calls
  `window.nanofluxReinitVaadin(target)` after injecting so the bridge binds.
- For a plain fixed-set choice where search adds nothing, prefer the custom pill
  picker (`PickerControl` in `views_items.templ`); see `/settings` home screen.
- `vaadin-grid` renders columns imperatively (a JS `renderer`), used for the
  drag-to-reorder pinned collections on `/settings` (`static/home-grid.js`). Its
  cell content is slotted from the light DOM, so htmx can reach the cloned row
  markup. Vaadin's base color tokens default via `light-dark()` (OS preference,
  not `data-theme`) and are remapped on `:root` in `app.css`.
