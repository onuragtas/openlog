SHELL := /bin/bash
IMAGE ?= openlog:dev
# The shared local stack is always project `openlog` (agents and demos join its network `openlog_default`).
# Test and demo tooling must pass its own -p when it reuses deploy/compose/docker-compose.yml.
COMPOSE := docker compose -p openlog -f deploy/compose/docker-compose.yml --env-file deploy/compose/.env.example
BIN := bin

# ---------------------------------------------------------------------------------------------
# Version injection (docs/contracts/releases-updates.md §1-2). Every Go binary gets
# Version/Commit/Date and the release signing public keys it trusts.
# ---------------------------------------------------------------------------------------------
VERSION ?= 0.0.0-dev
ifeq ($(origin COMMIT),undefined)
COMMIT := $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
endif
ifeq ($(origin DATE),undefined)
DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
endif
# Comma-separated base64 Ed25519 public keys (public; CI: repository variable).
OPENLOG_RELEASE_PUBLIC_KEYS ?=

# `make release-testkeys` once, then RELEASE_TESTKEYS=1 uses dist/testkeys/ for signing and trust.
DIST ?= dist
ifeq ($(RELEASE_TESTKEYS),1)
OPENLOG_RELEASE_SIGNING_KEY := $(shell cat $(DIST)/testkeys/signing.key 2>/dev/null)
OPENLOG_RELEASE_PUBLIC_KEYS := $(shell cat $(DIST)/testkeys/public.key 2>/dev/null)
endif
# The signing seed only reaches the tool through the environment, never the command line.
export OPENLOG_RELEASE_SIGNING_KEY

