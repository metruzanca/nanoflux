# Customization plan

Status: proposal for review. No code has been written.

Goal: make nanoflux highly configurable/customizable within reason, borrowing
the ideas users consistently praise in Inoreader and Miniflux without turning a
dead-simple reader into a theme engine.

Decisions already made:

- Customization philosophy: **bounded presets**, not arbitrary CSS/JS injection.
  Advanced escape hatches (custom CSS) are out of scope.
- Compatibility APIs (Fever / Google Reader): **not now**. Keep focus on the web
  UI and personalization.
- Implementation order is open; the tiers below are sized so they can ship
  independently.

## What people like in each product

### Miniflux (self-hoster crowd)

- Removes pixel trackers, strips tracking params (`utm_*`, `fbclid`, ...),
  rewrites FeedBurner links to the original, sets
  `rel="noopener noreferrer"` + `Referrer-Policy: no-referrer`.
- Media proxy and `youtube-nocookie.com`, optional Invidious player.
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

Architecture: Go 1.26 + stdlib `net/http` ServeMux, templ views, htmx 2.x,
hand-written `app.css`, SQLite, sqlc queries, hashcorp go-plugin. Auth-centric:
feeds belong to authors, collections group feeds, lists group items, favorites
is a special list. Dev via `mise`, selfhost via `make`.

### Per-user settings (`GET /settings`, `internal/httpapi/settings.go:136`)

- Profile picture (`users.avatar_key`).
- Home screen dashboard (`users.home_config`, JSON of collection sections with
  `list`/`grid` mode; `internal/httpapi/home.go:25`).
- Timezone (`users.timezone`).
- Theme dark/light/system (`users.theme`, `settings.go:243`).
- Auto-read window 3/7/30/60/off (`users.auto_read_after_days`,
  `settings.go:268`).
- Accent color, 8 presets + picker (`users.accent_color`, `settings.go:306`).
- Password, sessions, OPML import/export, extension download.
- Custom source icons (`source_icons`), URL mappings (`url_mappings`,
  `internal/httpapi/mappings.go`).
- Keyboard shortcuts (read-only list).

### Per-feed settings (`GET /feeds/{id}/edit`)

`title`, `feed_url`, `home_url`, `author_id`, `poll_interval_sec` (min 60),
`poll_interval_auto`, `enabled`, collection memberships, and ingest filter rules
(`views_feeds.templ:234`, update `web.go:890`).

### Admin / instance

Global signup toggle (`settings` key `allow_signup`), user management, backups,
plugins list + per-domain reset (`internal/httpapi/admin.go:100`).

### Environment (`internal/config/config.go:52`, `.env.example`)

`NF_ADDR`, `NF_DB`, `NF_FILE_STORE`, `NF_LOG_LEVEL`, `NF_POLL_INTERVAL`,
`NF_POLL_WORKERS`, `NF_USER_AGENT`, `NF_ADMIN_USER/PASS`, `NF_PLUGINS_DIR`,
`NF_S3_*`, `NF_BACKUP_*`.

### Theming / display

- CSS vars `--bg --panel --border --fg --muted --accent --danger --warn`
  (`app.css:1`), light overrides (`app.css:14`).
- Server renders `data-theme` and inline `--accent` on `<html>`
  (`views_layout.templ:125`); "system" resolved client-side (`app.js:20`).
- Layout: list vs two-column grid, per-scope **localStorage** (`app.js:126`);
  authors sort local (`app.js:155`); item sort direction server-side `?dir=`;
  home section mode per collection.
- No font size / family / density / reader-width preference. No serif theme.
- `theme-color` meta and PWA manifest hard-code `#111318`.

### Filters / search / tags

- Ingest filters (`filters` table, `schema.sql:131`): `action` hide/mark_read,
  `field` title/summary/link, `pattern`, `is_regex`. Applied at poll time
  (`poller.go:294`). UI is per-feed only (`web.go:855`); `feed_id NULL`
  (all-feeds) rules work in the store but have **no UI**.
- Search is stateless FTS5 (`items_fts`, schemaV10) with `title:`, `author:`,
  `feed:`, `unread:` qualifiers (`search.go:73`). No saved searches.
