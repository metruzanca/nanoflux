RUNTIME ?= $(shell command -v docker >/dev/null 2>&1 && echo docker || (command -v podman >/dev/null 2>&1 && echo podman || echo docker))
COMPOSE := $(RUNTIME) compose

GHCR_REPO := metruzanca/nanoflux
GHCR_IMAGE := ghcr.io/$(GHCR_REPO)

# The image is published under a concrete version tag (goreleaser strips the
# leading "v" from the git tag, so release v0.5.0 -> image 0.5.0) alongside
# "latest". Pulling the concrete version instead of "latest" busts the local
# Docker cache, which otherwise keeps serving a stale "latest" manifest.
#
# Recreating the container matters as much as the retag: compose (podman-compose
# included) decides whether to recreate a service by comparing the image
# reference *string*, not the resolved image id, so retagging "latest" alone
# leaves the old container running. update therefore forces a recreate.
latest_image_tag = $(shell \
	curl -fsSL "https://api.github.com/repos/$(GHCR_REPO)/releases/latest" 2>/dev/null \
	| sed -n 's/.*"tag_name": *"\(v[0-9][^"]*\)".*/\1/p' \
	| sed 's/^v//')

.DEFAULT_GOAL := help

.PHONY: help start stop restart update icons extension status logs shell version backup restore down

help:
	@echo "nanoflux - manage your instance"
	@echo ""
	@echo "  start     start the app (pulls the image, creates .env on first run)"
	@echo "  stop      shut the app down (containers and data kept)"
	@echo "  restart   restart the app"
	@echo "  update    pull the latest release image (by version) and redeploy"
	@echo "  status    show container status"
	@echo "  logs      tail the app logs"
	@echo "  shell     open a shell in the app container"
	@echo "  version   print the running app's version"
	@echo "  backup    snapshot the database and file store into backups/"
	@echo "  restore   restore from backups/ (usage: make restore ARCHIVE=backups/<file>.tar.gz)"
	@echo "  down      stop and remove containers (data kept)"
	@echo "  extension package the browser extension into dist/"
	@echo "  icons     regenerate the pwa + extension icons"
	@echo ""
	@echo "Run with podman: make <cmd> RUNTIME=podman"

start:
	@if [ ! -f .env ]; then \
		PW="$$(openssl rand -hex 24 2>/dev/null || od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"; \
		printf 'NF_ADMIN_USER=admin\nNF_ADMIN_PASS=%s\n' "$$PW" > .env; \
		echo "created .env - log in as admin with password $$PW"; \
	fi
	$(COMPOSE) up -d

stop:
	$(COMPOSE) stop

restart:
	$(COMPOSE) restart

update:
	@OLD="$$($(COMPOSE) exec -T nanoflux nanoflux version 2>/dev/null | tr -d '[:space:]')"; \
	TAG="$(latest_image_tag)"; \
	if [ -z "$$TAG" ]; then \
		echo "could not determine the latest release from GitHub; falling back to 'latest'"; \
		$(COMPOSE) pull; \
	else \
		echo "updating to $(GHCR_IMAGE):$$TAG"; \
		$(RUNTIME) pull $(GHCR_IMAGE):$$TAG; \
		$(RUNTIME) tag $(GHCR_IMAGE):$$TAG $(GHCR_IMAGE):latest; \
	fi; \
	$(COMPOSE) up -d --force-recreate; \
	NEW="$$(for i in $$(seq 1 30); do \
		V="$$($(COMPOSE) exec -T nanoflux nanoflux version 2>/dev/null | tr -d '[:space:]')"; \
		if [ -n "$$V" ]; then echo "$$V"; break; fi; \
		sleep 1; \
	done)"; \
	echo "version: $${OLD:-unknown} -> $${NEW:-unknown}"

status:
	$(COMPOSE) ps

icons:
	go run ./tools/iconsgen

# Package the browser extension for load-unpacked. The app also serves these
# files as a zip from /settings/extension.zip, built from the embedded copy.
extension:
	go run ./tools/extzip

logs:
	$(COMPOSE) logs -f

shell:
	$(COMPOSE) exec nanoflux sh

# Print the running container's version (nanoflux version).
version:
	$(COMPOSE) exec nanoflux nanoflux version

# Snapshot the database and file store into backups/. This runs the app's own
# `nanoflux backup`, which snapshots the SQLite database with VACUUM INTO (a
# consistent copy even while the server runs in WAL mode) and includes the local
# file store. A plain tar of the live volume would be inconsistent.
#
# Both targets refuse to run unless the volumes exist, so they can never mount a
# missing name (podman would silently create an empty volume and archive
# nothing). The volume names are pinned in docker-compose.yml.
VOLUMES := nanoflux-db nanoflux-files

backup:
	@mkdir -p backups
	@for v in $(VOLUMES); do $(RUNTIME) volume exists $$v || { echo "volume $$v does not exist - start the instance first (make start)"; exit 1; }; done
	$(RUNTIME) run --rm \
		-e NF_DB=/data/rss.db \
		-e NF_FILE_STORE=/filestore \
		-v nanoflux-db:/data \
		-v nanoflux-files:/filestore:ro \
		-v $(CURDIR)/backups:/backup \
		$(GHCR_IMAGE):latest \
		backup -o /backup

restore:
	@test -n "$(ARCHIVE)" || (echo "usage: make restore ARCHIVE=backups/nanoflux-<timestamp>.tar.gz"; exit 1)
	@case "$(ARCHIVE)" in */*) echo "ARCHIVE must be a filename inside backups/"; exit 1;; esac
	@test -f "backups/$(ARCHIVE)" || (echo "backups/$(ARCHIVE) not found"; exit 1)
	@for v in $(VOLUMES); do $(RUNTIME) volume exists $$v || { echo "volume $$v does not exist"; exit 1; }; done
	$(COMPOSE) stop
	@# Extract straight into the mounted volumes with alpine rather than the app
	@# CLI: the archive holds data/rss.db + filestore/… (no bare dir entries), so
	@# extract the whole archive at the root. Stale -wal/-shm are cleared so the
	@# restored DB opens cleanly.
	$(RUNTIME) run --rm \
		-v nanoflux-db:/data \
		-v nanoflux-files:/filestore \
		-v $(CURDIR)/backups:/backup:ro \
		alpine:3.20 \
		sh -c 'set -e; \
			rm -rf /data/* /data/.[!.]* /filestore/* /filestore/.[!.]* 2>/dev/null || true; \
			tar xzf "/backup/$(ARCHIVE)" -C /; \
			rm -f /data/rss.db-wal /data/rss.db-shm; \
			test -s /data/rss.db'
	@echo "restore complete - start the instance with: make start"

down:
	$(COMPOSE) down