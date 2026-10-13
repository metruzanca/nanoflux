# Native CLI + service plan

Status: proposed, not started.

## Goal

Make the `nanoflux` binary the single tool for running and managing an instance
on a personal machine: it installs itself as a per-user systemd service, runs the
server, and updates itself. This removes the need for a container or the root
`Makefile` on the primary self-host path.

Docker deployment is kept as an alternative for others and moves under
`deployments/docker/`.

## Why

The original reason for the `Makefile` was that the CLI lives inside the
container and is awkward to reach (`docker compose exec`). Running natively puts
the CLI and the server on the same host as the same binary, so admin commands
talk to the database directly and the service lifecycle is a CLI concern.

The earlier client-server idea (a remote admin API under `/api/admin/*` with a
backend abstraction in `internal/cli`) is **dropped**. It was only needed to
reach a containerized CLI; a native service makes it unnecessary.

## Decisions

- Native per-user systemd service is the primary path.
- Config is read from an in-app dotenv file (no env baked into the unit).
- Releases publish native binaries + checksums so `nanoflux update` can
  self-update.
- All docker files move to `deployments/docker/`, including the release
  `Dockerfile`.
- The remote admin API / client-server work from the previous plan is out of
  scope.

## 1. Repo restructure

Move into `deployments/docker/`:

- `docker-compose.yml`
  - `build.context: ../..`
  - `build.dockerfile: deployments/docker/Dockerfile.local`
  - Volume paths anchored to the repo root: `../../plugins`, `../../backups`.
- `Dockerfile.local`
- `Dockerfile` (release image, referenced by goreleaser)
- `Makefile` (the current docker-only Makefile; keeps `make update
  REGISTRY=ghcr|local`, `plugins`, `backup`, `restore`, etc.)

Other changes:

- Keep `.dockerignore` at the repo root (the build-context root); adjust as
  needed for the new layout.
- `.goreleaser.yaml`: set `dockers_v2.dockerfile: deployments/docker/Dockerfile`.
- `scripts/build-armv6.sh`: update the `Dockerfile` path it copies.
- Delete the root `Makefile`.
- `mise.toml` stays the dev entrypoint and needs no change (the dotenv loader
  falls back to `./.env`).

## 2. Service management

New `internal/service` package built on `github.com/kardianos/service` (v1.3).

`service.Config`:

- `Name: "nanoflux"`
- `DisplayName`, `Description`
- `Executable`: resolved `os.Executable()` at install time
- `Arguments: ["server"]`
- `WorkingDirectory`: the data dir (so relative NF_DB/NF_FILE_STORE defaults land
  there)
- `Option: { UserService: true, Restart: "always", LimitNOFILE: 65535,
  SystemdScript: <custom template> }`

Custom Linux `SystemdScript` (the kardianos default is wrong for user units):

- `[Unit]`: `Description`, `After=network-online.target`,
  `Wants=network-online.target`
- `[Service]`: `ExecStart={<exe>} server`, `WorkingDirectory`, `Restart=always`,
  `RestartSec=3`, `LimitNOFILE`, no `User=`
- `[Install]`: `WantedBy=default.target`

On non-Linux platforms, fall back to the kardianos defaults.

`install` must also:

- Create `~/.config/nanoflux`, `~/.local/share/nanoflux`, and
  `~/.local/share/nanoflux/plugins`.
- Write a commented config template at
  `~/.config/nanoflux/nanoflux.env` (mode 0600) if absent.
- Best-effort run `loginctl enable-linger $USER` so the service starts at boot
  without an interactive login.
- Enable the unit and `daemon-reload`.

`ExecStart` is plain `nanoflux server`; the server's existing
`signal.NotifyContext` handling is reused, so `svc.Run` is not used and there is
no double signal handling.

Add `--dry-run` to print the generated unit without touching systemd (used by
tests and for inspection).

### Command surface

Top-level verbs:

```
nanoflux install
nanoflux uninstall [--purge]
nanoflux start
nanoflux stop
nanoflux restart
nanoflux status
nanoflux logs
nanoflux update
```

- `logs`: shell to `journalctl --user -u nanoflux -f` on Linux; otherwise print
  the log location.
- `status`: service state plus the running version.
- `server` remains the foreground run (systemd `ExecStart`).

