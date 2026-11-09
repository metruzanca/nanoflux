# Pre-launch plan

Status: in progress. Phase 1 (docs), phase 2 (demo removal), and phase 3
(security hardening) have landed; phase 4 (resilience and stability) is next.
Last updated 2026-11-09. This is the working plan for changes that should land
before nanoflux is presented publicly. It is not a commitment to a date.

Goal: remove the demo surface, close the highest-value security and resilience
gaps, and fix the performance issues that would otherwise dominate the first
public thread. Keep every change reviewable: one concern per commit.

Principles:

- Security, resilience, stability, maintenance, and performance are in scope.
- Cosmetic refactors that create large diffs without a functional payoff are
  out of scope while the project is moving fast.
- The target deployment is a private network (LAN, NAS, Raspberry Pi, Tailscale),
  not a public multi-tenant service. That shapes the threat model.
- Every change should be shippable on its own, with tests or a verification
  step.

## Planned changes

### 1. README and project documents (done)

| Change | Why |
| --- | --- |
| Rewrite `README.md` around the author-centric model | The old README sold a feature list and invited a Miniflux comparison on Miniflux's terms. The new one states what nanoflux is and is not, explains authors and views, and frames it as a UI for social feeds. |
| Fix the `make start` port message in `Makefile` (`8080` to `20310`) | Compose maps `20310:8080`; the current message sends a fresh user to the wrong port. |
| Document `git clone --recursive` and `make plugins` | The default site plugins are git submodules. A plain clone leaves them empty, and `make plugins` has nothing to build. |
| Add `SECURITY.md` | The app accepts untrusted feed input, fetches user URLs, and loads executable plugins. There needs to be a disclosure path and a stated threat model. |
| Add `THIRD_PARTY_NOTICES` and set `sbom: true` in `.goreleaser.yaml` | `htmx` and `hls.js` are vendored without license notices, and release images currently ship no SBOM. This is cheap compliance and supply-chain hygiene. |
| Remove the external plugin link from the README | It conflicts with the repository rule about not naming external plugins in docs. The generic plugin section stays. |

### 2. Remove demo mode (done)

The demo feature is roughly 2,000 lines: `internal/demo/`, `store.CloneUser`,
`httpapi` demo handlers, config, env vars, and tests. Removing it also removes
the only caller of `Sessions.DeleteExpired`, so the session purge moves into the
maintenance sweeper in the same change.

| Change | Why |
| --- | --- |
| Delete `internal/demo/` and its tests | The feature is being removed; the package has no other consumer. |
| Delete `store.CloneUser` and its tests | `CloneUser` exists only to provision demo accounts. It is 1,041 lines and has no other caller. |
| Delete `internal/httpapi/demo.go`, move `root`/`landing`/`landingData` into `internal/httpapi/landing.go` | Keep the public landing page at `/` for anonymous visitors, but drop the demo CTA, throttle, provisioning, and countdown. |
| Remove `/demo` from routes and CSRF exemptions | No route, no exemption. |
| Remove demo config and `NF_DEMO_*` from `.env.example` | Dead configuration is worse than no configuration. |
| Remove demo session behavior from `internal/auth` | `SetDemoMode`, `CreateSessionTTL`, `SetCookieTTL`, and the ephemeral-expiry branch in `User` exist only for demo sessions. |
| Remove `demoFeedLimitReached` guards from feed-create paths | The checks touch `web.go`, `api.go`, `opml.go`, `extension.go`, and `pluginprovision.go`; each becomes simpler. |
| Add schemaV64: delete only users where `is_ephemeral = 1 AND is_admin = 0`, then drop `users.is_ephemeral` and `users.expires_at` | Leaves no dead schema behind. The `is_admin = 0` guard protects against a mis-marked live account. Demo clones are never admins by construction (`CloneUser` inserts `is_admin = 0`), so the guard should never exclude a real demo row. |
| Add a pre-flight check in `db.Migrate` before schemaV64: `SELECT COUNT(*) FROM users WHERE is_ephemeral = 1 AND is_admin = 1`; abort with a clear error if it is non-zero | SQL alone cannot refuse to run. This is the "do not delete live users" safety net: if the invariant is ever violated, the server stops before the destructive statement instead of deleting an admin. |
| Add `TestMigrateV64KeepsLiveUsers`: seed a live user, a demo user, and (separately) a demo-marked admin; assert the live user survives, the demo user is gone, and the admin case aborts | The migration is destructive. This is the regression proof that it removes only demo accounts. |
| Remove `CountPersistent`, `EphemeralStatus`, `ExpiredEphemeral`, and their queries | Replace `CountPersistent` with `Count` in `CreateFirstAdmin` and `allowSignup`, then delete the demo-only helpers and regenerate sqlc. |
| Remove `FeedStore.CountForUser` | It exists only to enforce the demo feed cap. |
| Add `Sessions.DeleteExpired()` to `maintenance.Sweeper` | Replaces the demo manager's purge. Without it, expired sessions accumulate forever on a normal instance. |
| Update `AGENTS.md` and `docs/customization.plan.md` | Both describe demo mode as a current feature. Leaving them stale would mislead the next agent or contributor. |