- Tags: only collection name pills on feed rows. No item tags/labels.
- Unread counts exist per scope but are not surfaced on every scope.

### Sharing / export

Item share links (`shared_items`), list share (`lists.share_token`), favorites
share (`users.favorites_share_token`). OPML in/out, whole-instance
backup/restore, JSON API.

### Plugins

`pluginapi.Fetcher` (`Meta/Match/Discover/Fetch`), capabilities, host-mediated
HTTP, native plugins (YouTube, Instagram, Patreon, X) + external. `feeds.plugin_name`
(schemaV31), `ReconcileFeeds` auto-disable/re-enable. `FetchRequest.Config`
(`pluginapi.go:97`) is plumbed but **never populated or persisted** — a hook for
per-feed plugin config.

### Keyboard / interaction

`j/k o/Enter v s m g/G arrows`, `/` search, `Ctrl/⌘+P` palette, `?` help,
swipe gestures, deep-link item modal, load-more.

## Gaps

- No font size/family/density/reader-width preference; no serif variants.
- List/grid and authors-sort are per-browser, not per-account (`todo.md:6`).
- No global (all-feeds) rule UI; rules limited to hide/mark_read.
- No tags/labels; no saved searches.
- No unread-count badges across all scopes.
- No tracking-param stripping / referrer policy / media proxy.
- No full-text extraction or custom scraper rules.
- No reader API, no integrations/webhooks, no passkeys.
- Per-plugin/per-feed plugin config is unimplemented.

## Proposed work

### Tier 1 - Display personalization (cheap, high satisfaction)

Pure presets; no new concepts; low migration risk. This is the honest
"customizable within reason".

- Add `users` columns (schemaV33): `font_size`, `font_family`
  (`sans`/`serif`/`system`), `density`, `content_width`.
- Emit as CSS vars on `<html>` in `views_layout.templ` next to `--accent`, and
  add the vars to `app.css:1` with serif theme variants. Update `theme-color`
  and the PWA manifest to follow the theme bg.
- New settings card in `views_settings.templ`.
- Move list/grid and authors-sort from localStorage to per-scope DB storage so
  the choice follows the account across devices (`todo.md:6`). Candidate: a
  `user_view_prefs(user_id, scope, ref_id, mode, sort)` table.

### Tier 2 - Power-user organization (the Inoreader core loop)

- Global rules: expose existing `feed_id NULL` filters in a settings card, and
  extend `action` with `star`, `tag`, `notify`.
- Tags/labels: new `tags` + `item_tags`, tag control in the item view, a tag
  scope in `store.ItemFilter`, and `tag:` in the search grammar.
- Saved searches: persist the existing query grammar (`saved_searches` table +
  sidebar list/run endpoints); the parser already exists.
- Unread-count badges on feeds, collections, tags, and lists.

### Tier 3 - Content & integrations (Miniflux's stickiest features)

Each independent and larger; sequence separately.

- Privacy pass: strip `utm_*`/`fbclid` etc. on ingest, `rel="noopener
  noreferrer"`, `Referrer-Policy: no-referrer`, `youtube-nocookie` rewrite,
  optional media proxy.
- Full-text extraction: per-feed "fetch original article" toggle + readability
  pass with per-feed CSS-selector scraper rules; pairs with the plugin system.
- Notifications/webhooks: per-user webhook + ntfy/Apprise-style hooks on new
  matching items.
- Auth: passkeys (WebAuthn) and/or generic OIDC.
- Reader APIs (Fever, Google Reader): explicitly deferred.

## Additional feature candidates (beyond the tiers)

Ideas not already covered above, grouped by theme. "Hook" notes point at code
that already exists and lowers the cost. Effort is a rough S/M/L.

### Reading experience

- **Podcast / audio support (M).** Enclosures are already parsed and rendered
  as `<audio controls>` (`views_items.templ:397`, `store/item.go` enclosure
  kinds). Build on it: playback speed memory, resume position per item,
  a continuous play queue across an author's feed, and audio controls in the
  item modal. Podcasts are one of the largest remaining RSS use cases.
