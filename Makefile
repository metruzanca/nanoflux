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

.PHONY: help start stop restart update icons extension status logs shell version backup down

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
	NEW="$$($(COMPOSE) exec -T nanoflux nanoflux version 2>/dev/null | tr -d '[:space:]')"; \
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

backup:
	@mkdir -p backups
	$(RUNTIME) run --rm \
		-v nanoflux-db:/data:ro \
		-v nanoflux-files:/filestore:ro \
		-v $(CURDIR)/backups:/backup \
		alpine:3.20 \
		sh -c 'tar czf /backup/nanoflux-$$(date +%Y%m%d-%H%M%S).tar.gz -C / data filestore'

restore:
	@test -n "$(ARCHIVE)" || (echo "usage: make restore ARCHIVE=backups/nanoflux-<timestamp>.tar.gz"; exit 1)
	@case "$(ARCHIVE)" in */*) echo "ARCHIVE must be a filename inside backups/"; exit 1;; esac
	$(COMPOSE) stop
	$(RUNTIME) run --rm \
		-v nanoflux-db:/data \
		-v nanoflux-files:/filestore \
		-v $(CURDIR)/backups:/backup:ro \
		alpine:3.20 \
		sh -c 'tar xzf "/backup/$(ARCHIVE)" -C / data filestore'
	@echo "restore complete - start the instance with: make start"

down:
	$(COMPOSE) down