# API and reader-client expansion plan

Status: planned
Target: next release (all pieces ship together)
Scope: personal access tokens, Google Reader compatible API, Fever compatible API,
`/api/v1` REST CRUD, docs and conformance tests.

## Why

The app is a Miniflux alternative, but it has no third-party client API: the
README says so, and the roadmap already lists "Reader interoperability" and
"Personal access tokens" as the next two items. Google Reader compatibility is
the single largest gap against Miniflux because it unlocks Reeder, NetNewsWire,
Fiery Feeds, FeedMe, Unread and the wider client ecosystem. Fever covers the
remaining clients and is cheap once the reader service exists. A documented
REST v1 makes the app scriptable beyond the browser extension.

## Decisions

- Ship tokens, Google Reader, Fever and REST v1 in one release, in that order,
  as independently green commits.
- Google Reader labels and Fever groups map to **user-created collections**.
  Auto collections (`collection.is_auto = 1`, including the per-site host
  collections and `no-collection`) are not exposed, matching OPML export.
- Client authentication uses **personal access tokens only**. Account passwords
  are never accepted by the reader APIs. Fever clients that compute
  `md5(username:password)` are supported with a derived key (below).
- `/api/v1` is full CRUD: authors, feeds, collections, entries, lists, views,
  filters, tokens, counters, OPML, discover.
- JSON only. No Atom output in this release. Log `output` values so we learn if
  a target client insists on Atom.
- No Feedbin API and no Miniflux-shaped `/v1`. Their models do not map cleanly
  onto authors, collections and views.
- Keep the existing `/api/` routes as a deprecated shim for installed
  extensions. The in-repo extension moves to `/api/v1`.

## Architecture

```
internal/reader/            shared service, no transport
  streams.go                parse stream ids, xt/it, cursors, time bounds
  items.go                  single readerItem projection
  subscriptions.go          list/subscribe/edit/unsubscribe, labels
  state.go                  read/favorite/mark-all batches
internal/httpapi/
  readerapi.go              mount points, auth middleware, error shapes
  greader.go                Google Reader transport
  fever.go                  Fever transport
  apiv1.go, apiv1_*.go      REST v1 handlers
  apitokens.go              settings card + token endpoints
```

Rules:

