# syntax=docker/dockerfile:1.7
# openlog-infra-agent as the PHP demo's forwarder (php_forwarder module, docs/contracts/php-agent.md §6).
# Build context: agents/php/demo/infra-agent; named contexts (see ../docker-compose.yml):
#   agent = agents/infra, release = libs/release   (same agent-build stage as test/localagents/php-host)
FROM --platform=$BUILDPLATFORM golang:1.26 AS agent-build
ARG TARGETOS=linux
ARG TARGETARCH
ARG AGENT_VERSION=0.9.1-php-demo
WORKDIR /src
COPY --from=release . /libs/release
COPY --from=agent go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY --from=agent . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X github.com/onuragtas/openlog/agents/infra/internal/version.Version=${AGENT_VERSION}" \
    -o /out/openlog-infra-agent ./cmd/openlog-infra-agent

FROM alpine:3.22
RUN apk add --no-cache ca-certificates \
    && install -d -m 0755 /etc/openlog-infra-agent /var/lib/openlog-infra-agent /run/openlog-infra-agent
COPY --from=agent-build /out/openlog-infra-agent /usr/bin/openlog-infra-agent
COPY config.yaml /etc/openlog-infra-agent/config.yaml
COPY --chmod=0755 entrypoint.sh /usr/local/bin/infra-agent-entrypoint
ENTRYPOINT ["/usr/local/bin/infra-agent-entrypoint"]
