# nanoflux

A dead-simple, self-hosted RSS reader. Go backend with an htmx web UI, a JSON
API for browser extensions, per-user accounts, feed discovery, and SQLite
storage.

## Features

- **Automagical adds** — paste a URL (a feed or a page); nanoflux discovers the
  feed, derives the title/home url, and pre-fills the author (name + avatar
  from the page).
- **Profiles (authors)** — one author can own many feeds; feeds can also be
  authorless and assigned later.
- **Collections** — group feeds into folders.
- **Read/unread** — mark items read, mark-all-read; items per feed and author.
- **Item preview** — click an item to preview it in a modal, or open the live
  version in a new tab.
- **Multi-user** — username/password accounts, per-user data, signup page.
- **JSON API** — `/api/...` endpoints for the browser extension (auth via the
  session token as `Authorization: Bearer`).

## Run from source

Requires Go 1.26+.

```bash
go run ./cmd/server
```

On first start with no users, a default **admin/admin** account is created.
Point your browser at http://localhost:8080.

Configuration (env vars):

| Variable | Default | Description |
| --- | --- | --- |
| `RSS_ADDR` | `:8080` | listen address |
| `RSS_DB` | `./data/rss.db` | sqlite database path |
| `RSS_FILE_STORE` | `<db dir>/filestore` | local directory for avatars and custom icons when no S3 endpoint is configured |
| `RSS_LOG_LEVEL` | `info` | log level: `debug`, `info`, `warn`, `error` |
| `RSS_POLL_INTERVAL` | `15m` | poller wake interval |
| `RSS_POLL_WORKERS` | `4` | concurrent feed fetches |
| `RSS_BOOTSTRAP_USER` / `RSS_BOOTSTRAP_PASS` | — | create the first account at startup (takes precedence over admin/admin) |

### Object storage

Avatars and custom source icons live in S3-compatible object storage. With no
S3 endpoint configured they fall back to the local disk directory
`RSS_FILE_STORE`; set `S3_ENDPOINT` to switch to S3 (note that `https://`
prefixes enable TLS).

| Variable | Default | Description |
| --- | --- | --- |
| `S3_ENDPOINT` | unset → local disk | S3-compatible object-storage endpoint |
| `S3_BUCKET` | `nanoflux` | bucket name |
| `S3_ACCESS_KEY` | — | access key |
| `S3_SECRET_KEY` | — | secret key |
| `S3_REGION` | `us-east-1` | bucket region |

## Run with containers (docker / podman)

The release pipeline builds a multi-arch image (linux/amd64 + linux/arm64) and
publishes it to the GitHub Container Registry. A `docker-compose.yml` is
included for building and running from source:

```bash
docker compose up --build
```

This mounts two named volumes: `nanoflux-db` at `/data` (the sqlite database)
and `nanoflux-files` at `/filestore` (avatars and custom icons), so both
survive container restarts. Point your browser at http://localhost:8080. Set
`RSS_BOOTSTRAP_USER`/`RSS_BOOTSTRAP_PASS` in a `.env` file to create the first
account at startup; otherwise the default `admin/admin` account is created.

To run a published image instead, replace the `build:` block in
`docker-compose.yml` with `image: ghcr.io/metruzanca/nanoflux:latest`, or run
it directly:

```bash
podman pull ghcr.io/metruzanca/nanoflux:latest

podman run -d --name nanoflux -p 8080:8080 \
  -v nanoflux-data:/data \
  -v nanoflux-files:/filestore \
  -e RSS_FILE_STORE=/filestore \
  -e RSS_BOOTSTRAP_USER=admin -e RSS_BOOTSTRAP_PASS=changeme \
  ghcr.io/metruzanca/nanoflux:latest
```

With docker, substitute `docker` for `podman`. If the image is private,
`podman login ghcr.io -u <user>` first.

## Release

Tags push `v*` trigger GitHub Actions to build binaries, a GitHub release, and
the `ghcr.io/metruzanca/nanoflux` images:

```bash
git tag v0.1.0 && git push origin v0.1.0
```

## Development

```bash
go test ./...
go vet ./...
goreleaser check
goreleaser release --snapshot --skip=docker   # build binaries locally
```