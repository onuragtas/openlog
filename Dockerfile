# syntax=docker/dockerfile:1
# One image containing every openlog backend binary. Default entrypoint: openlog-allinone.

# Web UI (embedded into openlog-api / openlog-allinone via web/embed.go).
# The web and Go stages run on the build machine's platform and cross-compile (no QEMU emulation
# in multi-arch builds); only the final stage is per target platform.
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
COPY web/scripts ./scripts
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH
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
# Empty: the official keys from release-public-keys.txt (one base64 key per line), so a source build
# verifies official releases and can update itself to them.
ARG OPENLOG_RELEASE_PUBLIC_KEYS=
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    OPENLOG_RELEASE_PUBLIC_KEYS="${OPENLOG_RELEASE_PUBLIC_KEYS:-$(grep -v '^#' release-public-keys.txt | tr -s '\n' ',' | sed 's/,$//')}" && \
    mkdir -p /out && GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w \
      -X github.com/onuragtas/openlog/internal/version.Version=${VERSION} \
      -X github.com/onuragtas/openlog/internal/version.Commit=${COMMIT} \
      -X github.com/onuragtas/openlog/internal/version.Date=${DATE} \
      -X github.com/onuragtas/openlog/internal/release.trustedKeys=${OPENLOG_RELEASE_PUBLIC_KEYS}" \
    -o /out/ ./cmd/... && \
    # openlog-renderer ships in its own image (target `renderer`); the main image has no browser.
    rm -f /out/openlog-renderer

# openlog-renderer (D-097): docker build --target renderer -t openlog-renderer .
# Built without the web UI; Go cross-compiles, Chromium comes from Alpine packages of the target platform.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS renderer-build
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
COPY libs/release/go.mod libs/release/
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# internal/app embeds web/dist (excluded by .dockerignore); the renderer serves no UI, a placeholder is enough.
RUN mkdir -p web/dist && printf '<!doctype html><title>openlog</title>\n' > web/dist/index.html
ARG VERSION=0.0.0-dev
ARG COMMIT=unknown
ARG DATE=
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p /out && GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w \
      -X github.com/onuragtas/openlog/internal/version.Version=${VERSION} \
      -X github.com/onuragtas/openlog/internal/version.Commit=${COMMIT} \
      -X github.com/onuragtas/openlog/internal/version.Date=${DATE}" \
    -o /out/openlog-renderer ./cmd/openlog-renderer

FROM alpine:3.22 AS renderer
# chromium: headless browser; font-dejavu/font-noto: text glyphs (Latin incl. Turkish, symbols); tini reaps
# Chromium's child processes.
RUN apk add --no-cache ca-certificates tzdata tini chromium font-dejavu font-noto \
    && addgroup -S -g 10001 openlog && adduser -S -D -h /home/openlog -u 10001 -G openlog openlog
COPY --from=renderer-build /out/openlog-renderer /usr/local/bin/openlog-renderer
ENV OPENLOG_RENDERER_CHROMIUM_PATH=/usr/bin/chromium \
    HOME=/tmp \
    XDG_CONFIG_HOME=/tmp/.config \
    XDG_CACHE_HOME=/tmp/.cache
USER 10001:10001
EXPOSE 8090 9464
ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/openlog-renderer"]

# Default target: openlog backend binaries.
FROM alpine:3.22
# wget (busybox) is used by container health checks.
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 openlog && adduser -S -D -H -u 10001 -G openlog openlog
COPY --from=build /out/ /usr/local/bin/
USER 10001:10001
EXPOSE 4317 4318 8080 9464
ENTRYPOINT ["/usr/local/bin/openlog-allinone"]
