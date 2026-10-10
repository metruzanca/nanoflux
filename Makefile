RUNTIME ?= $(shell command -v docker >/dev/null 2>&1 && echo docker || (command -v podman >/dev/null 2>&1 && echo podman || echo docker))
COMPOSE := $(RUNTIME) compose

GHCR_REPO := metruzanca/nanoflux
GHCR_IMAGE := ghcr.io/$(GHCR_REPO)

# Where the image comes from. `local` (the default) builds it from this checkout
# with Dockerfile.local, so `make start` works on a fresh clone with no release
# needed. `ghcr` uses the published image instead:
#   make start REGISTRY=ghcr   /   make update REGISTRY=ghcr
# (For raw `docker compose` outside this Makefile, NANOFLUX_IMAGE selects the
# image directly and defaults to nanoflux:local.)
REGISTRY ?= local
ifeq ($(REGISTRY),ghcr)
IMAGE := $(GHCR_IMAGE):latest
else
IMAGE := nanoflux:local
endif

# The running version baked into a local build: the nearest tag, else a short
# SHA. Exported so docker-compose passes it to the build as NANOFLUX_VERSION.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
export NANOFLUX_IMAGE := $(IMAGE)
export NANOFLUX_VERSION := $(VERSION)

.DEFAULT_GOAL := help

.PHONY: help build start stop restart update status logs shell version backup restore plugins

help:
	@echo "nanoflux - manage your instance"
	@echo ""
	@echo "  build     build the image from this checkout (local registry, the default)"
	@echo "  start     build if needed and start the app (creates .env on first run)"
	@echo "  stop      shut the app down (containers and data kept)"
	@echo "  restart   restart the app"
	@echo "  update    rebuild from source and redeploy (local), or pull the latest release (REGISTRY=ghcr)"
	@echo "  status    show container status"
	@echo "  logs      tail the app logs"
	@echo "  shell     open a shell in the app container"
	@echo "  version   print the running app's version"
	@echo "  backup    snapshot the database and file store into backups/"
	@echo "  restore   restore from backups/ and restart (usage: make restore ARCHIVE=backups/<file>.tar.gz)"
	@echo "  plugins   rebuild every plugin in plugins/ against the current pluginapi"
	@echo ""
	@echo "Run with podman: make <cmd> RUNTIME=podman"
	@echo "Use the published image instead of a local build: make <start|update> REGISTRY=ghcr"
	@echo "Development tasks (icons, extension, dev, gen) live in mise: mise tasks"

# Build the image from source (Dockerfile.local), tagged nanoflux:local. Only
# meaningful for the local registry; REGISTRY=ghcr pulls the published image.
build:
	@if [ "$(REGISTRY)" = ghcr ]; then \
		echo "REGISTRY=ghcr uses the published image; run '$(COMPOSE) pull'"; exit 1; \
	fi
	$(COMPOSE) build

start:
	@if [ ! -f .env ]; then \
		printf '# nanoflux environment (see .env.example for the full list)\n' > .env; \
		echo "created .env - the first account you sign up at http://localhost:8080 becomes the admin"; \
	fi
	@mkdir -p backups
	@if [ "$(REGISTRY)" = ghcr ]; then \
		echo "starting from published image $(IMAGE)"; \
		$(COMPOSE) pull; \
		$(COMPOSE) up -d; \
	else \
		echo "building $(IMAGE) from source and starting"; \
		$(COMPOSE) up -d --build; \
	fi

stop:
	$(COMPOSE) stop

restart:
	$(COMPOSE) restart

# local: pull the current checkout, rebuild the image, redeploy.
# ghcr:  fetch the newest release tag, pull, retag latest, redeploy.
update:
	@OLD="$$($(COMPOSE) exec -T nanoflux nanoflux version 2>/dev/null | tr -d '[:space:]')"; \
	if [ "$(REGISTRY)" = ghcr ]; then \
		TAG="$$(curl -fsSL "https://api.github.com/repos/$(GHCR_REPO)/releases/latest" 2>/dev/null \
			| sed -n 's/.*"tag_name": *"\(v[0-9][^"]*\)".*/\1/p' | sed 's/^v//')"; \
		if [ -z "$$TAG" ]; then \
			echo "could not determine the latest release from GitHub; falling back to 'latest'"; \
			$(COMPOSE) pull; \
		else \
			echo "updating to $(GHCR_IMAGE):$$TAG"; \
			$(RUNTIME) pull $(GHCR_IMAGE):$$TAG; \
			$(RUNTIME) tag $(GHCR_IMAGE):$$TAG $(GHCR_IMAGE):latest; \
		fi; \
	else \
		echo "pulling source and rebuilding $(IMAGE)"; \
		git pull --ff-only || echo "warning: git pull failed; building the current checkout"; \
		V="$$(git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')"; \
		NANOFLUX_VERSION="$$V" $(COMPOSE) build --pull; \
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
	@test -n "$(ARCHIVE)" || (echo "usage: make restore ARCHIVE=backups/nanoflux-<timestamp>-<version>.tar.gz"; exit 1)
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
#
# Plugins run inside the Alpine/musl container, so they must be static: CGO
# would link against the host's libc (a Nix store glibc path on NixOS), and the
# container's exec would then fail with "no such file or directory" because that
# interpreter does not exist there. GOOS=linux pins the target for hosts that
# build for another OS.
plugins:
	@for mod in plugins/*/go.mod; do \
		dir=$$(dirname "$$mod"); \
		name=$$(basename "$$dir"); \
		case "$$name" in nanoflux-plugin-*) out="$$name";; *) out="nanoflux-plugin-$$name";; esac; \
		echo "building $$out"; \
		GOOS=linux CGO_ENABLED=0 go -C "$$dir" build -o "../$$out" . || exit 1; \
	done
