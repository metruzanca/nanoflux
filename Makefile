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

.PHONY: help start stop restart update status logs shell version backup restore plugins

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
	@echo "  restore   restore from backups/ and restart (usage: make restore ARCHIVE=backups/<file>.tar.gz)"
	@echo "  plugins   rebuild every plugin in plugins/ against the current pluginapi"
	@echo ""
	@echo "Run with podman: make <cmd> RUNTIME=podman"
	@echo "Development tasks (icons, extension, dev, gen) live in mise: mise tasks"

start:
	@if [ ! -f .env ]; then \
		PW="$$(openssl rand -hex 24 2>/dev/null || od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"; \
		printf 'NF_ADMIN_USER=admin\nNF_ADMIN_PASS=%s\n' "$$PW" > .env; \
		echo "created .env - log in as admin with password $$PW"; \
	fi
	@mkdir -p backups
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

logs:
	$(COMPOSE) logs -f

shell:
	$(COMPOSE) exec nanoflux sh

# Print the running container's version (nanoflux version).
version:
	$(COMPOSE) exec nanoflux nanoflux version

# Snapshot the database and file store into backups/. The volumes, mounts, and
# env come from docker-compose.yml; the app's own `nanoflux backup` does the work
# (VACUUM INTO, so the snapshot is consistent even with the server running in WAL
# mode). `compose run` works whether or not the server is up.
backup:
	@mkdir -p backups
	$(COMPOSE) run --rm -T nanoflux backup -o /backups

# Restore from an archive in backups/, then bring the instance back up. The app
# CLI validates the archive and swaps the database/file store in place; stopping
# first means a restore works whether the instance was running or already
# stopped.
restore:
	@test -n "$(ARCHIVE)" || (echo "usage: make restore ARCHIVE=backups/nanoflux-<timestamp>.tar.gz"; exit 1)
	@case "$(ARCHIVE)" in */*) echo "ARCHIVE must be a filename inside backups/"; exit 1;; esac
	@test -f "backups/$(ARCHIVE)" || (echo "backups/$(ARCHIVE) not found"; exit 1)
	$(COMPOSE) stop
	$(COMPOSE) run --rm -T nanoflux restore /backups/$(ARCHIVE)
	$(COMPOSE) up -d
	@echo "restore complete - instance is back up"

# Build every plugin module in plugins/ into plugins/. Each subdirectory with a
# go.mod is one plugin; the binary is named nanoflux-plugin-<dir>, matching the
# name the plugin ships under and overwriting any previous build. Run this before
# `make update` so the container picks up binaries built against the current
# pluginapi.
plugins:
	@for mod in plugins/*/go.mod; do \
		dir=$$(dirname "$$mod"); \
		name=$$(basename "$$dir"); \
		case "$$name" in nanoflux-plugin-*) out="$$name";; *) out="nanoflux-plugin-$$name";; esac; \
		echo "building $$out"; \
		go -C "$$dir" build -o "../$$out" . || exit 1; \
	done
