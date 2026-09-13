# Local stack demo: the whole system with Docker

The M1 closing scenario ([roadmap](../plan/06-roadmap.md), "Tüm sistemin Docker ile ayağa kalkması"), repeatable on
one machine: signed local releases, the `deploy/compose` stack on the default ports, two Debian 12 "servers" that
install the agent with `scripts/install.sh`, an agent fleet auto-update, a backend self-update with
`openlog-updater`, and a restart of the backend without losing metrics.

```sh
make stack-demo                                   # every phase (about 15 minutes with warm image caches)
make stack-demo STACKDEMO_ARGS="--screenshots"    # plus Playwright screenshots stack-01 … stack-18
test/stackdemo/run.sh --help                      # phases and variables
```

The stack stays up at the end: <http://localhost:8080>, `admin@openlog.local` / `openlog-dev-password`
(from `deploy/compose/.env`). Stop and delete everything:

```sh
docker compose -p openlog --project-directory deploy/compose -f deploy/compose/docker-compose.yml \
  -f test/stackdemo/docker-compose.stackdemo.yml --profile updater down -v --remove-orphans
rm -rf deploy/compose/releases deploy/compose/backups
```

Requirements: Docker with Compose v2.17+ (4 CPUs / 6 GB free is comfortable), Go 1.26, `make`, `jq`, `curl`, `rsync`;
for `--screenshots` Node ≥ 20.19 (`web/` is copied to `$TMPDIR/openlog-stackdemo-web`, because node tooling does not
run inside iCloud-synced checkouts). Host ports: 4317, 4318, 8080, 9464 (openlog), 127.0.0.1:5432/8123/9000/9092
(dependencies), 127.0.0.1:18090 (release server; 18080 is the e2e API port), 127.0.0.1:5001 (registry; 5000 is AirPlay on macOS). Override with
the variables in `deploy/compose/.env.example` and `STACKDEMO_RELEASES_PORT` / `STACKDEMO_REGISTRY_PORT`.
Output (timings, screenshots): `$STACKDEMO_OUT` (default `$TMPDIR/openlog-stackdemo`).

**Test keys only.** Releases are signed with `dist/testkeys` (`make release-testkeys`, git-ignored) and the images and
agents are built to trust that key. Never publish these builds.

## What is running

| Service (compose project `openlog`) | From | Role |
|---|---|---|
| `postgres`, `kafka`, `clickhouse`, `bootstrap`, `openlog` | `deploy/compose/docker-compose.yml` | the `single` profile; `openlog` = ingest + processor + api + UI |
| `openlog-updater` | same file, profile `updater` | backend self-update (`notify`, later `auto`) |
| `releases` | `test/stackdemo/docker-compose.stackdemo.yml` | nginx serving `deploy/compose/releases` at `http://releases` (install.sh, index, manifests, artifacts) |
| `registry` | same | `registry:2`; the signed manifests reference the backend images by digest (`localhost:5001/openlog@sha256:…`) |
| `host-services` (`demo-web-1`) | same, `test/stackdemo/host` | Debian 12 with nginx, redis, postgresql; agent installed by install.sh |
| `host-plain` (`demo-plain-1`) | same | Debian 12 without services |

`deploy/compose/releases` is mounted read-only into `openlog` and `openlog-updater` at `/releases` (the base compose
file does this for everyone: it is where an air-gapped mirror and trusted key files go). The demo `.env` sets:

```sh
OPENLOG_IMAGE=openlog:0.9.0-stack
OPENLOG_UPDATE_CHECK_INTERVAL=1m                 # default 24h; the banner must react within the demo
OPENLOG_RELEASE_INDEX_URL=http://releases/index.json
OPENLOG_RELEASE_TRUSTED_KEYS_FILE=/releases/trusted-keys.txt
OPENLOG_RELEASE_MIRROR_DIR=/releases             # fleet catalog reads the mirror directly
OPENLOG_RELEASE_SERVE_MIRROR=true                # agents download from ingest (/v1/openlog/releases/…)
OPENLOG_RELEASE_CATALOG_REFRESH=20s
OPENLOG_FLEET_SYNC_INTERVAL=60s                  # agents clamp to ≥ 60 s
OPENLOG_FLEET_CONTROLLER_INTERVAL=10s
OPENLOG_UPDATER_MODE=notify                      # switched to auto in the backend phase
OPENLOG_UPDATER_INTERVAL=30s
OPENLOG_UPDATER_HEALTH_TIMEOUT=3m
```

## Phases

`test/stackdemo/run.sh [--screenshots] [phase…]`; without phases all run in this order. Each phase can be re-run on
its own against a running stack.