## 3. Native configuration

Load a dotenv file before `config.Load()`. Resolution order (first existing
wins; a missing file is fine; values already in the environment are never
overridden):

1. `--config <path>`
2. `NF_CONFIG`
3. `./.env` (dev convenience)
4. `$XDG_CONFIG_HOME/nanoflux/nanoflux.env` (service default;
   `~/.config/nanoflux/nanoflux.env`)
5. `/etc/nanoflux/nanoflux.env`

Use `github.com/joho/godotenv` (or a tiny internal parser). `godotenv.Load` does
not override existing env by default, which matches requirement 4.

Relative defaults (`./data/rss.db`, `./plugins`) are kept; the service's
`WorkingDirectory` makes them resolve under the data dir. No config-default
changes are needed.

## 4. `nanoflux update`

- Current version comes from the ldflags `main.version`.
- Current binary: `filepath.EvalSymlinks(os.Executable())` so package-manager
  symlinks are handled.
- Query the GitHub latest release. If it equals the current version, exit "up to
  date" unless `--force`.
- Download the matching `nanoflux_<ver>_<os>_<arch>.tar.gz` and `checksums.txt`
  (HTTPS only), verify sha256, extract the binary, and atomically `rename` it
  over the executable.
- `svc.Restart()` (or `systemctl --user restart nanoflux`); if the service is
  not installed, replace the binary and tell the user to restart.
- Flags: `--check` (report only), `--force`, `--version vX`, `--restart-only`.
- Dev flow: `go install ./cmd/server` then `nanoflux restart` (or
  `nanoflux update --restart-only`).
- The release lookup/download is behind a small interface so it can be unit
  tested with a fake.

## 5. Release packaging

- `.goreleaser.yaml`: add an `archives` block for `linux`/`darwin` x
  `amd64`/`arm64` as tar.gz, plus a `checksum`. Keep `dockers_v2` unchanged
  except the `dockerfile` path.
- Update the release footer (currently says "no binaries are published") to
  describe the native archives and the install path.
- Add `scripts/install.sh` (`curl -fsSL .../install.sh | sh`) that fetches the
  latest release, verifies the checksum, and installs to `~/.local/bin`.

## 6. Docs

- `README.md`: split install into **Native (recommended, systemd)** and
  **Docker**; document `nanoflux install/start/status/update` and the
  `~/.config/nanoflux` / `~/.local/share/nanoflux` layout. The Docker section
  points at `deployments/docker/` and `make`.
- `.env.example`: document `NF_CONFIG`.
- `AGENTS.md`: update the "mise is for development, make is for selfhosting an
  instance" line to reflect the CLI-managed native service and the docker
  Makefile under `deployments/docker/`.

## Tests and verification

Unit tests:

- Dotenv resolution order and non-override behavior.
- Service config construction and generated unit via `install --dry-run`.
- `update`: version comparison, asset-name resolution, `checksums.txt` parse and
  verify, atomic replace into a temp dir (fake release client).

Commands:

- `go build ./...`
- `go test ./...`
- `go vet ./...`

Manual smoke (opt-in, not automated, mutates the user's systemd):
`nanoflux install --dry-run`, then a real install/start/status/logs/update.

## Risks and caveats

- **systemd user units**: kardianos' default template hardcodes
  `WantedBy=multi-user.target` and `RestartSec=120`; the custom `SystemdScript`
  must override both, and `loginctl enable-linger` is required for boot-start
  without login.
- **Self-replace permissions**: a per-user install in `~/.local/bin` is writable
  by the user, so `update` needs no sudo; a system install would.
- **Dev vs release version**: `go run` reports `dev`, so `update` always sees a
  newer release. Use `restart` in dev and `update` on installed instances.
- **Docker paths after the move**: anchored to the repo root via `../..`;
  verify `make start` from `deployments/docker/` and the goreleaser docker build
  both still work.

## Implementation order

1. Repo move plus goreleaser, `build-armv6.sh`, and `.dockerignore` updates.
2. Dotenv loading.
3. `internal/service` plus install/uninstall/start/stop/restart/status/logs.
4. `nanoflux update` plus release archives and `install.sh`.
5. Docs and tests.
</content>
