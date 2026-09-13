VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || true)
ifeq ($(strip $(VERSION)),)
VERSION := 0.0.0-dev
endif
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
# Comma-separated base64 Ed25519 public keys (CI: repository variable OPENLOG_RELEASE_PUBLIC_KEYS).
OPENLOG_RELEASE_PUBLIC_KEYS ?=

MOD := github.com/onuragtas/openlog/agents/infra
LDFLAGS := -s -w \
	-X $(MOD)/internal/version.Version=$(VERSION) \
	-X $(MOD)/internal/version.Commit=$(COMMIT) \
	-X $(MOD)/internal/version.Date=$(DATE) \
	-X $(MOD)/internal/release.trustedKeys=$(OPENLOG_RELEASE_PUBLIC_KEYS)
PKG := ./cmd/openlog-infra-agent
DIST := dist
# The module replaces libs/release with ../../libs/release: Docker builds need the repo root.
REPO_ROOT := $(abspath $(CURDIR)/../..)

.PHONY: all build build-linux test vet fmt validate-rules test-linux test-update-e2e docker clean

all: vet test build-linux

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/openlog-infra-agent $(PKG)

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/openlog-infra-agent-linux-amd64 $(PKG)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/openlog-infra-agent-linux-arm64 $(PKG)

test:
	go test ./...

vet:
	go vet ./...
	GOOS=linux go vet ./...

fmt:
	gofmt -w cmd internal rules test

validate-rules: build
	$(DIST)/openlog-infra-agent -validate-rules -config /dev/null

# Run the test suite inside a Linux container (as an unprivileged user so permission tests run).
test-linux:
	docker run --rm -v "$(REPO_ROOT)":/src -v openlog-gomod:/go/pkg/mod -w /src/agents/infra golang:1.26 \
		sh -c 'go test ./... && GOFLAGS=-buildvcs=false go vet ./...'

# Self-update scenario in a Debian 12 container (see the header of test/update/run.sh).
test-update-e2e:
	./test/update/run.sh

docker:
	docker build -f Dockerfile --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) \
		--build-arg OPENLOG_RELEASE_PUBLIC_KEYS=$(OPENLOG_RELEASE_PUBLIC_KEYS) \
		-t openlog/openlog-infra-agent:$(VERSION) $(REPO_ROOT)

clean:
	rm -rf $(DIST)
