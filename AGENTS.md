## Project Rules

- Commit messages must follow the Conventional Commits spec.
- Document all environment variables in `.env.example`.
- App links are internal by default, ↗ on every external link.
- mise is for development, make is for selfhosting an instance
- Do not mention any external plugins in internal code, comments or docs
- Prefer extending `pluginapi` over site-specific core changes. When a request
  concerns one site and the direct fix would add a special case to the core app,
  stop and extend `pluginapi` instead, then have the core call into a native or
  external plugin. Core should not accumulate per-site edge cases. (See "Plugins
  own site-specific behavior".)
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

When a site-specific need appears, the default is a plugin, not core. Reach for
a new `pluginapi.Capability` (URL-matched when the behavior is keyed to a URL) or
an optional interface advertised by a host-filled `Meta` flag, and wire the core
to call the registry at one chokepoint. Keep anything that runs per render pure
and cheap (a Match-only capability needs the plugin's `Match` to be
capability-aware; see the invariant below). Native when the logic is general or
needs no isolation; external when it is opt-in, fragile, or ToS-sensitive.

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
  `Decorated.DedupeKey` is an optional view-time content identity: the store
  copies it onto `ItemWithFeed.DedupeKey`, and `httpapi.dedupItems` collapses two
  cross-feed items that share it into one row (survivor + `Sources`) even when
  their titles differ. reddit keys it by the poster plus the post's external
  `[link]` destination, so one user crossposting the same link to several
  subreddits under different titles shows once. It is matched **before** the
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
- **`ProxyBypasser`** (no capability constant): a plugin whose image hosts
  hotlink freely but refuse the host's server-side `/img` request implements
  `BypassProxy(rawurl) bool`. `plugin.Registry.BypassesProxy` asks every plugin
  that implements it and any true answer makes `web.ProxiedImageURL` return the
  raw URL. `httpapi.Server.SetPlugins` installs the predicate into `web`
  (`web.SetProxyBypass`) once at startup. Pure; the reference is the reddit
  plugin's `redditMediaHosts` list. External plugins are the gRPC client, which
  does not implement this, so only native plugins can bypass today.

`Match` **must honour the capability it is asked about**: return false for any
capability the plugin does not claim. A plugin that ignores it (an early
`func Match(u, _ Capability) bool { return isSiteURL(u) }`) spuriously claims
URL-matched capabilities the registry asks about — which broke the Match-only
`CapImageCache` for Instagram/Patreon feeds until their `Match` was made
cap-aware. Optional capabilities that pair `Match` with a type assertion are
tolerant (a non-implementer is skipped; the external gRPC client's unsupported
RPC is handled), but Match-only ones are not.

The reddit plugin (`internal/plugin/native/reddit`) is the reference for all of
these: `Match` returns true for `CapSharedKey`, `CapDecorate`, `CapURLPolicy`,
`CapDiscover`, `CapRender` and `CapDocs` on reddit hosts; it does **not** claim
`CapFetch`. A reddit **search** URL (`/search` or `/r/{sub}/search`) is not a
subreddit/user feed, so the plugin returns false for `CapDiscover`,
`CapURLPolicy` and `CapDocs` on it (otherwise discovery would read
`/r/{sub}/search` as r/{sub} and the URL policy would rewrite it to the `.rss`
origin), leaving those URL-matched capabilities to a plugin that owns search.
The item-link capabilities (`CapRender`, `CapDecorate`) and the feed-URL-matched
`CapSharedKey` still apply, so search results cross-dedupe with subscribed feeds
and render reddit's normal attribution. `ItemWithFeed.Kind` drives the row card (`KindText` default,
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
- Sizes drive the feed page and author stats ("cached media", `web.FormatBytes`),
  summed from the DB via `ItemStore.StorageByFeed`/`StorageByAuthor` (membership-
  scoped). The `.ct` sidecars are disk-store metadata; `List`/`Stat` ignore them.
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
  The generated `ListItems*` queries are fixed-shape, so tag filtering uses the
  hand-written `ItemStore.listPageTagged` (dynamic `EXISTS` per tag; supports
  newest/oldest/magic and all scopes), mirroring `SearchPage`'s raw-SQL pattern.
  The HTTP layer reads repeated `?tags=` (`tagParams`, capped at
  `maxTagFilters`); tab/sort/pagination links carry them (`appendTags`).
- UI: the item modal shows its tags as clickable chips (linking to
  `/feeds/{id}?tags=`); a scoped feed/author/collection list gets a
  magnifying-glass tag-filter dialog (`comboMulti` over the scope's
  `ListCategories` options) plus active-filter chips; the feed edit filter card
  gets a collapsible (`<details class="tag-cloud">`) tag list with counts whose
  buttons prefill the add-rule form (`data-tag-fill` in `app.js`).


## Filter modes (block / allow)

A feed's rules are a block list by default; a feed can instead be an allow list.

- `feeds.filter_mode` (schemaV47) is `block` (default) or `allow`.
  `filtermatch.Decide(mode, rules, fields)` is the single decision shared by
  `poller.ingest` and the HTTP preview/retroactive paths. `block`: the first
  matching rule wins (`delete` drops the item, `mark_read` stores it read; no
  match keeps it). `allow`: the item is kept iff it matches **at least one**
  rule, the per-rule action is ignored, and no rules at all keeps everything (a
  safety default so flipping the toggle cannot wipe a feed). `NormalizeMode`
  maps an unknown/empty value to `block`.
- `poller.ingest` returns `(newItems, filtered)`. `PollOlder` treats a page with
  zero new items as history-exhausted only when nothing was filtered, so an
  all-filtered page cannot stop a "load older items" walk.
- The feed edit filters card has a mode picker (`POST /feeds/{id}/filter-mode`,
  `Server.feedFilterMode`). Switching to allow re-applies the whole rule set to
  stored items and removes those matching no rule; switching back to block only
  affects future polls (removed items are already gone). Adding a rule in allow
  mode likewise re-applies the full set; in block mode only the new rule is
  applied. `feedRulePreview` is set-level in allow mode (keep = matches any rule)
  and per-rule in block mode. `store.FeedStore.SetFilterMode` verifies
  ownership, and `cloneFeeds` carries the mode so demo clones keep it.

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
- `GET /favorites/algorithm` is the editor (`favoritesAlgorithmPage`): feeds
  listed by favorite count with a three-state `rankControl`; `POST
  /feeds/{id}/rank` swaps the control. `FeedStore.ListWithFavorites` backs it.
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
  lists, filters, author links, source icons (URLs only) and view prefs are
  copied. Blobs (`avatar_key`, `icon_key`) are **not** shared, so deleting one
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
  implementation, never a bare type assertion. An external plugin is reached as
  the gRPC client, which satisfies every optional interface, so the host trusts
  the capability the plugin reported (`Meta.HasFeedAdmin`, computed by the gRPC
  server from the remote implementation); a native plugin is checked by the real
  assertion. Only KTN manages feeds today, so no other feed shows the UI.
- Host wiring: `Registry.Provisioners()` / `ProvisionerByName(name)` and
  `Registry.MatchFeedAdmin(u)` / `FeedAdminSettings(feedURL)`. `Info` gained
  `ProvisionLabel` and `CanManageFeeds` (host-filled, like `HasDocs`).
- UI: `GET /fragments/provision-form?plugin=` and `POST /feeds/provision` (add
  flow, `views_pluginprovision.templ`), and `POST /feeds/{id}/plugin-admin` for
  the title-sync action. The "managed feed" card (`feedPluginPanel`) lives on a
  feed's **edit** page, not its regular page; remote deletion is the delete
  form's opt-in "also delete it on the remote service" checkbox (`feedDelete`),
  so the card itself has no destructive action.
- Reference: `internal/plugin/native/killthenewsletter`. It does **not** claim
  `CapFetch` (its Atom feeds are read by the generic parser), so
  `feeds.plugin_name` stays empty and `ReconcileFeeds` is untouched — the same
  pattern reddit uses. Its instance is a plugin setting (see below).

### Plugin settings

A plugin can declare admin-editable configuration (a service base URL, API
tokens, session cookies) instead of reading env vars or a sidecar config file.

- `pluginapi.Configurable` (`Settings() []SettingField`, `Configure(values)`),
  advertised by host-filled `Meta.HasSettings`. `SettingField.Kind` is `text`,
  `password` (write-only), `url`, or `bool` (a checkbox, delivered to `Configure`
  as `"1"`/`"0"`). Added in APIVersion 0.5; `bool` is a later additive kind.
- **Delivery is a push.** `plugin.ConfigureAll` (called in `Setup`, before
  `ReconcileFeeds`) reads each configurable plugin's stored values and calls
  `Configure`, and the admin save handler re-pushes after a write. Push is what
  lets `Match` honor configuration: `Match` receives no Host, so a plugin that
  matches URLs by a configured host (KTN) caches the value in `Configure`.
- **Storage** reuses the global `settings` table, keyed `plugin.<name>.<field>`
  (`SettingStore.PluginSettings`/`SetPluginSettings`). No migration.
- **UI**: the admin plugins card renders a settings form per configurable plugin
  (`POST /admin/plugins/{name}/settings`); a `password` field shows only whether
  a value is set and offers a "clear" checkbox, so a secret is never rendered
  back. Saving stores, calls `Configure`, and re-renders the card.
- `internal/plugin/native/killthenewsletter` is the reference: its `base_url`
  setting selects the public or a self-hosted instance, and changes both what
  `Match` recognizes and what `Provision`/`Action` call.

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
  for `com.atproto.repo.listRecords`; `bsky.social` answers `com.atproto.repo`
  calls but **401**s `getProfile`. The plugin's `apiBase`, `repoBase`, `imgBase`
  and `videoBase` vars encode this so a test can point them at a mock host.
- Media: images use `cdn.bsky.app/img/feed_fullsize/plain/{did}/{cid}` (direct,
  cacheable). Video is attached as an **HLS** enclosure
  (`application/vnd.apple.mpegurl`, URL
  `video.bsky.app/watch/{did}/{cid}/playlist.m3u8`); the raw
  `com.atproto.sync.getBlob` MP4 is not used because it returns the whole file
  and ignores `Range`, so seeking is limited. HLS segments are CORS-open and
  Range-capable, and the stored master playlist is fetched fresh at play time
  (its session-scoped rendition URLs need no storage). The video's poster frame
  (`video.bsky.app/watch/.../thumbnail.jpg`) is the item thumbnail; it is served
  as `application/octet-stream`, so `imgProxy` sniffs the bytes (see below).
  The enclosure sets `Kind: pluginapi.EnclosureKindHLS`; `web.EnclosureKind`
  resolves it to `hls`, the template renders `data-hls`, and a lazily-loaded
  hls.js (`static/hls-video.js`) plays it (`docs/vendored-web-components.md`).
- The `/img` proxy trusts an upstream `image/*` Content-Type but otherwise sniffs
  the first bytes with `http.DetectContentType`, echoing the real type. Without
  this, hosts that mislabel images (Bluesky's video thumbnails) render as broken.
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
