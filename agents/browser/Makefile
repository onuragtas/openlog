# Browser SDK (openlog-browser). Every target runs inside a node container, so no local Node.js is needed
# (NODE=local runs the npm scripts directly, e.g. in CI). Mirrors agents/node/Makefile.
NODE_VERSION ?= 22
NM_VOLUME ?= openlog-browser-agent-nm

ifeq ($(NODE),local)
RUN := sh -c
else
RUN := docker run --rm -v "$(CURDIR)":/src -v $(NM_VOLUME):/src/node_modules -w /src node:$(NODE_VERSION)-alpine sh -c
endif

.PHONY: install build lint typecheck test size clean

install:
	$(RUN) 'npm ci --no-audit --no-fund'

build:
	$(RUN) 'npm run build'

lint:
	$(RUN) 'npm run lint'

typecheck:
	$(RUN) 'npm run typecheck'

test:
	$(RUN) 'npm test'

# Prints the minified+gzip size of the built bundle and fails when it grows past the budget (scripts/size.mjs).
size:
	$(RUN) 'npm run build && npm run size'

clean:
	rm -rf dist .test-build