GO_MODULE := github.com/onuragtas/openlog
AGENT_MODULE := $(GO_MODULE)/agents/infra
VERSION_LDFLAGS := -X $(GO_MODULE)/internal/version.Version=$(VERSION) \
	-X $(GO_MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(GO_MODULE)/internal/version.Date=$(DATE) \
	-X $(GO_MODULE)/internal/release.trustedKeys=$(OPENLOG_RELEASE_PUBLIC_KEYS)
# The agent: internal/version + internal/release of its own module.
AGENT_LDFLAGS := -s -w \
	-X $(AGENT_MODULE)/internal/version.Version=$(VERSION) \
	-X $(AGENT_MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(AGENT_MODULE)/internal/version.Date=$(DATE) \
	-X $(AGENT_MODULE)/internal/release.trustedKeys=$(OPENLOG_RELEASE_PUBLIC_KEYS)

GOFMT_DIRS := cmd internal schema migrations libs test web/embed.go agents/infra/cmd agents/infra/internal agents/infra/rules

.PHONY: all build test lint vet fmt fmt-check docker compose-up compose-down compose-logs loadgen clean

all: lint test build

build:
	@mkdir -p $(BIN)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(VERSION_LDFLAGS)" -o $(BIN)/ ./cmd/...

test:
	go test ./...

lint: vet fmt-check

fmt-check:
	@unformatted=$$(gofmt -l $(GOFMT_DIRS)); if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi

vet:
	go vet ./...

fmt:
	gofmt -w $(GOFMT_DIRS)

docker:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) \
		--build-arg OPENLOG_RELEASE_PUBLIC_KEYS=$(OPENLOG_RELEASE_PUBLIC_KEYS) -t $(IMAGE) .

compose-up:
	$(COMPOSE) up -d --build --wait

compose-down:
	$(COMPOSE) down -v

compose-logs:
	$(COMPOSE) logs -f openlog

# Send load to a local stack: make loadgen ARGS="-hosts 50 -duration 1m"
loadgen:
	go run ./cmd/openlog-loadgen -endpoint http://localhost:4318 -license-key dev-license-key $(ARGS)

clean:
	rm -rf $(BIN)

# End-to-end test: infra agent on Debian target hosts -> compose `single` stack (docs/operations/e2e.md).
# E2E_KEEP=1 keeps the stack; E2E_SKIP_OUTAGE=1 skips the outage phases; E2E_UI=1 adds the Playwright UI phase.
.PHONY: e2e tlstest
e2e:
	go test -tags e2e -v -count=1 -timeout 45m ./test/e2e

# TLS/SASL integration test: Kafka SSL+SASL_SSL, 2-shard ClickHouse on the secure port, PostgreSQL
# verify-full, certificates generated at test time (compose project openlog-tlstest).
tlstest:
	go test -tags tlstest -v -count=1 -timeout 30m ./test/e2e/tlstest

# Tiered storage test: MinIO + 2-shard x 2-replica ClickHouse with the storage policy of deploy/compose/clickhouse,
# moves, reads, deletion, restart, S3 outage (compose project openlog-tiered; docs/operations/tiered-storage.md).
.PHONY: tieredtest
tieredtest:
	cd test/e2e/tieredtest && go test -tags tieredtest -v -count=1 -timeout 30m .

# Web UI (requires Node >= 20.19). `build` does not need Node: without `make web`
# the binaries embed the placeholder web/dist/index.html.
.PHONY: web web-test
web:
	cd web && npm ci --no-audit --no-fund && npm run build

web-test:
	cd web && npm run typecheck && npm run lint && npm test

# ---------------------------------------------------------------------------------------------
# Releases (docs/operations/releasing.md)
#
#   make release-testkeys
#   make release-local VERSION=0.9.0 RELEASE_TESTKEYS=1 RELEASE_BASE_URL=http://127.0.0.1:18090
#
# Output: $(DIST)/v$(VERSION)/ (artifacts, manifest.json(.sig), index.json(.sig), install.sh, install-server.sh) and
# $(DIST)/index.json(.sig) covering every $(DIST)/v*/manifest.json.
# ---------------------------------------------------------------------------------------------
# Releases root; artifacts are served from $(RELEASE_BASE_URL)/v$(VERSION)/<name>.
RELEASE_BASE_URL ?= https://github.com/onuragtas/openlog/releases/download
RELEASE_NOTES_URL ?= https://github.com/onuragtas/openlog/releases/tag/v$(VERSION)
# Empty: beta for pre-releases, stable otherwise.
RELEASE_CHANNEL ?=
# Space-separated key=version compatibility overrides, e.g. "min_upgrade_from=0.3.0".
RELEASE_COMPAT ?=
# ghcr.io/onuragtas/openlog@sha256:… (set by CI after pushing the image).
RELEASE_IMAGE ?=
# Extra build-index arguments, e.g. "--entry 0.3.0=https://…/v0.3.0/manifest.json".
RELEASE_INDEX_ARGS ?=
RELEASE_ARCHES ?= amd64 arm64
# 1: fail instead of skipping the charts when neither helm nor Docker is available (CI).
RELEASE_REQUIRE_HELM ?= 0
HELM ?= helm
# release-helm runs helm from this image when $(HELM) is not installed.
HELM_IMAGE ?= alpine/helm:3.17.3@sha256:d899e6316789fec04ee95300a18e454b7942539cbb3d89bde3e0655d6ca2e895
# Charts packaged by release-helm as <chart>-<version>.tgz (manifest helm_charts, releasing.md).
RELEASE_HELM_CHARTS ?= openlog openlog-agent
NFPM_IMAGE ?= goreleaser/nfpm:v2.47.0@sha256:a662cb167d7b6d3a83920c83d76b12d02b8ac5dd2c13e5c62c15270b23f6df0c
NFPM ?= docker run --rm --user $$(id -u):$$(id -g) -v "$(CURDIR)":/work -w /work $(NFPM_IMAGE)
RELEASE_SERVE_PORT ?= 18090

RELEASE_DIR := $(DIST)/v$(VERSION)
RELEASE_STAGE := $(DIST)/.stage/v$(VERSION)
RELEASE_TOOL := $(BIN)/openlog-release
BACKEND_CMDS := $(filter-out openlog-release openlog-loadgen,$(notdir $(wildcard cmd/openlog-*)))
RELEASE_MANIFEST_ARGS = --version $(VERSION) --released-at $(DATE) \
	--base-url "$(RELEASE_BASE_URL)/v$(VERSION)" --notes-url "$(RELEASE_NOTES_URL)" \
	$(if $(RELEASE_CHANNEL),--channel $(RELEASE_CHANNEL)) \
	$(foreach c,$(RELEASE_COMPAT),--compat $(c)) \
	$(if $(RELEASE_IMAGE),--image openlog=$(RELEASE_IMAGE)) \
	--migrations postgres=migrations/postgres --migrations clickhouse=schema/clickhouse

.PHONY: release-local release-tool release-testkeys release-check release-agent release-packages \
	release-backend release-helm release-manifest release-index release-serve release-clean

# Go agent modules (docs/operations/releasing.md "Go agent modules"). Before tagging vX.Y.Z:
#   make release-prepare VERSION=X.Y.Z   bumps agents/go/version.go + in-repo requires; commit the result
#   make go-agent-release-check VERSION=X.Y.Z   what release.yml enforces on the tagged commit
#   make go-agent-verify                  builds modules without replace against a local proxy of the tree
.PHONY: release-prepare go-agent-release-check go-agent-verify
release-prepare:
	scripts/go-agent-release.sh prepare "$(VERSION)"

go-agent-release-check:
	scripts/go-agent-release.sh check "$(VERSION)"

go-agent-verify:
	scripts/go-agent-release.sh verify

release-local: release-check release-agent release-packages release-backend release-helm release-manifest release-index
	@rm -rf $(RELEASE_STAGE)
	@echo "release $(VERSION) ready in $(RELEASE_DIR)"; ls -l $(RELEASE_DIR)

# PHP agent artifacts (openlog-php-agent_<v>_linux_<arch>.tar.gz|deb|rpm|apk, php-agent.md §7.1) for the Docker host's
# architecture, into $(RELEASE_DIR) before release-local (which puts every artifact found there into the manifest).
# release.yml builds all 36 modules on native amd64/arm64 runners; locally pick a subset, e.g.
#   make release-php-agent VERSION=0.9.0 PHP_TARGETS="8.2-nts-glibc" PHP_PACKAGES=""
PHP_TARGETS ?=
PHP_PACKAGES ?= deb rpm apk
.PHONY: release-php-agent
release-php-agent:
	@mkdir -p $(RELEASE_DIR)
	TARGETS="$(PHP_TARGETS)" PACKAGES="$(PHP_PACKAGES)" agents/php/packaging/build-artifacts.sh "$(VERSION)" "$(RELEASE_DIR)"

# Java agent jar (openlog-javaagent-<v>.jar + .sha256, component java-agent, format jar) into $(RELEASE_DIR) before
# release-local. Gradle runs in eclipse-temurin:21-jdk; JAVA_BUILD=local uses the host JDK (release.yml, CI dry run).
.PHONY: release-java-agent
release-java-agent:
	agents/java/scripts/release-jar.sh "$(VERSION)" "$(RELEASE_DIR)"

# Language agent packages into $(RELEASE_DIR) before release-local: openlog-node-<v>.tgz (component node-agent),
# openlog_agent-<pep440>-py3-none-any.whl + sdist (python-agent), OpenLog.Agent.<v>.nupkg (dotnet-agent), each + .sha256.
# Builds run in node:22-alpine, python:3.12-slim and dotnet/sdk:8.0; RUN_TESTS=0 skips the agents' tests.
.PHONY: release-language-agents
release-language-agents:
	agents/node/scripts/release-pack.sh "$(VERSION)" "$(RELEASE_DIR)"
	agents/python/scripts/release-dist.sh "$(VERSION)" "$(RELEASE_DIR)"
	agents/dotnet/scripts/release-nupkg.sh "$(VERSION)" "$(RELEASE_DIR)"

release-tool:
	@mkdir -p $(BIN)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(VERSION_LDFLAGS)" -o $(RELEASE_TOOL) ./cmd/openlog-release

# Throwaway key pair for local and CI test releases. NEVER use it for a real release.
release-testkeys: release-tool
	@mkdir -p $(DIST)/testkeys && chmod 700 $(DIST)/testkeys
	@if [ -s $(DIST)/testkeys/signing.key ]; then echo "keeping existing $(DIST)/testkeys"; else \
		umask 077 && $(RELEASE_TOOL) keygen > $(DIST)/testkeys/release.env && \
		sed -n 's/^OPENLOG_RELEASE_PUBLIC_KEYS=//p' $(DIST)/testkeys/release.env > $(DIST)/testkeys/public.key && \
		sed -n 's/^OPENLOG_RELEASE_SIGNING_KEY=//p' $(DIST)/testkeys/release.env > $(DIST)/testkeys/signing.key && \
		echo "wrote $(DIST)/testkeys/{release.env,public.key,signing.key} (test only)"; fi
	@echo "public key: $$(cat $(DIST)/testkeys/public.key)"

release-check:
	@[[ "$(VERSION)" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$$ ]] || \
		{ echo "VERSION=$(VERSION): want SemVer without a leading v, e.g. 0.9.0 or 0.10.0-beta.1"; exit 1; }
	@[ -n "$$OPENLOG_RELEASE_SIGNING_KEY" ] || { echo "OPENLOG_RELEASE_SIGNING_KEY is empty. For a test release: make release-testkeys, then add RELEASE_TESTKEYS=1 (docs/operations/releasing.md)"; exit 1; }
	@[ -n "$(OPENLOG_RELEASE_PUBLIC_KEYS)" ] || { echo "OPENLOG_RELEASE_PUBLIC_KEYS is empty: binaries without trusted keys cannot update themselves"; exit 1; }
	@mkdir -p $(RELEASE_DIR) $(RELEASE_STAGE)

# Agent tarballs: openlog-infra-agent_<v>_linux_<arch>/{openlog-infra-agent,LICENSE,README.md,packaging/}
release-agent: release-tool
	@set -euo pipefail; mkdir -p $(RELEASE_DIR); for arch in $(RELEASE_ARCHES); do \
		name=openlog-infra-agent_$(VERSION)_linux_$$arch; stage="$(RELEASE_STAGE)/$$name"; \
		rm -rf "$$stage"; mkdir -p "$$stage"; \
		echo "building $$name"; \
		( cd agents/infra && CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath \
			-ldflags "$(AGENT_LDFLAGS)" -o "$(CURDIR)/$$stage/openlog-infra-agent" ./cmd/openlog-infra-agent ); \
		cp agents/infra/LICENSE agents/infra/README.md "$$stage/"; \
		cp -R agents/infra/packaging "$$stage/packaging"; \
		$(RELEASE_TOOL) archive --out "$(RELEASE_DIR)/$$name.tar.gz" --prefix "$$name" "$$stage"; \
	done

# .deb/.rpm from the staged agent directories (needs release-agent and the signing key).
# The packages carry a signed manifest of the same version in versions/<v>/ (the agent needs it for
# rollback_floor). It cannot be the final manifest.json, which contains the packages' own sha256, so
# it lists the agent tarballs only; everything else (version, compatibility, images, released_at) is
# identical.
release-packages: release-agent
	@set -euo pipefail; scripts="$(RELEASE_STAGE)/pkg-scripts"; rm -rf "$$scripts"; mkdir -p "$$scripts"; \
	for s in packaging/scripts/*.sh; do sed 's/@VERSION@/$(VERSION)/g' "$$s" > "$$scripts/$${s##*/}"; chmod 0755 "$$scripts/$${s##*/}"; done; \
	emb="$(RELEASE_STAGE)/embedded-manifest"; rm -rf "$$emb"; mkdir -p "$$emb"; \
	for arch in $(RELEASE_ARCHES); do cp "$(RELEASE_DIR)/openlog-infra-agent_$(VERSION)_linux_$$arch.tar.gz" "$$emb/"; done; \
	$(RELEASE_TOOL) build-manifest $(RELEASE_MANIFEST_ARGS) --dist "$$emb"; \
	$(RELEASE_TOOL) sign --key-env OPENLOG_RELEASE_SIGNING_KEY "$$emb/manifest.json"; \
	if [ -n "$${OPENLOG_RELEASE_SIGNING_KEY_2:-}" ]; then $(RELEASE_TOOL) sign --key-env OPENLOG_RELEASE_SIGNING_KEY_2 "$$emb/manifest.json"; fi; \
	$(RELEASE_TOOL) verify --keys "$(OPENLOG_RELEASE_PUBLIC_KEYS)" --check-artifacts "$$emb/manifest.json"; \
	for arch in $(RELEASE_ARCHES); do \
		name=openlog-infra-agent_$(VERSION)_linux_$$arch; config="$(RELEASE_STAGE)/nfpm-$$arch.yaml"; \
		sed -e 's|$${VERSION}|$(VERSION)|g' -e "s|\$${ARCH}|$$arch|g" \
			-e "s|\$${STAGE}|$(RELEASE_STAGE)/$$name|g" -e "s|\$${SCRIPTS}|$$scripts|g" \
			-e "s|\$${MANIFEST_DIR}|$$emb|g" \
			packaging/nfpm/infra-agent.yaml > "$$config"; \
		for fmt in deb rpm; do \
			echo "packaging $$name.$$fmt"; \
			$(NFPM) package --config "$$config" --packager $$fmt --target "$(RELEASE_DIR)/$$name.$$fmt"; \
		done; \
	done

# Backend binaries: openlog_<v>_linux_<arch>/{openlog-*,LICENSE,README.md}
release-backend: release-tool
	@set -euo pipefail; mkdir -p $(RELEASE_DIR); for arch in $(RELEASE_ARCHES); do \
		name=openlog_$(VERSION)_linux_$$arch; stage="$(RELEASE_STAGE)/$$name"; \
		rm -rf "$$stage"; mkdir -p "$$stage"; \
		echo "building $$name: $(BACKEND_CMDS)"; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "-s -w $(VERSION_LDFLAGS)" \
			-o "$$stage/" $(addprefix ./cmd/,$(BACKEND_CMDS)); \
		cp LICENSE README.md "$$stage/"; \
		$(RELEASE_TOOL) archive --out "$(RELEASE_DIR)/$$name.tar.gz" --prefix "$$name" "$$stage"; \
	done

release-helm:
	@set -euo pipefail; mkdir -p $(RELEASE_DIR); \
	out="$$(cd $(RELEASE_DIR) && pwd)"; \
	if command -v "$(HELM)" >/dev/null 2>&1; then helm=("$(HELM)"); dest="$$out"; \
	elif command -v docker >/dev/null 2>&1; then \
		helm=(docker run --rm --user "$$(id -u):$$(id -g)" -e HOME=/tmp -v "$(CURDIR)":/work:ro -v "$$out":/out -w /work $(HELM_IMAGE)); dest=/out; \
	elif [ "$(RELEASE_REQUIRE_HELM)" = 1 ]; then echo "neither helm (HELM=$(HELM)) nor docker found"; exit 1; \
	else echo "WARNING: neither helm (HELM=$(HELM)) nor docker found; the release has no Helm charts"; exit 0; fi; \
	for chart in $(RELEASE_HELM_CHARTS); do \
		rm -f "$$out/$$chart-$(VERSION).tgz"; \
		"$${helm[@]}" package "deploy/helm/$$chart" --version $(VERSION) --app-version $(VERSION) --destination "$$dest"; \
	done

release-manifest: release-tool
	@set -euo pipefail; \
	for s in install.sh install-server.sh; do cp scripts/$$s $(RELEASE_DIR)/$$s; chmod 0755 $(RELEASE_DIR)/$$s; done; \
	rm -f $(RELEASE_DIR)/manifest.json $(RELEASE_DIR)/manifest.json.sig; \
	$(RELEASE_TOOL) build-manifest $(RELEASE_MANIFEST_ARGS) --dist $(RELEASE_DIR); \
	$(RELEASE_TOOL) sign --key-env OPENLOG_RELEASE_SIGNING_KEY $(RELEASE_DIR)/manifest.json; \
	if [ -n "$${OPENLOG_RELEASE_SIGNING_KEY_2:-}" ]; then $(RELEASE_TOOL) sign --key-env OPENLOG_RELEASE_SIGNING_KEY_2 $(RELEASE_DIR)/manifest.json; fi; \
	$(RELEASE_TOOL) verify --keys "$(OPENLOG_RELEASE_PUBLIC_KEYS)" --check-artifacts $(RELEASE_DIR)/manifest.json

release-index: release-tool
	@set -euo pipefail; rm -f $(DIST)/index.json $(DIST)/index.json.sig; \
	$(RELEASE_TOOL) build-index --out $(DIST)/index.json --base-url "$(RELEASE_BASE_URL)" \
		--keys "$(OPENLOG_RELEASE_PUBLIC_KEYS)" $(RELEASE_INDEX_ARGS) $(DIST)/v*/manifest.json; \
	$(RELEASE_TOOL) sign --key-env OPENLOG_RELEASE_SIGNING_KEY $(DIST)/index.json; \
	if [ -n "$${OPENLOG_RELEASE_SIGNING_KEY_2:-}" ]; then $(RELEASE_TOOL) sign --key-env OPENLOG_RELEASE_SIGNING_KEY_2 $(DIST)/index.json; fi; \
	$(RELEASE_TOOL) verify --keys "$(OPENLOG_RELEASE_PUBLIC_KEYS)" $(DIST)/index.json; \
	cp $(DIST)/index.json $(DIST)/index.json.sig $(RELEASE_DIR)/

# Serve $(DIST) for local update/install tests: RELEASE_BASE_URL=http://<host>:$(RELEASE_SERVE_PORT)
release-serve:
	cd $(DIST) && python3 -m http.server $(RELEASE_SERVE_PORT)

release-clean:
	rm -rf $(DIST)/v* $(DIST)/.stage $(DIST)/index.json $(DIST)/index.json.sig

# Whole system from zero with Docker (docs/operations/local-stack-demo.md): signed local releases
# $(STACKDEMO_FROM)/$(STACKDEMO_TO), compose project `openlog` on the default ports, two Debian hosts
# with install.sh, agent fleet update and backend self-update, restart check. Leaves the stack running.
#   make stack-demo STACKDEMO_ARGS="--screenshots"      (or phases: STACKDEMO_ARGS="up agents")
.PHONY: stack-demo
STACKDEMO_ARGS ?=
stack-demo:
	test/stackdemo/run.sh $(STACKDEMO_ARGS)

# ---------------------------------------------------------------------------------------------
# Linters run by CI (.github/workflows/ci.yml); Docker images pinned by digest.
# ---------------------------------------------------------------------------------------------
SHELLCHECK_IMAGE ?= koalaman/shellcheck:v0.11.0@sha256:61862eba1fcf09a484ebcc6feea46f1782532571a34ed51fedf90dd25f925a8d
ACTIONLINT_IMAGE ?= rhysd/actionlint:1.7.12@sha256:b1934ee5f1c509618f2508e6eb47ee0d3520686341fec936f3b79331f9315667
KUBECONFORM_IMAGE ?= ghcr.io/yannh/kubeconform:v0.8.0@sha256:faffaf43f95aa6425306e1ab8d6fcad72acb9049158f38e574c085ea1ec0f64e
SHELL_SCRIPTS := scripts/install.sh scripts/install-server.sh scripts/go-agent-release.sh packaging/scripts/*.sh packaging/test/*.sh test/stackdemo/run.sh test/stackdemo/host/entrypoint.sh \
	agents/node/scripts/release-pack.sh agents/python/scripts/release-dist.sh agents/dotnet/scripts/release-nupkg.sh

.PHONY: shellcheck actionlint helm-lint package-test install-test
shellcheck:
	docker run --rm -v "$(CURDIR)":/mnt -w /mnt $(SHELLCHECK_IMAGE) $(SHELL_SCRIPTS)

actionlint:
	docker run --rm -v "$(CURDIR)":/repo -w /repo $(ACTIONLINT_IMAGE) -color .github/workflows/ci.yml .github/workflows/release.yml

helm-lint:
	"$(HELM)" lint deploy/helm/openlog
	"$(HELM)" template openlog deploy/helm/openlog | docker run --rm -i $(KUBECONFORM_IMAGE) \
		-strict -summary -ignore-missing-schemas -kubernetes-version 1.30.0 -
	"$(HELM)" lint deploy/helm/openlog-agent --set clusterName=ci,endpoint=https://ingest.example:4318,licenseKey=x
	"$(HELM)" template openlog-agent deploy/helm/openlog-agent --set clusterName=ci,endpoint=https://ingest.example:4318,licenseKey=x,cluster.replicas=2 \
		| docker run --rm -i $(KUBECONFORM_IMAGE) -strict -summary -ignore-missing-schemas -kubernetes-version 1.30.0 -

# Install the built .deb/.rpm in debian:12 / rockylinux/rockylinux:9 containers (needs release-local).
package-test:
	packaging/test/packages.sh $(RELEASE_DIR) $(VERSION)

# Run scripts/install.sh in debian, ubuntu, rocky and alpine containers against $(DIST) served over HTTP.
install-test:
	packaging/test/install.sh $(DIST) $(VERSION)

# Backend upgrade tests (docs/operations/upgrading.md). Both build versioned test images from the
# working tree and use their own compose projects (openlog-mixedtest: ports 28xxx,
# openlog-updtest: ports 25xxx + a local registry).
# Releases N (0.9.0) and N+1 (0.9.1) side by side; contract migrations wait for N to stop.
.PHONY: mixed-version updater-acceptance
mixed-version:
	go test -tags mixedversion -v -count=1 -timeout 30m ./test/mixedversion

# Compose auto-update: 0.9.0 -> 0.9.1 (backup, migrations, data kept) -> broken 0.9.2 rolled back.
updater-acceptance:
	go test -tags autoupdate -v -count=1 -timeout 40m ./test/autoupdate
