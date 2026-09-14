# Live validation, 2026-09-14

Goal: validate the roadmap's "paylaşılan openlog'da canlı doğrulama" and "gerçek sunucuda uçtan uca doğrulama" items
against the shared local stack (compose project `openlog`) and the published GitHub release.

**Status: partial.** The shared stack did not start, so no item was validated against a running openlog and the
roadmap was not changed.

## Blocker

```sh
docker compose -p openlog -f deploy/compose/docker-compose.yml --env-file deploy/compose/.env.example up -d --build --wait
# Error response from daemon: failed to set up container networking:
#   endpoint with name openlog-kafka-1 already exists in network openlog_default
```

`docker network inspect openlog_default` still lists stale endpoints for `openlog-kafka-1` and
`openlog-clickhouse-1` (their containers were removed outside compose). Only `openlog-postgres-1` runs; kafka,
clickhouse, bootstrap and openlog stay `Created`. The fix needs the owner's approval:

```sh
docker network disconnect -f openlog_default openlog-kafka-1
docker network disconnect -f openlog_default openlog-clickhouse-1
# then the compose command above again
```

## Checklist

| # | Item | Result | Evidence |
|---|------|--------|----------|
| 1 | Shared stack up, /readyz, login, version page | blocked | stale network endpoints (above) |
| 2 | Demo agents (hosts, APM services/transactions/traces, logs↔traces, metrics, containers↔services, nginx/redis/postgres integrations, PHP forwarder spans, Java/Python/Node/Go, fleet versions) | blocked | needs item 1 |
| 2a | .NET demo service in `test/localagents` | added, image builds | `dotnet-shop` (+ `dotnet-shop-loadgen`); `docker build` of `test/localagents/dotnet-shop` OK; not run against openlog |
| 3a | Published release install on Debian 12 + systemd + Docker (v0.1.12, arm64) | ✅ | `curl -fsSL …/releases/latest/download/install.sh \| sh -s -- --license-key dev-license-key --endpoint http://host.docker.internal:4318`: sha256 verified, deb installed, `systemctl is-active openlog-infra-agent` = active |
| 3b | docker group reconciled | ✅ | `id openlog-agent` → `groups=…,102(docker)`; reconcile WARN with the revert command |
| 3c | Containers discovered, container logs tailed | ✅ (agent side) | journal: `tailing log file` for the nginx/redis/php-fpm containers; `integration instance started` for docker, nginx, redis |
| 3d | nginx (stub_status off) → needs_configuration with hint | ✅ (agent side) | journal: `status: needs_configuration`, `no stub_status, NGINX Plus API or VTS page on 127.0.0.1:8081 (tried /api/, …, /nginx_status, /stub_status, …)` |
| 3e | redis enabled automatically | ✅ (agent side) | journal: redis instance started on `172.17.0.3:6379`, no needs_configuration status |
| 3f | PHP-FPM container detected | ✅ (agent side) | journal: `php runtime detection changed … php_detected: true, reason: php-fpm`; forwarder socket started |
| 3g | Host in UI API, integration status in UI, remote nginx config without SSH, Services-tab APM hint | blocked | needs item 1 (the agent buffers: `export failed; payloads kept for retry`) |
| 4a | Add data "Linux" command | ✅ (install) | same command as 3a (generated with `sudo`; run as root in the test host) |
| 4b | Add data "Docker" command | ❌ bug, fixed in release.yml | `ghcr.io/onuragtas/openlog-infra-agent:0.1.12` and `:latest` do not exist: no release job published the image |
| 4c | Add data "Java" command | ✅ (download) | `sha256sum -c openlog-javaagent-0.1.12.jar.sha256` → OK, installed to `/opt/openlog/openlog-javaagent.jar`; data flow blocked by item 1 |
| 4d | Add data "Node.js" (`npm install @openlog/node`) | ❌ not published | registry.npmjs.org `@openlog/node` → 404 (release job skips without `NPM_TOKEN`) |
| 4e | Add data "Python" (`pip install openlog-agent`) | ❌ not published | pypi.org `openlog-agent` → 404 (release job skips unless `PYPI_PUBLISH=true`) |
| 4f | Add data ".NET" (`dotnet add package OpenLog.Agent`) | ❌ not published | nuget.org `openlog.agent` → 404 (release job skips without `NUGET_API_KEY`) |

Commands were generated with `buildInstallCommands` from `web/src/lib/install-commands.ts` (Node 22
`--experimental-strip-types`, onboarding `otlp_http = http://openlog:4318`, `agent_version = 0.1.12`).

## Changes

- `.github/workflows/release.yml`: new `infra-agent-image` job builds `agents/infra/Dockerfile` for linux/amd64 and
  linux/arm64 and pushes `ghcr.io/onuragtas/openlog-infra-agent:<version>` (and `:latest` on stable); `release`
  waits for it. The Dockerfile was built locally (`openlog-infra-agent 0.1.11-local`).
- `test/localagents`: `dotnet-shop` (OpenLog.SampleApp, net8.0, Npgsql/EF Core on `orders-db`, Redis) and a curl
  loadgen.

## Remaining

- Fix the stale network endpoints, start the stack, rerun items 1, 2, 2a, 3g, 4b–4f.
- Publish `@openlog/node`, `openlog-agent` and `OpenLog.Agent` (secrets/variables), or show a fallback in the
  Add data cards until they are published.
- Real user server: operational step.