### 3. Security hardening (done; CSP tightening deferred)

| Change | Why |
| --- | --- |
| Cap feed response bodies in `feedparse.Fetch` (32 MiB) | `io.ReadAll(resp.Body)` is unbounded. Any feed host can stream until the process runs out of memory. |
| Cap plugin `Host.Do` response bodies (64 MiB) | Plugins are trusted, but a buggy plugin should not be able to exhaust the host's memory with one response. |
| Add `http.MaxBytesReader` to JSON handlers (1 MiB) and a global POST cap (10 MiB) | `readJSON` decodes an unbounded body, and `/api/login` is unauthenticated. This is a trivial memory DoS today. |
| Set `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, and `MaxHeaderBytes` on `http.Server` | Only `ReadHeaderTimeout` is set; slow clients can pin connections. |
| Apply CSRF to cookie-authenticated `/api/` requests; exempt only `Authorization: Bearer` and `/api/login` | The API accepts the session cookie, so the blanket `/api/` exemption relies on `SameSite=Lax` alone. `app.js` already sends the token on non-GET fetches, so the web UI keeps working. |
| Scope `settingsSessionsRevoke` to the caller's user id | The handler deletes any session by hash. The hash is secret, but this is a missing ownership check. |
| Compare a dummy bcrypt hash on login when the username does not exist | Removes the timing oracle that reveals valid usernames. |
| Add `NF_TRUST_PROXY` (default off) governing both client IP and secure-cookie detection. When on, take the last `X-Forwarded-For` entry and trust `X-Forwarded-Proto`/`X-Forwarded-Ssl`. When off, use `RemoteAddr` and `r.TLS` only. Document it in `.env.example` and the README reverse-proxy note. | Behind a reverse proxy the login limiter currently shares one bucket, so one attacker can lock out every user. Today `SecureRequest` trusts `X-Forwarded-Proto` but `clientIP` does not trust `X-Forwarded-For`; one setting should govern both. Gating both is the safe default and removes header spoofing from direct clients. A proxy deployment must set it or cookies lose `Secure`; call that out in the upgrade notes. |
| Scrub the plugin environment (`os.Environ()` to a small allowlist) | Plugins inherit S3 and backup secrets today. They are trusted, but they do not need the server's credentials. |

### 4. Resilience and stability

| Change | Why |
| --- | --- |
| Add a panic-recovery middleware that logs the stack and renders the 500 page | A handler panic currently drops the connection with a blank response. |
| Recover panics around `pollHost` and `PollOne` | A panic in a feed parser or plugin decode runs in a background goroutine and kills the process. |
| Respawn a crashed plugin on the next call | `managedExternal` only clears a dead client when the idle reaper kills it. If a plugin process crashes, every call fails until restart. |
| Make `/healthz` ping the database with a short timeout | Today it returns 200 even when SQLite is unavailable. |
| Route the first poll after feed creation through the poller's host pacing | `createFeed` fires `go PollOne` per add, bypassing the per-host spacing the poller exists to enforce. Adding several feeds to one host bursts it. |
| Bound `Registry.playCache` (and cap `minCache`) | `playCache` is keyed by item link and never evicted, so it grows for the life of the process. |
| Add `PRAGMA optimize` to the sweeper | Keeps planner statistics fresh without a manual step. |

### 5. Performance

| Change | Why |
| --- | --- |
| Batch the `/authors` unread counts | `authorRows` calls `CountFiltered` once per author that owns a saved view, and `ListAuthorsWithFeedCount` uses `COUNT(DISTINCT)` over a joined `visible_items`. This is the reported slowdown. Replace it with one grouped count plus one `UNION ALL` for view-bearing authors. |
| Skip re-sanitizing an unchanged item summary on poll | `UpsertMany` runs `sanitize.HTML` on every item on every poll, even when the summary did not change. |
| Batch category sync in `UpsertMany` | The current per-item delete-and-insert multiplies statements on a page of items. |
| Add a short TTL cache for nav counts (5 seconds, measure first) | `CountUnread` and `ListAuthorsWithFeedCount` run on every GET page. A 5 second per-user cache removes the fixed cost. TTL only, with no explicit invalidation: a badge may lag a few seconds, which is acceptable and much simpler to reason about. |
| Cap pinned home sections (for example, 12) | `home` runs one `ListPage` per pinned collection; an unbounded config is an unbounded page cost. |
| Add benchmarks for the author list, unread page, home, and ingest | There are no benchmarks today, and the stated target is a Raspberry Pi. A 50k-item fixture gives a regression signal without CI. |

### 6. Maintenance and verification

| Change | Why |
| --- | --- |
| Add a `make check` target (`gofmt -l`, `go vet`, `go test ./...`) | Gives a single local gate while CI is paused. |
| Add a schema equality test (`schema.sql` vs migrations) | `schema.sql` is hand-maintained and can silently drift from `migrate.go`. |
| Run `gofmt -w internal/httpapi/web.go` | It is the only unformatted file, which signals there is no formatting gate. |
| Bump stale dependencies in `pluginapi` and run `govulncheck` once | `pluginapi` is still on older `x/net` and `hclog` versions than the root module. One scan now is cheap. |

## Commit plan

1. `docs: rewrite the README and add the pre-launch plan`
2. `refactor: remove demo mode` (includes schemaV64 and the session purge)
3. `fix(security): cap request bodies and set server timeouts`
4. `fix(security): scope session revoke and close the login timing gap`
5. `fix(security): require CSRF for cookie-authenticated API requests`
6. `feat(security): add proxy trust and scrub the plugin environment`
7. `docs(security): add SECURITY.md and third-party notices`
8. `build: enable SBOMs in release images`
9. `fix(poller): recover panics and respawn crashed plugins`
10. `fix(feeds): pace the first poll of a newly created feed`
11. `perf(authors): batch unread counts and add benchmarks`
12. `chore: add make check, schema equality test, and dependency refresh`

## Deferred and considered

These were identified and are not in the current pass. Each entry records why it
matters and why it is deferred.

| Change or fix | Why it matters | Why deferred |
| --- | --- | --- |
| CI workflow (`go vet`, tests, race, lint, vuln scan) | Prevents broken releases and catches races. | Deliberately paused during fast iteration; it slowed the push-to-main loop. Restore before the public presentation. |
| Opening pull requests | Community contributions and review. | Owner decision: PRs open when the project is presented. |
| Google Reader / Fever API | Unlocks Reeder, NetNewsWire, and the mobile client ecosystem, the biggest "not an alternative" objection. | Large surface with its own compatibility and versioning burden. It is the top interop item but not part of this cleanup. |
| Full-text search | Expected in a reader, removed in schemaV58. | Intentional removal until the right shape is clear. Adding a search UI and index before that would likely be rewritten. |
| Save/notify integrations and webhooks | Apprise, ntfy, Discord, Telegram, wallabag, linkding. | Each integration is ongoing maintenance, and the project explicitly avoids a plugin-free integration zoo. Revisit after the client API. |
| Readability extraction and per-feed CSS-scraper rules | Miniflux's scraper is a common reason people stay. | A plugin `Enrich` capability covers full text for sites a plugin owns. A generic scraper and rule language are a large feature, not a hardening fix. |
| OIDC, WebAuthn, email, self-service password reset | Broader auth for shared or public instances. | The target user is a single admin on a private network. Adding an email dependency contradicts the "no mail server" design. |
| Internationalization | Miniflux ships many languages. | Every string is in templ; it is a large mechanical change and not on the critical path for the first presentation. |
| Custom CSS and JavaScript | Some self-hosters want it. | Rejected by design: the customization philosophy is bounded presets. Reopening it would undo an explicit opinion. |
| Splitting the god files (`httpapi/web.go`, `store/item.go`) | Easier navigation for humans. | Large diff, no functional payoff, and AI agents handle the file sizes. Pure churn during fast iteration. |
| Threading `context.Context` through the store | Cancellation and deadlines would reach SQLite; a disconnected client would stop its query. | Mechanical but very wide diff across every repository and handler. No current correctness bug. Better done once CI and the test suite are back. |
| Unifying `visible_items` with `authorRulesVisibleSQL` | Two copies of the same visibility predicate can drift. | Guarded by `TestAuthorRulesVisibleSQLMatchesView`, and a rewrite is risky SQL work. Revisit with the context refactor. |
| PostgreSQL or MySQL support | Some operators prefer a server database. | SQLite is a deliberate single-file deployment choice for the Pi/NAS target. |
| Plugin sandboxing (WASM, containers, seccomp) | Plugins are arbitrary executables with full server privileges. | The threat model is a private network with trusted plugins, and a sandbox is a major project. Documented instead of pretended. |
| Built-in TLS / Let's Encrypt | Removes the reverse-proxy requirement. | A reverse proxy is the expected deployment and is already documented. Cert management inside the app is a large operational surface. |
| Signed release images (cosign) | Verifiable supply chain. | Requires key management and a release process change. `sbom: true` and third-party notices are the cheap first step. |
| Per-user quotas and signup hardening | Protects a hosted instance from resource abuse. | Signups are admin-controlled after the first account, and the app is not intended for public hosting. |
| Denormalized unread counters | Avoids counting unread items on every page. | A schema and write-path change with invalidation risk. Measure the TTL cache first. |
| Rewriting the `CAST(sqlc.arg(...) AS INTEGER) = 0` query pattern | It may prevent index use on some queries. | Needs `EXPLAIN QUERY PLAN` evidence before changing generated query shapes. Measure first. |
| Batching home-section queries | Home runs one query per pinned section. | `LIMIT 9` should stop early on the time index. Capping sections is the cheap fix; batching waits for measurements. |
| Tightening CSP `connect-src` | A narrower `connect-src` reduces exfiltration paths if script injection ever occurs. | hls.js fetches remote manifests and segments over XHR, and the media hosts are not known ahead of time. Tightening it would break plugin HLS video. A page-specific policy or a player that loads through `media-src` is needed first. |
| `ListItems` / `CountUnread` materialized counters | Same as denormalized counters above. | Deferred until the measured benefit justifies the invalidation complexity. |

## Verification

After the demo removal:

- `go vet ./...` and `go test ./...` pass.
- `mise run gen` produces no unexpected diff after the templ and query edits.
- `grep -ri "demo"` in Go, templ, config, and docs finds only the intentional
  use of the word (for example, `demo` as a test string).
- A fresh database migrates from schemaV1 to schemaV64, and a database at
  schemaV63 upgrades cleanly.
- Seed a live user and a demo user, upgrade from schemaV63, and confirm the live
  user still exists while the demo user is gone. The migration test covers the
  same case, including the abort path for a demo-marked admin.
- `/` still renders the landing page for anonymous visitors and the unread
  dashboard for signed-in users.
- Signup, login, feed add, item read/unread, and settings still work.

After the performance change:

- Record the `/authors` timing before and after on a large fixture.
- Confirm the number of queries for the author list is constant, not
  proportional to the number of view-bearing authors.

## Decisions taken

1. **Delete former demo users.** schemaV64 deletes only rows where
   `is_ephemeral = 1 AND is_admin = 0`. `CloneUser` is the only code path that
   ever sets the flag, and it always writes `is_admin = 0`, so the marker is
   trustworthy. A pre-flight check aborts the migration if any row is
   `is_ephemeral = 1 AND is_admin = 1`. `TestMigrateV64KeepsLiveUsers` proves a
   live user survives and the abort path works.
2. **Omit the external plugin link from the README.** The repository rule says
   docs should not name external plugins. The generic plugin section stays.
3. **One `NF_TRUST_PROXY` setting, default off.** It gates both `clientIP`
   (`X-Forwarded-For`, last entry) and `SecureRequest`
   (`X-Forwarded-Proto`/`X-Forwarded-Ssl`). Direct clients get no header trust.
   A reverse-proxy deployment must set it or cookies lose `Secure`; the env
   example and README will say so.
4. **Nav counts use a 5 second TTL only.** No explicit invalidation. A badge may
   lag a few seconds after a read toggle, which is acceptable for the simplicity
   gained.

## Open questions

None currently. New questions should be added here before they change the
scope of a commit.

