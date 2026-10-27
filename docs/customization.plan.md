# Customization plan

Status: living document, last refreshed 2026-10-26 against the current code.
Several items below have landed since the first draft; see
[Recently landed](#recently-landed-since-the-first-draft). The remaining tiers
are proposals, not commitments.

Goal: make nanoflux highly configurable/customizable within reason, borrowing
the ideas users consistently praise in Inoreader and Miniflux without turning a
dead-simple reader into a theme engine.

Decisions:

- Customization philosophy: **bounded presets**, not arbitrary CSS/JS injection.
  Advanced escape hatches (custom CSS) are out of scope.
- Per-scope display state belongs to the **account**, not the browser
  (`user_view_prefs` now backs list/grid/cards).
- Compatibility APIs (Fever / Google Reader) were deferred. A refreshed audit
  puts a Google Reader-compatible API at the top of the interop work; the
  decision to schedule it is still open (see [Open questions](#open-questions)).
- Implementation order is open; the tiers below are sized so they can ship
  independently.

## Verdict: where nanoflux stands against Miniflux

Short version: on personal reading and self-hosting operations nanoflux is
ahead; on interoperability and ingestion plumbing Miniflux is ahead.

### Ahead

- Author-centric organization plus collections, user lists, favorites,
  bookmarks, saved pages and per-scope share tokens. Miniflux has categories
  and bookmark integrations, not native lists.
- Magic sort and per-feed taste ranking.
- Polling: adaptive intervals, time-of-day scheduling, per-host fair rotation,
  learned rate-limit windows and one shared cooldown between poller and plugins.
- Site-specific plugins with a real capability boundary and per-plugin settings.
- Signed-URL image caching, S3 object storage, automatic local/S3 backups and
  `storage report`/`gc`.
- Complete enclosure players (HLS, video, audio, poster, duration), not just
  attachment links.
- Browser extension, PWA share target, merged command palette, per-user accent
  and grid preferences.
- SQLite single-file deployment, admin poll health, CLI, demo mode, Umami
  analytics.

### At parity

RSS/Atom/JSON Feed (via `gofeed`), OPML import/export, full-text search,
share links, favicons and source icons, sanitization and strict CSP, multi-user
and admin, multiple enclosures, keyboard navigation, PWA install, background
scheduler, tracking-parameter stripping, `Referrer-Policy: no-referrer` and an
image/media proxy.

### Behind, in rough priority order

1. Reader API and mobile client ecosystem (Google Reader, Fever). The JSON API
   is extension-only.
2. Third-party save/notify integrations and webhooks.
3. Generic Readability full-text extraction, per-feed CSS scraper rules,
   rewrite rules and regex include/exclude filters.
4. Authentication breadth: passkeys/WebAuthn, OAuth2/OIDC, reverse-proxy auth.
5. Internationalization (Miniflux ships 20 languages; nanoflux is English only).
6. Privacy remainder: FeedBurner link rewriting, pixel-tracker removal,
   privacy-friendly embed hosts (`youtube-nocookie.com`, alternative frontends).
7. Transport and bot-bypass knobs: built-in TLS/Let's Encrypt, custom certs,
   per-feed cookies/proxy, HTTP/2 and systemd integration.
8. Minor: OPML URL import, serif/density/reader-width presets, custom CSS/JS
   (deliberately rejected).

## What people like in each product

### Miniflux (self-hoster crowd)

- Removes pixel trackers, strips tracking params (`utm_*`, `fbclid`, ...),
  rewrites FeedBurner links to the original, sets
  `rel="noopener noreferrer"` plus `Referrer-Policy: no-referrer`.
- Media proxy and privacy-friendly embed hosts (for example,
  `youtube-nocookie.com`), optional alternative frontends.
- Readability full-text extraction, per-feed custom CSS-selector scraper
  rules, and regex include/exclude filters.
- Custom stylesheets and JavaScript to personalize the UI.
- Serif and sans themes in light/dark/system.
- Google Reader + Fever APIs (unlocks Reeder, NetNewsWire, and other clients).
- 25+ save/notify integrations (Apprise, ntfy, Discord, Telegram, Slack,
  wallabag, linkding, ...), webhooks, bookmarklet.
- Auth beyond passwords: passkeys (WebAuthn), Google OAuth2, OIDC,
  reverse-proxy auth.
- Custom User-Agent, cookies, proxy, optional invalid-cert tolerance.

### Inoreader (power-user crowd)

- Tags/labels and a rules engine (match, then star/tag/hide/notify).
- Saved searches and global search across sources.
- Per-feed and per-view display memory (list/grid, sort) that follows the
  account, not the browser.
- Unread counts surfaced on every scope.
- Keyboard navigation, mark above/below.
- Newsletters-to-feeds, web-page change monitoring, Google News alerts.
- Browser extension "save/send to".

## Current nanoflux surface

Architecture: Go 1.26.6 + stdlib `net/http` ServeMux, templ views, htmx 2.x,
hand-written `app.css`, SQLite, sqlc queries, hashicorp go-plugin (plugin API
0.5). Author-centric: feeds belong to authors, collections group feeds, lists
group items, favorites and bookmarks are native special lists. Dev via `mise`,
selfhost via `make`.

### Per-user settings (`GET /settings`)

- Profile picture (`users.avatar_key`).
- Home screen dashboard (`users.home_config`, JSON of collection sections with
  `list`/`grid` mode; `internal/httpapi/home.go`).
- Timezone (`users.timezone`).
- Appearance card: theme light/dark/system (`users.theme`), accent color
  (`users.accent_color`), hide unread counts (`users.hide_unread_counts`),
  move unread nav into the user menu (`users.hide_unread_nav`), home grid max
  columns 2..6 (`users.grid_max_columns`).
- Auto-read window 3/7/30/60/off (`users.auto_read_after_days`).
- Password and active sessions, OPML import/export, extension download,
  keyboard shortcut reference.

### Per-scope display state

- View mode (list/grid/cards) per page scope is stored per user in
  `user_view_prefs(user_id, scope, mode)`, so it follows the account.
- Authors sort (abc/newest/unread/magic) is still client-side `localStorage`.
- Item sort is server state (`?sort=`), with `?dir=` kept as a legacy alias.
- Home section mode is per collection in `home_config`.

### Per-feed settings

The feed editor lives on the author edit page (one form for the author and all
loaded feeds); `GET /feeds/{id}/edit` redirects there.

- `title`, `feed_url`, `home_url`, `poll_interval_sec` (min 60),
  `poll_interval_auto`, `enabled`, collection memberships.
- `cache_images` (schemaV45), forced on when the plugin claims
  `CapImageCache` and then not user-disableable.
- Plugin panel: docs (`GET /fragments/plugin-docs`) and, when the plugin claims
  `CapFeedAdmin`, remote save/delete via `POST /feeds/{id}/plugin-admin`.
- Feed cards on the author page carry refresh, feed-scoped mark-all-read,
  load-older, home/feed links, last-polled state and cached-media stats.

### Admin / instance

Global signup toggle (`settings` key `allow_signup`), user management, automatic
and manual backups, poller health stats, plugins with owned domains/URLs, plugin
settings forms and plugin docs (`internal/httpapi/admin.go`). Optional Umami
analytics and public demo mode are env-driven.

### Environment (`internal/config/config.go`, `.env.example`)

`NF_ADDR`, `NF_DB`, `NF_DB_MAX_CONNS`, `NF_FILE_STORE`, `NF_S3_*`,
`NF_SESSION_TOUCH_INTERVAL`, `NF_LOG_LEVEL`, `NF_PPROF_ADDR`, `NF_POLL_INTERVAL`,
`NF_POLL_WORKERS`, `NF_POLL_HOST_SPACING` (default 60s),
`NF_USER_AGENT`, `NF_ALLOW_PRIVATE_FETCH`, `NF_PLUGINS_DIR`,
`NF_PLUGIN_IDLE_TIMEOUT`, `NF_BACKUP_*`, `NF_DEMO_*`, `NF_ANALYTICS_*`.

### Theming / display

- CSS vars `--bg --panel --border --fg --muted --accent --danger --warn`
  (`app.css`), with light overrides.
- Server renders `data-theme`, `--accent` and grid columns on `<html>`;
  "system" resolves client-side.
- Layout is list/grid/cards per scope, grid capped by `--grid-columns`.
- No font size/family/density/reader-width preference; no serif theme.
- `theme-color` meta is still hard-coded to `#111318` in `views_layout.templ` and
  does not follow the theme.

### Filters / search / tags

- The per-feed ingest filter-rule system was removed for a clean
  reimplementation; `schemaV53` dropped the `filters` table and
  `feeds.filter_mode`. There is no ingest rule editor or retroactive
  application today. The categories the parser/plugins surface remain and power
  the tag filter.
- Tag/feed filtering is transient query state (`internal/httpapi/filters.go`):
  each row has `show`/`hide`, `tag`/`feed`, `any`/`all`, one or more values, and
  negative author filters are supported. A scoped feed/author/collection list
  gets an editable filter dialog, active-filter chips and match counts.
- Search is stateless FTS5 (`items_fts`, `schemaV10`) over title + summary, with
  `title:`, `author:`, `feed:`, `unread:` qualifiers. No saved searches, and
  item bodies are not indexed.
- Tags: `item_categories` backs tag chips in the item modal and the scoped
  filter dialog. `tag:` works in search.
- Unread counts appear in the topbar and on author/collection rows, not on every
  scope (lists and tags lack badges).

### Privacy / security (already in place)

- `feedparse.StripTracking` removes marketing params (`utm_*`, `fbclid`, ...)
  from item links, image URLs and enclosure URLs at ingest.
- `Referrer-Policy: no-referrer`, `rel="noopener noreferrer"` on external links,
  strict CSP with no inline script, sanitized item HTML at the store boundary.
- Image proxying through `/img`, including body images (`ProxyImageSrcs`), with
  raster-only sniffing; plugin `CapBypassProxy` can opt out per host.
- SSRF-safe dialer, hashed sessions, CSRF on unsafe methods.
- Not yet: FeedBurner link rewriting, pixel-tracker removal, `youtube-nocookie`
  or alternative-frontend rewrites.

### Sharing / export

Item share links (`shared_items`), list share (`lists.share_token`), favorites
share (`users.favorites_share_token`), bookmarks share
(`users.bookmarks_share_token`), public pages at `/l/`, `/f/`, `/b/`. OPML in and
out, whole-instance backup/restore, extension-only JSON API.

### Plugins

`pluginapi.Fetcher` plus URL-matched capabilities: `CapDiscover`, `CapFetch`,
`CapRender`, `CapDocs`, `CapSharedKey`, `CapDecorate`, `CapURLPolicy`,
`CapEnrich`, `CapProvision`, `CapFeedAdmin`, `CapImageCache`, `CapBypassProxy`,
`CapIcon`. External modules load over gRPC from `NF_PLUGINS_DIR`; the core
compiles no site integrations in. `feeds.plugin_name` (schemaV31) records
ownership, and `ReconcileFeeds` re-derives it at startup.

- `Enricher` (`CapEnrich`) fills item bodies at ingest (full text, translation,
  transcript); plugin-driven only, with no per-feed core readability pass.
- `Configurable` (`Settings`/`Configure`, APIVersion 0.5) gives admin-editable
  plugin settings, pushed on save. Per-feed `FetchRequest.Config` is on the wire
  but is still never populated by the dispatcher.

### Keyboard / interaction

`←/→` previous/next while reading (modal pager and horizontal swipe), `Ctrl/⌘+P`
or `Ctrl/⌘+Shift+P` command palette plus search, swipe down from the topbar for
the palette, Tinder-style row swipe actions, `Esc` closes dialogs, deep-link
item modal, load-more.

## Gaps

Ordered by leverage, not by effort.

### Interoperability

- No Google Reader or Fever API, so third-party mobile/desktop clients cannot
  connect. The JSON API exists only for the extension.
- No personal access tokens; scripts and the extension use a session.
- No save-to integrations and no webhooks/notifications.
- No auth beyond local passwords (no passkeys, OAuth2/OIDC, reverse-proxy auth).
- English-only UI.

### Content

- No generic Readability extraction and no per-feed "fetch original article"
  toggle. `CapEnrich` covers plugin-owned sites only.
- No user-defined scraper rules, rewrite rules or regex include/exclude rules.
- No ingest rule engine (the old one was removed pending reimplementation).
- FTS indexes title + summary only, not bodies.
- No saved searches.
- No per-feed HTTP auth, cookies, proxy or invalid-cert option.
- Privacy remainder: FeedBurner, pixel trackers, privacy-friendly embeds.

### Organization and reading

- No unread badges on lists or tags.
- Authors sort remains per-browser.
- No font size/family/serif/density/reader-width preference; `theme-color` and
  the PWA manifest do not follow the theme.
- No bulk selection, mark-read-on-scroll, retention/cleanup per feed, or smart
  virtual feeds.
- No podcast resume/queue, reading time, scroll-position memory, or
  distraction-free/print views.

### Plugins and operations

- No sink/notifier capability, so save-to and webhooks need core work.
- Per-feed plugin config (`FetchRequest.Config`) is still unimplemented.
- No invite codes, per-user quotas, metrics endpoint, or offline PWA reading.

## Recently landed since the first draft

These were listed as gaps or proposals in the previous version and are now in
the code. Do not re-plan them.

- Tracking-param stripping (`feedparse.StripTracking`), `Referrer-Policy:
  no-referrer`, and the `/img` media proxy with body-image rewriting.
- Tag/feed filter rows with `show`/`hide`, `any`/`all`, multi-value rows,
  editable rows, match counts and negative author filters.
- Per-account view mode via `user_view_prefs`; grid columns, hide-counts and
  hide-unread-nav appearance controls.
- Feed image caching for signed URLs, S3 object storage, automatic backups,
  `storage report` and `storage gc`.
- Plugin `CapEnrich` (full text/translation/transcript), `CapProvision`,
  `CapFeedAdmin`, `CapImageCache`, `CapBypassProxy`, `CapIcon`, and plugin
  settings (`Configurable`).
- Polling: adaptive intervals, time-of-day scheduling, per-host fairness,
  learned rate-limit windows and the shared cooldown.
- Magic sort, lists, saved pages, native bookmarks, list/favorites/bookmarks
  share pages, auto-read, demo mode and Umami analytics.
- Command palette merged with entity search; row and modal swipe gestures;
  add-to-list now uses the Vaadin multi-select combo box.

## Proposed work

### Tier 1 - Display personalization (remaining)

Pure presets, no new concepts, low migration risk.

- Add `users` columns for `font_size`, `font_family`
  (`sans`/`serif`/`system`), `density` and `content_width`.
- Emit them as CSS vars on `<html>` next to `--accent`; add serif and density
  variants to `app.css`.
- Make `theme-color` and the PWA manifest follow the theme background.
- New appearance controls in the existing `settingsAppearance` card.
- Move authors sort from `localStorage` into `user_view_prefs` (add a `sort`
  column or a second per-scope key) so it follows the account like view mode.
- Optional: infinite scroll, focus reader and print stylesheet as display
  prefs.

### Tier 2 - Power-user organization

- Rebuild ingest rules: a rule editor with `field`/`pattern`, reactive and
  retroactive application, global (all-feeds) rules, and actions
  `delete`/`mark_read`/`star`/`tag`/`notify`.
- Saved searches: persist the existing query grammar (`saved_searches` table
  plus sidebar run/list endpoints; the parser already exists).
- Unread badges on lists, tags and every scope the filter dialog supports.
- A tag control in the item view and saved tag scopes.
- Bulk selection with batch read/unread, favorite, tag and list actions.
- Per-feed retention/cleanup (max items or delete read older than N days) to
  bound SQLite growth. Auto-read already flips the flag but never deletes.

### Tier 3 - Content and interoperability

Each item is independent and larger. The refreshed audit recommends this order.

1. **Reader API.** Add Google Reader compatible endpoints first (and Fever
   second) so Reeder, NetNewsWire and similar clients work. Keep the existing
   JSON API for the extension. This is the highest-leverage gap.
2. **Personal access tokens** so scripts and the extension stop storing a
   password; use them for the reader API too.
3. **Full-text extraction.** Per-feed "fetch original article" toggle with a
   Readability pass, plus optional per-feed CSS-selector scraper rules.
   Reuse/align with `CapEnrich` so plugin sites and generic sites share one
   storage path (`items.content`).
4. **Notifications/webhooks.** A per-user webhook plus ntfy/Apprise-style hooks
   on new matching items, ideally as a plugin `Sink`/`Notifier` capability so
   destinations stay out of core.
5. **Privacy remainder.** FeedBurner link rewriting, pixel-tracker removal and
   privacy-friendly embed rewrites (`youtube-nocookie.com`, optional alternative
   frontends).
6. **Auth breadth.** Passkeys (WebAuthn) and/or generic OIDC; reverse-proxy
   auth for private deployments.
7. **i18n.** Extract UI strings into message catalogues. Large and invasive, but
   the biggest reach expansion. Only worth it if the audience needs it.

## Additional feature candidates (beyond the tiers)

Ideas not already covered above, grouped by theme. "Hook" notes point at code
that already exists and lowers the cost. Effort is a rough S/M/L. Items marked
**done** moved to [Recently landed](#recently-landed-since-the-first-draft).

### Reading experience

- **Podcast / audio support (M).** Enclosures already render as `<audio>`,
  `<video>` and HLS players with posters and durations. Remaining: playback
  speed memory, resume position per item, a continuous play queue across an
  author's feed, and audio controls in the item modal.
- **Reading time / word count (S).** Derive from item content at ingest or
  render time; show on the item row. Pairs with Miniflux's YouTube read time.
- **Scroll-position memory (M).** Remember how far into a long item the reader
  was and restore it on reopen; drives a "continue reading" surface.
- **Infinite scroll as an option (S).** Current load-more is a button; make
  auto-load a display pref.
- **Focus / distraction-free reader (S).** Hide nav and lists inside the item
  view; a display pref.
- **Print / PDF-friendly view (S).** A print stylesheet for the item.

### Feeds and sources

- **Keyword / search feeds (M).** Save a query and poll it as a watchlist that
  surfaces new matches. Builds on the existing FTS5 grammar and the poller.
- **Newsletter-to-feed (L).** A per-user inbound address. The kill-the-newsletter
  plugin covers the remote-service version; a native inbox still needs a mail
  listener and per-user address minting.
- **Web page change monitoring (L).** Watch non-RSS pages for textual or price
  changes. `internal/discover` and the plugin framework already probe arbitrary
  pages; this adds a snapshot/diff store.
- **More social sources as plugins (M per network).** The framework is proven:
  Bluesky, YouTube, Reddit, Reddit search, Patreon, Instagram, Tumblr, RedGIFs,
  ImageFap, Imaglr, BDSMlr, Literotica, Milovana and Kill the Newsletter are
  bundled; X lives in a separate repository.
- **Bookmarklet plus the extension (S).** A zero-install fallback for Firefox,
  Safari and mobile.

### Browser extension and sharing

- **Context-menu add (S).** The extension requests only `activeTab`/`storage`;
  add a context menu and toolbar action.
- **Send current page item to nanoflux.** **Done**: the extension save-page flow
  stores a hidden system-feed item (`schemaV34`), defaulting to bookmarks and
  surfacing in lists, favorites, bookmarks and search.
- **Share-sheet parity on desktop (S).** The PWA is a mobile share target;
  surface the same "send to" endpoint in the extension.
- **Export a list or favorites as its own RSS feed (S).** Share tokens and public
  pages exist; add an RSS representation.

### Organization and triage

- **Personal access tokens for the JSON API (S).** Still open; high-trust win.
- **Expose the existing dedup as a toggle (S).** `internal/httpapi/dedup.go`
  already collapses near-identical titles across feeds; surface a setting and
  show alternate sources more prominently.
- **Per-feed / per-scope display templates (M).** Beyond list/grid/cards:
  compact vs card, thumbnail size, summary lines. View prefs are the storage.
- **Mute by domain or author (S).** Rebuild the ingest rule system first; then
  add a "mute this site" action from an item.
- **Smart / virtual feeds (M).** Saved scopes like "all unread in this
  collection", "favorites", "recently added". `ItemFilter` already supports the
  scoping; built-ins need no table.
- **Send-to / save integrations (M).** Wallabag, linkding, Readwise, email, etc.
  Same shape as the webhook/Sink work.

### Fetching and content quality

- **Per-feed HTTP auth, headers and cookies (M).** Basic/digest auth or a
  cookie for private/paid feeds. This is where `FetchRequest.Config` could
  finally be populated for plugins.
- **Per-feed proxy and TLS options (M).** Proxy and "allow invalid cert" for
  sites that block the server's IP or use self-signed certs.
- **Feed health panel (S/M).** Admin poll health already exists; add a per-feed
  last-error, consecutive-failure and "stale, no new items in N days" badge.
  `feeds.disabled_reason` and the 429 badge establish the pattern.
- **Index item bodies in full-text search (S/M).** `items_fts` covers title +
  summary; including content changes the FTS table and triggers but makes
  search far more useful.
- **OPML URL import (S).** Miniflux accepts a URL to an OPML; nanoflux takes a
  file. JSON Feed already parses via `gofeed`.

### Notification and automation

- **Digest via webhook (M).** A daily/weekly webhook payload of unread items.
  The app has no mail infrastructure.
- **Per-rule notifications (M).** Wire the `notify` action from Tier 2 to ntfy,
  Apprise or a generic webhook.

### Data portability and trust

- **Full JSON export of items and read/favorite state (M).** Today only OPML
  (subscriptions) and whole-instance backups exist.
- **Import from other readers' JSON (M).** Accept Feedly/Inoreader exports so
  read state survives migration.
- **Audit log (M).** Record admin and account actions for shared instances.

### Accessibility and internationalization

- **Message catalogues / i18n (L).** All UI strings are hardcoded English in the
  templ views. Invasive, but the single biggest reach expansion.
- **Customizable keyboard shortcuts (M).** Shortcuts are fixed in `app.js`;
  make them a settings card.
- **RTL support, reduced-motion and high-contrast presets (S/M each).** Bounded
  theme axes, not freeform CSS.
- **Dyslexia-friendly / Atkinson font option (S).** A font-family preset
  alongside serif/sans in Tier 1.

### Instance operations

- **Invite-only signup with codes (S/M).** Signup is a single global
  `allow_signup` boolean; invite codes are friendlier for private instances.
- **Per-user quotas (M).** Cap feeds or items per user on shared instances.
- **Health / metrics endpoint (S).** `NF_PPROF_ADDR` covers profiling; expose
  poller and store status for Prometheus or an uptime check.
- **Instance default theme/accent for new users (S).** Seed new users from the
  existing `settings` key/value table.
- **Offline PWA reading (L).** The app installs and registers a share target but
  has no service worker caching.

## Plugin ideas

The plugin system is a URL-shaped source adapter: `Match(url, cap)` decides what
a plugin handles, and `Fetch` returns normalized items through host-mediated
HTTP (`pluginapi/pluginapi.go`). Plugins are executables in `NF_PLUGINS_DIR` and
may read their own env vars for API keys (see `docs/writing-plugins.md`). All
default integrations are external plugins; the core compiles none in.

Three shapes of idea are worth distinguishing:

1. **Source plugins** fit today's API directly.
2. **Enricher / extractor plugins** (full text, translation, transcripts) are
   supported today through `CapEnrich` and `items.content`.
3. **Sink / notifier plugins** (save-to, webhooks) still need the reverse hook,
   an outbound action on a new or matched item.

### Source plugins - easy (public API or feed, no key)

- **Hacker News.** Official Firebase API and Algolia search API are open and
  keyless; covers front page, per-user submissions and comment threads.
- **Lobsters, Lemmy, PeerTube.** Public JSON or ActivityPub/Atom; mostly parsing
  work.
- **AO3 (Archive of Our Own) and Royal Road.** Serialized fiction has no good
  reader feed and is a niche nanoflux serves well.
- **Public Telegram channels.** Read `t.me/s/<channel>` previews; no bot token
  needed for read-only.
- **Vimeo, SoundCloud, Bandcamp, Flickr, DeviantArt, ArtStation.** Public APIs
  or feeds; enrich beyond the generic parser.
- **Kick / Twitch / Rumble.** Streaming is a live-ish feed; higher effort due to
  API auth.

### Source plugins - need a key or auth (per-plugin env var)

- **Rate-limited social sites.** A recurring story (see `AGENTS.md`); a plugin
  with OAuth can do better than an anonymous feed the host paces.
- **GitHub Releases / Issues / Commits.** Atom feeds exist, but an API-backed
  plugin can follow a whole org or a search.
- **Mastodon / Misskey.** `@user.rss` exists; an API plugin handles arbitrary
  instances and instance-wide trends.
- **Pinterest, TikTok, Threads, Instagram (authenticated).** Closed APIs;
  scraping is brittle and violates terms. Lower priority and a maintenance
  liability.

### Enricher / extractor plugins (supported via `CapEnrich`)

- **Full-text extraction.** The Tier 3 readability work as a plugin, so the core
  stays lean and users choose per-feed extraction.
- **YouTube transcripts / description expansion.** The YouTube plugin exists; a
  transcript enricher adds real reading material.
- **Translation.** Machine-translate an item body at ingest.
- **Paywall / archive resolver.** Try archive.today or a reader-mode URL when
  the original is paywalled.
- **AMP unwrapping.** A transform plugin for AMP-only pages.

### Sink / notifier plugins (needs an outbound hook)

- **Save-to: wallabag, linkding, Linkwarden, Readeck, Readwise.** One plugin
  interface, many destinations, matching Miniflux's integration list.
- **Notifiers: ntfy, Apprise, Discord, Slack, Telegram.** Overlaps the Tier 3
  webhook work; a plugin interface keeps each destination out of core.
- **Search / export sinks.** Push favorites to a static site or Obsidian vault.

### API changes these imply

- Add one outbound capability kind (a sink/notifier) with its own `Match` gate.
  The post-fetch transform side already exists as `CapEnrich`.
- Persist `FetchRequest.Config` per feed so plugins can take a section, channel
  or extraction depth.
- Consider a plugin-declared settings schema for per-feed fields, beyond the
  existing instance-wide `Configurable` settings.

### Shortlist by leverage

Beyond the tiers: reader API (unblocks mobile apps), personal access tokens
(unblocks scripting and removes stored passwords), full-text body search and
extraction, webhooks/Sink capability, per-feed HTTP auth, and podcast playback.

For plugins specifically: **Hacker News** (keyless), **AO3 / Royal Road** (the
serialized-fiction niche), a **social-site OAuth plugin** (kills the recurring
429 story), and the **Sink capability** (unlocks save-to and notifications
without growing core). Bluesky and the bundled social plugins are done.

## Open questions

- Schedule the Google Reader API now, or keep deferring compatibility clients?
- Full-text: a core Readability pass with a per-feed toggle, or a plugin
  (`CapEnrich`) only? The former helps every truncated feed; the latter keeps
  core lean.
- Density semantics: spacing-only, or also thumbnail/card size in grid mode?
- Should `--font-serif` be a separate theme axis or a font-family preset?
- Should authors sort join `user_view_prefs` (schema change) or stay a per-user
  JSON preference?
