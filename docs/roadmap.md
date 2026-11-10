# Roadmap

Status: living document, last updated 2026-11-09. nanoflux is pre-1.0; the
order below is a priority signal, not a commitment.

This is the single source of truth for release readiness, near-term work, and
deferred ideas. `README.md` describes what nanoflux is today; this file describes
what it should become next.

Principles:

- The target deployment is a private network (LAN, NAS, Raspberry Pi,
  Tailscale), not a public multi-tenant service.
- Customization is bounded presets, not custom CSS or JavaScript.
- Site-specific behavior belongs in a plugin through a URL-matched capability.
  Core does not accumulate per-site cases.
- Features that need ongoing maintenance should earn their place. No integration
  zoo in core.
- Breaking changes are expected until 1.0.

Current baseline: RSS, Atom and JSON Feed; OPML; author-centric organization;
collections, lists, saved views, favorites and bookmarks; tag, feed and time
filters; per-author allow/block rules; magic sort; auto-read; share links;
native media players and resume; host-side image caching; S3; automatic backups;
browser extension and PWA; multi-user accounts; admin CLI; external plugins;
SQLite only.

## Release readiness

These are the gates before the project is presented publicly. They are not
features, but they determine whether the first public thread is about the
product or about the process.

| Task | Why |
| --- | --- |
| Restore CI (`gofmt`, vet, tests, race, lint, `govulncheck`) | `main` moves fast; a local `make check` exists, but a broken commit can still be tagged. |
| Open pull requests and add issue templates | The README says PRs open at presentation. Prepare the workflow before the traffic arrives. |
| Decide release signing | SBOMs and third-party notices are in place. `cosign` signing remains open. |
| Screenshots and a short tour | The landing page and README carry the positioning; the repo needs current visuals. |
| Confirm the plugin trust story is prominent | Plugins are unsandboxed executables with server privileges. It is documented, and it should stay impossible to miss. |

## Next release

Ordered by leverage. Each item should be shippable on its own.

### 1. Reader interoperability

Add Google Reader compatible endpoints first, then Fever. This unlocks Reeder,
NetNewsWire and the wider client ecosystem, and it is the largest single gap
against Miniflux.

- Keep the existing JSON API for the extension.
- Use the same item and feed scopes as the web UI, including author rules.
- Version the endpoints and document the supported subset.
- Do not block the release on Fever; Google Reader compatibility is the win.

### 2. Personal access tokens

Scripts and the extension should not store a password or reuse a session token.
Add scoped personal access tokens, use them for the reader API, and let the
extension switch to one.

### 3. Item search

Design a replacement from scratch. The FTS5 implementation was removed in
schemaV58 because the shape was wrong, not because search is unwanted.

- Search title, body and link.
- Keep it user-scoped and author-rule aware.
- Decide whether the index is FTS5 again or a simpler token index.
- Add saved searches later, once search itself is stable.

### 4. Ingest rules

Rebuild the rule system that was removed pending a clean design.

- Match on field and pattern, with tag, feed and time predicates.
- Actions: delete, mark read, favorite, tag, notify.
- Reactive for new items, and explicitly retroactive when the user asks.
- Global rules and per-feed rules.
- Reuse the existing filter row shape so the UI and saved views stay consistent.

### 5. Reading and display

Bounded presets, stored on the account.

- Font size, font family (`sans`, `serif`, `system`), density and content width.
- Make `theme-color` and the PWA manifest follow the theme.
- Move the authors sort from `localStorage` into account state.
- Optional: infinite scroll, focus reader, print stylesheet.
- Unread badges on lists, tags and every scope the filter dialog supports.
- Bulk selection with batch read/unread, favorite, tag and list actions.

### 6. Content extraction

Add a per-feed "fetch original article" toggle with a Readability pass, and keep
`CapEnrich` for plugin-owned sites. One storage path (`items.content`) for both.

- Optional per-feed CSS-selector scraper rules.
- Optional rewrite rules and regex include/exclude filters.
- Do not put a generic scraper in core before the rule language is designed.

### 7. Notifications and sinks

Add one outbound capability, a sink or notifier, matched on the item and its
feed. Destinations stay out of core.

- Per-user webhook first.
- Apprise/ntfy-style notifiers behind the same capability.
- Save-to destinations (read-later, notes, bookmarks) use the same hook.
- Per-rule notifications from the ingest rule system.

