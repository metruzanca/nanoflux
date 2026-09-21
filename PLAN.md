# nanoflux — Implementation Plan

Dead-simple self-hosted RSS app. Go backend + htmx frontend + (later) a Chrome
extension. SQLite, session auth, background poller, feed discovery.

## Checklist

- [x] **P0 — Scaffolding**
  - [x] Add dependencies (modernc sqlite, gofeed, bcrypt)
  - [x] `internal/config` — env config (`NF_ADDR`, `NF_DB`, `NF_POLL_INTERVAL`, `NF_ADMIN_USER/PASS`)
  - [x] `internal/db` — open sqlite (WAL, FK on), embedded migration runner
  - [x] `cmd/server/main.go` — boots config, db, serves `/healthz`
- [x] **P1 — Store layer** (shared by server + future CLI)
  - [x] Repositories: users, sessions, authors, feeds, items, collections
  - [x] All queries scoped by `user_id`
  - [x] Tests against in-memory sqlite
- [x] **P2 — Auth**
  - [x] `POST /login`, `POST /logout`, `GET /login` page
  - [x] bcrypt password hashing
  - [x] Sessions in DB, httpOnly cookie (also usable as Bearer token header)
  - [x] Auth middleware (all routes except login/healthz)
  - [x] Bootstrap first user from env if `users` empty
- [x] **P3 — Poller + parsing**
  - [x] `internal/feedparse` — normalize gofeed output -> items
  - [x] Background poller: ticker, per-feed interval, conditional GET (etag/last-modified), worker pool, timeout
  - [x] `POST /feeds/{id}/refresh` manual refresh
  - [x] Upsert items by `(feed_id, guid)`, new items `read=false`
- [x] **P3b — Feed discovery** (`internal/discover`)
  - [x] `Discover(ctx, url) []Candidate` — strategies: direct parse, HTML link scan, well-known paths, host-specific (bluesky, youtube, reddit, github)
  - [x] Candidates validated by fetching + parsing server-side
  - [x] Fixture tests
- [x] **P4 — htmx web UI**
  - [x] `GET /` unread item list, mark read / mark-all-read
  - [x] Feeds: list, add (author select-or-create), edit, delete, refresh
  - [x] Authors: list, create/edit/delete, shows their feeds
  - [x] Collections: list/create, add/remove feeds
  - [x] htmx fragments per entity
- [x] **P5 — JSON API** (for extension)
  - [x] `POST /api/login` (token for header auth)
  - [x] `GET /api/unread-count`, `GET /api/items`
  - [x] `POST /api/items/{id}/read`
  - [x] `POST /api/discover`, `POST /api/save`
  - [x] CORS preflight handling for extension origin
- [x] **P6 — Admin** (`nanoflux user` CLI + `/admin` page)
  - [x] `is_admin` flag (schemaV16); bootstrap account is admin
  - [x] Cobra CLI on `internal/store`: `user list`, `create`, `set-admin`, `reset-password`, `delete`, `feed list`
  - [x] `/admin` web page (stats, signup toggle + dismissible banner, list, reset password, toggle admin, delete)
  - [x] Signup control: DB-backed `allow_signup` setting (schemaV17), login/signup gating
  - [x] Backup/restore: `nanoflux backup`/`restore` + `make restore`, shared `data/`+`filestore/` layout
  - [x] Guards: last admin protected, no self-delete, session revocation on reset
- [ ] **Deferred (explicitly later)**
  - [ ] Chrome extension (MV3) — popup, badge, save-to-collection
  - [ ] MCP server wrapping `internal/store`

## Decisions locked in

- **Ingestion**: background poller goroutine + per-feed manual refresh.
- **Auth**: session-based. httpOnly cookie for web UI; `/api/login` also returns
  the session token so the extension can send `Authorization: Bearer <token>`
  (same session table — no separate token type). Cross-site cookies need HTTPS,
  so the extension uses the header form.
- **Collections**: groups of feeds (folders).
- **Save page** (extension) = **feed discovery**: find the page's RSS/Atom feed
  and save it (e.g. `bsk.app/profile/metru.dev` -> that profile's RSS). Basic
  case is scanning HTML for `<link rel="alternate" type="application/rss+xml">`;
  special cases handled by host-specific strategies.
- **Data model**: per-user isolation — each user has their own authors, feeds,
  collections, and read state. `read` is a column on `items` (safe because feeds
  are per-user).

## Tech choices

- SQLite via `modernc.org/sqlite` (pure Go, no cgo -> clean container builds).
  Integer PKs, WAL mode, foreign keys on.
- Parsing: `github.com/mmcdole/gofeed` (RSS 2.0 / Atom / JSON-feed).
- Passwords: `golang.org/x/crypto/bcrypt`.
- HTTP: stdlib `net/http` with Go 1.22+ method routing. No framework.
- Templates: templ (`github.com/a-h/templ`), generated to committed `_templ.go`
  files; htmx served as a vendored static file.
- Store access: sqlc-generated queries (`internal/store/sqlcgen`), wrapped by
  typed `*Store` repositories.
- Blob storage: minio-go S3 when `NF_S3_ENDPOINT` is set; local disk
  (`filestore.NewDisk`) otherwise.
- Session tokens from `crypto/rand`.

## Repo layout

```
cmd/server/main.go          # entrypoint: config, db, poller, http server
cmd/cli/                    # (later) cobra admin CLI
internal/config/            # env config
internal/db/                # open sqlite (WAL), migrate (embedded schema)
internal/store/             # repositories shared by server + CLI
internal/auth/              # bcrypt, sessions, middleware
internal/poller/            # background fetch loop + manual refresh
internal/feedparse/         # wraps gofeed; normalizes feeds -> items
internal/discover/          # feed discovery (strategies)
internal/httpapi/           # route registration, handlers, templ views
internal/web/               # templ helpers/components + static assets
extension/                  # (later) MV3
```

## Schema

```sql
users(id, username UNIQUE, password_hash, created_at)
sessions(id, token UNIQUE, user_id, created_at, expires_at)
authors(id, user_id, name, url, avatar_url, description, created_at)
feeds(id, user_id, author_id, title, feed_url, home_url, description,
      etag, last_modified, last_polled_at, poll_interval_sec, enabled)
items(id, feed_id, guid, title, link, summary, image_url, published_at,
      fetched_at, read, UNIQUE(feed_id, guid))
collections(id, user_id, name, created_at)
collection_feeds(collection_id, feed_id, PRIMARY KEY(collection_id, feed_id))
```

No `kind='bookmark'` in v1 — a save is always a real discovered feed. If
discovery fails, the UI says so; the user can paste a feed URL manually.

## Feed discovery strategies (in order)

1. **Direct**: parse the URL itself as a feed.
2. **HTML link scan**: `<link rel="alternate" type="rss/atom/feed+json">`,
   resolve relative hrefs.
3. **Well-known paths**: `/feed`, `/feed.xml`, `/rss`, `/rss.xml`, `/atom.xml`,
   `/index.xml`, `/feeds/posts/default`, `/?feed=rss`.
4. **Host-specific**: `bsk.app`/`bsky.app` profiles (`/profile/{h}/rss`),
   YouTube channel_id, Reddit `{url}.rss`, GitHub `releases.atom`.

Candidates are only returned after being fetched + parsed server-side.

## Verification

- `go test ./...` (store/parser/discover/handlers via `httptest`)
- `go vet`, `gofmt`
- Manual smoke: bootstrap user, run server, add a feed, confirm items land.