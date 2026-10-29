## Project Rules

- Commit messages must follow the Conventional Commits spec.
- Document all environment variables in `.env.example`.
- App links are internal by default, ↗ on every external link.
- mise is for development, make is for selfhosting an instance
- Do not mention any external plugins in internal code, comments or docs
- Prefer extending `pluginapi` over site-specific core changes. When a request
  concerns one site and the direct fix would add a special case to the core app,
  stop and extend `pluginapi` instead, then have the core call into a plugin.
  Core should not accumulate per-site edge cases. (See "Plugins own
  site-specific behavior".)
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

Some hosts (their anonymous feed endpoints allow only ~1 request/IP/minute)
reject polling with 429. The app treats this as pacing, not failure:

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
  and wakes after `hostSpacing` (`NF_POLL_HOST_SPACING`, default 60s; learned
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
  adding a feed at limit says "rate-limiting requests (HTTP 429)" rather than a
  bare "no feed found".

## Poll interval floor

A feed also has a minimum automatic interval, not only a cadence:

- `NF_POLL_MIN_INTERVAL` (default 1h) is the global floor. A plugin can raise it
  for feeds it claims with the URL-matched `CapMinPollInterval`, via
  `Meta.MinPollInterval`; `Registry.MinPollInterval` takes the maximum and
  memoizes it per URL. The host uses the larger of the two everywhere.
- `poller.SetMinPollInterval` receives the composed resolver. `feedFloorSec` is
  the floor for every feed, and `adaptiveFloorSec` is
  `max(adaptiveFloor (900), feedFloorSec)`, used by `adaptiveInterval`. Do not
  hardcode 900 for the adaptive floor again.
- Retroactive enforcement is at due time, not a rewrite: `pollDue` runs
  `applyPollFloor`, dropping a feed whose stored interval elapsed but whose floor
  has not, and advances the wake to its floor deadline. A feed due by an explicit
  `next_poll_at` (rate-limit retry or clock-aligned schedule) is never held back.
  Do not add a startup or cadence rewrite of `poll_interval_sec` to the floor:
  that would make lowering `NF_POLL_MIN_INTERVAL` ineffective for existing feeds.
  The feed editor shows the effective interval and clamps on save.
- `httpapi.Server.pollFloorSec` composes the same values for create/edit, the
  feed form's minimum, and the admin card. Every create path uses
  `defaultPollIntervalSec`, and `applyFeedForm` uses `clampPollIntervalSec`; do
  not reintroduce a bare 900.
- `buildAdminPoll` measures demand and behind-ness at
  `max(poll_interval_sec, floor)`, so the card matches what the poller will do.
- A manual refresh and "load older items" are explicit user actions and stay
  exempt. The rate-limit `next_poll_at` remains authoritative.
- The floor does **not** replace per-host spacing. `NF_POLL_HOST_SPACING`
  (default 60s) still paces any two fetches to the same registrable host through
  `pollGroup`/`hostNextHit`; `applyPollFloor` runs before grouping. Keep both:
  the floor protects one feed, the spacing protects the host (which is what keeps
  many reddit feeds from tripping a rate limit together).

## Time-of-day polling

A feed's adaptive interval measures *how often* it posts, not *when*, so a daily
poster first polled at 10am would stay on 10am and its 3pm post would be seen up
to a day late. `poller.scheduleNextPoll` fixes that by planning the next fetch
just after the author's typical posting time, stored in the existing
`feeds.next_poll_at` "do not poll before" gate:

- Detection (`preferredFetchTime`, `internal/poller/interval.go`): circular mean
  of the newest ~20 item times of day **in UTC** (an author's absolute rhythm; no
  user-timezone plumbing). Requires at least `scheduleMinSamples` (6) posts and a
  circular resultant of at least `scheduleMinStrength` (0.5), so only a clear
  single-slot rhythm is scheduled. Spread-out or bimodal feeds keep pure interval
  polling.
- Target: next occurrence of the preferred time + `scheduleBuffer` (5m) + a
  deterministic per-feed jitter (`feedID mod scheduleJitter`, 10m) so feeds that
  share an hour don't all fetch at once. Per-host spacing and fair rotation still
  enforce the hard minimum.
- The target is `min(next occurrence, last_polled + poll_interval_sec)`: a
  schedule only ever brings a fetch **forward**, never delays it beyond the
  interval, and it is clamped up to `last_polled + poll floor` so it cannot
  undercut the per-feed minimum. Only adaptive (`poll_interval_auto`) and
  non-stale feeds are scheduled.
- It runs on every successful poll, **including `304`**, recomputing from the
  actual poll time so wake jitter cannot drift the alignment. A non-rate-limit
  error clears the schedule so the feed retries on its normal interval.
- `feeds.next_poll_scheduled` (schemaV54) marks the deadline as a schedule rather
  than a rate-limit backoff. `feedRow.Cooling()` and the `feedRefresh` guard must
  treat a scheduled deadline as **not** cooling: no "rate limited" badge, and a
  manual refresh may override it. `ListFeedsDue` itself is unchanged; the rate
  limit invariant above still holds.
- `FeedStore.EarliestNextPollAt` seeds `pollDue`'s wake so a scheduled fetch is
  hit near its target instead of on the next base tick.
- Constants (`scheduleBuffer`, `scheduleJitter`, `scheduleMinSamples`,
  `scheduleMinStrength`) are in `internal/poller/interval.go`; see
  `docs/fetching.md` ("Scheduled to the author's time of day").

## Plugins own site-specific behavior

Site-specific logic lives in a plugin, not the core, through URL-matched
capabilities (`pluginapi.Capability`, added additively over the single `Fetcher`
gRPC service and `APIVersion`-gated). A site whose feed URL shape is known and
rate-limited owns its rules in its plugin while its feed is still fetched by the
generic parser, so `feeds.plugin_name` stays empty:

When a site-specific need appears, the default is a plugin, not core. Reach for
a new `pluginapi.Capability` (URL-matched when the behavior is keyed to a URL) or
an optional interface advertised by a `Meta` flag, and wire the core
to call the registry at one chokepoint. Keep anything that runs per render pure
and cheap (a Match-only capability needs the plugin's `Match` to be
capability-aware; see the invariant below). All plugins are external
executables loaded from `NF_PLUGINS_DIR`.

- **`URLPolicy`** (`CapURLPolicy`): `CanonicalizeFeedURL(raw)` returns the
  redirect-free stored shape, and `FeedToken(feedURL)` the `r/<sub>` / `u/<name>`
  token its items carry. `cmd/server/main.go` installs the registry's
  `StoreURLPolicy` on the store (`Store.SetURLPolicy`); `FeedStore` create/edit
  and `CanonicalizeFeedURLs` call it, and the preview form's `stripWWW` is
  skipped for a URL a plugin owns (`Server.urlPolicyOwned`). A plugin with a
  URL policy holds the former `store.CanonicalFeedURL` logic (`urlpolicy.go`).
- **`Discover` derived candidates** (`Candidate.Derived`): the plugin derives the
  feed from the page URL with no request. The add/discovery flows return it
  without fetching the page (`Server.derivedCandidate`, `preview.go`), replacing
  the deleted `discover.Derive`. A plugin-owned page also skips the icon/avatar
  page fetch (`pageIconURL`, `createFeed`, `Server.urlPolicyOwned`).
- **`SharedKeyer`** (`CapSharedKey`): `SharedKeys` assigns a feed's freshly
  parsed items a cross-feed `SharedKey` after either fetch path (index-addressed
  entries). `feedparse.FetchFeed` runs it via `runSharedKeys`; `poller.ingest`
  copies `SharedKey` into `store.Item.SharedKey`, and `Upsert` stores it as
  `cross_key` (`crossFeedKey` now just trims the plugin-supplied key); the
  plugin decides the key's shape. `MergeCrossFeedDuplicates` merges rows that
  already share one.
- **`Decoration`** (`CapDecorate`): `Decorate` returns each item's source
  attribution parts (with tokens) and card `Kind`. It is view-time and pure (no
  network). `cmd/server/main.go` installs the registry's `StoreDecorator`
  (`Store.SetItemDecorator`); the store's `attachSources` calls `decorate`,
  resolving each token to the user's subscribed feed via the URL policy's
  `FeedToken` (internal link) or the part's URL (external). This replaces the
  deleted `httpapi.attribution.go` and the store's old link helpers.
  `Decorated.DedupeKey` is an optional view-time content identity: the store
  copies it onto `ItemWithFeed.DedupeKey`, and `httpapi.dedupItems` collapses two
  cross-feed items that share it into one row (survivor + `Sources`) even when
  their titles differ. A plugin keys it by the content plus its author, so the
  same content reposted in several places under different titles shows once. It
  is matched **before** the
  fuzzy-title path and, unlike title matching, applies to cross-feed posts
  (which carry a `CrossKey`). Same-feed items are still never merged.
- **`Enricher`** (`CapEnrich`): `Enrich` produces an item body (full text,
  translation, transcript), matched per item link. It runs in the poller on
  **newly stored** items only (`poller.enrichStored`, wired via
  `Poller.SetItemEnricher` to the `Dispatcher`), best-effort: an error logs and
  skips rather than failing the poll, and an item already carrying content is not
  re-enriched. The body is stored as `items.content` (schemaV41), separate from
  `items.summary` because `UpdateItemSnapshotByID` refreshes the summary every
  poll; `httpapi.itemBody` renders content when present, else the summary.
  `examples/plugin-enrich` is the reference.
- **`CapBypassProxy`** (URL-matched capability): a plugin whose image hosts
  hotlink freely but refuse the host's server-side `/img` request claims this
  capability for those image hosts. `plugin.Registry.BypassesProxy` matches the
  URL, and any true answer makes `web.ProxiedImageURL` return the raw URL.
  `httpapi.Server.SetPlugins` installs the predicate into `web`
  (`web.SetProxyBypass`) once at startup. Pure; a plugin lists the media hosts
  that hotlink freely.
- **`CapIcon`** (URL-matched capability): a plugin may supply a built-in source
  icon for its site when no cached favicon exists. `Registry.Icon` matches the
  URL and calls `IconProvider.Icon`, and `httpapi` uses it before the generic
  globe fallback. Pure and cheap; it runs while serving an icon request.

`Match` **must honour the capability it is asked about**: return false for any
capability the plugin does not claim. A plugin that ignores it (an early
`func Match(u, _ Capability) bool { return isSiteURL(u) }`) spuriously claims
URL-matched capabilities the registry asks about. For example, a Match-only
image-cache capability broke for feeds whose plugin ignored the capability until
its `Match` was made capability-aware. Optional capabilities that pair `Match`
with a type assertion are tolerant (a non-implementer is skipped; the external
gRPC client's unsupported RPC is handled), but Match-only ones are not.

A plugin that owns URL-matched capabilities is the reference for all of
these: `Match` returns true only for the
capabilities it claims (`CapSharedKey`, `CapDecorate`, `CapURLPolicy`,
`CapDiscover`, `CapRender`, `CapDocs`) on its hosts; it does **not** claim
`CapFetch` when the generic parser reads its feeds. A URL that is not a feed is a
case where the plugin returns false for the URL-matched capabilities that would
misread it (otherwise discovery would read it as a feed shape and the URL policy
would rewrite it to the feed origin), leaving those to a plugin that owns it. The
item-link capabilities (`CapRender`, `CapDecorate`) and any capability matched on
the item's URL still apply, so results cross-dedupe with subscribed feeds and
render the plugin's attribution. `ItemWithFeed.Kind` drives the row card (`KindText` default,
`KindImage`, `KindGallery`, `KindLink`, `KindVideo`, `KindAudio`); a generic
single-image baseline (`web.IsSingleImagePost`) still applies when no plugin
classified the item, and `ThumbURL` overrides the row thumbnail.

## Image caching (short-lived image URLs)

Some sites sign their image URLs with a lifetime shorter than the poll interval,
so a stored remote URL is broken by the time the card renders. A plugin whose
site does this claims `Match(u, CapImageCache)` (appended capability; no
interface method, no new RPC — it rides the existing `Match` capability int).
The host then caches that feed's images itself.

- The feature is opt-in: a feed with no plugin starts with caching off and the
  user can turn it on from the feed's edit page (`feeds.cache_images`,
  schemaV45). A plugin that claims `CapImageCache` **forces** it on and the user
  cannot turn it off (`plugin.Registry.CachesImages`, `Server.imageCacheForced`;
  `feedUpdate`/`createFeed` OR the stored flag with the forced value).
- At poll time `poller.ingest` (`cacheImages` on the feed) calls
  `imagecache.Cacher.Cache` for the item's `ImageURL` and each image-typed
  enclosure. It is best-effort: a failure logs and leaves that URL remote, and
  it never fails a poll. Downloads are size-capped and concurrent within an item.
- Cache keys are `cache/<folder>/<itemID>/<slot>.<ext>`, where folder is the
  plugin name (`plugin.Registry.ImageCacheFolder`; `feeds` when no plugin
  matches). The stable item id makes a key survive a re-poll even though the
  signed remote URL rotates. A blob already at the slot is reused via
  `filestore.Store.Exists` (no re-download); the primary image is cached first
  and an enclosure that is the same URL (the generic parser mirrors an image
  enclosure into `ImageURL`) reuses its key.
- Keys are stored on `items.image_cache_key` (schemaV45, written by
  `ItemStore.SetItemImageCacheKey`, deliberately **not** touched by the poll
  snapshot refresh) and `item_enclosures.cache_key`. Their byte sizes live on
  `items.image_cache_size` / `item_enclosures.cache_size` (schemaV50), written
  on a fresh download; a reused blob reports size 0 and the store keeps the
  recorded size (a re-poll never zeroes it). `ReplaceEnclosures` carries a
  slot's previous key and size forward when the incoming enclosure has none, so
  turning caching off keeps already-cached images showing; it only stops new
  downloads.
- Render: `web.CachedImageURL(key, remote)` builds `/cache/<key>?u=<remote>`.
  `itemViewData.cachedImageSrc`/`enclosureSrc` use it only for the item's own
  image (not a body image) and only when the view may use the authenticated
  route; `rowThumb` uses it for list cards. `GET /cache/{key...}` (auth-only,
  like `/img`) serves the bytes, or proxies `?u=` when the blob is gone (purged
  cache), so a removed folder degrades to the remote URL instead of a broken
  image.
- Sizes drive the author feed cards and author stats ("cached media",
  `web.FormatBytes`), summed from the DB via `ItemStore.StorageByFeed`/
  `StorageByAuthor` (membership-scoped). The `.ct` sidecars are disk-store
  metadata; `List`/`Stat` ignore them.
- Purge: deleting a user (`Users.ListObjectKeys` includes the item/enclosure
  cache keys) or a feed (`FeedStore.Delete` returns the keys of the items it
  actually removes; re-homed cross-feed items keep theirs) deletes those blobs
  best-effort. `filestore.Store.List` backs `nanoflux storage report` (bytes by
  kind and cache plugin, plus orphaned cached media), `nanoflux storage gc
  --apply` (delete cached blobs no row references) and `nanoflux storage
  backfill` (fill sizes for media cached before schemaV50). Backups exclude the
  whole `cache/` subtree (`backup.excludedFileStoreDirs`), so cached media never
  bloats a snapshot; avatars and icons are still archived.
- A plugin claims the capability with a single `Match` case, so no interface or
  wire change is needed.

## Item categories

Feeds carry context that surfaces without any site-specific code: a feed may tag
every entry with a section (`<category label="..."/>`) and its author
(`<name>/...</name>`). One feed gives the poster as the item author; another
gives the destination section as the category.

- `feedparse.normalizeItem` maps an entry's `<category>` values plus its author
  name(s) into `Item.Categories` (leading `/` stripped, deduped). This is the
  generic parser path. Plugin-fetched items set `pluginapi.Item.
  Categories` directly; `plugin/dispatch.go` copies them into `feedparse.Item`,
  so both paths reach the same store (a plugin can mark a post kind, e.g. a
  reblog, with a single label).
- `items.categories` (schemaV36) stores them newline-joined (denormalized);
  `Upsert`/`UpdateItemSnapshot` write them, so an already-stored item gains
  categories on re-poll without a re-fetch. `poller.ingest` copies them through.
- Categories feed the tag filter (below) and are available to plugins via
  `pluginapi.Item.Categories`. The former ingest filter-rule system was removed
  for a clean reimplementation; no per-feed rule table remains.

### Tags (normalized category table)

Categories are also normalized for tag browsing and list filtering.

- `item_categories(item_id, category)` (schemaV48, PK + index on `category`,
  FK `ON DELETE CASCADE`) is one row per tag. `ItemStore.Upsert` replaces an
  item's rows via `syncItemCategories` (same newline codec as the column);
  `mergeDedupGroup` folds a loser's tags into the survivor; `CloneUser` copies
  them; `BackfillItemCategories` (run at startup in `cmd/server/main.go`) fills
  rows for items stored before the migration, and is a no-op once populated.
- `ItemStore.ListCategories(userID, ItemFilter{FeedID|AuthorID|CollectionID})`
  returns each distinct tag with its item count, most-used first.
- `ItemFilter.Tags` filters a list to items carrying **every** listed tag (AND).
  `ItemFilter.ExcludeTags` hides items carrying any listed tag, and
  `ItemFilter.ExcludeFeedID` hides items seen through one feed. Per-row filters
  use `ItemFilter.TagGroups` / `FeedGroups`: each group has its own values and
  `All` (AND vs OR) and `Exclude` flags. The generated `ListItems*` queries are
  fixed-shape, so any tag/group/negative filter uses the hand-written
  `ItemStore.listPageTagged` (dynamic `EXISTS`/`NOT EXISTS`; supports
  newest/oldest/magic and all scopes), mirroring `SearchPage`'s raw-SQL pattern.
  `unreadFilteredQuery` (mark-all/count) applies the same predicates.
- HTTP filters are transient query state (`internal/httpapi/filters.go`): each
  applied row has a kind (`tag`/`feed`), action (`show`/`hide`), combine
  (`any`/`all`) and one or more values. A single value uses the legacy URL
  shape `?f=tag:show:r/golang`; multi-value rows use
  `?f=tag:show:any:["r/golang","r/rust"]`. Legacy `?tags=`/`?feed=` still parse
  as show/all filters. Rows are ANDed together for `show` and ORed for `hide`
  (any matching hide row excludes). The modal form submits one `filter_row`
  token per row plus repeated `filter_value_<token>` values. Tags are capped at
  `maxTagFilters`. Tab, sort, chip and pagination links carry the filters with
  `listFilters.appendFilters`.
- UI: the item modal shows its tags as clickable chips (linking to
  `/authors/{id}?tags=`); a scoped feed/author/collection list gets a filter
  dialog with one editable row per applied filter (`show`/`hide` action,
  `tag`/`feed` kind, `any`/`all` combine, multi-select values) plus x (remove)
  and + (insert another row) controls; a persistent "add filter" button keeps
  an empty row list recoverable. The tag/feed select is hidden when only one
  kind is available; with one feed the kind defaults to `tag` and feed filters
  are dropped. Each row's kind change swaps its value options client-side
  (`data-filter-row-template`, `data-filter-rows`, `data-add-filter-row`,
  `data-filter-kind` in `app.js`). Active-filter chips drop one row; a hidden
  filter's chip uses the
  `chip-hide` style. Author-page feed rows are validated against the author
  before use (`authorFilters`).

## Author filters (always-on allow/block)

Authors can carry persistent filter rules that apply everywhere, configured in
the author settings card. They reuse the transient filter system rather than a
parallel implementation.

- **Same rule shape.** `store.FilterRule` (`kind`, `action`, `combine`, `values`)
  is the canonical row. The HTTP layer's `listFilter` adds labels and URL
  serialization; `store.FilterGroups` is the one conversion to `TagGroup` /
  `FeedGroup`, used by transient filters, author rules, and the preview. Actions
  are `show`/`hide` internally; the author card labels them **allow**/**block**.
- **Semantics.** Every allow row must match (like show rows); any matching block
  row excludes (like hide rows). An author with no allow rows allows everything
  not blocked. Matching keys on the item's owner author (`items.feed_id ->
  feeds.author_id`), which is the author a row displays. Feed rules match through
  `item_feeds` membership restricted to that author's feeds.
- **Storage (schemaV55).** `author_filter_rules` + `author_filter_values`, owned
  by `AuthorFilterStore` (`store.AuthorFilters`). `POST /authors/{id}/edit`
  replaces them when the form carries `author_filters_present`; a partial save
  without the card leaves them alone. Feed delete removes its id from rules and
  drops any rule left empty; `CloneUser` remaps author ids and feed values.
- **One read path.** The `visible_items` view (schemaV55, mirrored in
  `schema.sql`) applies the rules in SQL. Item lists, counts, badges, search,
  read history, list items, and public shared lists select from `visible_items`
  instead of `items`. Item detail (`GetItem*`) and shared-item fetch stay on raw
  `items`, so a blocked item is still reachable by direct link. Auto-read
  retention (`MarkItemsOlderThanRead`) is unchanged. Any new item read path must
  use `visible_items`; write paths and storage/dedup stay on `items`.
  `visible_items` is `SELECT i.*`, so a future migration that adds an `items`
  column must recreate the view in the same migration.
- **Writes respect it too.** Mark-all read/unread and the scoped filtered mark
  gate on `visible_items`, so hidden items keep their state.
- **Preview.** `POST /authors/{id}/filters/preview` evaluates the submitted
  (draft) rows against raw items scoped to the author's owned feeds: allowed is
  the normal group predicate, blocked is its complement. It never persists and
  never double-applies saved rules. The author card re-posts on change
  (`hx-include="#author-filter-fields"`) and on `authorfilterschanged` from
  `app.js`.
- **Transient filters still work.** The funnel dialog remains; its show/hide
  rows intersect with the always-on rules. `app.js` scopes row cloning to the
  nearest `[data-filter-rows]` / `[data-filter-row-template]` and value options
  to the nearest `[data-tag-items]` container, so the dialog and the author card
  share the same component.

## Feeds live on the author page

There is no `/feeds/{id}` page. An author's feeds render as cards inside the
`details.author-feeds` section of `/authors/{id}` (`FeedRow`), each carrying the
actions that used to live on the feed page: a link to the feed's scoped item
list, refresh (`POST /feeds/{id}/refresh`), feed-scoped mark-all-read, "load
older items" (`POST /feeds/{id}/older`, which appends OOB to the author's item
list), the external home/feed link, the last-polled line and the cached-media
stat. `GET /feeds/{id}/edit` still redirects to the author edit page.

The author edit page is **one form**: the author's fields and every loaded feed
panel submit together to `POST /authors/{id}/edit`, so a user can change several
feeds (or the author and a feed) and save once. `feedEditorBody` therefore
carries no `<form>` of its own; `feedFields` takes a name prefix
(`feedFieldPrefix(id)`, `feed_<id>_title` etc.) and each panel has a hidden
`feed_ids` marker, so `authorUpdate` only touches the feeds present in the
submission (a collapsed, never-opened panel is left untouched). `applyFeedForm`
is the shared per-feed writer, called with prefix `""` by the standalone
`POST /feeds/{id}/edit`. Nested forms are avoided: the feed delete control is a
`formaction="/feeds/{id}/delete"` submit button inside the page form (with a
per-button `data-confirm`, honoured by `app.js` via `e.submitter`), the remote
opt-in is `delete_remote_<id>`, and the plugin panel's sync is an htmx button
(`hx-params="action"`, `hx-vals`) rather than a form. htmx-in-panel actions
(refetch, refresh now, load older) use `hx-params="none"` so clicking them does
not post the whole page form.

A feed's item list is the author page filtered with `?feed={id}`
(`Server.authorFeedFilter` validates ownership; `authorScopedItems` sets
`ItemFilter.FeedID`), which scopes the tabs, counts, sort, tags, pagination and
the header mark-all to that feed. Every former `/feeds/{id}` link now points at
an author page: the feed card title at `/authors/{id}?feed=`, item/source/author
links at `/authors/{id}`, the item modal's tag chips at `/authors/{id}?tags=`,
the search-palette feed entity and the provision success link at
`/authors/{id}?feed=`. The item menu's old "go to feed" is "go to author" (and
is omitted on the author's own page). Display-mode scopes drop `/feeds/{id}`;
the author page's scope stays `/authors/{id}`.

## Appearance

Per-user display preferences live in the settings "appearance" card and are
stored on `users` (schemaV43), rendered server-side into the topbar and
`<html>`:

- `users.hide_unread_counts` hides the numeric unread/authors badges in the
  topbar. `basePage` adds `class="hide-counts"` to `<body>` when set (CSS hides
  `.nav-count`), and `app.js`'s `refreshNavCounts` no-ops while that class is
  present so the client badge refresh cannot recreate a hidden badge.
  `settingsAppearanceUnreadCounts` (`POST /settings/appearance/unread-counts`).
- `users.hide_unread_nav` moves the `unread` nav item into the user menu.
  Because that changes shared topbar markup rather than a settings fragment,
  `settingsAppearanceUnreadNav` (`POST /settings/appearance/unread-nav`)
  responds with `HX-Refresh: true` so htmx reloads the page.
- `users.grid_max_columns` (2..6, default 2) caps grid display columns. It is
  emitted on `<html>` as `--grid-columns` (both via `basePage`); app.css uses
  `repeat(var(--grid-columns, 2), ...)`. `store.ClampGridColumns` is the
  canonical clamp. `settingsAppearanceGridColumns`
  (`POST /settings/appearance/grid-columns`) rejects out-of-range values.
- Theme and accent color (`users.theme`, `users.accent_color`) were re-homed
  into the same appearance card but keep their existing routes
  (`POST /settings/theme`, `POST /settings/accent`); their handlers render the
  card's per-control sub-fragments (`appearanceTheme`/`appearanceAccent`).
  `views_settings.templ` no longer has standalone theme/accent cards.

## Auto-read

- `users.auto_read_after_days` (schemaV32) is a per-user retention window:
  unread items older than it (by `COALESCE(published_at, fetched_at)`) are
  marked read. `0` = off; default 30. Set via the settings auto-read card
  (`POST /settings/auto-read`, options 3/7/30/60/off).
- `ItemStore.MarkOlderThanRead(userID, days)` is the sweep. It ignores favorite
  and bookmark state (both are swept) and is user-scoped through `feeds.user_id`.
  `days <= 0` is a no-op.
- `maintenance.Sweeper` runs it at startup and every 24h
  (`maintenance.DefaultInterval`), started in `cmd/server/main.go`; the settings
  handler also sweeps that user immediately on save so the change is visible.
- The sweep only flips `read`/`read_at` — nothing is deleted, so it is
  reversible with "mark all unread".

## Bookmarks and favorites

Two native per-item lists, each a boolean column plus a `users.*_share_token`
(mirroring each other), with distinct meanings:

- **Bookmarks** (`items.bookmark`, schemaV42) are save-for-later: the extension
  and the in-app "save url for later" dialog default to them. `POST
  /bookmarks/share`, public `GET /b/{token}`, pinned on `/lists`, and a tab on
  author pages. No backfill: bookmarks start empty.
- **Favorites** (`items.favorite`) are the taste signal for the magic sort below.
  `users.favorites_share_token`, public `GET /f/{token}`.

Both are toggled from the item row/modal (`favToggle`/`bookmarkToggle`) and the
add-to-list picker (`listsCombo` pseudo-values `favorites`/`bookmarks`).

## Magic sort

Item lists can sort by feed taste instead of time. `ItemFilter.Magic` dispatches
to `ListItemsMagic` (offset-paged, no keyset cursor; `?sort=magic`).

- Ranking is hard-tiered and transparent: the manual rank dominates, then the
  feed's owner-feed favorite count, then item time. `CASE WHEN f.rank > 0 THEN 0
  WHEN f.rank < 0 THEN 2 ELSE 1 END`, then `fav_count DESC`, then
  `COALESCE(published_at, fetched_at) DESC`.
- `feeds.rank` (schemaV42) is the per-feed manual lever: -1 lowered, 0 neutral,
  +1 raised. `FeedStore.SetRank` clamps to that range.
- `GET /settings/algorithm` is the editor (`favoritesAlgorithmPage`): feeds
  listed by favorite count with a three-state `rankControl`; `POST
  /feeds/{id}/rank` swaps the control. `FeedStore.ListWithFavorites` backs it.
  The settings page's magic sort card summarizes it (feed, favorite, raised and
  lowered counts) and links to it; the lists page no longer links to the editor.
- The sort picker (`SortControl`/`SimpleSortControl`) replaces the old two-way
  `?dir=` control on every item list. `itemSortOf` still reads legacy `?dir=asc`
  as `oldest`.

## Saved pages

The extension can save an arbitrary page ("watch later") when the current page
has no feed. A saved page is a normal `items` row under a hidden per-user system
feed, so lists, favorites, bookmarks, FTS search and share pages all work
unchanged.

- `authors.is_system` / `feeds.is_system` (schemaV34) mark the hidden pair.
  `Store.EnsureSystemFeed`/`EnsureSystemAuthor` create them lazily;
  `Store.SavePage` upserts by `guid` (`page:<normalized-url>`, so re-saving is
  idempotent) and adds list membership (a `listID` of 0 skips membership).
- The system feed is `enabled = 0`, never polled, and excluded from every feed
  and author listing (`ListFeeds*`, `ListAuthors*`, `ListAllFeeds`, collections,
  OPML, plugin reconcile). `FeedStore.ByID`/`AuthorStore.ByID` return
  `ErrNotFound` for them, so their pages 404 instead of rendering.
- Saved pages surface **only** in lists, favorites, bookmarks and search. The
  `ListItems` queries keep them out of the unread/read/feed/author/collection
  streams (`AND (f.is_system = 0 OR favorites = 1 OR bookmarks = 1)`);
  `CountUnread`/`MarkAllItemsRead`/`MarkAllItemsUnread` exclude them. They stay
  favoriteable, bookmarket, searchable and subject to the auto-read sweep.
- `ItemWithFeed.FeedIsSystem` drives rendering: the row/modal show "saved" as
  plain text (no link to the hidden feed/author) and `dedupItems` never merges a
  saved page into a feed item.
- The extension/in-app save defaults to the native **bookmarks** list
  (`saveTargetBookmarks`), creating no user list; the picker can pick a real list
  or type a new name. Routes: `POST /api/ext/page-form` and
  `POST /api/ext/page-save`.

## Demo mode (public marketing deployment)

`NF_DEMO_MODE=1` turns the app into its own marketing site while keeping one
codebase. Logged-out `/` always renders a landing page (demo or not); only the
CTA differs. The demo is a real temporary account, not a shared sandbox.

- The landing page (`views_marketing.templ`, `Server.landing`) is a bespoke
  document (its own nav/footer, no app topbar). It is served by `Server.root`
  for anonymous `/`; a signed-in visitor still gets `Server.home`.
- `POST /demo` (`Server.demoStart`) provisions an ephemeral account and signs
  the visitor in. A visitor holding any valid session is redirected to `/`
  instead of given a second account. Creation (not use) is limited to **one demo
  per client IP** by `demoThrottle` (in-memory, keyed on `clientIP`, which is
  `RemoteAddr` only — behind a proxy that collapses to one demo site-wide; this
  is a deliberate anti-bot measure, not an identity check). A throttled request
  redirects to `/?demo=busy`, which renders a notice.
- Provisioning clones the admin seed named by `NF_DEMO_USER`
  (`store.CloneUser`). The clone gets a random adjective-animal username
  (`<adjective>-<animal>`), `is_admin=0`, a random unusable password, fresh NULL
  share tokens, and `is_ephemeral=1`/`expires_at` (schemaV44). `NF_DEMO_TTL`
  (default 2h) is both the account expiry and the session lifetime.
- **Every cloned feed is created `enabled = 0`** so a new visitor never
  triggers outbound fetches; poll bookkeeping (etag/last-polled/next-page/
  next-poll-at) is reset. The seed's own feeds stay enabled so the showcase
  refreshes normally. Do not "helpfully" copy `enabled`; pausing is the point.
- `home_config` pinned collection sections are remapped to the cloned collection
  ids; items keep read/favorite/bookmark state and feed memberships; collections,
  lists, author links, source icons (URLs only) and view prefs are copied. Blobs (`avatar_key`, `icon_key`) are **not** shared, so deleting one
  account's objects can never reach the other's.
- `auth.Authenticator.SetDemoMode(true)` makes session resolution enforce the
  account's absolute expiry and **skip the sliding `Touch`**, so activity cannot
  extend a demo past its deadline. `CreateSessionTTL`/`SetCookieTTL` mint the
  shorter-lived session.
- `demo.Manager.Run` purges expired ephemeral users every 10m (object keys first,
  best-effort), deletes the row, and drops lapsed sessions. `ExpiredEphemeral`
  treats a NULL/empty `expires_at` as expired.
- The add-feed cap applies only to ephemeral users:
  `Server.demoFeedLimitReached` is checked before every feed-create path
  (`feedCreate`, `authorFeedCreate`, `apiSave`, extension `saveFeed`, OPML
  import). The allowance is `seedFeedCount + NF_DEMO_MAX_FEEDS` (default 5), read
  live so editing the seed changes it without a restart.
- `users.List`/`CountPersistent` exclude ephemeral accounts, so the admin user
  list and the "first account" bootstrap ignore demo users. `Server.allowSignup`
  is false in demo mode; it is also false once any real account exists unless
  the admin re-enables it (see "First account and signup" below). The seed admin
  logs in normally.
- `views_layout.templ`'s topbar renders a `#demo-countdown` badge when the
  request carries a demo status (`demoFrom` context); `app.js` ticks it and
  sends the visitor to `/` at zero. Do not put it on the landing page.

## Plugins

- `feeds.plugin_name` (schemaV31) records which plugin currently owns a feed;
  empty means the generic parser. Set on create via
  `FeedStore.CreateWithPlugin` (callers use `Server.pluginNameFor`). Ownership
  is **derived, never locked**: `plugin.ReconcileFeeds` re-matches every feed's
  URL at startup, writes the winning plugin's name (or clears it when none
  matches) and never parks a feed just because a plugin is missing. A feed whose
  plugin disappeared therefore falls back to the generic parser. Feeds parked by
  older builds (`feeds.disabled_reason = 'plugin not loaded: <name>'`) are
  re-enabled once (`store.IsPluginMissingReason`/`ReenableAutoDisabled`); a
  **user-paused** feed (no reason) is never resumed.
- There is no reset action: `POST /admin/plugins/reset` and the
  "plugin-owned domains" card section are gone. The plugins card shows, per
  loaded plugin, the domains and distinct feed URLs it currently matches, which
  is the debug view for ownership, including plugins that match only part of a
  shared domain. Routing at fetch time was already by URL match, so the stored
  name is display/bookkeeping only.
- See `docs/fetching.md` (how fetching works) and
  `docs/writing-plugins.md` (author guide).

### Plugin control plane (create and manage remote feeds)

The plugin API is not only pull: a plugin can create a feed on a remote service
and manage one the user already subscribed to. Both are URL-matched capabilities
(`pluginapi.CapProvision`, `CapFeedAdmin`), added in APIVersion 0.4.

- **`Provisioner`** (`CapProvision`) creates a remote feed. There is no URL to
  match, so the host advertises the plugin only when `Meta.ProvisionLabel` is
  non-empty; the label is the topbar add-menu ("#add-menu-pop") entry, rendered
  as "Add a {label}". The returned `Provisioned.FeedURL` is stored and polled by
  the generic parser unless the plugin also claims `CapFetch`.
  `Provisioned.Fields` are display-only values (the inbox address) rendered
  read-only with a copy control. The create form lives in the shared
  `#provision-dialog` (`GET /fragments/provision-form?plugin=`,
  `openProvisionForm` in `app.js`), and a created feed gets its own author named
  after the feed title.
- **`FeedAdmin`** (`CapFeedAdmin`) manages an existing feed: `FeedFields(feedURL)`
  is **pure** (view-time, no network I/O) and returns the display-only fields;
  `Action` performs `"save"` (the host passes the current `title`/`icon`) and
  `"delete"` (`Deleted: true` drops the local feed too). (Named `FeedFields`, not
  `Settings`, so a plugin can also implement `Configurable`.)
  **Feed management is opt-in**: `Registry.MatchFeedAdmin` checks a *genuine*
  implementation, never a bare type assertion. A plugin is reached as the gRPC
  client, which satisfies every optional interface, so the host trusts the
  capability the plugin reported (`Meta.HasFeedAdmin`, computed by the plugin's
  gRPC server from its implementation). Only a plugin that reports this
  capability shows the UI.
- Host wiring: `Registry.Provisioners()` / `ProvisionerByName(name)` and
  `Registry.MatchFeedAdmin(u)` / `FeedAdminSettings(feedURL)`. `Info` gained
  `ProvisionLabel` and `CanManageFeeds` (plugin-reported for the latter, with
  `HasDocs`).
- UI: `GET /fragments/provision-form?plugin=` and `POST /feeds/provision` (add
  flow, `views_pluginprovision.templ`), and `POST /feeds/{id}/plugin-admin` for
  the title-sync action. The "managed feed" card (`feedPluginPanel`) lives on a
  feed's **edit** page, not its regular page; remote deletion is the delete
  form's opt-in "also delete it on the remote service" checkbox (`feedDelete`),
  so the card itself has no destructive action.
- A plugin may implement the control plane without claiming `CapFetch` when its
  feeds are read by the generic parser, so `feeds.plugin_name` stays empty and
  `ReconcileFeeds` is untouched. Such a plugin's service instance is normally a
  plugin setting (see below).

### Plugin settings

A plugin can declare admin-editable configuration (a service base URL, API
tokens, session cookies) instead of reading env vars or a sidecar config file.

- `pluginapi.Configurable` (`Settings() []SettingField`, `Configure(values)`),
  advertised by `Meta.HasSettings`. `SettingField.Kind` is `text`,
  `password` (write-only), `url`, or `bool` (a checkbox, delivered to `Configure`
  as `"1"`/`"0"`). Added in APIVersion 0.5; `bool` is a later additive kind.
- **Delivery is a push.** `plugin.ConfigureAll` (called in `Setup`, before
  `ReconcileFeeds`) reads each configurable plugin's stored values and calls
  `Configure`, and the admin save handler re-pushes after a write. Push is what
  lets `Match` honor configuration: `Match` receives no Host, so a plugin that
  matches URLs by a configured host caches the value in `Configure`.
- **Storage** reuses the global `settings` table, keyed `plugin.<name>.<field>`
  (`SettingStore.PluginSettings`/`SetPluginSettings`). No migration.
- **UI**: the admin plugins card renders a settings form per configurable plugin
  (`POST /admin/plugins/{name}/settings`); a `password` field shows only whether
  a value is set and offers a "clear" checkbox, so a secret is never rendered
  back. Saving stores, calls `Configure`, and re-renders the card.
- A plugin whose service base URL is configurable updates what `Match`
  recognizes and what `Provision`/`Action` call when `Configure` pushes a new
  value. Plugin-specific settings belong in that plugin's own repository docs.

### Plugin docs

- A plugin may implement `pluginapi.Docser` (`Docs() string`, Markdown) to
  document itself; `Meta.Summary` is its one-line admin-card description.
  `HasDocs` on `Meta` is filled by the plugin's gRPC server from its
  implementation, not authored.
- Docs are matched on `CapDocs`, **not** on feed ownership, so a plugin that
  does not own a feed's fetch can still document it: a feed fetched by the
  generic parser (`plugin_name` empty) can still be documented by the plugin
  that knows its URL and the categories its parser adds. The same URL-matched
  pattern carries `CapSharedKey`, `CapDecorate`, `CapURLPolicy` and
  `CapDiscover` (see "Plugins own site-specific behavior").
- `Registry.Docs(name)` / `Registry.MatchDocs(url)` / `Registry.ByName(name)`
  back it; `Registry.ErrNotFound` distinguishes "no such plugin" from
  "plugin has no docs" (`pluginapi.ErrUnsupportedCapability`).
- `Registry.Docs` is called lazily by `GET /fragments/plugin-docs?plugin=<name>`
  (auth-only, **not** admin-only: the feed editor offers the same docs next
  to the feed title). The readme must not do network I/O.
- The docs modal is the shared `#plugin-docs-dialog` in `views_layout.templ`,
  filled by `openPluginDocs(name)` in `app.js`. Entry points: the admin plugin
  card and the feed editor's "plugin docs" button.
- Markdown is rendered by `web.Markdown` (goldmark, raw HTML disabled; anchors
  get `class="external"` + `target/rel`). goldmark is a root-module dep only;
  `pluginapi` stays dependency-free.
- Plugin readmes live beside their plugin code (`readme.md`, `//go:embed`):
  `plugins/<name>`. The readme is user-facing and rendered in the docs modal;
  implementation notes and site-specific invariants belong in the plugin
  repository's own `AGENTS.md`.

### Item media duration

- `items.duration_sec` (schemaV37) is a media item's runtime in seconds, NULL
  when unknown. `pluginapi.Item.DurationSec` carries it across the gRPC plugin
  boundary; `store.Item.DurationSec` and `poller.ingest` pass it through.
- It is part of the upsert snapshot, so re-polling refreshes it on an existing
  row (`UpdateItemSnapshot`). Cards show it as a bottom-right pill
  (`web.FormatDuration`); 0/unknown renders nothing.

### Enclosure render kinds (core-owned players)

A plugin declares *what* an enclosure is; the core owns *how* it renders, so no
plugin ships a player. This replaces the old "a manifest needs a frontend we do
not have" limitation.

- `pluginapi.Enclosure` gained `Kind`, `Poster` and `Title` (APIVersion stays
  `0.5`; additive). `Kind` is a string: `""` (auto), `image`, `audio`, `video`,
  `hls`, `link`, with `pluginapi.EnclosureKind*` constants.
- One resolver is the source of truth: `feedparse.ResolveEnclosureKind(kind,
  rawurl, mime)`. A declared kind wins; otherwise MIME, then URL extension
  (`.m3u8` → `hls`). `web.EnclosureKind` (render) and `imagecache.isImage`
  (cache) both delegate to it, so a generic-parser `.m3u8` is `hls` too.
- The `pluginapi` and `feedparse` constant sets are duplicated by necessity
  (`pluginapi` cannot import `internal/feedparse`); keep them in sync.
- `views_items.templ` dispatches on the resolved kind: `hls` → `<video
  data-hls poster>` played by the lazily-loaded hls.js (`static/hls-video.js`);
  `video`/`audio` → native `<video>`/`<audio controls>`; `image` inline; else a
  download link. The poster is the enclosure's `Poster`, else the item's
  thumbnail (`itemViewData.enclosurePoster`).
- Storage: `item_enclosures.kind`/`poster` (schemaV46); `poller.storeEnclosures`
  and `store.ReplaceEnclosures`/`Enclosures` carry them.
- `RenderRequest` also gained `GUID`, `Kind`, `Categories` and `Enclosures`, so a
  view-time renderer sees the stored media instead of only
  link/summary/imageURL.
- Plugin-shipped assets and arbitrary custom renderers (a plugin bundling its
  own JS/CSS) are **not** supported; that would be a separate capability. Any
  media a site can express through these kinds should use them.

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

The same post can be subscribed twice: through one feed and through another that
also carries it. It is stored as one row, not two, so read/favorite/list/share
state is shared and the combined streams count it once.

- `items.user_id` (schemaV38) denormalizes the owner; `items.cross_key` is the
  per-user cross-feed identity (`site:type_<id>`, empty when not dedupable).
  `items.feed_id` remains the owner/display feed. `item_feeds(item_id, feed_id)`
  is the membership set; feeds list items through it, while the display join
  still uses the owner feed.
- `store.crossFeedKey` normalizes the plugin-supplied `SharedKey` into the stored
  key (it now just trims). The identity is the plugin's: its `SharedKeyer` sets
  the key, identical in every feed that carries the post. Nothing else is
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
  of another feed, so deleting one feed does not delete a post still reachable
  via another.
- Attribution comes from the plugin's `Decoration` (see "Plugins own
  site-specific behavior"): the old core attribution helpers are gone, and the
  row/modal render `ItemWithFeed.Attribution` (already resolved by the store from
  the decoration's tokens). `ItemWithFeed.Sources` is populated from
  `item_feeds`; the title-based `httpapi.dedupItems` remains only for near-
  duplicate titles and skips items with a `CrossKey`.
- A poster (or sub) link must not depend on the item's `item_feeds` membership:
  a cross-feed post seen through one feed only gains a membership in the poster's
  feed once *that* feed has polled the post, so resolving the poster from
  `Sources` alone left it externally linked until then (the "same feed, same
  poster, only one linked" bug). The store's `decorate` resolves each
  decoration token against the user's **subscribed** feeds via the URL policy's
  `FeedToken` (derived from the feed URL, not its title, so a rename still
  resolves). It runs inside `attachSources` (user-scoped) on every list/detail
  path; the public list page passes user 0 and skips the resolution.

## Combo boxes and controls (shadcn-templ)

Single- and multi-select form fields use copied shadcn-templ components, so long
option lists are searchable. See
[`docs/vendored-web-components.md`](docs/vendored-web-components.md) for the
full guide (regeneration, adding components, gotchas). The invariants that bite:

- Component source lives in `internal/web/ui` (pinned shadcn-templ
  `v2.0.0-beta.16`). The Tailwind output
  (`internal/web/static/shadcn.css`), the hashed JS bundle, `_templ.go` files and
  `internal/web/ui/scripts_bundle.go` are generated and committed. Run
  `mise run gen` and `mise run ui:gen` after changing components, then commit
  the outputs. Runtime and the Docker build never need node or Tailwind.
- Use the uncontrolled props: `DefaultValue`, `DefaultValues`,
  `DefaultChecked`. The controlled props (`Value`, `Values`, `Checked`) make
  the JS wait for an owner update and ignore the user.
- The components render their own hidden native inputs, so normal form submits
  and htmx see the expected field values. Field-level htmx attributes go on the
  wrapper via `comboFieldAttrs` (`views_combo.templ`), which adds
  `hx-disinherit="*"`, a `from:` trigger on the hidden input, and `hx-include`
  for the field. Do not put htmx attributes on the text input.
- Components self-initialize through their MutationObserver, including markup
  cloned by `app.js` or injected with `fetch` + `innerHTML`. Keep
  `htmx.process` for htmx attributes only.
- The home pinned-collection list is plain server markup reordered by
  `static/home-reorder.js`, not a component with an imperative renderer.
- Popups are portaled into the nearest `<dialog>` by `static/shadcn.js`, because
  a body-portaled popup would render behind a native modal dialog.
- Tailwind preflight is intentionally not imported. `app.css` keeps the page
  base and scopes its generic form rules with `:not([data-slot])`. Do not
  remove those guards or shadcn controls will inherit the app button/input look.
- For a plain fixed-set choice where search adds nothing, prefer the custom pill
  picker (`PickerControl` in `views_items.templ`); see `/settings` home screen.

## Security invariants

- **First account and signup.** There is no admin env var and no default
  account. `Server.allowSignup` opens signup only while `CountPersistent()==0`
  (checked before the `allow_signup` setting, so a settings read error cannot
  lock out a fresh install), or when the admin has enabled it. The first signup
  runs `UserStore.CreateFirstAdmin`, which promotes the account in the same
  transaction and flips `allow_signup='0'`; a concurrent second signup simply
  becomes a normal user. `schemaV17` seeds `allow_signup='0'`. Recovery is the
  CLI (`nanoflux user create --admin`, `user set-admin`).
- **Ingest sanitizer.** `internal/sanitize.HTML` (bluemonday, raster-only media,
  no script/style/iframe/svg/event handlers/js URLs) is applied at the store
  write boundary: `ItemStore.Upsert` (covers feed summaries, plugin items and
  saved pages) and `ItemStore.SetContent` (enriched bodies). Item bodies are
  still rendered with `templ.Raw`, so the sanitizer is the only thing keeping
  feed HTML inert; do not add a new write path that stores an item body without
  going through these methods.
- **SSRF guard.** `internal/safedial.Client` refuses non-public addresses and
  dials the resolved IP itself (DNS-rebinding safe). It backs the poller client
  (hence plugin `Host.Do` and `imagecache`), the httpapi client (discovery,
  oEmbed, avatars, icons, `/img`). Do not construct a bare `http.Client` for a
  user-supplied URL. `NF_ALLOW_PRIVATE_FETCH=1` is the LAN opt-out, read per
  client construction.
- **CSRF.** `csrfMiddleware` requires `hex(sha256("nanoflux-csrf:"+session))` on
  unsafe methods, from `X-CSRF-Token` (htmx/fetch, set in `app.js` from the
  `<meta name="csrf-token">`) or a `csrf_token` hidden field (`@csrfField()`).
  Exempt: `/login`, `/signup`, `/demo` and `/api/` (Bearer). Every new
  authenticated plain `<form method="post">` needs `@csrfField()`; htmx forms
  are covered by the header.
- **CSP / no inline JS.** `securityHeaders` sets a strict CSP plus
  `X-Frame-Options`, `nosniff` and HTTPS-conditional HSTS. `script-src` has no
  `unsafe-inline`/`unsafe-eval`, so inline `on*` handlers and htmx `hx-on` are
  forbidden: wire behaviors through `data-*` hooks in `app.js`. `style-src`
  keeps `unsafe-inline` (inline style attributes and vendored component styles).
  The landing page's highlight.js bootstrap is `static/marketing.js`, not
  inline. `frame-src` is intentionally `https: http:` (not an allowlist), like
  `img-src`/`media-src`: the app renders third-party player iframes resolved at
  view time (oEmbed providers such as vimeo or imgur, and plugin `Render`
  `EmbedSrc`), whose hosts cannot be known ahead of time. This is safe because
  stored feed/plugin HTML cannot inject an iframe (`sanitize.HTML` strips it at
  the store boundary), so the only frames are core-emitted, and `'self'` is
  deliberately absent so an embed cannot frame the app's own pages. Keep
  `frame-ancestors 'none'`, `object-src 'none'` and the strict `script-src`.
- **Images are raster-only.** `internal/imageutil.Sniff` rejects SVG/HTML; it
  gates `/img`, author avatars, source icons and `imagecache`, and the
  byte-serving routes add `Content-Security-Policy: default-src 'none'; sandbox`.
  Never trust an upstream `Content-Type` alone.
- **Sessions are hashed.** `SessionStore` stores `sha256(raw token)`; lookups
  hash the presented value. The settings page keys revoke by the stored hash and
  `DeleteByHash`; `DeleteUserSessionsExcept` hashes the kept token. The raw token
  still lives only in the HttpOnly cookie (and CSRF derives from it).
- **Error text.** Fetch failures are shown through `feedPreviewError` (categorized,
  never the URL or raw error); the underlying error is logged with the URL
  (`feed preview failed`, poller `poll feed`). Do not return `err.Error()` to a
  user.