- **Reading time / word count (S).** Derive from item content at ingest or
  render time; show on the item row. Pairs with the "read time" idea Miniflux
  does for YouTube.
- **Scroll-position memory (M).** Remember how far into a long item you were
  and restore on reopen; drives a "continue reading" surface.
- **Infinite scroll as an option (S).** Current load-more is a button; some
  users expect auto-load. Make it a display pref (fits Tier 1).
- **Focus / distraction-free reader (S).** Hide nav and lists inside the item
  view; a display pref.
- **Print / PDF-friendly view (S).** A print stylesheet for the item.

### Feeds and sources

- **Keyword / search feeds (M).** Inoreader's monitoring feeds: save a query
  and poll it as a watchlist that surfaces new matches. Builds on the existing
  FTS5 grammar (`search.go:73`) and the poller; no external source needed for
  feeds already subscribed.
- **Newsletter-to-feed (L).** A per-user inbound address that turns received
  email into items, so newsletters land in the reader instead of the inbox.
  Needs a mail listener and per-user address minting; the app has no mail
  infrastructure today.
- **Web page change monitoring (L).** Non-RSS pages watched for textual or
  price changes (Inoreader "track changes"). `internal/discover` and the plugin
  framework already probe arbitrary pages; this adds a snapshot/diff store on
  top.
- **More social sources as plugins (M per network).** Inoreader and Feedly win
  on Mastodon, Bluesky, Reddit, and Telegram. Native plugins exist for YouTube,
  Instagram, Patreon, and X, so the framework is proven; each new network is a
  plugin rather than core work.
- **Bookmarklet plus the extension (S).** The extension only installs on
  Chromium browsers; a bookmarklet is a zero-install fallback for Firefox,
  Safari, and mobile.

### Browser extension and sharing

- **Context-menu add (S).** The extension requests only `activeTab`/`storage`
  (`extension/manifest.json:6`). Add a context menu and toolbar action so a
  right-click on any page or link adds it as a feed.
- ~~**Send current page item to nanoflux (M).** Inoreader's "save article":
  clip the page you are reading into a list, bypassing feeds entirely.~~ **Done**:
  the extension's save-page flow (`POST /api/ext/page-form`, `/api/ext/page-save`,
  `extension/popup.js`) stores a page as a hidden system-feed item
  (`schemaV34`), defaulting to a "watch later" list; it surfaces only in lists,
  favorites and search. Pairs with personal access tokens (still open).
- **Share-sheet parity on desktop (S).** The PWA already registers as a mobile
  share target; surface the same "send to" endpoint in the extension.

### Organization and triage

- **Personal access tokens for the JSON API (S).** The JSON API and browser
  extension currently authenticate with a session; add `api_tokens` so scripts
  and the extension stop storing a password. Easy, high-trust win.
- **Expose the existing dedup as a toggle (S).** `internal/httpapi/dedup.go`
  already collapses near-identical titles across feeds at view time; surface a
  setting and show the alternate sources more prominently.
- **Per-feed / per-scope display templates (M).** Beyond list/grid: choose
  compact vs card, thumbnail size, and how many lines of summary. Per-scope
  view prefs (Tier 1) is the storage this builds on.
- **Retention / cleanup per feed (M).** Keep at most N items or delete read
  items older than N days, to bound SQLite growth. Complements auto-read,
  which only flips the read flag and never deletes.
- **Mute by domain or author (S).** Ingest filters cover keyword/title matches;
  add a "mute this site" action from an item, stored as a filter on the feed's
  domain.
- **Bulk selection and batch actions (M).** Select rows, then mark read/unread,
  favorite, tag, or move to a list. Needed once tags exist.
- **Mark read on scroll (S).** Opt-in display pref; common in mobile readers.
- **Smart / virtual feeds (M).** Saved scopes like "all unread in this
  collection", "favorites", "recently added", rendered as sidebar entries. The
  `ItemFilter` struct already supports the needed scoping; no table required
  for the built-ins.
- **Send-to / save integrations (M).** One-click POST of an item to wallabag,
  linkding, Readwise, email, etc. Same shape as the webhook work in Tier 3.

