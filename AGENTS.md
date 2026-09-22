# AGENTS.md

Project-specific guidance for coding agents working in this repository.

## Conventional commits

Commit messages must follow the Conventional Commits spec (`<type>: <description>`,
imperative, lowercase, under ~70 chars). The project's older commits predate this
convention and are not a template.

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
- **No self-links on scoped pages.** An author page is already "on" that
  author, so it must not link back to itself: `FeedRow` never renders the
  author name (feed rows only ever appear under their own author page), and
  author-scoped item rows suppress the author link. The suppression is a
  `HideAuthor` flag threaded through `scopedItemsData` →
  `ItemsListPage`/`ItemsList`/`ItemRow`/`itemRowInner`; read/favorite toggle
  handlers get it back via `hx-vals='{"hideAuthor":"1"}'` on the row's buttons
  so a swapped row stays consistent. The item modal meta keeps its author link
  (it's an overlay cross-link).
- **Authors list.** `/authors` rows are a single condensed line — the linked
  name + unread count (accent, left of the feed count) + feed count (no external
  URL). The sort picker orders by abc, newest, or most unread (client-side, see
  the htmx section). The "add feed" dialog is
  the global add flow; its `feedPreviewFields` (and the scrape builder) only
  render the new-author fields when no existing author is selected, and
  `authorFormFragment` returns an **empty 200** (not 204 — htmx doesn't swap on
  204) so picking an existing author clears `#new-author`.
- This applies to the webapp UI only (`internal/web/templates`); the
  `website/` Hugo marketing site is out of scope.

## Relative timestamps

Timestamps render server-side, relative to the user's configured timezone.
`timeFmt` in `templates.go` takes the user's IANA timezone name (empty =
server local time) and renders "Today at 3:04pm", "Yesterday at 3:04pm",
"N days/weeks/months ago", or an absolute fallback. The timezone is a per-user
setting (`users.timezone`, set on `/settings`). Every template call passes the
timezone through the view data — `ItemWithFeed.Timezone`, `feedRow.Timezone`,
`itemViewData.Timezone`, and `settingsIconRow.Timezone` are stamped by the
handlers via `withTZ`. Do not render a timestamp with a raw format call; always
go through `timeFmt` so it respects the user's timezone.

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

1. **Preview endpoints** (`/fragments/feed-preview` — the combined add form
   for an author + their first feed):
   the form's `hx-target` *is* the preview container, so errors replace it via a
   normal swap. Return `400` + the `form_error` fragment using `renderError(w, msg)`.
   Do NOT use OOB here — an out-of-band div targeting the same element as the
   normal target is fragile.

2. **Mutation forms** (`POST /feeds`, `POST /authors/{id}/feeds`, `/authors`,
   `/collections`): the target is the list; the error slot is a separate
   `#add-…-error` div inside the open dialog. Return `400` + an OOB swap into
   that slot using `writeFormError(w, target, msg)`.

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

## GitHub feeds

GitHub exposes per-user and per-repo Atom feeds, wired up as host-specific
discovery rules in `internal/discover/host.go` (`hostSpecificURLs`):

- `github.com/USERNAME` maps to `https://github.com/USERNAME.atom`, whose
  candidate title is overridden to "USERNAME's Github activity".
- `github.com/OWNER/REPO` offers three candidates to pick from:
  `releases.atom`, `commits.atom`, `tags.atom`. Candidates that fail to fetch
  (e.g. a repo with no releases) are dropped by the usual `tryFeed` filter, so
  the picker only lists feeds that exist.
- Host-specific rules run **before** the page-HTML scan in `Discover` (step 2),
  so the fixed GitHub titles aren't preempted by any `<link rel="alternate">`
  the page advertises. Direct feed URLs still win (step 1).

## YouTube channel feeds

