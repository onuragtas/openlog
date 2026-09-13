# syntax=docker/dockerfile:1
# One image containing every openlog backend binary. Default entrypoint: openlog-allinone.

# Web UI (embedded into openlog-api / openlog-allinone via web/embed.go).
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
COPY web/scripts ./scripts
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
# Local module replaced in go.mod; it must exist before dependencies are resolved.
COPY libs/release/go.mod libs/release/
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# Replace the placeholder dist with the real UI build.
COPY --from=web /src/web/dist ./web/dist
# Product version and compiled-in release signing public keys (docs/contracts/releases-updates.md §1-2).
ARG VERSION=0.0.0-dev
ARG COMMIT=unknown
ARG DATE=
ARG OPENLOG_RELEASE_PUBLIC_KEYS=
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p /out && go build -ldflags="-s -w \
      -X github.com/onuragtas/openlog/internal/version.Version=${VERSION} \
      -X github.com/onuragtas/openlog/internal/version.Commit=${COMMIT} \
      -X github.com/onuragtas/openlog/internal/version.Date=${DATE} \
      -X github.com/onuragtas/openlog/internal/release.trustedKeys=${OPENLOG_RELEASE_PUBLIC_KEYS}" \
    -o /out/ ./cmd/...

FROM alpine:3.22
# wget (busybox) is used by container health checks.
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 openlog && adduser -S -D -H -u 10001 -G openlog openlog
COPY --from=build /out/ /usr/local/bin/
USER 10001:10001
EXPOSE 4317 4318 8080 9464
ENTRYPOINT ["/usr/local/bin/openlog-allinone"]
