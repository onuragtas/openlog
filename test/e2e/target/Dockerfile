# syntax=docker/dockerfile:1
# Target hosts for the openlog end-to-end test (test/e2e). The agent source comes from the
# named build context "agent" (agents/infra), see test/e2e/docker-compose.e2e.yml.
#
#   plain:    Debian 12 + openlog-infra-agent, no services
#   services: plain + nginx, redis-server, postgresql, a fake "mysql" client with secrets in its cmdline
#             and a private dockerd (static binaries from docker:dind) for container inventory/metrics

FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS=linux
ARG TARGETARCH
ARG AGENT_VERSION=0.1.0-e2e
WORKDIR /src
# go.mod: replace github.com/onuragtas/openlog/libs/release => ../../libs/release (= /libs/release from /src)
COPY --from=release . /libs/release
COPY --from=agent go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY --from=agent . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X github.com/onuragtas/openlog/agents/infra/internal/version.Version=${AGENT_VERSION}" -o /out/openlog-infra-agent ./cmd/openlog-infra-agent
# A long-running process that ignores its arguments; installed as /usr/local/bin/mysql so the
# MySQL-specific "-p<password>" masking rule applies to it. The same static binary is the only
# file of the e2e container image (openlog-e2e/sleeper), imported without any registry access.
COPY <<'EOF' /fake/main.go
package main

import (
	"os"
	"os/signal"
	"syscall"
)

func main() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGTERM, os.Interrupt)
	<-c
}
EOF
RUN --mount=type=cache,target=/root/.cache/go-build cd /fake && go mod init fake && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/mysql .

# Static Docker Engine binaries (dockerd, containerd, runc, ...).
FROM docker:28-dind AS dind

FROM debian:12 AS plain
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates openssl procps \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --no-create-home --shell /usr/sbin/nologin openlog-agent \
    && install -d -o openlog-agent -g openlog-agent -m 0750 /var/lib/openlog-infra-agent \
    && install -d -m 0755 /etc/openlog-infra-agent /etc/openlog-infra-agent/discovery.d /data
COPY --from=build /out/openlog-infra-agent /usr/bin/openlog-infra-agent
COPY entrypoint.sh /usr/local/bin/e2e-entrypoint
RUN chmod 0755 /usr/local/bin/e2e-entrypoint
ENTRYPOINT ["/usr/local/bin/e2e-entrypoint"]

FROM plain AS services
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends nginx redis-server postgresql \
    && rm -rf /var/lib/apt/lists/* \
    # Without try_files a missing file logs "[error] open() ... failed" to error.log (test/e2e agent_logs).
    && sed -i '/try_files/d' /etc/nginx/sites-available/default
COPY --from=build /out/mysql /usr/local/bin/mysql
COPY --from=dind /usr/local/bin/dockerd /usr/local/bin/containerd /usr/local/bin/containerd-shim-runc-v2 \
    /usr/local/bin/runc /usr/local/bin/docker /usr/local/bin/docker-init /usr/local/bin/docker-proxy /usr/local/bin/
COPY --from=build /out/mysql /opt/e2e-sleeper/sleep