| Phase | What happens | Checks / output |
|---|---|---|
| `clean` | `down -v` of project `openlog`, removes `deploy/compose/releases`, `backups` | |
| `env` | `deploy/compose/.env` = `.env.example` + the settings above | |
| `release` | `make release-testkeys`; for 0.9.0 and 0.9.1: `make docker VERSION=… IMAGE=openlog:<v>-stack RELEASE_TESTKEYS=1`, push to the registry, `make release-local VERSION=… RELEASE_TESTKEYS=1 RELEASE_BASE_URL=http://releases RELEASE_ARCHES=<docker arch> RELEASE_IMAGE=<digest>` | `OK signature`, `OK artifact` lines of every release |
| `up` | mirror = `dist/v0.9.0` + index over **0.9.0 only** (0.9.1 stays unpublished), `docker compose up -d --wait` | start time, `docker compose ps`, `/readyz` with `"version":"0.9.0"` |
| `agents` | in each host: `curl -fsSL http://releases/install.sh \| sh -s -- --license-key dev-license-key --endpoint http://openlog:4318 --base-url http://releases --method tarball`; the host's supervisor loop (instead of systemd) starts the agent once the config has a key; then `logs.auto_from_discovery: true` on `demo-web-1` and an agent restart | seconds from install to the host in `GET /api/v1/hosts` (target < 2 min) |
| `policy` | `PUT /api/v1/fleet/policy` mode `notify`, waves `[50,100]`, soak 1 min; screenshots stack-01 … stack-14 | |
| `publish` | mirror gets `v0.9.1`, index re-built over 0.9.0 + 0.9.1 and re-signed | `GET /api/v1/version` `latest_available` = 0.9.1 (banner, stack-15), fleet catalog lists 0.9.1, updater `available` |
| `fleet` | policy `auto` | one line per 10 s: rollout state, wave, counters, versions (stack-16); both hosts on 0.9.1 (stack-17); in each host `current -> versions/0.9.1`, `-version`, `update-state.json`; ingest `openlog_release_mirror_requests_total` |
| `backend` | `OPENLOG_UPDATER_MODE=auto` in `.env`, `docker compose up -d openlog-updater` | `/readyz` 0.9.1, updater steps backup → pull → migrate → recreate → health → cleanup → contract-migrate, `backups/*.dump`, `OPENLOG_IMAGE` rewritten to the digest, hosts and metrics still there (stack-18) |
| `restart` | `docker compose restart postgres kafka clickhouse openlog openlog-updater` | downtime, `system.uptime` samples per host over the restart window with the largest gap (10 s interval → no gap above ~20 s means nothing was lost), `openlog.agent.export.items` by outcome (`buffered` rose, `dropped` 0) |

Screenshots (`--screenshots`, Turkish UI, 1440×900, `$STACKDEMO_OUT/shots`):

| File | Shows |
|---|---|
| `stack-01-login` | sign-in page |
| `stack-02-hosts` | both hosts |
| `stack-03-host-overview` | `demo-web-1` charts |
| `stack-04-services` | discovered nginx, Redis, PostgreSQL |
| `stack-05-inventory` | host inventory |
| `stack-06-host-logs` | nginx log lines filtered by discovered service |
| `stack-07-logs` | log explorer |
| `stack-08-inventory-search` | `openssl` package on both hosts |
| `stack-09-settings-license-keys`, `stack-10-settings-members` | settings |
| `stack-11-fleet-overview`, `stack-12-fleet-policy` | fleet: both agents 0.9.0 and update-capable, policy |
| `stack-13-version` | Settings → Organization: version, release check, updater |
| `stack-14-dark` | host overview in dark mode |
| `stack-15-update-banner` | "openlog 0.9.1 yayınlandı" for the admin |
| `stack-16-fleet-rollout`, `stack-17-fleet-done` | rollout in progress, both agents on 0.9.1 |
| `stack-18-after-backend-update` | version 0.9.1 and the updater's steps |

## Doing it by hand

The script only strings documented commands together; the essential ones:

```sh
make release-testkeys
make docker VERSION=0.9.0 IMAGE=openlog:0.9.0-stack RELEASE_TESTKEYS=1
make release-local VERSION=0.9.0 RELEASE_TESTKEYS=1 RELEASE_BASE_URL=http://releases RELEASE_ARCHES=arm64 \
  RELEASE_IMAGE=localhost:5001/openlog@sha256:…

# publish into the mirror: copy dist/v<version>, then an index over what the mirror holds
bin/openlog-release build-index --out deploy/compose/releases/index.json --base-url http://releases \
  --keys "$(cat dist/testkeys/public.key)" deploy/compose/releases/v*/manifest.json
OPENLOG_RELEASE_SIGNING_KEY="$(cat dist/testkeys/signing.key)" \
  bin/openlog-release sign --key-env OPENLOG_RELEASE_SIGNING_KEY deploy/compose/releases/index.json

DC="docker compose -p openlog --project-directory deploy/compose -f deploy/compose/docker-compose.yml -f test/stackdemo/docker-compose.stackdemo.yml --profile updater"
$DC up -d --wait
$DC exec host-plain sh -c 'curl -fsSL http://releases/install.sh | sh -s -- --license-key dev-license-key --endpoint http://openlog:4318 --base-url http://releases --method tarball'
$DC logs -f host-plain                         # supervisor: "starting versions/0.9.0", agent output
$DC exec host-plain sh -c 'readlink /opt/openlog/infra-agent/current; cat /var/lib/openlog-infra-agent/update-state.json'
```

`OPENLOG_AGENT_CONTAINER=0` (set on both hosts) makes the tarball layout inside a container update-capable; without it
the agent detects the container and reports `update_capable: false`.

## Troubleshooting

| Symptom | Check |
|---|---|
| `release` fails at `docker push` | registry port taken (`STACKDEMO_REGISTRY_PORT`); on macOS 5000 is AirPlay |
| no banner after `publish` | `OPENLOG_UPDATE_CHECK_INTERVAL` in `.env`; `docker compose logs openlog \| grep "update check"` |
| fleet catalog `error` | `GET /api/v1/fleet/summary` `.catalog`; the index must be re-signed after every change in the mirror |
| agents stay on 0.9.0 | `GET /api/v1/fleet/hosts` `.update`; host log (`$DC logs host-plain`): `update` messages; policy `mode` |
| updater `error: current version …` right after `up` | transient: the updater starts before `openlog`; it retries within a minute |
| backend update `rolled_back` | `GET /api/v1/version` `.updater.steps`; `$DC logs openlog-updater` |