`discover.PageMeta` special-cases YouTube channel pages: it uses the channel's
`og:image` (`yt3.googleusercontent.com`) as the icon instead of the hashed build
favicon, and `pageTitle` strips the " - YouTube" suffix YouTube appends to
`<title>`, so the author-name prefill reads "Eddy Burback" rather than
"Eddy Burback - YouTube". Both consumers (`renderFeedPreviewForm`,
`authorPrefill`, the extension's `saveFeed`) get the clean name/avatar.

The **embed** player in the item modal (`itemContent`, the `EmbedURL` iframe)
carries `referrerpolicy="origin"`. The app sets a global
`Referrer-Policy: no-referrer` header, which would otherwise strip the Referer
the YouTube player requires to authorize playback (error 153); sending the
embedding origin satisfies it. Keep that per-element override in place.

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

## Instagram profile feeds

Instagram has no public feed/API. `internal/feedparse/instagram.go` scrapes the
profile page (`instagram.com/<handle>`) and extracts the recent-post grid
embedded in the web client's Relay payload (`polaris_timeline_connection`).
`fetchInstagramProfile` runs as a short-circuit inside `feedparse.Fetch` when
the URL is an Instagram profile, so discovery, the poller, and preview all work
unchanged (the profile URL is the stored `feed_url`, like X).

- **Crawler user-agent is required.** Instagram serves a login wall with no
  posts to browser user-agents; only crawler identities get the post grid.
  `instagramUserAgent` (a Googlebot-compatible string) is a package var. This
  is the strongest caveat in the codebase: Instagram's `robots.txt` prohibits
  automated collection, so the scrape is unofficial and can break without
  notice. On failure `fetchInstagramProfile` returns an error and the feed
  fails gracefully.
- Each node gives `pk`, `caption.text` (may be null), the cover thumbnail,
  `media_type`, and `product_type` (`clips` = reel) — but **not** the shortcode
  or `taken_at`. Item GUIDs are `instagram:<pk>`.
- **Shortcode is derived from `pk`** (base64-url-safe alphabet,
  `instagramShortcode`), giving `https://www.instagram.com/reel/<code>/` for
  clips and `/p/<code>/` otherwise.
- **Publish time is derived from `pk`** (`(pk >> 23) + instagramEpoch` ms,
  `instagramMediaTime`); it can be up to ~a minute before the real `taken_at`,
  but the date is exact.
- Thumbnails are stored as the **stable media endpoint**
  `https://www.instagram.com/p/<code>/media/?size=l` because the cdn URLs are
  signed and expire; items are never updated after insert (`UpsertItem` does
  `DO NOTHING`), so a signed URL would rot.
- The Relay payload node is extracted by string-aware brace matching (captions
  contain braces) and deduped by `pk`. Only the first grid page (~12 posts) is
  available; older posts require login, so there is no "load older items"
  backfill.
- `instagramProfileHosts` is a package var so tests can inject a mock host.

## Scraped-site feeds (CSS selectors)

Some sites have no feed at all. When discovery finds none, the add-feed preview
offers "build a feed by scraping this page": the user picks CSS selectors (with
a best-effort auto-detect) and sees a live sample before saving. The saved feed
is a normal row — `feeds.kind='scrape'` (schemaV24) — and the poller extracts
items from the page on every poll, just like the X/YouTube scrapers.

- **Recognition is stored, not URL-shaped.** `feeds.kind` (`store.ScrapeKind`)
  tells the poller to call `feedparse.Scrape` instead of `feedparse.Fetch`
  (`PollOne` in `internal/poller/poller.go`). Scrape feeds are created/updated
  via `FeedStore.CreateScrape`/`UpdateScrape`; the six selectors live in
  `feeds.scrape_config` as JSON (`ScrapeConfig{Item,Title,Link,Summary,Date,Image}`).
- **The engine is `internal/feedparse/scrape.go`** using `github.com/andybalholm/cascadia`
  (CSS selector matching over `x/net/html`, no goquery). Item link/title fall
  back to the first in-item `<a href>` when their selectors are blank; dates
  prefer the `datetime` attribute, then text, parsed from a small layout set
  (approximate, like X); relative URLs resolve against the page base and links
  run through `StripTracking`. Item GUIDs are `scrape:<sha1(link+title)>` for
  stable dedup. `AutoDetect` tries common containers (`article`, `.post`,
  `.entry`, ...) and returns the first that yields ≥2 linked items — best
  effort, user-editable.
- **Web flow:** `feedPreview` renders `noFeedFound` (a `role="alert"` banner
  with an opt-in button) instead of a bare "no feed found" error; the button
  posts to `POST /fragments/scrape-builder`, which renders the builder
  (`scrapeBuilder` in `views_feeds.templ`) with the live sample. Selector
  changes re-preview via `POST /fragments/scrape-preview`. Save posts the normal
  `/feeds` form with `kind=scrape`; errors use the standard
  `writeFormError`/`renderError` shapes. The builder and the edit page
  (`feedFields` renders the selector fields when `kind='scrape'`) share the
  `scrape_*` form field names — keep them in sync.
- **Web-only:** the JSON API (`/api/save`) still rejects non-feeds; scrape feeds
  are created from the web UI only. Do not route a scrape feed through
  `feedparse.Fetch`.

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

## Feed pagination (load older items)

Some feeds expose their older history in pages — via a standard `rel="next"`
link (`<link rel="next" href="...">` / `<atom:link rel="next" ...>`) or a
`?page=N` / `?paged=N` query-parameter convention. nanoflux never auto-backfills
that history (it can be extremely long); instead the feed's page offers a
"load older items" button that fetches the next page(s) on demand.

- Detection lives in `internal/feedparse` (`nextPageFromBody` /
  `nextPageByParam`): a feed-level `rel="next"` link wins (resolved against the
  fetched URL), otherwise the fetched URL's own `page`/`paged` param is
  incremented. `Result.NextPageURL` rides along on every `Feed` result, so
  discovery, preview, the poller, and the API all stay unchanged.
- The cursor is stored per feed in `feeds.next_page_url` (schemaV20). The
  poller records it only on a feed's **first** poll (a newly added feed);
  routine polls never touch it, so they can't clobber a user's in-progress walk
  by resetting it to page two. A newly added feed also gets that first poll
  immediately: `Server.pollFeedNow` (`internal/httpapi/web.go`) fires a
  detached `Poller.PollOne` from the add flows (`createFeed`, `apiSave`) so
  items appear without waiting for the next tick — it's a no-op when no poller
  is attached (tests) or the feed is disabled. "Load older items"
  (`POST /feeds/{id}/older` → `Poller.PollOlder`) walks up to
  `maxBackfillPages` (5) pages per click and stops, clearing the cursor, when a
  page has no next link, yields zero new items (dedup hit — the terminator for
  `?page=N` feeds), or loops back to a visited URL. A click that hits the cap
  keeps the cursor for another click. Errors leave the cursor untouched so the
  click can be retried.
- The feed page renders the control via `feedOlderControl` (only when the
  cursor is set); the endpoint swaps `#feed-older` (button → "full history
  loaded") and OOB-swaps `#scoped-items` so the imported items appear.

## Settings and custom source icons

`/settings` lets users set a profile-picture URL and add custom per-domain brand
icons that override the built-in X/YouTube/globe set.

- Source icons render as `<img src="/icons/{hostname}">` (see the `SourceIcon`
  component in `internal/web/icons.templ`); the auth-required
  `GET /icons/{domain}` handler serves the user's **cached** custom icon bytes,
  else a built-in SVG. This is what makes per-user icons work without threading
  the user into every fragment.
- Custom icons are stored in the `source_icons` table (domain unique per user,
  `icon_data` BLOB holds the cached bytes). Added/refreshed by fetching the
  user's `icon_url` server-side (`fetchAndCacheIcon`, capped at 1MB, must be
  `image/*`); failures keep the row with a "not cached" note and a refresh button.
- Domain matching is an exact lowercase hostname match (no subdomain
  wildcards). `normalizeDomain` accepts bare hostnames or URLs.
- The avatar is a **file upload**: bytes go to object storage (`avatars/<userID>`)
  and are served at the auth-required `GET /avatar` (`Cache-Control: private,
  no-cache`); `User.HasAvatar` (from `avatar_key IS NOT NULL`) decides whether
  the topbar shows the photo or the default initial-letter avatar (`Initial` in
  `internal/web/templates.go`). The topbar avatar is a button that opens a dropdown menu
  (settings, log out) — see `topbar` in `views_layout.templ` and `app.css`. The
  avatar form re-renders itself (error inside the
  swapped card), unlike the create-form OOB pattern.

## URL mappings

`/settings` also lets users define "url pattern -> feed url" rules (`url_mappings`
table, schemaV19) that pre-fill the add-feed form when an entered URL matches.
They are only consulted at add time — `feedPreview` (`POST /fragments/feed-preview`)
and `apiDiscover` (`POST /api/discover`) — never retroactively, so editing a
mapping never changes feeds that were already created (they store their resolved
`feed_url`).

- The engine is `internal/urlmap` (pure, unit-tested): a pattern is a literal
  url with **`{name}` placeholders** (`abc.com/{user}`), each matching one
  non-slash segment, and the template references captures via `{name}`
  (`{user}.abc.com/feed`). `Compile` builds a `(?i)^...$` regex from it — no
  regex syntax in the pattern (dots, `*`, etc. match literally), matching is
  against the scheme-stripped `host/path` (trailing slash trimmed),
  case-insensitive, whole-string only (no substring matches), and captures
  keep their original case. A pattern must contain ≥1 placeholder and the
  template may only reference names the pattern defines.
- `mappedFeedURL` in `internal/httpapi/mappings.go` lists the user's mappings
  (oldest first) and applies the first match. Mappings that no longer compile
  are skipped with a server-side log, never fatal.
- **Auto-filled urls strip a leading `www.` subdomain** (`stripWWW` in
  `internal/httpapi/web.go`): the find-author preview form's feed/home fields,
  the scrape builder, and the new-author url prefill are all cleaned so
  "https://www.example.com" shows up as "https://example.com". Manual edits on
  save are left alone.
- **Fallback:** when a mapped URL yields no feed (direct fetch or discovery), the
  original URL is discovered instead, so a stale mapping never blocks adding a
  feed. The entered URL becomes the feed's `home_url` whenever a mapping applied.
- Settings UI (`views_mappings.templ`) mirrors the icon card: add form with a
  **test** field that posts pattern/template/test-url to `POST /fragments/mapping-test`
  (pure transform, preview-target swap), and rows with an inline edit form
  (`GET /fragments/mapping-edit/{id}` / `POST /settings/mappings/{id}`, cancel via
  `GET /fragments/mapping-row/{id}`). Every error path uses the standard
  `writeFormError`/`renderError` shapes with `role="alert"`.

## Object storage (S3 / local disk)

Avatars and custom-icon bytes live in S3-compatible object storage, not the DB.
The DB stores object **keys** (`users.avatar_key`, `source_icons.icon_key`).
`internal/filestore` exposes the `Store` interface (`Put`/`Get`/`Delete`/
`EnsureBucket`); tests use `filestore.NewMemory()`.

- Config is `NF_S3_*` env vars (`NF_S3_ENDPOINT`, `NF_S3_BUCKET`, `NF_S3_ACCESS_KEY`,
  `NF_S3_SECRET_KEY`, `NF_S3_REGION`). When `NF_S3_ENDPOINT` is unset, the store is the
  local disk directory `NF_FILE_STORE` (default `<db dir>/filestore`) via
  `filestore.NewDisk` (`internal/filestore/disk.go`); keys map to files under
  that root, and the content type is written to a sibling `.ct` file. Set
  `NF_S3_ENDPOINT` (use an `https://` prefix for TLS) to switch to S3 via minio-go.
- A configured-but-unreachable endpoint fails fast at startup (see
  `newFileStore` in `cmd/server/main.go`); the message points at `NF_S3_ENDPOINT`.
- `Store.MigrateLegacyFiles` moves pre-object-storage DB blobs to objects once,
  at startup; the legacy `avatar_data`/`icon_data` columns are left in place but
  cleared.
- Object keys: `avatars/<userID>`, `icons/<userID>/<domain>`,
  `author-avatars/<userID>/<authorID>`. All keys are deterministic — a refetch
  overwrites the same object in place and never creates an orphaned file.
- Author avatars are cached here from `authors.avatar_url` (schemaV21). The
  avatar renders from `GET /authors/{id}/avatar` once cached, else falls back
  to the `/img` proxy; the author edit page has a "refetch avatar" button
  (`POST /authors/{id}/avatar-refresh`). Changing/clearing `avatar_url` on
  update and deleting an author both purge the cached object (best-effort). On
  the author edit page the avatar url input sits with the avatar image/heading
  inside the edit form (`authorAvatarFields`); the refetch button swaps only
  the inner `#author-avatar-card` (image + status), so it never clobbers an
  unsaved avatar url value.

## Collections and feed editing

- The collections index renders each collection as a `.card` with feed/unread/
  read counts, from `CollectionStore.ListWithCounts` (one query per user). The
  delete button lives on the **collection edit page**, not the index row.
- A collection's page has a "+ add feed" **button → modal** (not an inline form)
  and an **edit** link. `GET /collections/{id}/edit` edits the name
  (`CollectionStore.Rename`) and holds the delete form plus the feed list (with
  remove); `collectionDelete` redirects `303 → /collections`. Auto collections
  are read-only (no edit/delete, add-feed rejected).
- Feed rows on the author page only offer **edit** and **refresh**; pausing is
  the feed edit page's "enabled (poll this feed)" checkbox, and **delete** lives
  on the feed edit page (`feedDelete` redirects `303` back to the author page).
  The `/toggle` route remains but has no UI.
- Adding a feed is **blocked when the user already has a feed with that exact
  `feed_url`** (`Server.feedURLExists`, normalized like the extension's `saved`
  check) — a warning renders into `#add-feed-error`. Duplicate titles/home URLs
  are allowed; only the feed URL is the identity.

## Author links

An author can carry **plain external links** (a Twitch/Discord page, a
homepage) that are bookmarks, not subscriptions. They live in `author_links`
(schemaV26) and are never polled and hold no items — keeping "feeds" ==
"polled subscriptions" so OPML export, collections, unread counts, and feed
editing stay clean.

- Links are edited **inside the author edit form**, not on the author page:
  `authorLinkFields` renders one row per existing link (`link_id` hidden +
  `link_label` + `link_url`) plus an add-row `<template>` cloned by
  `addAuthorLinkRow` in app.js. `authorUpdate` reconciles them via
  `reconcileAuthorLinks`: a blank/invalid url, or an existing link whose row was
  removed (its ✕), is deleted; a row with no id creates; the rest update in
  place. The `links_present` hidden marker guards the reconcile, so a POST
  without the link fields (an API client) leaves links untouched.
- The author page renders the links **inline in the author card, under the home
  url** (`authorPageLinks`), with no "links" heading or dialog. The label is
  optional; `web.LinkLabel` falls back to the URL's hostname (minus `www.`) when
  it's blank. Links are external links and carry `class="external"` (see the
  links rule above).
