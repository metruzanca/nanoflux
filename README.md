# nanoflux

A self-hosted reader for the feeds you actually follow: people, channels, and
the sites that don't publish RSS.

nanoflux is a different shape of reader. Most readers organize subscriptions
into folders. nanoflux groups them under the **author** who publishes them,
because in the end we follow people, or small groups of people. One author can
own several feeds, so the two YouTube channels you follow live together under
one name, with one avatar and one set of links, instead of being split across
two folders.

It started as a Miniflux alternative. The goal now is narrower and clearer:
take the slice of a reader that matters for social feeds and make that slice
excellent, with an interface that treats a feed like a profile rather than a
file.

**Status:** pre-1.0. `main` moves fast, breaking changes are expected, and the
plugin API is not stable yet.

## What it is

- **Author-centric.** Every feed belongs to an author. A feed with no obvious
  author gets a one-feed author, which costs nothing and keeps the model
  uniform.
- **A UI for social feeds.** Avatars, links, source lines, per-post attribution,
  and one card style that adapts to text, images, galleries, link posts, video
  and audio.
- **Views for what authors can't express.** A multi-author blog is one feed with
  many people in it. A view is a saved filter (by tag, feed, or time) that you
  assign to an author, so one aggregator can feed several author pages.
- **Fine-grained filtering.** Filter any list by tag, feed, and time. Authors
  carry always-on allow/block rules that apply to their lists, counts, and
  badges.
- **Polite fetching.** Adaptive intervals, conditional GET, per-host pacing,
  learned rate-limit windows, and time-of-day scheduling for feeds that post on
  a schedule.
- **Extensible where RSS runs out.** A plugin can use a site's API, parse a
  page, or create the feed on the service. That is how nanoflux handles sites
  that never had an RSS feed.

## What it isn't

- **Not a drop-in Miniflux replacement (yet).** If you need Reeder, NetNewsWire,
  Apprise, ntfy, webhooks, or 20 languages, Miniflux is the better answer today.
- **No third-party client API.** There is no Google Reader or Fever endpoint, so
  mobile and desktop clients cannot connect. The browser extension uses a small
  JSON API of its own.
- **No integrations or webhooks.** No save-to-Pocket, no notification sinks.
- **No custom CSS or JavaScript.** The customization surface is bounded presets
  on purpose.
- **No core scraper or readability pass.** A plugin can enrich an item body,
  but there is no per-feed CSS-selector scraper in the core.
- **No full-text search yet.** It was removed on purpose and will return when
  the shape is clear.
- **No email verification, self-service password reset, 2FA, or OIDC.** After
  the first account, the admin decides whether signup is open. There is no mail
  server involved.
- **Not a public multi-tenant service.** nanoflux is designed for one person or
  a household on a Raspberry Pi, a NAS, or a Tailscale network.

## Why authors

The model is easiest to see with an example:

- You follow one person on Bluesky, YouTube, and Patreon. That is three feeds,
  one person. nanoflux shows one author with a profile picture, a description,
  links, and all three feeds' posts in one place.
- You follow a group blog with five writers. That is one feed, five people. You
  save five views, one per writer, and assign each view to an author. Each
  writer gets a page with only their posts, and the group's own feed still shows
  everything.

Folders can't express either case well. Authors can.

## Features

**Reading**

- Unread, read, favorites, bookmarks, lists, and saved pages.
- Item preview with keyboard navigation and a command palette.
- Magic sort: rank feeds by your favorites, then raise or lower a feed by hand.
- Auto-read: old unread items fade out after 3, 7, 30, or 60 days, or never.
- Cross-feed identity: the same post seen through two subscriptions is one row,
  so read and favorite state is shared.

**Organizing**

- Authors own feeds, collections group feeds, lists group posts.
- Saved views: a named filter recipe that acts as a source on an author page.
- Tag, feed, and time filters on every list, plus per-author allow/block rules.
- Per-scope share links for items, lists, favorites, and bookmarks.

**Media**

- Enclosure players for video, audio, and HLS, with posters and durations.
- Resume playback across devices, server-side.
- Host-side image caching for feeds whose image URLs expire.
- Optional S3-compatible object storage.

**Fetching**

- Per-feed adaptive intervals, a global poll floor, and a per-plugin floor.
- Per-host FIFO queues and learned rate-limit windows.
- Time-of-day scheduling for feeds that post at a regular hour.
- OPML import and export.

**Operations**

- Single-file SQLite, no external database.
- First signup becomes the admin; there is no default account.
- Automatic backups to disk or S3, with retention and a CLI restore.
- Admin poll health, feed errors, and storage report/gc.
- Installable PWA and a browser extension.

## Install

Build and run from source:

```bash
git clone --recursive https://github.com/metruzanca/nanoflux
cd nanoflux
make start
```

Open http://localhost:20310. The first account you create becomes the admin.

`--recursive` pulls the default site plugins. If you cloned without it:

```bash
git submodule update --init --recursive
make plugins   # needs Go on the host; the server itself does not
```

Plugins are optional. Without them, nanoflux reads every standard RSS, Atom, and
JSON feed and skips the site-specific integrations.

Updating is the same story: `make update` pulls, rebuilds, and redeploys.

### Prebuilt images (GHCR)

