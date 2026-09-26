# nanoflux

A dead-simple, self-hosted RSS reader. Go backend with an htmx web UI, a JSON
API for browser extensions, per-user accounts, feed discovery, and SQLite
storage.

## Features

- **Author-centric** — every feed belongs to an author; feeds are always
  viewed through the author who publishes them.
- **Automagical adds** — paste a URL and nanoflux discovers the
  feed, derives the title/home url, and creates an **author** with their first
  feed attached.
- **Collections** — group feeds into folders.
- **Item preview** — view items without leaving the app.
- **Multi-user** — username/password accounts, per-user data, simple signup page. (no emails)
- **Multi-user** — Admin panel and admin cli.
- **Browser Extension** — for quickly adding a new feed, or saving the current page to a list when it has no feed.
- **Lists & saved pages** — group items into named lists (favorites is built in), and keep arbitrary pages as "watch later" entries that live in your lists and search without cluttering the unread stream.
- **Optional S3 support** — Nanoflux uses local file storage by default, but supports S3.
- **Backups** — Configurable Automatic backup every 1 day, keeping the 7 most recent or trigger them manually.
- **Polite fetching** — per-feed adaptive intervals, conditional GET, and per-host rate-limit backoff, so a throttled site never starves the rest of your feeds. See [`docs/fetching.md`](docs/fetching.md).
- **Auto-read** — old unread items are marked read automatically (3, 7, 30, or 60 days; off), so a backlog you never cared about fades away. Reversible with "mark all unread".

## Usage

The recommended approach is to use the docker container.

```bash
docker run -d --name nanoflux -p 8080:8080 \
  -v nanoflux-db:/data \
  -v nanoflux-files:/filestore \
  -e NF_FILE_STORE=/filestore \
  -e NF_ADMIN_USER=admin -e NF_ADMIN_PASS=changeme \
  ghcr.io/metruzanca/nanoflux:latest
```

We also include a docker-compose.yml and have setup a `Makefile` with admin/maintenance commands.
Making the easiest way to run nanoflux, clone the repository and run `make start`

```bash
git clone https://github.com/metruzanca/nanoflux
cd nanoflux
make start
```

Updating nanoflux is also made easy with the Makefile, just run `make update`.

NOTE: Signups are **open by default** so a fresh instance is usable. You may disable them on `/admin`.
You can create new accounts from the CLI inside the container or re-enable signups briefly.


### CLI

For selfhosting convenience, I've setup a `Makefile` with the most likely commands you'll need, most are wrappers of docker exec. It also supports podman.

| Command | What it does |
| --- | --- |
| `make start` | start the app (pulls the image, creates `.env` on first run) |
| `make stop` | shut the app down (containers and data kept) |
| `make restart` | restart the app |
| `make status` | show container status |
| `make logs` | tail the app logs |
| `make shell` | open a shell in the app container |
| `make version` | print the running app's version |
| `make backup` | snapshot the database and file store into `backups/` |
| `make restore ARCHIVE=backups/<file>.tar.gz` | stop, restore from a backup, and restart |

Some admin operations are more easily done via a CLI app.
Nanoflux ships with one, you can access it easily by running
`make shell` to get shell access into your container.

Then you can run `nanoflux` as a cli tool:

```bash
nanoflux user list
nanoflux user create <username>              # prompts for the password; --admin grants admin
nanoflux user set-admin <username> true
nanoflux user reset-password <username>     # prompts for the new password
nanoflux user delete <username>             # prompts to confirm; use --yes to skip
nanoflux feed list                          # every feed across users (no item content)
nanoflux item backfill-thumbs               # fill missing YouTube thumbnails (no network)
nanoflux user --help
nanoflux backup                # writes backups/nanoflux-<timestamp>.tar.gz
nanoflux restore backups/nanoflux-20260921-120000.tar.gz   # refuses while the server is running
```

### Configuration

Configuration is done with environment variables. See
[`.env.example`](./.env.example) for the full list, including defaults and
descriptions. Copy it to `.env` and edit as needed.

### Backup and restore

`make backup` writes a tarball of the database and file store to `backups/`:

```bash
make backup
ls backups/
make restore ARCHIVE=backups/nanoflux-20260921-120000.tar.gz
```

## Browser extension

Nanoflux has a browser extension for adding feeds quickly and easily.
You can install it by loading the `extension` directory as an "unpacked extension"
OR by downloading the unpacked extension from the user settings page.

When the current page has no feed, the extension offers to **save the page**
instead. A saved page is stored as a list item ("watch later" by default, or any
other list you pick or name) with its title, description and thumbnail fetched
from the page. It appears in your lists, favorites and search, but not in the
unread/read streams or home, so it stays out of your reading flow until you open
it. Favorites is the special built-in list; create others from `/lists`.

## Installable PWA

Nanoflux is a progressive web app, you can install it for convenience on Android/IOS.
Installing nanoflux as a PWA also registers it as a **share target** allowing you to quickly add a feed.

## Contributing

I'm not accepting contributions at this time — but issues are very welcome for
feature requests and bug reports.

This is an AI coding project: if your agent can write a feature, mine probably
can too. The difference is that I have a better understanding of the project goals, so
I'd rather build it myself. A clear issue describing the feature or the bug is
the most useful thing you can send — though coffee is a close second:

<a href='https://ko-fi.com/C1C51JBGUD' target='_blank'><img height='36' style='border:0px;height:36px;' src='https://storage.ko-fi.com/cdn/kofi3.png?v=6' border='0' alt='Buy Me a Coffee at ko-fi.com' /></a>

## License

License: PolyForm Perimeter 1.0.1
Source-available. Free to self-host. Commercial hosted offerings require permission.

- Website: https://nanoflux.app
- Source: https://github.com/metruzanca/nanoflux

UI icons are from [Lucide](https://lucide.dev), used under the ISC License
(Copyright © Lucide Contributors).
