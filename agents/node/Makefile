# Node.js agent (@openlog/node). Every target runs inside a node container, so no local Node.js is needed
# (NODE=local runs the npm scripts directly, e.g. in CI).
NODE_VERSION ?= 22
NM_VOLUME ?= openlog-node-agent-nm
COMPOSE := docker compose -p openlog-nodeagent-test -f test/integration/docker-compose.yml

ifeq ($(NODE),local)
RUN := sh -c
else
RUN := docker run --rm -v "$(CURDIR)":/src -v $(NM_VOLUME):/src/node_modules -w /src node:$(NODE_VERSION)-alpine sh -c
endif

.PHONY: install build lint typecheck test test-integration bench fixtures clean

install:
	$(RUN) 'npm ci --no-audit --no-fund && npm run test:apps:install'

build:
	$(RUN) 'npm run build'

lint:
	$(RUN) 'npm run lint'

typecheck:
	$(RUN) 'npm run typecheck'

test:
	$(RUN) 'npm test'

# Real PostgreSQL, MySQL and Redis in compose project openlog-nodeagent-test (removed afterwards).
test-integration:
	$(COMPOSE) up -d --wait postgres mysql redis
	NODE_VERSION=$(NODE_VERSION) NODE_MODULES_VOLUME=$(NM_VOLUME) $(COMPOSE) run --rm runner; status=$$?; $(COMPOSE) down -v; exit $$status

bench:
	$(RUN) 'npm run bench'

# Regenerates test/interop/go-sampler-fixtures.json from the Go agent's sampler (needs Go).
fixtures:
	scripts/gen-go-fixtures.sh

clean:
	rm -rf dist .test-build