- `AuthorLinkStore` (`Create`/`Update`/`Delete`/`ListByAuthor`) and the
  `author_links` table cascade on author/user delete; there is no object-storage
  to purge. The JSON API and OPML ignore links.

## Environment variables

All application configuration flows through environment variables with a single
`NF_` prefix (the app's codename, nanoflux). This is a hard convention — do not
introduce a variable with any other prefix.

- **Naming:** `NF_<GROUP>_<NAME>` (e.g. `NF_POLL_INTERVAL`, `NF_S3_BUCKET`).
  Server settings are read in `internal/config/config.go`; object-storage
  settings in `internal/filestore/filestore.go` (`ConfigFromEnv`). The first
  account's bootstrap credentials are `NF_ADMIN_USER` / `NF_ADMIN_PASS` — never
  rename them back to a `RSS_*`/bootstrap spelling.
- **Defaults and types:** `config.Load` uses `getenv`/`durationEnv`/`intEnv`
  helpers; a new option must follow the existing pattern (empty string means
  "unset", fall back to the default) and get a sensible default so the app runs
  with zero configuration.
- **Documenting:** every env var is listed in three places: the README
  configuration table(s), the committed `.env.example`, and this file when the
  setting is load-bearing. Adding a variable means touching all of them.
- **Backward compatibility:** renames are intentional breaking changes — legacy
  spellings (e.g. the old `RSS_*` / `S3_*` names) are read by nobody and
  silently ignored. Do not add fallback reads for them.
- Config is only ever read from env at startup; there is no config file, and
  `config.Load` is pure (no I/O) so tests can call it with `t.Setenv`.

## Store layer (sqlc)

`internal/store` data access is generated by sqlc. `sqlc.yaml` at the repo root
drives it: the canonical schema is `internal/store/schema.sql` (keep it in sync
with the migrations in `internal/db/migrate.go`), queries live in
`internal/store/queries/*.sql`, and code is generated into the `sqlcgen`
package (`internal/store/sqlcgen`, `//go:generate sqlc generate`). The public
`*Store` types wrap the generated `Queries` and convert `sql.Null*` to plain
domain types; never hand-write `row.Scan` calls or `nullStr`/`boolInt`
boilerplate — add a query to the `.sql` files and regenerate. The generated
with the migrations in `internal/db/migrate.go`), queries live in
`internal/store/queries/*.sql`, and code is generated into the `sqlcgen`
package (`internal/store/sqlcgen`, `//go:generate sqlc generate`). The public
`*Store` types wrap the generated `Queries` and convert `sql.Null*` to plain
domain types; never hand-write `row.Scan` calls or `nullStr`/`boolInt`
boilerplate — add a query to the `.sql` files and regenerate. The generated
files are committed, so CI/builds need no sqlc step.

