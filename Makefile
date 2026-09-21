RUNTIME ?= $(shell command -v docker >/dev/null 2>&1 && echo docker || (command -v podman >/dev/null 2>&1 && echo podman || echo docker))
COMPOSE := $(RUNTIME) compose

.DEFAULT_GOAL := help

.PHONY: help start stop restart update status logs shell backup down

help:
	@echo "nanoflux - manage your instance"
	@echo ""
	@echo "  start     start the app (pulls the image, creates .env on first run)"
	@echo "  stop      shut the app down (containers and data kept)"
	@echo "  restart   restart the app"
	@echo "  update    pull the latest image and redeploy"
	@echo "  status    show container status"
	@echo "  logs      tail the app logs"
	@echo "  shell     open a shell in the app container"
	@echo "  backup    snapshot the database and file store into backups/"
	@echo "  restore   restore from backups/ (usage: make restore ARCHIVE=backups/<file>.tar.gz)"
	@echo "  down      stop and remove containers (data kept)"
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
	$(COMPOSE) pull
	$(COMPOSE) up -d

status:
	$(COMPOSE) ps

logs:
	$(COMPOSE) logs -f

shell:
	$(COMPOSE) exec nanoflux sh

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