### Fetching and content quality

- **Per-feed HTTP auth, headers, and cookies (M).** Private/paid feeds need
  basic/digest auth or a cookie. The fetch path already carries per-feed
  request state; this is where `FetchRequest.Config` (`pluginapi.go:97`) could
  finally be populated for plugins too.
- **Per-feed proxy and TLS options (M).** Proxy and "allow invalid cert", as
  Miniflux offers, for sites that block the server's IP or use self-signed
  certs.
- **Feed health panel (S/M).** Surface last error, consecutive failure count,
  and a "stale, no new items in N days" badge. `feeds.disabled_reason` and the
  429 badge already establish the pattern.
- **Index item bodies in full-text search (S/M).** `items_fts` currently covers
  title + summary (`schemaV10`); including content changes the FTS table and
  its triggers, but makes search far more useful.
- **URL import in OPML, and JSON Feed (S).** Miniflux takes a URL to an OPML;
  `gofeed` already handles JSON Feed, so mostly plumbing.

### Notification and automation

- **Export a list or favorites as its own RSS feed (S).** nanoflux already mints
  share tokens and serves public pages; add an RSS representation. Neat,
  self-referential, and useful for piping into other tools.
- **Digest via webhook (M).** A daily/weekly webhook payload of unread items,
  since the app has no mail infrastructure.
- **Per-rule notifications (M).** Wire the `notify` action from Tier 2 to ntfy,
  Apprise, or a generic webhook.

### Data portability and trust

- **Full JSON export of items and read/favorite state (M).** Today only OPML
  (subscriptions) and whole-instance backups exist; users can't leave with
  their state in a portable form.
- **Import from other readers' JSON (M).** Beyond OPML, accept Feedly/Inoreader
  exports so read state survives migration.
- **Audit log (M).** Record admin and account actions; useful for shared
  instances.

### Accessibility and internationalization

- **Message catalogues / i18n (L).** All UI strings are hardcoded English in
  the templ views. Extracting them is invasive but is the single biggest
  reach expansion (Miniflux ships 20 languages).
- **Customizable keyboard shortcuts (M).** Shortcuts are fixed in `app.js`;
  making them user-editable is a natural settings card.
- **RTL support, reduced-motion, and high-contrast presets (S/M each).** These
  fit the bounded-preset philosophy: extra theme axes, not freeform CSS.
- **Dyslexia-friendly / atkinson font option (S).** A font-family preset
  alongside serif/sans in Tier 1.

### Instance operations

- **Invite-only signup with codes (S/M).** Currently signup is a single global
  `allow_signup` boolean; invite codes are friendlier for private instances.
- **Per-user quotas (M).** Cap feeds or items per user on shared instances.
- **Health / metrics endpoint (S).** Expose poller and store status for
  Prometheus or an uptime check.
- **Instance default theme/accent for new users (S).** The `settings` key/value
  table already backs the one global setting; seed new users from it.
- **Offline PWA reading (L).** The app installs and registers a share target
  but has no service worker caching; offline read is a real project.

## Plugin ideas

The plugin system is a URL-shaped source adapter: `Match(url, cap)` decides what
it handles, and `Fetch` returns normalized items through host-mediated HTTP
(`pluginapi/pluginapi.go:145`). Native plugins are compiled in; external ones are
executables in `NF_PLUGINS_DIR` and may read their own env vars for API keys
(see `docs/writing-plugins.md`). Native coverage today: YouTube, Instagram,
Patreon, X.

Three shapes of idea are worth distinguishing:

1. **Source plugins** fit today's API directly.
2. **Enricher / extractor plugins** (full text, translation, transcripts) need a
   new capability kind, because the API has no post-fetch transform hook.
3. **Sink / notifier plugins** (save-to, webhooks) need the reverse hook, an
   outbound action on a new or matched item.

### Source plugins - easy (public API or feed, no key)

- **Bluesky.** Public AT Protocol endpoints serve profiles and feeds without
  auth; a strong replacement for the X plugin, which is fragile. High value.