**Full-text search is the one exception.** `ItemStore.SearchPage` runs a
hand-written FTS5 query (`MATCH` against the `items_fts` virtual table) because
sqlc cannot introspect FTS5 virtual tables — it mirrors the `ListItems` column
set and reuses the generated `sqlcgen.ListItemsRow` scanner. In `schema.sql`,
`items_fts` is declared as a plain table matching the virtual table's columns
so sqlc stays happy; the real DB builds it as an external-content FTS5 table in
migration `schemaV10`. Do not try to fold the search query back into sqlc, and
keep the plain-table declaration in sync with the virtual table's columns.

**Bulk range-read.** `MarkItemsBeforeRead`/`MarkItemsAfterRead` mark every
unread item in the **same feed** as the target that is newer (before) or older
(after) than it — the list order is `COALESCE(published_at, fetched_at), id`
DESC. They are deliberately feed-scoped (not whole-list), so the `⋯` menu's
"mark all before/after as read" is unambiguous on any page.

**Sort direction.** Item lists are newest-first by default; a `?dir=asc` param
flips them oldest-first (`ItemFilter.Ascending`). `ListPage` picks `ListItems` vs
`ListItemsAsc` (`ListStore.ItemList` mirrors this with `ListItemsInListAsc`), and
the keyset cursor flips with it: `before=<id>` (desc) vs `after=<id>` (asc).
`moreURL`/`pageCursor`/`scopedFilter`/`cursorID` in `internal/httpapi/web.go`
thread the direction; search and the public shared pages stay newest-first.

