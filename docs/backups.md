# Backups

nanoflux can snapshot the whole instance — the SQLite database and, when blobs
live on local disk, the file store (avatars, custom icons) — into a portable
`.tar.gz` archive. The same format is produced by every path:

- `make backup` (compose instances)
- `nanoflux backup` (host installs and inside the container)
- the server's automatic backups (`NF_BACKUP_*`)

Because the format is shared, an archive from any of these restores with any of
the restore paths.

## What's in an archive

```
data/rss.db          # the whole database, snapshotted with VACUUM INTO
filestore/           # local-disk blobs (omitted for S3-backed instances)
  avatars/           # user profile pictures
  icons/             # custom per-domain source icons
  author-avatars/    # cached author avatars
```

The database snapshot uses SQLite `VACUUM INTO`, so it is a consistent
point-in-time copy even while the server is running in WAL mode. The `-wal` and
`-shm` sidecars are intentionally not included.

## Automatic backups

Set `NF_BACKUP_INTERVAL` (a Go duration, e.g. `24h`) and a destination to turn
on scheduled backups:

- **Local:** `NF_BACKUP_DIR=/backups`
- **S3-compatible:** `NF_BACKUP_S3_ENDPOINT`, `NF_BACKUP_S3_BUCKET`,
  `NF_BACKUP_S3_ACCESS_KEY`, `NF_BACKUP_S3_SECRET_KEY`,
  `NF_BACKUP_S3_REGION`, and optional `NF_BACKUP_S3_PREFIX`.

`NF_BACKUP_KEEP` (default `7`) controls retention: after each run the oldest
archives beyond that count are pruned. `0` keeps everything.

With an **S3 destination the file store is not included** — the snapshot holds
only the database. Back up the S3 objects separately with your provider (or
skip it if they live in the same account you're already protecting).

Admins can see the last run and trigger one on demand from the **backups** card
on `/admin`.

### Compose default

The bundled `docker-compose.yml` enables filesystem backups out of the box:
the server snapshots to `/backups`, which is bind-mounted to the host's
`./backups` directory, once every 24 hours, keeping the newest 7. Override or
disable them in `.env`:

```sh
NF_BACKUP_INTERVAL=0      # turn automatic backups off
NF_BACKUP_INTERVAL=12h    # back up twice a day
NF_BACKUP_KEEP=30         # keep more snapshots
```

Because the destination is the same `./backups` directory `make backup` uses,
an automatic snapshot restores the usual way (see below).

## Restore

### Compose instances

```bash
make restore ARCHIVE=backups/nanoflux-20260621-120000.tar.gz
make start
```

`make restore` stops the app, replaces the data volumes from the archive, and
brings the instance back up.

For a backup the server uploaded to S3, download the object first, drop it in
`backups/`, and restore it the same way.

### Host installs / inside the container

```bash
nanoflux restore backups/nanoflux-20260621-120000.tar.gz
```

Restore refuses while the server is running (`make stop` first) and validates
the archive (opens + migrates the database) before touching live files.

## Verify a backup

```bash
tar tzf backups/nanoflux-20260621-120000.tar.gz | head
# expect: data/rss.db then filestore/...
```

The database inside can be opened directly with any SQLite tool to confirm it
is intact (`PRAGMA integrity_check;`).

## Notes

- A backup is a **full snapshot**, not incremental. Archives are small (a few
  MB) and self-contained, which keeps restore trivial.
- Keep at least one archive off the machine that runs nanoflux (a different
  disk, a NAS, or an S3 bucket in another account). A local-only copy does not
  protect against disk failure.