- **Hacker News.** Official Firebase API and Algolia search API are open and
  keyless; covers front page, per-user submissions, and comment threads.
- **Lobsters, Lemmy, PeerTube.** All expose public JSON or ActivityPub/Atom;
  mostly parsing work.
- **AO3 (Archive of Our Own) and Royal Road.** Serialized fiction has no good
  reader feed and is a niche nanoflux is well suited to serving.
- **Public Telegram channels.** Read `t.me/s/<channel>` previews; no bot token
  needed for read-only.
- **Vimeo, SoundCloud, Bandcamp, Flickr, DeviantArt, ArtStation.** Public
  APIs or feeds; enrich beyond the generic parser.
- **Kick / Twitch / Rumble.** Streaming is a live-ish feed; higher effort due
  to API auth.

### Source plugins - need a key or auth (per-plugin env var, as `docs/writing-plugins.md` describes)

- **Reddit.** Already a recurring rate-limit story (see `AGENTS.md`); a plugin
  with OAuth could do better than anonymous `.rss`, which the host currently
  paces. Worth it given how common it is.
- **GitHub Releases / Issues / Commits.** Atom feeds exist, but an API-backed
  plugin can follow a whole org or a search, which the generic parser cannot.
- **Mastodon / Misskey.** `@user.rss` exists; an API plugin handles arbitrary
  instances and instance-wide trends.
- **Pinterest, TikTok, Threads, Instagram (auth'd).** Closed APIs; scraping is
  brittle and violates terms. Lower priority and a maintenance liability.

### Enricher / extractor plugins (needs a new capability)

- **Full-text extraction.** The Tier 3 readability work as a plugin, so the
  core stays lean and users choose per-feed extraction. Needs a post-fetch hook.
- **YouTube transcripts / video description expansion.** The native YouTube
  plugin exists; a transcript enricher adds real reading material.
- **Translation.** Machine-translate an item body on demand or at ingest;
  needs the same post-fetch transform point.
- **Paywall / archive resolver.** Try archive.today or a reader-mode URL when
  the original is paywalled.
- **Tracking-param and AMP unwrapping.** A transform plugin could do the
  Tier 3 privacy pass at item level.

### Sink / notifier plugins (needs an outbound hook)

- **Save-to: wallabag, linkding, Linkwarden, Readeck, Readwise.** One plugin
  interface, many destinations, matching Miniflux's 25+ integrations.
- **Notifiers: ntfy, Apprise, Discord, Slack, Telegram.** Overlaps the Tier 3
  webhook work; a plugin interface keeps each destination out of core.
- **Search / export sinks.** Push favorites to a static site or Obsidian vault.

### API changes these imply

- Add capability kinds beyond `CapDiscover`/`CapFetch` (`pluginapi.go:26`):
  a post-fetch transform and an outbound action, each with its own Match gate.
- `FetchRequest.Config` (`pluginapi.go:97`) is the natural place for per-feed
  plugin settings (subreddit, channel, extraction depth) once it is persisted.
- Consider a plugin-declared settings schema so each plugin renders its own
  config fields in the feed-edit form, instead of hardcoding them per plugin.

### Shortlist by leverage

If picking a few beyond the tiers: personal access tokens (unblocks scripting
and lets the extension drop passwords), podcast playback (large, underserved
audience on top of existing enclosure rendering), per-feed HTTP auth (unlocks
private feeds), full-text body search (cheap and broadly felt), and list/faves
as RSS (small, distinctive).

For plugins specifically: **Bluesky** and **Hacker News** (keyless, replace the
fragile X plugin), **AO3 / Royal Road** (the serialized-fiction niche nanoflux
already serves), a **Reddit OAuth plugin** (kills the recurring 429 story), and
the **extractor/sink capability kinds** (unlocks full-text and save-to without
growing core).

## Open questions

- Tier 1 first, or fold in Tier 2 global-rule UI since the store already
  supports it?
- Per-scope view-pref storage: dedicated table vs a single JSON column on
  `users` (home config already uses JSON).
- Density semantics: spacing-only, or also thumbnail/card size in grid mode?
- Should `--font-serif` live as a separate theme axis or as font-family presets?
