# openlog PHP agent demo. Every compose call uses project openlog-php (the shared `openlog` stack is never touched).
#   make -C agents/php/demo images              # build all images (OPENLOG_EXT=0: no extension)
#   make -C agents/php/demo images OPENLOG_EXT=1 # compile agents/php/ext (openlog.so) into every PHP image
#   make -C agents/php/demo up | status | load | loop | loop-stop | logs | down | clean
SHELL := /bin/bash
DEMO_DIR := $(CURDIR)
OPENLOG_EXT ?= 0
OPENLOG_DEBUG_DUMP ?= 0
DC := docker compose -p openlog-php -f "$(DEMO_DIR)/docker-compose.yml"
APPS := mariadb postgres redis forwarder php-laravel-83 nginx-laravel-83 php-laravel-74 nginx-laravel-74 \
	php-symfony-83 nginx-symfony-83 php-wordpress-82 php-plain-71

.PHONY: images up down status load loop loop-stop logs clean

images:
	OPENLOG_EXT=$(OPENLOG_EXT) $(DC) --profile load build

# Needs the shared openlog network (compose project `openlog`, network openlog_default).
up:
	@docker network inspect $${OPENLOG_NETWORK:-openlog_default} >/dev/null 2>&1 || { \
	  echo "network $${OPENLOG_NETWORK:-openlog_default} not found: start the shared openlog first (or set OPENLOG_NETWORK)"; exit 1; }
	OPENLOG_DEBUG_DUMP=$(OPENLOG_DEBUG_DUMP) $(DC) up -d --no-build --wait $(APPS)

down:
	$(DC) --profile load down --remove-orphans

status:
	@$(DC) --profile load ps --format 'table {{.Service}}\t{{.State}}\t{{.Status}}\t{{.Ports}}'
	@echo
	@for u in 28901/health 28902/health 28903/health 28904/wp-json/demo/v1/health 28905/index.php; do \
	  printf '%-45s %s\n' "http://127.0.0.1:$$u" "$$(curl -s -o /dev/null -m 5 -w '%{http_code}' http://127.0.0.1:$$u)"; done
	@echo
	@$(DC) exec -T php-laravel-83 php -m 2>/dev/null | grep -qx openlog && echo "openlog.so loaded (php-laravel-83)" || echo "openlog.so NOT loaded (php-laravel-83)"
	@$(DC) logs --no-log-prefix --tail 200 forwarder 2>/dev/null | grep ' msg=stats ' | tail -1

load:
	bash "$(DEMO_DIR)/load.sh" --cli

# Continuous low-rate traffic from a container (profile load).
loop:
	$(DC) --profile load up -d --no-build loadgen

loop-stop:
	$(DC) --profile load rm -sf loadgen

logs:
	$(DC) logs -f --tail 50 forwarder

clean:
	$(DC) --profile load down --remove-orphans --volumes
