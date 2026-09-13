# openlog Go APM agent (Apache-2.0). Every directory with a go.mod is a separate module.
MODULES := . instrumentation/grpc instrumentation/chi instrumentation/gin instrumentation/echo examples
GOFMT_DIRS := $(shell find . -name '*.go' -not -path './vendor/*' -exec dirname {} \; | sort -u)

.PHONY: all test vet fmt fmt-check bench tidy set-version

all: fmt-check vet test

test:
	@set -e; for m in $(MODULES); do echo "== go test $$m"; (cd $$m && go test ./...); done

vet:
	@set -e; for m in $(MODULES); do echo "== go vet $$m"; (cd $$m && go vet ./... && GOOS=linux go vet ./...); done

fmt:
	gofmt -w $(GOFMT_DIRS)

fmt-check:
	@unformatted=$$(gofmt -l $(GOFMT_DIRS)); if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi

bench:
	go test -run '^$$' -bench . -benchmem ./openloghttp ./openlogsql

tidy:
	@set -e; for m in $(MODULES); do (cd $$m && go mod tidy); done

# Release tooling: rewrite the product version (D-025) before tagging agents/go/vX.Y.Z.
set-version:
	@test -n "$(VERSION)" || (echo "VERSION=x.y.z required"; exit 1)
	sed -i.bak -E 's/^const Version = ".*"/const Version = "$(VERSION)"/' version.go && rm -f version.go.bak