- Every read goes through `visible_items` (or the store's authored-scope path),
  exactly like the web UI, so author allow/block rules apply to lists, counts,
  Fever id lists and mark-all.
- One `readerItem` DTO feeds Google Reader contents, Fever items and v1
  entries. New item fields must be added there, never patched into one surface.
- Transport handlers do not query the DB directly. They call `internal/reader`.
- No new per-site logic. Nothing in this plan is site-specific.

## Schema (schemaV67)

```sql
CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  scope        TEXT NOT NULL DEFAULT 'reader',
  token_hash   TEXT NOT NULL UNIQUE,
  fever_key    TEXT UNIQUE,
  created_at   TEXT NOT NULL,
  last_used_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_api_tokens_user ON api_tokens(user_id);
```

- Raw token: 32 lowercase hex characters (`crypto/rand`). Displayed once at
  creation and never again. Stored as `sha256(raw)`.
- `fever_key` is `md5(lower(username) + ":" + raw)`, computed at creation. It
  lets a Fever client that asks for email and password work by entering the
  username and the token as the password. The input has full token entropy, so
  this is not a meaningful weakening of the password hash. It is revocable with
  the token.
- Scopes start as `reader`; `api` is allowed for v1-only tokens. A token with
  scope `reader` works on all three surfaces. Do not add an admin scope.
- User deletion removes rows through the FK cascade. Confirm foreign keys are
  enabled in the delete path (the existing user delete already drops sessions
  and object keys).
- No other schema change. `feeds.last_polled_at`, `feeds.last_item_at`,
  `item_feeds`, `collection_feeds`, `visible_items` and
  `ItemStore.MarkAllReadFiltered` cover the protocol needs.

## Personal access tokens

- `store.APITokenStore`: `Create` (returns raw once), `List`, `Delete`,
  `ResolveReader(raw)` and `ResolveFever(key)`, `TouchLastUsed` (throttled to
  one write per minute per token, mirroring `auth.Authenticator.touch`).
- Settings gets an "apps and API" card, modelled on the existing extension
  card: server URL, create form (name), table of tokens (name, created, last
  used, revoke), and setup text for Google Reader and Fever. The created token
  is rendered once in the swap; never re-rendered.
- New CLI for headless/self-hosted setups:
  `nanoflux api token create <username> <name>`,
  `nanoflux api token list <username>`,
  `nanoflux api token revoke <username> <id>`.
- Limits: 20 tokens per user, name required, 100 characters max.
- Tests must prove the raw token is never stored or re-shown.

## Authentication

| Surface | Accepted credentials |
| --- | --- |
| `/reader/...` | PAT via `Authorization: GoogleLogin auth=<t>`, `Authorization: Bearer <t>`, HTTP Basic `username:token`, or the `T` form/query parameter |
| `/fever/` | PAT via `api_key`, or the derived `fever_key` |
| `/api/v1/...` | PAT or the existing session token (used by the in-repo extension) |
| `POST /accounts/ClientLogin` | `Email` = username, `Passwd` = PAT; returns the same PAT as `Auth` |

ClientLogin returns the PAT itself rather than minting a session, so revoking
the token takes effect immediately. Clients treat `Auth` as opaque.

Auth middleware rules:

- Reader prefixes never read the session cookie. This keeps the CSRF exemption
  safe: a cross-site form post cannot authenticate.
- Add `/accounts/`, `/reader/` and `/fever/` to `csrfExempt`, and keep
  rejecting cookie-only requests in the reader auth middleware.
- Failure shapes:
  - `/reader/api/0/*`: `401`, `X-Reader-Google-Bad-Token: true`,
    `text/plain`, body `Unauthorized`.
  - `/accounts/ClientLogin`: `401` JSON `{"error_message":"..."}`.
  - `/fever/`: HTTP 200 `{"api_version":3,"auth":0}`.
- `POST /accounts/ClientLogin` goes through `s.loginLimiter` keyed by client IP.
- Extend the `cors` helper to `/reader/`, `/fever/` and `/accounts/`, with
  `Authorization, Content-Type` allowed, methods `GET, POST, OPTIONS`, and no
  credentials.
- Add `/reader/`, `/fever/` and `/accounts/` to `navCountsPath`'s skip list.
- Request logging already prints `r.URL.Path` only, so the Fever `api_key`
  query value and the `T` parameter never reach logs. Keep it that way.

## Google Reader compatible API

### Endpoints

| Method and path | Behavior |
| --- | --- |
| `POST /accounts/ClientLogin` | plain `SID=...\nLSID=...\nAuth=...`, or JSON with `output=json` |
| `GET /reader/api/0/token` | returns the PAT as plain text |
| `GET /reader/api/0/user-info` | `userId`, `userName`, `userProfileId`, `userEmail` |
| `GET /reader/api/0/subscription/list` | `feed/<id>`, title, `url`, `htmlUrl`, `categories` |
| `POST /reader/api/0/subscription/quickadd` | discover and create, return `numResults`, `streamId`, `streamName` |
| `POST /reader/api/0/subscription/edit` | `ac=subscribe`, `unsubscribe`, `edit` |
| `GET /reader/api/0/tag/list` | starred state and collection labels, with unread counts |
| `GET /reader/api/0/unread-count` | reading-list total and per-feed counts |
| `GET /reader/api/0/stream/items/ids` | one page of ids |
| `POST /reader/api/0/stream/items/contents` | bodies for repeated `i` |
| `GET /reader/api/0/stream/contents/{stream...}` | one page of contents, also accept `s` query |
| `POST /reader/api/0/edit-tag` | add/remove read, kept-unread, starred |
| `POST /reader/api/0/mark-all-as-read` | `s` plus optional `ts` |
| `POST /reader/api/0/rename-tag` | rename a collection |
| `POST /reader/api/0/disable-tag` | delete a collection, keep its feeds |
| any other `/reader/api/0/` path | `[]` with 200, logged at debug |

### Streams

- `user/-/state/com.google/reading-list` and `user/<id>/...` equivalents.
- `feed/<numeric id>` for reads. `feed/<absolute url>` is accepted everywhere
  for leniency, and required by `ac=subscribe`.
- `user/-/state/com.google/starred` maps to favorites.
- `user/-/state/com.google/read` maps to read items.
- `user/-/label/<name>` maps to a user-created collection. URL-decode the name
  and resolve case-insensitively through `tagnorm.Label`.
- Unknown stream ids return an empty page, not an error.

### Parameters

- `n`: page size. Default 20 for contents, 100 for ids, cap 1000.
- `c`: continuation. Emit the cursor item id as a decimal string; the client
  echoes it. Direction follows `r` (`o` ascending, anything else descending).
  The store's keyset cursor is already `(COALESCE(published_at, fetched_at),
  id)`, so pages stay stable under equal timestamps and live inserts.
- `r`: `o` for oldest first, default newest first.
- `ot` and `nt`: time bounds in Unix seconds. Convert to the UTC storage format
  and apply as exclusive bounds (one-second adjustment) through `TimeGroup`.
- `xt`: exclude a target. `state/com.google/read` means unread only;
  `feed/<id>` maps to `ItemFilter.ExcludeFeedID`; labels map to
  `ExcludeTags`/feed groups as appropriate.
- `it`: include a target. Support starred, read and label (collection).
- `output`: accept but otherwise ignore, always JSON. Log unexpected values at
  debug.

### Item ids

Accept all common forms, resolve to the numeric row id:

- `tag:google.com,2005:reader/item/<16 hex>`
- `tag:google.com,2005:reader/item/<hex>`
- bare 16-character hex
- decimal id

Emit long form in contents responses and decimal strings in
`stream/items/ids`. Unknown ids are skipped, not an error.

### Item shape

`id`, `categories`, `title`, `published`, `updated`, `crawlTimeMsec`,
`timestampUsec`, `author`, `alternate`, `summary.content`, `content.content`,
`origin` (`streamId`, `title`, `htmlUrl`), `enclosure` and `canonical`.

- Body is `items.content` when enrichment set it, else `items.summary`. Both are
  already sanitized at the write boundary.
- `categories` carries read, starred and the item's collections as labels.
  Item tags are deliberately not emitted: they would collide with folder
  labels and clients would offer them as folders.
- `origin` is the owner feed (`feed/<id>`). A cross-feed item still appears in
  every feed stream it belongs to through `item_feeds`.
- `iconUrl` is omitted because `/icons/{domain}` is session-authenticated.
  Clients fall back to their own favicon fetch.

### Subscriptions and labels

- Category entries are collections the feed belongs to, with
  `id = user/<id>/label/<name>`, `label`, `type = folder`.
- `ac=subscribe`: canonicalize the URL, create when absent (idempotent when it
  exists), create an author named after the feed title when none is supplied,
  add the feed to the requested label (creating the collection when missing),
  assign the auto collection and wake the poller.
- `ac=edit`: `t` renames the feed, `a` adds label memberships, `r` removes them.
  Accept numeric and URL feed ids.
- `ac=unsubscribe`: delete the feed through the shared delete path (cached
  media purge and optional remote delete). A remote delete is never implicit
  here; the API has no opt-in flag for it.
- `subscription/quickadd` uses the same plugin-first discovery as
  `/api/discover`, and reports `numResults: 0` when nothing is found.
- `rename-tag` rejects auto collections and names that normalize to empty.
- `disable-tag` deletes the collection only; the feeds stay subscribed. The
  collection may be a saved-view member, so state the effect in the response
  docs.

### Write semantics

- `edit-tag` reads `a` and `r` from the form body only, matching common client
  behavior. Conflicting add and remove for the same state is a 400. Unknown
  state tags are ignored, `broadcast`/`like` are ignored.
- Batch state changes in one transaction (`SetReadMany`, `SetFavoriteMany`) so
  marking 500 ids is one write.
- `mark-all-as-read` maps `s` to a feed, collection or reading-list filter and
  `ts` to a `TimeGroup` upper bound. Accept seconds and 16+ digit microseconds.
  Omitted `ts` means "everything now". Return `OK`.
- Success responses for writes are plain text `OK`.

## Fever compatible API

Single endpoint `/fever/`, JSON only, accepting GET for reads and POST for
writes.

| Selector | Result |
| --- | --- |
| `groups` | collections plus a synthetic `All` group (id 0) |
| `feeds` | feeds plus `feeds_groups` |
| `favicons` | empty list; `favicon_id` is 0 |
| `unread_item_ids` | comma-separated visible unread ids |
| `saved_item_ids` | comma-separated favorite ids |
| `items` | `items` plus `total_items` |
| `mark=item` | `as=read|unread|saved|unsaved` |
| `mark=feed` | `as=read`, `id`, `before` |
| `mark=group` | `as=read`, `id`, `before`; id 0 means all |

Details:

- Every successful response includes `api_version: 3`, `auth: 1` and
  `last_refreshed_on_time` (newest feed poll, else now).
- Group ids are collection ids. The synthetic `All` group uses id 0 and lists
  every feed in `feeds_groups`; this keeps ungrouped feeds reachable in clients
  that navigate by group.
- `items` precedence: `since_id`, then `max_id`, then `with_ids`. Fixed limit
  50, ordered ascending for `since_id` and `with_ids`, descending for `max_id`.
- `is_saved` is the favorite flag, `is_read` is the read flag,
  `created_on_time` is `COALESCE(published_at, fetched_at)` as Unix seconds,
  `html` is the reader body.
- `mark=item` sets the flag absolutely, unlike Miniflux's documented
  `saved`/`unsaved` toggle bug. `mark=feed` and `mark=group` honor `before`
  through `MarkAllReadFiltered`; id 0 on `mark=group` ignores `before`.
- `total_items` is the visible item count for the user, excluding saved pages.
- Missing or invalid `api_key` returns HTTP 200 with `{"api_version":3,"auth":0}`.
- Do not write asynchronously in a goroutine. The mark is cheap (one UPDATE) and
  synchronous behavior is easier to test.

## REST v1

Base: `/api/v1`. Auth: PAT or session token. Errors:
`{"error":{"code":"...","message":"..."}}`, status matched to the cause. Paging
uses the store's keyset cursors: `before_id`, `after_id`, `limit`, plus
`has_more` and `next_before_id` in the response. Never expose raw store errors.

### Resources

| Resource | Routes |
| --- | --- |
| meta | `GET /me`, `GET /version`, `GET /counters` |
| authors | `GET /authors`, `POST /authors`, `GET/PATCH/DELETE /authors/{id}` |
| author filters | `GET/PUT /authors/{id}/filters` |
| feeds | `GET /feeds`, `POST /feeds`, `GET/PATCH/DELETE /feeds/{id}`, `POST /feeds/{id}/refresh`, `GET /feeds/{id}/entries`, `POST /feeds/{id}/mark-all-read` |
| collections | `GET /collections`, `POST /collections`, `GET/PATCH/DELETE /collections/{id}`, `GET /collections/{id}/feeds`, `GET /collections/{id}/entries`, `POST /collections/{id}/refresh`, `POST /collections/{id}/mark-all-read` |
| entries | `GET /entries`, `GET /entries/{id}`, `PATCH /entries/{id}`, `POST /entries/mark-all-read` |
| lists | `GET /lists`, `POST /lists`, `GET/PATCH/DELETE /lists/{id}`, `GET/POST /lists/{id}/items`, `DELETE /lists/{id}/items/{itemID}`, `POST /lists/{id}/share` |
| views | `GET /views`, `POST /views`, `GET/PATCH/DELETE /views/{id}` |
| tags | `GET /tags` |
| discover | `POST /discover` |
| OPML | `GET /opml`, `POST /opml` |
| tokens | `GET /tokens`, `POST /tokens`, `DELETE /tokens/{id}` |

### Notes

- `POST /collections/{id}/refresh` is the queue from the web collection page:
  mark eligible feeds due with `next_poll_at = now` and
  `next_poll_scheduled = 1`, skip paused and cooling feeds, wake the poller. Do
  not loop `PollOne` synchronously.
- `POST /feeds/{id}/refresh` mirrors the web button: respect `next_poll_at`
  unless the deadline is a schedule, and use a detached timeout context.
- `DELETE /feeds/{id}` accepts `?delete_remote=1` for the plugin opt-in. The
  default is local only.
- Entry filters mirror `ItemFilter`: `feed`, `author`, `collection`, `list`,
  `view`, `tags`, `exclude_tags`, `unread`, `read`, `favorite`, `bookmark`,
  `before_id`, `after_id`, `sort` (`newest`, `oldest`, `magic`), `limit`.
- `GET /counters` returns unread, read, favorite, bookmark totals plus optional
  per-feed and per-collection maps, all from the visible item scope.
- `GET /opml` reuses the export builder; `POST /opml` reuses the import path and
  reports created/skipped counts.
- Author delete must reject an author that still owns feeds (`409`), matching
  the constraint the web UI enforces.
- Views use the same filter row validation as saved views. An empty view is a
  `400`.

### Shared feed service

Extract the create/update/delete side effects from the web handlers into
service functions used by both web and v1:

- canonicalize URL through the plugin URL policy
- author resolution and auto-created author
- auto collection assignment
- plugin ownership name on create
- poller wake after create
- cache key purge after delete
- optional remote plugin delete
- default and floor poll interval composition

This is the one refactor the plan insists on. Without it, `/api/v1` becomes a
second implementation that quietly diverges from the web UI.

## Mapping summary

| nanoflux | Google Reader | Fever |
| --- | --- | --- |
| feed | `feed/<id>` | feed |
| user collection | label / folder | group |
| author | feed metadata only | not a group |
| favorite | `state/com.google/starred` | saved |
| read | `state/com.google/read` | `is_read` |
| item tag | omitted | omitted |
| bookmark | not exposed | not exposed |
| saved page | excluded from streams | excluded |
| author block rules | via `visible_items` | via `visible_items` |
| views | not exposed | not exposed |

## Commit series

Each commit must build, format and pass tests on its own.

1. `feat(api): personal access tokens`
   schemaV67, store, settings card, CLI, auth middleware, CORS/CSRF/nav-counts
   plumbing, token tests.
2. `feat(reader): shared reader service`
   stream parser, item projection, store queries (`ListIDs`, `ByIDs`,
   `EnclosuresByItemIDs`, batch setters, `Feeds.ByURL`, bulk collection
   membership), unit tests.
3. `feat(reader): Google Reader compatible API`
   endpoints, auth responses, catch-all, conformance tests.
4. `feat(reader): Fever compatible API`
   endpoints, groups, id lists, mark operations, tests.
5. `refactor(feeds): shared feed service`
   web handlers and API call one create/update/delete path.
6. `feat(api): /api/v1 authors, feeds, collections, entries`
   CRUD, paging, counters, state changes, tests.
7. `feat(api): /api/v1 lists, views, filters, tokens, OPML, discover`
   complete CRUD, tests.
8. `docs(api): reader compatibility, v1 reference, README and roadmap`
   extension moves to v1, `/api/` marked deprecated, AGENTS invariants.

## Test plan

Auth matrix:

- missing token, malformed token, revoked token, expired session on v1
- session cookie alone must fail on `/reader/` and `/fever/`
- Basic with correct and wrong username
- `GoogleLogin auth=`, `Bearer`, `T` in form and query
- Fever `api_key` and derived `fever_key`
- ClientLogin rate limiting

Scoping and rules:

- blocked author invisible in lists, counts, Fever ids and GReader streams
- cross-feed item appears in both feed streams and once in reading-list
- saved pages excluded from streams, present in favorites and bookmarks
- collection membership respects views where the web list does

Google Reader:

- subscription list shape, auto collections excluded, categories correct
- tag list counts match `CountUnreadFiltered`
- all four item id forms resolve
- item contents body uses content when present, summary otherwise
- continuation has no gaps or duplicates with identical timestamps
- continuation across ascending and descending directions
- `ot`/`nt` bounds inclusive/exclusive behavior, seconds and microseconds
- `xt=read`, `xt=feed/<id>`, `it=starred`, `it=label`
- edit-tag batches and conflicts, unknown tags ignored
- mark-all-as-read with and without `ts`
- subscribe/subscribe-again/edit/rename/unsubscribe
- quickadd success and `numResults: 0`
- rename-tag and disable-tag effects
- 401 header and body shapes, catch-all `[]` 200

Fever:

- auth failure shape, base fields on every response
- groups include `All` (id 0), feeds_groups covers every feed
- item pagination precedence and limit
- unread and saved id lists
- absolute saved/unsaved
- mark item/feed/group with `before`, group id 0 marks all
- total_items matches the visible count

REST v1:

- tenant isolation on every resource
- CRUD round trips and validation errors
- keyset paging and `has_more`
- feed create side effects (author, auto collection, poll wake)
- feed delete purges cached media; remote delete only with the flag
- collection refresh enqueues instead of polling synchronously
- mark-all filters and counters stay consistent with the list
- token create/list/revoke, raw value shown once
- OPML export/import round trip
- author delete with feeds is rejected

Plus `gofmt`, `go vet`, `go test -race ./...` and `mise run gen` for the
settings card (templ output is committed).

## Documentation

- `docs/api.md`: v1 auth, token setup, resource and error reference, paging,
  examples.
- `docs/reader-api.md`: supported Google Reader subset, Fever subset, deviations
  (JSON only, no icons, labels are collections, item tags omitted, bookmarks
  not exposed, catch-all behavior, id formats, continuation semantics).
- README: replace the "No third-party client API" limitation with the supported
  clients and the token setup, and keep the "not a drop-in replacement" honesty
  about webhooks, Apprise, ntfy and translations.
- `docs/roadmap.md`: mark reader interoperability and personal access tokens as
  shipped, and leave search and ingest rules as the next items.
- `AGENTS.md`: add an "API surface" section with the invariants:
  - public reads select from `visible_items` or the authored-scope path
  - reader prefixes never accept the session cookie
  - raw tokens are never logged, stored or re-displayed
  - one `readerItem` projection for all three surfaces
  - compatibility parsers live in `internal/reader`, not in handlers
  - the GReader catch-all `[]` 200 is deliberate
  - new item fields must be added to the projection before any surface exposes
    them.

## Non-goals

- Atom output
- Feedbin API
- Miniflux-shaped `/v1`
- search streams and real item search (roadmap item 3; return a valid empty
  response so clients do not error)
- item tags or bookmarks as GReader labels
- exposing views, auto collections or authors as folders/groups
- webhooks or push notifications
- admin-scoped tokens
- a public API docs route in the app

## Risks and mitigations

- **Second implementation drift.** Shared feed service plus `internal/reader`
  keep web and API on one path. Test both after the refactor.
- **Rule bypass.** Every read and write predicate goes through the store's
  visible/authored-scope path. Add a blocked-author test per surface.
- **CSRF through compat routes.** Never accept the cookie there. Test that a
  cookie-only POST fails.
- **Token leakage.** Query strings carry the Fever `api_key`; apps and proxies
  may log them. Document HTTPS and keep app logging path-only. Never re-show a
  token.
- **Pagination under identical timestamps.** Keep the `(time, id)` keyset and
  test equal timestamps explicitly.
- **Large surface in one release.** The commit series keeps every step green
  and reviewable; do not merge a commit with failing tests to "finish" the
  release.