Official releases are published as container images to
`ghcr.io/metruzanca/nanoflux` ↗. To use one instead of a local build, pass
`REGISTRY=ghcr`:

```bash
make start REGISTRY=ghcr    # pull and run the latest release
make update REGISTRY=ghcr   # update to the latest release
```

With plain Docker:

```bash
docker run -d --name nanoflux -p 8080:8080 \
  -v nanoflux-db:/data \
  -v nanoflux-files:/filestore \
  -e NF_FILE_STORE=/filestore \
  ghcr.io/metruzanca/nanoflux:latest
```

On a fresh install there is no account yet: signup is open for exactly one
account, and the first person to sign up becomes the admin. Signups close
automatically right after. Re-open them from `/admin`, or create and recover an
admin from the CLI.

### CLI

Some admin operations are easier from a shell. `make shell` opens one inside the
container, then:

| Command | What it does |
| --- | --- |
| `nanoflux user list` | list accounts |
| `nanoflux user create <username>` | create an account, prompts for the password (`--admin` grants admin) |
| `nanoflux user set-admin <username> true` | promote or demote |
| `nanoflux user reset-password <username>` | set a new password |
| `nanoflux user delete <username>` | delete an account |
| `nanoflux feed list` | every feed across users, no item content |
| `nanoflux feed refetch` | re-poll feeds whose last error is HTTP 400 |
| `nanoflux backup` | write a snapshot into `backups/` |
| `nanoflux restore <archive>` | restore a snapshot, refuses while the server runs |
| `nanoflux storage report` | bytes per kind and cache plugin |
| `nanoflux storage gc --apply` | remove cached media no row references |

### Configuration

Environment variables only. See [`.env.example`](./.env.example) for the full
list, defaults, and descriptions. Copy it to `.env` and edit as needed.

### Backup and restore

`make backup` writes a tarball of the database and file store to `backups/`:

```bash
make backup
make restore ARCHIVE=backups/nanoflux-<timestamp>-<version>.tar.gz
```

Cached item media (`filestore/cache/`) is excluded: it is derived, can be many
gigabytes, and would dominate the archive. Avatars and icons are included.
`nanoflux storage report` and `nanoflux storage gc --apply` reclaim space from a
running instance.

## Browser extension and PWA

Load `extension` as an unpacked extension, or download the packaged build from
the settings page. When the current page has no feed, the extension offers to
save the page as a bookmark instead.

Installing nanoflux as a PWA also registers it as a share target, so the OS
share sheet can add a feed or save a page.

## Plugins

nanoflux's site integrations are external plugins: small executables dropped
into the plugin directory. None are compiled into the server. The default site
plugins live in their own repositories and are pulled in as git submodules;
`make plugins` builds them.

See [`docs/writing-plugins.md`](docs/writing-plugins.md) to write your own.

> **Plugins are arbitrary executables.** A plugin runs as a child process with
> the same OS privileges as the server and can read or modify anything the
> server can, including the database and file store. There is no sandbox. Only
> install plugin binaries you trust, and make sure the plugin directory is not
> writable by untrusted users or processes.

## Security and threat model

nanoflux is built for a private network. The expected deployment is a Raspberry
Pi or a small server on your LAN, reached over Tailscale or a VPN. It is not
hardened for anonymous public signups or for hosting other people's data.

The core:

- Sanitizes feed HTML at ingest, so a malicious feed cannot run script.
- Refuses outbound requests to private, loopback, link-local, and cloud metadata
  addresses (DNS-rebinding safe). `NF_ALLOW_PRIVATE_FETCH=1` is the LAN opt-out.
- Serves images only after sniffing them as raster formats, and proxies remote
  images by default so feed hosts do not see your IP.
- Stores sessions and share tokens hashed, sets `HttpOnly`/`SameSite` cookies,
  requires a CSRF token on browser requests, and uses bearer tokens for the
  extension API. The app ships a strict CSP without inline script.
- Keeps the first-signup bootstrap and closes signups behind it.

It does not:

- Sandbox plugins. See the plugin warning above.
- Terminate TLS. Put a reverse proxy in front of it for any deployment beyond
  localhost. When that proxy terminates TLS, set `NF_TRUST_PROXY=1` so secure
  cookies and login throttling use the forwarded scheme and client IP.

If you expose nanoflux beyond your network: keep signups closed, put it behind
TLS and authentication, and audit every plugin. To report a vulnerability
privately, see [SECURITY.md](SECURITY.md).

## Status and contributing

nanoflux is pre-1.0 and moving fast. Issues are very welcome. Pull requests will
open when the project is presented publicly. See [`docs/roadmap.md`](docs/roadmap.md)
for release readiness, near-term work, and deferred ideas.

It is built with heavy AI assistance. The design decisions, reviews, and the
opinions in this README are human.

Coffee is welcome too:

<a href='https://ko-fi.com/C1C51JBGUD' target='_blank'><img height='36' style='border:0px;height:36px;' src='https://storage.ko-fi.com/cdn/kofi3.png?v=6' border='0' alt='Buy Me a Coffee at ko-fi.com' /></a>

## License

MIT. See [LICENSE](LICENSE). UI icons are from [Lucide](https://lucide.dev) ↗,
used under the ISC License. Third-party notices are in
[THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES).

- Website: https://nanoflux.app ↗
- Source: https://github.com/metruzanca/nanoflux ↗