### 8. Privacy remainder

- FeedBurner link rewriting.
- Pixel-tracker removal.
- Privacy-friendly embed rewrites (`youtube-nocookie.com`, optional alternative
  frontends).
- Keep the existing tracking-parameter stripping and image proxy.

### 9. Auth breadth

- Passkeys / WebAuthn first.
- Generic OIDC second.
- Reverse-proxy auth for private deployments.
- Do not add email infrastructure. Password reset stays an admin/CLI action.

### 10. Internationalization

Extract UI strings into message catalogues. This is invasive but the biggest
reach expansion. Do it after the reader API and search, not before.

### 11. Plugin controls

- Persist per-feed plugin configuration (`FetchRequest.Config`) so a plugin can
  take a section, channel or extraction depth.
- Add a plugin-declared settings schema for per-feed fields, beyond the current
  instance-wide `Configurable` settings.
- Keep the URL-matched capability boundary; do not add site checks to core.

## Native CLI and service (proposed)

Goal: make the `nanoflux` binary the single tool for running an instance on a
personal machine. It installs itself as a per-user systemd service, runs the
server, and updates itself. Docker remains an alternative under
`deployments/docker/`.

Decisions already made:

- Native per-user service is the primary self-host path.
- Configuration comes from an in-app dotenv file, not an environment baked into
  the unit.
- Releases publish native binaries and checksums so `nanoflux update` can
  self-update.
- The remote admin API and client-server CLI idea are dropped. A native service
  makes them unnecessary.

Work:

1. **Repo layout.** Move `docker-compose.yml`, `Dockerfile`, `Dockerfile.local`
   and the Docker `Makefile` under `deployments/docker/`. Update goreleaser,
   `scripts/build-armv6.sh`, `.dockerignore` and `.gitmodules` paths. Keep
   `mise.toml` as the dev entrypoint.
2. **Config loading.** Add a dotenv loader before `config.Load()`. Resolution
   order: `--config`, `NF_CONFIG`, `./.env`, the XDG config path, then
   `/etc/nanoflux/`. Never override a value already in the environment. Add
   `NF_CONFIG` to `.env.example`.
3. **Service management.** Add `internal/service` on `kardianos/service`.
   Commands: `install`, `uninstall`, `start`, `stop`, `restart`, `status`,
   `logs`. Use a custom user-unit systemd template, set the working directory to
   the data dir, enable linger best-effort, and add `--dry-run` to print the
   unit. Keep `server` as the foreground entrypoint.
4. **Self-update.** `nanoflux update` reads the current version, queries the
   latest release, downloads the matching archive and `checksums.txt`, verifies
   SHA-256, atomically replaces the executable, and restarts the service. Flags:
   `--check`, `--force`, `--version`, `--restart-only`.
5. **Packaging.** Publish `linux`/`darwin` x `amd64`/`arm64` tar.gz archives and
   a checksum file. Add `scripts/install.sh` for a checksum-verified install to
   `~/.local/bin`. Update the release footer, which currently says no binaries
   are published.
6. **Docs.** Split README install into Native (recommended) and Docker. Document
   the `~/.config/nanoflux` and `~/.local/share/nanoflux` layout.

Risks: the kardianos default systemd unit is wrong for user services (wanted-by
and restart delay must be overridden); a user install in `~/.local/bin` is
writable, so self-replace needs no sudo; `go run` reports `dev`, so development
should use `restart`, not `update`; verify the Docker paths after the move.

## Later

Grouped candidates, not yet scheduled. Keep the list honest: add an item only
when it is wanted, not because a competitor has it.

### Reading

- Podcast/audio queue, speed memory and continuous play across an author.
- Reading time and word count.
- Scroll-position memory and a "continue reading" surface.
- Print/PDF-friendly item view.

### Feeds and sources

- Keyword/search feeds that surface new matches.
- Newsletter-to-feed with a per-user inbound address.
- Web page change monitoring with a snapshot/diff store.
- A bookmarklet fallback for browsers without the extension.
- More source plugins, but only where a public API or a stable feed exists.

### Extension and sharing

- Context-menu add.
- Share-sheet parity on desktop.
- Export a list or favorites as its own RSS feed.

### Organization

