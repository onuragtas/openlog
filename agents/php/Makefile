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

.PHONY: spike images up verify bench mem crash-test down clean \
	ext-matrix ext-asan ext-compat ext-fuzz bench-micro bench-http soak artifacts package-test

# ---- openlog.so (agents/php/ext) ----
# phpt matrix 7.1–8.4 NTS + ZTS/musl (CI php-ext job)
ext-matrix:
	bash "$(PHP_DIR)/ext/build/run-matrix.sh"

# AddressSanitizer + UBSan debug PHP 8.3 built from source; any sanitizer report fails
ext-asan:
	VERSIONS="" EXTRA="8.3-asan" bash "$(PHP_DIR)/ext/build/run-matrix.sh"

# the suite with Xdebug, Datadog ddtrace, New Relic, OPcache JIT and Swoole loaded (network access needed)
ext-compat:
	VERSIONS="" EXTRA="8.3-xdebug 8.3-ddtrace 8.3-newrelic 8.3-jit 8.3-swoole" bash "$(PHP_DIR)/ext/build/run-matrix.sh"

# libFuzzer on src/ol_text.c (FUZZ_SECONDS=60)
ext-fuzz:
	bash "$(PHP_DIR)/ext/fuzz/run.sh"

# per-request cost (php-cgi -T timing + callgrind); SRC_A=<older ext tree> for an A/B
bench-micro:
	bash "$(PHP_DIR)/bench/ext/micro/run.sh"

# PHP-FPM + nginx + wrk overhead benchmark (base / off / on / lean)
bench-http:
	bash "$(PHP_DIR)/bench/ext/http/run.sh"

# MINUTES=10 of mixed load on long-lived FPM workers: RSS growth and crashes
soak:
	bash "$(PHP_DIR)/bench/ext/soak/run.sh"

# release tarball + deb/rpm/apk for this architecture: make artifacts VERSION=0.9.1 OUT=/tmp/php-agent
artifacts:
	@[ -n "$(VERSION)" ] && [ -n "$(OUT)" ] || { echo "usage: make artifacts VERSION=x.y.z OUT=dir"; exit 2; }
	bash "$(PHP_DIR)/packaging/build-artifacts.sh" "$(VERSION)" "$(OUT)"

# install tests of the packages in debian:12, rockylinux:9, alpine:3.20
package-test:
	@[ -n "$(VERSION)" ] && [ -n "$(OUT)" ] || { echo "usage: make package-test VERSION=x.y.z OUT=dir"; exit 2; }
	sh "$(PHP_DIR)/packaging/test.sh" "$(OUT)" "$(VERSION)"

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
