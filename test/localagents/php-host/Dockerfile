# syntax=docker/dockerfile:1
# php-host (test/localagents): Debian 12 + PHP 8.3-FPM + nginx + a tiny PHP app + the openlog PHP extension +
# openlog-infra-agent, whose php_forwarder module receives the spans (docs/contracts/php-agent.md).
#
# Named build contexts (see ../docker-compose.yml): agent = agents/infra, release = libs/release, phpext = source of the
# PHP extension. Until agents/php/ext exists this is the option-A spike (agents/php/prototype-a/ext), whose datagram
# format predates contract v1; patch-v1.pl converts it at build time (no-op checks fail loudly if the source changes).

FROM --platform=$BUILDPLATFORM golang:1.26 AS agent-build
ARG TARGETOS=linux
ARG TARGETARCH
ARG AGENT_VERSION=0.9.1-local
WORKDIR /src
COPY --from=release . /libs/release
COPY --from=agent go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY --from=agent . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X github.com/onuragtas/openlog/agents/infra/internal/version.Version=${AGENT_VERSION}" \
    -o /out/openlog-infra-agent ./cmd/openlog-infra-agent

FROM php:8.3-fpm-bookworm AS ext
COPY --from=phpext . /src/ext
COPY patch-v1.pl /usr/local/bin/patch-v1.pl
RUN cd /src/ext \
    && if grep -q '"span_id' openlog.c; then perl /usr/local/bin/patch-v1.pl openlog.c; fi \
    && phpize && ./configure --enable-openlog && make -j"$(nproc)" \
    && install -D -m 0755 modules/openlog.so /out/openlog.so

FROM php:8.3-fpm-bookworm
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends nginx curl iptables procps ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --no-create-home --shell /usr/sbin/nologin openlog-agent \
    # The documented group handling (README "PHP forwarder"): the agent user is a member of the PHP-FPM group, so the
    # socket can be 0660 root-less with group www-data.
    && usermod -aG www-data openlog-agent \
    && install -d -o openlog-agent -g openlog-agent -m 0750 /var/lib/openlog-infra-agent \
    && install -d -m 0755 /etc/openlog-infra-agent
COPY --from=ext /out/openlog.so /tmp/openlog.so
RUN install -m 0644 /tmp/openlog.so "$(php-config --extension-dir)/openlog.so" && rm /tmp/openlog.so
COPY openlog.ini /usr/local/etc/php/conf.d/zz-openlog.ini
COPY nginx.conf /etc/nginx/sites-available/default
COPY app/ /srv/app/
COPY --from=agent-build /out/openlog-infra-agent /usr/bin/openlog-infra-agent
COPY entrypoint.sh /usr/local/bin/php-host-entrypoint
RUN chmod 0755 /usr/local/bin/php-host-entrypoint && php -m | grep -qx openlog && nginx -t
ENTRYPOINT ["/usr/local/bin/php-host-entrypoint"]
