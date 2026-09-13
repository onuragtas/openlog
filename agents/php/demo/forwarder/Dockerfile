# syntax=docker/dockerfile:1.7
# openlog PHP demo forwarder (php-agent.md v1). Build context: agents/php/demo/forwarder
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY *.go ./
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /forwarder .

FROM alpine:3.22
COPY --from=build /forwarder /usr/local/bin/forwarder
ENV OPENLOG_PHP_SOCKET=/run/openlog-infra-agent/php.sock \
    OPENLOG_ENDPOINT=http://openlog:4318
ENTRYPOINT ["/usr/local/bin/forwarder"]
