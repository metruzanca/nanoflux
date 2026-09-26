## Project Rules

- Commit messages must follow the Conventional Commits spec.
- Document all environment variables in `.env.example`.
- App links are internal by default, ↗ on every external link.
- mise is for development, make is for selfhosting an instance
- Do not mention any external plugins in internal code, comments or docs
- Do not run `make` or `podman` or `docker` commands without user's approval. The container is likely the user's production deployment. Use go to run the app locally instead e.g. go run cmd/server/main.go which runs on 8080. You may kill port 8080 if necessary.

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
