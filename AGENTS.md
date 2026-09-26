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
- `store.CanonicalFeedURL` rewrites reddit URLs (`old.`/`np.` → `www.`,
  `/u/` → `/user/`) on create and via a startup pass, since the old hosts now
  redirect to a login wall.

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
