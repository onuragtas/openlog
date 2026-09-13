# openlog PHP agent spike (M2, D-034). Everything runs in Docker under compose project openlog-phpspike.
#   make -C agents/php spike   # build, start, verify traces, benchmark, failure tests, tear down
SHELL := /bin/bash
# Run as `make -C agents/php …`: CURDIR keeps paths with spaces intact (abspath/dir would split them).
PHP_DIR := $(CURDIR)
ROOT := $(CURDIR)/../..
# Explicit project name on every compose call: the shared openlog stack (project `openlog`) is never touched.
DC := docker compose -p openlog-phpspike -f "$(PHP_DIR)/spike/docker-compose.spike.yml"
APPS := mariadb redis php-base nginx-base php-a nginx-a forwarder php-b nginx-b
OPENLOG_ADMIN ?= http://127.0.0.1:9464

.PHONY: spike images up verify bench mem crash-test down clean

spike: images up verify bench mem crash-test down

images:
	docker build -t openlog-phpspike/forwarder:dev "$(PHP_DIR)/prototype-a/forwarder"
	docker build -t openlog-phpspike/app:dev -f "$(PHP_DIR)/spike/app.Dockerfile" "$(PHP_DIR)"

# Requires the shared local openlog to be running (deploy/compose, project `openlog`); this spike does not start it.
up:
	@curl -fsS -m 5 "$(OPENLOG_ADMIN)/readyz" >/dev/null || { echo "shared openlog not ready at $(OPENLOG_ADMIN)"; exit 1; }
	$(DC) up -d --wait $(APPS)

verify:
	bash "$(PHP_DIR)/bench/verify.sh"

bench:
	bash "$(PHP_DIR)/bench/run.sh"

mem:
	bash "$(PHP_DIR)/bench/mem.sh"

crash-test:
	bash "$(PHP_DIR)/bench/crash-test.sh"

down:
	$(DC) --profile bench down --remove-orphans
	-docker volume rm openlog-phpspike_php-sock

clean: down
	-docker image rm openlog-phpspike/app:dev openlog-phpspike/forwarder:dev
