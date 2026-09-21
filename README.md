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
| `NF_ADDR` | `:8080` | listen address |
| `NF_DB` | `./data/rss.db` | sqlite database path |
| `NF_FILE_STORE` | `<db dir>/filestore` | local directory for avatars and custom icons when no S3 endpoint is configured |
| `NF_LOG_LEVEL` | `info` | log level: `debug`, `info`, `warn`, `error` |
| `NF_POLL_INTERVAL` | `15m` | poller wake interval |
| `NF_POLL_WORKERS` | `4` | concurrent feed fetches |
| `NF_ADMIN_USER` / `NF_ADMIN_PASS` | — | create the first account at startup (takes precedence over admin/admin) |

### Object storage

Avatars and custom source icons live in S3-compatible object storage. With no
S3 endpoint configured they fall back to the local disk directory
`NF_FILE_STORE`; set `NF_S3_ENDPOINT` to switch to S3 (note that `https://`
prefixes enable TLS).

| Variable | Default | Description |
| --- | --- | --- |
| `NF_S3_ENDPOINT` | unset → local disk | S3-compatible object-storage endpoint |
| `NF_S3_BUCKET` | `nanoflux` | bucket name |
| `NF_S3_ACCESS_KEY` | — | access key |
| `NF_S3_SECRET_KEY` | — | secret key |
| `NF_S3_REGION` | `us-east-1` | bucket region |

## Run with containers (docker / podman)

The release pipeline builds a multi-arch image (linux/amd64 + linux/arm64) and
publishes it to the GitHub Container Registry as
`ghcr.io/metruzanca/nanoflux:latest`. Clone the repo and drive the included
`docker-compose.yml` with `make` (docker or podman, auto-detected):

```bash
git clone https://github.com/metruzanca/nanoflux
cd nanoflux
make start
```

Point your browser at http://localhost:8080. On first start, `make start`
generates a `.env` file with `NF_ADMIN_USER=admin` and a random
`NF_ADMIN_PASS` (printed to the console and recoverable from `.env`). The
file is only created when missing, so a restart won't change your password.
Set the variables yourself in `.env` before the first run to pick your own
credentials; otherwise the default `admin/admin` account is created only when
no bootstrap credentials exist. All variables are documented in `.env.example`.

This mounts two named volumes: `nanoflux-db` at `/data` (the sqlite database)
and `nanoflux-files` at `/filestore` (avatars and custom icons), so both
survive container restarts and updates.

### Daily use

| Command | What it does |
| --- | --- |
| `make start` | start the app (pulls the image, creates `.env` on first run) |
| `make stop` | shut the app down (containers and data kept) |
| `make restart` | restart the app |
| `make status` | show container status |
| `make logs` | tail the app logs |
| `make shell` | open a shell in the app container |
| `make backup` | snapshot the database and file store into `backups/` |
| `make restore ARCHIVE=backups/<file>.tar.gz` | stop the app and restore from a backup |
| `make down` | stop and remove containers (data kept) |

`make help` lists these, and `make <cmd> RUNTIME=podman` forces podman.

### Updating

```bash
make update
```

Pulls the latest `latest` image and redeploys the container; your data is
preserved.

### HTTPS

nanoflux detects whether a request arrived over HTTPS and only then marks the
session cookie `Secure` — so plain-HTTP LAN and dev installs keep working. It
checks TLS directly and the `X-Forwarded-Proto`/`X-Forwarded-Ssl` headers from
a reverse proxy, so serving through Caddy, Traefik or Tailscale-serve hardens
the cookie automatically. If you use Nginx, forward the scheme explicitly:

```nginx
location / {
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_pass http://127.0.0.1:8080;
}
```

A simple Caddy setup:

```caddy
nanoflux.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

To run a published image directly, without the Makefile:

```bash
docker run -d --name nanoflux -p 8080:8080 \
  -v nanoflux-db:/data \
  -v nanoflux-files:/filestore \
  -e NF_FILE_STORE=/filestore \
  -e NF_ADMIN_USER=admin -e NF_ADMIN_PASS=changeme \
  ghcr.io/metruzanca/nanoflux:latest
```

Substitute `podman` for `docker` as needed. If the image is private,
`podman login ghcr.io -u <user>` first.

## Admin

nanoflux has two admin surfaces with the same capabilities: list users, create
users, reset passwords, grant/revoke the admin flag, and delete users. Deleting
a user removes all of their data (feeds, items, avatars, icons). The first
account created at startup (the bootstrap account) is always an admin.

### Web

Log in as an admin and open **admin** from the user menu (top-right). The page
shows instance stats (users, feeds, authors, items, unread), a **signups**
toggle, and a banner suggesting you disable open signup (dismissible per
admin). Each user row has a password-reset form, a make/remove-admin toggle,
and a delete button with confirmation. You cannot delete your own account, and
the last admin can never be removed.

Signups are **open by default** so a fresh instance is usable; once you have
your account, disable them on `/admin` so strangers can't register. With
signup off, the signup page shows a notice and the login page stops linking to
it — new accounts come from the CLI's `user create`.

### CLI

The same operations run inside the container:

```bash
make shell
nanoflux user list
nanoflux user create <username>              # prompts for the password; --admin grants admin
nanoflux user set-admin <username> true
nanoflux user reset-password <username>     # prompts for the new password
nanoflux user delete <username>             # prompts to confirm; use --yes to skip
nanoflux feed list                          # every feed across users (no item content)
nanoflux user --help
```

`nanoflux` with no arguments (or `nanoflux server`) starts the web server, so
the docker image and compose file are unchanged; any other first argument
routes to the CLI. Instances created before the admin flag existed grant one
with `nanoflux user set-admin <your-username> true`.

### Backup and restore

`make backup` writes a tarball of the database and file store to `backups/`:

```bash
make backup
ls backups/
make restore ARCHIVE=backups/nanoflux-20260921-120000.tar.gz
```

`make restore` stops the app, replaces the data volumes from the archive, and
leaves the instance stopped for you to start (`make start`) when ready.

The same format is available from the CLI for host installs:

```bash
nanoflux backup                # writes backups/nanoflux-<timestamp>.tar.gz
nanoflux restore backups/nanoflux-20260921-120000.tar.gz   # refuses while the server is running
```

The database is always backed up. Blobs on local disk are included; if you
point `NF_S3_ENDPOINT` at S3-compatible storage, back up those objects with
your provider.

## Release

Tags push `v*` trigger GitHub Actions to build the multi-arch
`ghcr.io/metruzanca/nanoflux` images and draft a GitHub release with the
changelog. Releases carry no binary artifacts — the app ships as containers
only, and the release notes link to the package registry:

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