- Per-feed or per-scope display templates beyond list/grid/cards.
- Mute by domain or author, after the ingest rule system exists.
- Smart/virtual feeds for saved scopes.
- More save-to destinations behind the sink capability.

### Fetching

- Per-feed HTTP auth, headers and cookies.
- Per-feed proxy and TLS options.
- Per-feed retention or cleanup to bound SQLite growth.
- Feed health panel with consecutive failures and "stale" badges.
- OPML import from a URL.

### Data and trust

- Full JSON export of items and read/favorite state.
- Import from other readers' JSON.
- Audit log for shared instances.

### Accessibility

- Customizable keyboard shortcuts.
- RTL, reduced-motion and high-contrast presets.
- Dyslexia-friendly font option.

### Operations

- Invite-only signup with codes.
- Per-user quotas.
- Prometheus/health metrics endpoint.
- Instance default theme and accent for new users.
- Offline PWA reading.

### Plugins

- Public-API source plugins (keyless where possible).
- Authenticated social plugins that handle rate limits better than anonymous
  feeds.
- An extractor/enricher plugin for full text, translation and transcripts.
- A sink/notifier capability for save-to and notifications.

## Deferred or rejected

| Item | Why it matters | Why deferred or rejected |
| --- | --- | --- |
| CI during fast iteration | Catches races and broken builds. | Paused while the project moves fast. `make check` is the local gate; restore CI before presentation. |
| Custom CSS and JavaScript | Some self-hosters want a theme engine. | Rejected by design. Bounded presets are the opinion; reopening it would undo it. |
| Splitting `httpapi/web.go` and `store/item.go` | Easier navigation for humans. | Large diff, no functional payoff. AI agents handle the file sizes. |
| Threading `context.Context` through the store | Cancellation and deadlines reach SQLite. | Mechanical but very wide. No current correctness bug. Do it once CI is back. |
| Unifying `visible_items` and `authorRulesVisibleSQL` | Two copies of a visibility predicate can drift. | Guarded by `TestAuthorRulesVisibleSQLMatchesView`; the rewrite is risky SQL work. |
| PostgreSQL or MySQL | Some operators prefer a server database. | SQLite is a deliberate single-file choice for the Pi/NAS target. |
| Plugin sandboxing (WASM, containers, seccomp) | Plugins run with full server privileges. | The threat model is a private network with trusted plugins. A sandbox is a project of its own. |
| Built-in TLS / Let's Encrypt | Removes the reverse-proxy requirement. | A reverse proxy is the expected deployment. Cert management is a large operational surface. |
| Signed release images | Verifiable supply chain. | Requires key management. SBOMs and notices are the cheap first step. |
| Per-user quotas and signup hardening | Protects a hosted instance. | Signups are admin-controlled after the first account. |
| Denormalized unread counters | Avoids counting on every page. | A schema and write-path change with invalidation risk. The nav TTL cache is the current answer. |
| Rewriting the `CAST(sqlc.arg(...) AS INTEGER) = 0` pattern | May prevent index use. | Needs `EXPLAIN QUERY PLAN` evidence first. |
| Batching home-section queries | Home runs one query per pinned section. | `LIMIT 9` stops early; sections are capped at 12. Measure before batching. |
| CSP `connect-src` tightening | Reduces exfiltration paths. | hls.js loads remote manifests and segments over XHR. Media hosts are unknown ahead of time. |
| `ListItems`/`CountUnread` materialized counters | Same as denormalized counters. | Wait for measurements. |
| Remote admin API / client-server CLI | Reach a containerized CLI. | Dropped. The native service plan makes it unnecessary. |

## Known bugs and small fixes

- Adding a feed can swap the author and feed names. Default the feed title to
  `$USERNAME's $DOMAIN` and keep the author name distinct.

## Open questions

- Schedule the Google Reader API now, or keep deferring compatibility clients?
  Recommendation: ship it next.
- Full-text extraction in core or only through `CapEnrich`? Recommendation: a
  core Readability pass with a per-feed toggle, plus plugin extraction for
  sites that need credentials.
- Density: spacing only, or also thumbnail/card size in grid mode?
- Serif: a separate theme axis or a font-family preset? Recommendation: a
  font-family preset.
- Authors sort: account state or a per-user JSON preference? Recommendation:
  account state, like view mode.