## Web UI (templ)

The web UI is built with templ, not `html/template`. Page and fragment
components live in `internal/httpapi/views_*.templ` (co-located with the
handler view structs) and are generated to `_templ.go` files next to them; the
`internal/web` package holds the pure helpers (`StripHTML`, `TimeFmt`,
`YoutubeEmbedURL`, `IsImagePost`/`IsLinkPost`/`IsGallery`/`GalleryThumb`,
`Initial`, `PageTitle`) plus the `SourceIcon`/`FavIcon` components and static
serving. `web.Render(w, r, component)` executes a component; handlers call
their page component directly (e.g. `basePage("unread", u, homePage(...))`).

The app is an installable PWA: `internal/web/static/manifest.webmanifest` and
`sw.js` are served at the root (`/manifest.webmanifest`, `/sw.js` — the latter
so the worker's scope is `/`) by `serveEmbedded` in `internal/httpapi/logo.go`
(which reads them via `web.ReadStatic`). The `head` template links the manifest
and the iOS/`theme-color` metas, and `app.js` registers `/sw.js` in secure
contexts. The worker is deliberately non-caching (the reader is auth-gated and
live), so it only exists to satisfy installability. PWA icons (`pwa-192.png`,
`pwa-512.png`, `apple-touch-icon.png`) are committed PNGs regenerated by
`tools/iconsgen` (`make icons`). Installation needs HTTPS — the compose
instance listens on plain HTTP, so it must sit behind a TLS proxy.

The manifest also registers a **share target** (`GET /add?url=…`), so sharing a
link to the installed app on Android opens nanoflux's add-feed flow. `shareAdd`
(`internal/httpapi/share.go`) extracts the URL (`url`, else the first URL in
`text`) and renders `shareAddPage`, which triggers the normal
`/fragments/feed-preview` flow into `#share-preview`. The discovery step is
shared between `feedPreview` and the share flow via `discoverCandidates`
(`internal/httpapi/preview.go`). The **global** add flow (no fixed author — the
`/authors` dialog and the share page) makes the saved feed return `HX-Redirect`
to its author page (`feedCreate`), while the author-scoped feed flow appends the
new feed row in place (`renderFeedPreviewForm` sets `Redirect` from
`fixedAuthor == nil`). Share targets are Chromium/Android-only (iOS Safari
ignores them).

## Browser extension distribution

The MV3 extension lives in `extension/`. `extension/embed.go` (package
`extension`) embeds its files so the server can serve them as a zip at the
auth-required `GET /settings/extension.zip` (`settings_extension.go`), giving
every user a store-free install path; the `/settings` "browser extension" card
shows the download plus load-unpacked steps. `make extension` writes the same
zip to `dist/` via `tools/extzip`. The `*_templ`/`extension.go` JSON API it
talks to is documented under the API surface (`/api/discover` now returns a
`saved` flag + `saved_feed_id` and the user's `accent`, and `/api/ext/feed-form`
+ `/api/ext/save` render the add form/save as htmx fragments). `saveFeed`'s
auto-created author gets its avatar (and home URL) from the page the user was on
(`pageIconURL` → `discover.PageMeta` → site favicon, or the channel's og:image
on YouTube), then caches it like the web flow. Auth that must survive a login
round trip (e.g. a shared `/add` link opened logged-out) uses a safe relative
`next` param: `auth.Require` adds `?next=…` to its `/login` redirect and `login`
returns there.

- **templ syntax quirk (v0.3.1020):** use bare `if`/`for`/`switch` statements
  (not `@if`/`@for` — those generate broken Go). A `{ expr }` block must NOT
  immediately follow a `@Component(...)` call; separate them with a newline.
- The layout (`head`/`topbar`/`basePage`, item dialog) is `views_layout.templ`.
  All inline JS lives in `static/app.js` (modal, user menu, keyboard nav, htmx
  error-swap override, dialog reset) — templ cannot parse `{}` inside raw
  `<script>` blocks, so no inline scripts.
- htmx fragment rules from the error-handling section still apply; error
  banners render via the `FormError`/`FormErrorOOB` components (`role="alert"`).
- Generated `_templ.go` files are committed; regenerate with `templ generate`
  (wired into `mise dev` and `mise gen`).

## Admin features

Admins manage users through two equivalent surfaces: the `/admin` web page
(linked from the user menu when `users.is_admin`) and the `nanoflux user` CLI
run inside the container. Both share the store methods (`UserStore.ResetPassword`,
`SetAdmin`, `Delete`, `CountAdmins`, `ListObjectKeys`).

- The admin flag is `users.is_admin` (schemaV16). The bootstrap account is
  always marked admin at creation; older installs get one via
  `nanoflux user set-admin <username> true` — there is no automatic promotion.
- `adminOnly` in `internal/httpapi/admin.go` gates every `/admin` route: GET
  requests get a rendered 403 page, htmx POSTs a `form_error` OOB fragment — no
  bare statuses. Never bypass it by calling the handlers directly.
- The last admin can never be deleted or demoted (`CountAdmins <= 1` guard),
  and the web UI refuses to delete your own account.
- Deleting a user purges their object-storage blobs (`avatars/<id>`,
  `icons/<id>/<domain>`, via `UserStore.ListObjectKeys`) best-effort, then
  deletes the row (FK cascade removes sessions/feeds/items/...). A flaky store
  must not block deletion — failures are logged warnings. `reset-password`
  also revokes every session (`SessionStore.DeleteUserSessions`).
- The CLI lives in `internal/cli` (cobra) and is dispatched from
  `cmd/server/main.go`: `nanoflux` with no args (or `server`) runs the server;
  any other first argument routes to the CLI. It opens the same SQLite file via
  `config.Load`/`db.Open`/`db.Migrate`, so it works against a running instance.
  `user delete` prompts on a TTY and requires `--yes` otherwise; `--password`
  skips the prompt on `reset-password`/`create`.
- Signup is a DB-backed global setting (`settings.allow_signup`, default on),
  read via `SettingStore.AllowSignup` and toggled from `/admin` (`POST
  /admin/settings/signup`). When off, `/signup` renders a disabled notice and
  the login page hides its signup link. The admin banner urging admins to
  consider disabling signup is dismissed per-admin-user
  (`users.signup_banner_dismissed`, `POST /admin/settings/signup-banner-dismiss`).
- `/admin` also shows global stats and `feed list` is a cross-user, metadata-only
  view (owner, title, state, last polled) — it must never render item summaries
  or content (NSFW). Dashboard counts come from `CountAll*`/`CountAllUnread*`
  queries.
- Backups use a `data/` + `filestore/` archive layout shared by `make backup`
  and `nanoflux backup` (the DB is snapshotted via `VACUUM INTO`; local-disk
  blobs are tarballed; S3 blobs are the provider's job). `nanoflux restore` and
  `make restore` (which stops compose first) restore the same layout. Do not
  change the layout without changing both.

## Auth and session hardening

- **Login throttling** is `loginLimiter` in `internal/httpapi/loginlimiter.go`,
  keyed by client IP (5 failures / 15 min → 429 for the rest of the window).
  It guards both `POST /login` and `POST /api/login`; a successful login clears
  the count. `clientIP` uses `RemoteAddr` — it deliberately does not trust
  `X-Forwarded-For`, so behind a proxy the limit is per-proxy-IP.
- **Secure cookies** are auto-detected, not configured: `auth.SecureRequest`
  returns true when `r.TLS` is set or `X-Forwarded-Proto`/`X-Forwarded-Ssl`
  indicate https. `SetCookie`/`ClearCookie` take the request and set `Secure`
  accordingly. Plain HTTP (LAN/dev) must keep working — never force Secure via
  an env var.
- **Change password** (`POST /settings/password`) verifies the current password
  and calls `DeleteUserSessionsExcept(userID, currentToken)` so only the active
  session survives. **Sessions** are listed/managed at `/settings`
  (`ListUserSessions`, `POST /settings/sessions/{token}/revoke`); revoking the
  current session logs the user out.

## Image enclosures

Some feeds deliver an item's image only as an `<enclosure>` with no
`media:thumbnail`; those items used to render as a bare external link.

- `EnclosureKind` (`internal/web/templates.go`) returns `"image"` for MIME
  `image/*` or common image extensions, matched on the URL's parsed path so
  signed URLs (`photo.jpg?e=…&t=…`) still hit. `itemContent` renders image
  enclosures inline (single → `.image-lightbox`, several → `.gallery`) and
  drops them from the bare-link list; `ImageEnclosures` collects them. The
  inline render is skipped when the item is already showing an image (`v.Image`
  lightbox, a reddit gallery) or its body/description embeds an `<img>`
  (`web.BodyHasImage`) — feeds that populate the description with the image
  must not render the same enclosure twice.
- At poll time `normalizeItem` falls back to the first image enclosure's URL
  as `image_url` when the feed supplied none, so image-enclosure items get a
  thumbnail in lists (and the masonry grid). They stay text posts —
  `IsImagePost` still requires an img-only summary.
- schemaV23 backfills `items.image_url` from existing rows' first `image/*`
  enclosure for items that predate the fallback.

## Feed poll failures

`feeds.last_error` (schemaV18) records the last poll failure; `SetPollMeta`
takes the error text (empty = success, truncated at 200 chars by the poller).
The error is surfaced only to the feed's owner: a "last poll failed" badge on
the feed row, the full (truncated) text on the feed page, and `feed list` marks
the state `error`. Never render it cross-user (NSFW).

## Adaptive polling and stale feeds

Each feed's `poll_interval_sec` can be derived from its posting cadence
(`feeds.poll_interval_auto`, schemaV25). When auto is on, `PollOne` recomputes
the interval on a poll that ingested new items as the average gap between the
feed's most recent posts (`adaptiveInterval` in `internal/poller/interval.go`),
clamped to `[adaptiveFloor=900, adaptiveCeil=86400]` seconds. New feeds default
to auto; the migration kept feeds that had a custom interval on manual
(`poll_interval_auto = 0 WHERE poll_interval_sec <> 900`). The feed edit form's
checkbox turns auto on/off; with auto on the posted interval is ignored.

`feeds.last_item_at` records the feed's newest item time (published, falling
back to fetched) and only ever moves forward — it is distinct from
`last_polled_at`. On every successful poll the poller refreshes it. A feed
whose newest item is at least `staleAfter` (7 days) old is backed off to a
1-day poll interval regardless of the auto toggle, so a quiet feed isn't polled
often. This is not an error: the UI shows an amber `badge warn` / `.notice`
("no new posts in N days", escalating to "may be abandoned" at
`abandonedAfter` = 30 days) via `web.StaleFeedFor` — see `staleBadge` in
`views_feeds.templ`. The poller ticker (`NF_POLL_INTERVAL`, default 15 min)
still bounds the effective minimum cadence.

## htmx and client-side JS

The web UI uses **htmx v2.0.4** (vendored at `internal/web/static/htmx.min.js`, loaded
deferred in `views_layout.templ`). When debugging swap/mutation bugs, remember:

- **Defaults are the foot-gun:** an `hx-*` element with no `hx-target` targets
  *itself*, and the default swap is `innerHTML`. A button that `hx-post`s a
  fragment without setting both will inject the response into its own innerHTML.
  Buttons that only trigger server work and then reload the page (e.g. the
  feed-detail refresh button in `views_feeds.templ`) must set `hx-swap="none"`
  so the fragment is discarded; the feeds-list row version instead targets
  `#feed-{id}` with `hx-swap="outerHTML"`.
- **Use `hx-on::after-request` (double colon)**, not the single-colon spelling.
  In htmx 2.0.4 `hx-on:` maps the name to a raw DOM event, so
  `hx-on:after-request` listens for a DOM event named `after-request` that never
  fires and the handler silently never runs. `hx-on::after-request` maps to
  `htmx:after-request`. `event.detail.successful` is `false` on `4xx`/`5xx` (see
  the error-handling section), so `if (event.detail.successful) ...` handlers
  that close dialogs or reload leave things in place on error — by design.
- The global `htmx:beforeSwap` override (below) makes `>= 400` bodies swap;
  `hx-swap="none"` still suppresses them, so an error response to a
  no-swap/reload button is invisible to the user — log it server-side.
- `htmx:afterRequest`/`htmx:afterSwap` fire for all requests; app.js uses the
  former to re-apply theme/accent after settings swaps and the latter to
  re-highlight the active row after a row `outerHTML` swap and to re-apply the
  display mode.

**Global JS** all lives in `internal/web/static/app.js` (no inline scripts —
templ cannot parse `{}` in raw `<script>` blocks). It installs:

- `htmx:beforeSwap` override: `shouldSwap = true` for any `status >= 400` so
  error fragments render. Load-bearing — do not remove or narrow.
- Theme + accent: resolves `data-theme="system"` against
  `prefers-color-scheme`, and re-applies theme/accent via `htmx:afterRequest`
  after `/settings/theme` and `/settings/accent` swaps. `applyTheme` /
  `applyAccent` are globals.
- Picker control: `PickerControl` (`views_items.templ`) is the shared pill +
  dropdown (`togglePicker`/`closePickers`/`setPickerState` in app.js) behind
  three instances. **Client-side** pickers have no `Href`; a delegated click maps
  the picker's `data-picker` + the option's `data-option` to a setter. The
  **display mode** (list/grid, `localStorage["nanoflux.items.mode"]`) toggles the
  `masonry` class on `#items-list`; the **authors sort** (abc/newest/unread,
  `localStorage["nanoflux.authors.sort"]`) reorders `#authors-list` rows by their
  `data-name`/`data-created`/`data-unread`. Both re-run on every `htmx:afterSwap` because tab
  switches, "mark all read", load-more, and author adds recreate the list. The
  server renders the default state — do not try to read localStorage server-side.
  **Server-side** pickers set `Href` (the sort direction) and are plain links.
  Grid mode is a two-column CSS masonry (`ul.items.masonry`, one column below
  720px); it reuses the same item rows, with thumbs going full-width.
- Item modal: `openItem(el)` fetches `/items/{id}/view` into
  `#item-dialog-body` and `markRowRead(id)` flips the row to read. `currentItemId`
  tracks the open item. The fragment renders the share + ⋯ controls in a
  `#item-dialog-controls-src` wrapper inside the (scrollable) body; `openItem`
  relocates that wrapper into the header slot `#item-dialog-controls`, next to
  "open live" and ✕, so those controls stay pinned while the body scrolls.
- Item "⋯" menu: the per-item `itemMenu` (`views_items.templ`) sits at the right
  of every card's `.row-actions` (list and masonry grid) and in the item modal.
  Menus are scoped by `data-item-id` (`toggleItemMenu`/`itemMenuAction`/`closeItemMenus`
  in app.js), so many cards can each have one. Opening a menu adds `menu-open` to
  the row `<li>` so its dropdown escapes the swipe container's `overflow:hidden`.
  "add to list" fetches `GET /items/{id}/lists` and injects `itemListsDialogInner`
  into the single shared `<dialog id="item-lists-dialog">` (in `views_layout.templ`);
  `itemListsUpdate` re-renders that inner content with an `innerHTML` swap.
  "mark all before/after as read" POSTs `/items/{id}/read-before|after` and reloads
  on success.
- User dropdown: `toggleUserMenu` + outside-click and Escape handlers.
- Keyboard: ArrowLeft/Right move through the item list while the modal is open;
  `j`/`k` (or arrows) move an `.active-row` cursor, `o`/Enter open, `v` opens
  the original, `s` favorite, `m` read toggle, `g`/`G` first/last, `?` shows the
  shortcut sheet; `/` focuses the search box. `htmx:afterSwap` re-adds
  `.active-row` after a row is swapped.
- Swipe actions (touch only): swiping an item row right toggles favorite, left
  toggles read, by clicking the row's `.fav-btn`/`.read-btn` so the htmx swap
  is reused. Implemented with touch events (`touchmove` is `passive: false` and
  `preventDefault()`ed once the gesture is horizontal so Android never cancels
  it as a scroll); `touch-action: pan-y` on `ul.items li` keeps vertical
  scrolling native. A `data-suppress` marker swallows the leftover synthetic
  click so a swipe can't open the modal. NOTE: static JS/CSS are `//go:embed`-ed,
  so `mise dev` serves stale app.js/app.css until the server rebuilds/restarts.
- Dialog cleanup: on `close`, every dialog's form inputs are cleared and
  `[id$="-preview"]` containers emptied.

## Lists and favorites

Favorites is the **special list**: it is still backed by the `items.favorite`
column (the row's `fav-btn`, the `s` shortcut, swipe actions, and `/favorites`
all work unchanged). User-created lists of items live in `lists`/`list_items`
(schemaV22) and are an extension of favorites.

- **Add-to-list picker:** the item modal has a `⋯` button (`itemMenu` in
  `views_items.templ`, JS in `app.js`) whose "add to list" entry opens
  `#item-lists-dialog`. The picker lists **favorites** first (its checkbox
  mirrors `items.favorite`) plus every user list. `POST /items/{id}/lists`
  reconciles `list_items` (and favorites) and re-renders the dialog, so
  unchecking a list in the same dialog is how an item is removed from it.
- **Index/detail:** `/lists` pins favorites first (`FavoritesListRow`) then the
  user's lists; `/lists/{id}` lists its items newest-added first (or oldest via
  `?dir=asc`), keyed on `list_items.created_at`, paged with the standard
  `before=`/`after=` cursor (`ListStore.ItemList`). `ListStore.AddItem`/`RemoveItem`
  verify both list and item belong to the user (like collection feed membership).
  Delete lives on `GET /lists/{id}/edit` (rename via `ListStore.Rename` + the
  delete form); the index rows and the list page have no delete button.
- **Sharing:** every list has a public link — `lists.share_token` for
  user-created lists, `users.favorites_share_token` (unique index) for the
  favorites list. `SetShare`/`ShareFavorites` reuse an existing token, so
  sharing is idempotent. The public pages `/l/{token}` (lists) and `/f/{token}`
  (favorites) are unauthenticated, render via `sharedListPage`, and follow the
  same rule as `sharedItemPage`: links go only to external content with
  `class="external"`, never to internal author/feed pages. `ListStore` and
  `UserStore` resolve tokens by `ByToken`/`ByFavoritesShareToken`.
- **Modal htmx binding (load-bearing):** `openItem` in `app.js` injects the
  item modal via plain `fetch` + `innerHTML`, so htmx never binds elements
  inside it on its own. Every htmx control rendered inside the modal — the
  item's share button and the list-picker form — depends on the
  `htmx.process` call after the innerHTML assignment (it processes the whole
  `#item-dialog`, so relocated controls are covered). Do not remove it;
  a newly added modal control that "does nothing" is usually missing this.

## Running the dev server

`mise dev` runs the server and auto-watches `.go` and `.templ` files,
regenerating templates and restarting on change (`templ generate` runs before
each start). If a port is already taken, it's likely a `mise dev` instance is
still running — no need to kill it, just edit the source files and it will
rebuild and rerun. `mise gen` regenerates templ + sqlc on demand.
