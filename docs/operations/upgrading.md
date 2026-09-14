# Upgrading openlog (backend)

How the backend (ingest, processor, api, allinone, migrations) moves from one release to the next on
Docker Compose and Kubernetes, manually or with `openlog-updater`. Agents update themselves separately
(docs/contracts/releases-updates.md §3); a backend upgrade never breaks agents (backend `N` accepts
agents `N`, `N-1`, `N-2`).

Background: [plan §5](../plan/09-releases-updates.md), [contract §5–§6](../contracts/releases-updates.md), D-025…D-029.

## 1. Compatibility rules

| Rule | What it means for you |
|---|---|
| One product version | Image, chart, UI and agents share `X.Y.Z`. Every binary reports it: `-version`, `/readyz` body (`"version"`), `GET /api/v1/version`, and the `X-Openlog-Version` header on every ingest, api and admin response (gRPC: `x-openlog-version` header metadata). |
| `N` and `N+1` run together | During a rolling update pods of both versions share Kafka, ClickHouse and PostgreSQL. This is tested in CI (`make mixed-version`). |
| Migrations are `expand` or `contract` | Every file in `migrations/postgres` and `schema/clickhouse` starts with `-- openlog:phase expand` or `-- openlog:phase contract`. Expand changes (new tables, nullable columns) are safe for the previous release and run **before** new code starts. Contract changes (drops, renames) declare `-- openlog:requires-all-at-least <version>` and run only when no older instance is alive. |
| Upgrade path | A release's manifest may require `compatibility.min_upgrade_from`; older installations must upgrade through an intermediate release. `openlog-updater` picks that intermediate release automatically. |
| Downgrades | Allowed down to the manifest's `rollback_floor` only because expand migrations stay; contract migrations of the newer release may already have run, so prefer restoring the backup for a real downgrade. |

### How contract migrations are gated

Every ingest, processor, api and allinone process with `OPENLOG_POSTGRES_DSN` upserts a row into
`component_heartbeats` (component, instance id, version, last_seen) every 30 s and deletes it on
graceful shutdown. `openlog-migrate`:

- applies every pending `expand` migration;
- applies a `contract` migration only if its own version and **every** instance seen within the last
  5 minutes are `>=` the required version; otherwise it logs the reason and skips it — the next run
  retries (the updater runs migrate again after a successful update);
- treats unreadable heartbeats (no PostgreSQL, table missing) as "unknown" and skips.

```sh
openlog-migrate -plan              # what would run and why, changes nothing
openlog-migrate                    # apply
openlog-migrate -force-contract    # also contract migrations blocked by older instances (breaks them)
```

```
live instances (seen within 5m0s): 2
  openlog-allinone  openlog-n-1-3f2a9c1e  0.9.0  last seen 2026-09-13T10:01:12Z
  openlog-api       api-7d9c-0b61fe22     0.9.1  last seen 2026-09-13T10:01:20Z

DATABASE    VERSION  NAME                   PHASE               ACTION   REASON
postgres    0003     component_heartbeats   expand              applied
postgres    0004     drop_legacy_column     contract (>= 0.9.1) skip     openlog-allinone openlog-n-1-3f2a9c1e runs 0.9.0 < 0.9.1
```

Processes without PostgreSQL (`OPENLOG_AUTH_MODE=static` without a DSN) cannot heartbeat, so contract
migrations are skipped there until forced. Writing a contract migration: it must not be needed by any
later expand migration, and its required version must be the first release that no longer uses the
dropped object.

## 2. Release check and UI

The api leader (one pod, PostgreSQL advisory lock) fetches the signed release index at most once a day
(retry after 6 h on failure), verifies the index and the newest manifest of `OPENLOG_UPDATE_CHANNEL`,
and stores the result in PostgreSQL. Every pod serves it in `GET /api/v1/version`:

```json
{"version": "0.9.0", "commit": "abc123", "date": "2026-09-01T12:00:00Z",
 "latest_available": {"version": "0.9.1", "notes_url": "https://github.com/onuragtas/openlog/releases/tag/v0.9.1", "checked_at": "2026-09-13T03:00:00Z"},
 "update_check": "enabled", "updater": {"engine": "compose", "mode": "notify", "state": "available", "target_version": "0.9.1"}}
```

- Admins and owners see a banner "openlog 0.9.1 is available — Release notes" (dismissed per version in the browser).
- **Settings → Organization → Version and updates** has two buttons for admins and owners (not on installations
  with `OPENLOG_SIGNUP_ENABLED=true`):
  - **Check now** runs the release check immediately and asks `openlog-updater` to check too (at most once per
    30 s); no need to wait for the daily check or `OPENLOG_UPDATER_INTERVAL`.
  - **Update now** (shown when a newer release is known and an updater reports) asks the updater to install that
    release now — also in `notify` mode, since an admin confirmed it. The confirmation has a checkbox
    "Install now, also outside the maintenance window"; without it the request fails outside
    `OPENLOG_UPDATER_MAINTENANCE_WINDOW`. The page shows the request state and the updater's steps (backup, pull,
    migrate, recreate, health, rolled back/succeeded), keeps its data while `openlog` is recreated
    ("The server is restarting… reconnecting") and offers a reload when the new version answers.
  - The Compose updater picks requests up within `OPENLOG_UPDATER_REQUEST_POLL` (10 s). The page says so when the
    updater does not poll (stopped, or an updater container older than this feature: recreate it with
    `docker compose --profile updater up -d openlog-updater`). Requests no updater picks up expire after 15 min.
    On Kubernetes the CronJob handles requests at its next run (§4).
  - Mechanism and audit actions: [releases-updates.md §5.1](../contracts/releases-updates.md), D-041.
- Fleet (agents): **Deploy now** on an active rollout skips the remaining wave soak times (100 % at once). Agents
  waiting for a wave sync every `OPENLOG_FLEET_ROLLOUT_SYNC_INTERVAL` (60 s), so they start within about a minute;
  maintenance windows of the policy and the halt threshold still apply.
- Everyone sees "openlog was updated to X — Reload" when API responses carry a different `X-Openlog-Version`
  than the backend that served the open page (after an upgrade old lazy-loaded UI chunks no longer exist).
- `OPENLOG_UPDATE_CHECK=disabled` turns the check off; `OPENLOG_RELEASE_INDEX_URL` points to a mirror
  (air-gapped: copy `index.json`, the manifests and their `.sig` files to any HTTP server; signatures are
  still verified). Builds without trusted keys report `update_check: failed` ("no trusted release keys").

## 3. Docker Compose

### Manual

```sh
cd deploy/compose
docker compose exec -T postgres pg_dump -U openlog -d openlog -Fc > backup-$(date +%F).dump   # backup
sed -i.bak 's|^OPENLOG_IMAGE=.*|OPENLOG_IMAGE=ghcr.io/onuragtas/openlog:0.9.1|' .env
docker compose pull openlog
docker compose run --rm --entrypoint openlog-migrate openlog          # expand migrations first
docker compose up -d --wait openlog                                    # allinone also migrates on start
curl -s localhost:9464/readyz                                          # {"version":"0.9.1",...}
```

Roll back: set the previous `OPENLOG_IMAGE` and `docker compose up -d openlog` (expand migrations are
compatible with the previous release).

### Automatic: `openlog-updater`

```sh
docker compose --profile updater up -d openlog-updater
docker compose logs -f openlog-updater
```

| Variable | Default | |
|---|---|---|
| `OPENLOG_UPDATER_MODE` | `notify` | `off`, `notify` (record "available", audit once, UI shows it), `auto` |
| `OPENLOG_UPDATE_CHANNEL` | `stable` | `beta` also sees stable releases |
| `OPENLOG_RELEASE_INDEX_URL` | GitHub latest `index.json` | mirror URL |
| `OPENLOG_RELEASE_TRUSTED_KEYS_FILE` | – | extra public keys (release images have the official keys compiled in) |
| `OPENLOG_UPDATER_INTERVAL` | `1h` | check interval |
| `OPENLOG_UPDATER_REQUEST_POLL` | `10s` | how often "Check now" / "Update now" requests from the UI are read from PostgreSQL (`update_requests`) |
| `OPENLOG_UPDATER_MAINTENANCE_WINDOW` | – (any time) | UTC, e.g. `sat,sun 02:00-05:00; mon-fri 03:00-03:30` (`*` = every day; `end <= start` crosses midnight) |
| `OPENLOG_UPDATER_HEALTH_TIMEOUT` | `5m` | how long the new version has to become ready |
| `OPENLOG_UPDATER_SERVICES` | `openlog` | compose services to recreate |
| `OPENLOG_UPDATER_HEALTH_URLS` | `http://openlog:9464/readyz` | must return 200 with the target version |
| `OPENLOG_UPDATER_POSTGRES_SERVICE` / `_PGDUMP_USER` / `_PGDUMP_DATABASE` | `postgres` / `openlog` / `openlog` | backup source |
| `OPENLOG_UPDATER_BACKUP_DIR` / `_BACKUP_KEEP` | `./backups` (mounted at `/backups`) / `5` | `pg_dump -Fc` files `openlog-<UTC time>-<from version>.dump` |
| `OPENLOG_UPDATER_ENV_FILE` | `/compose/.env` | `OPENLOG_IMAGE=` is rewritten after a verified update |
| `OPENLOG_UPDATER_IMAGE_REPOSITORY` | – | pull the manifest digest from a mirror repository |
| `OPENLOG_UPDATER_COMPOSE_PROJECT` | detected from the updater container | |

Each run: read the version the stack reports (`/readyz`), verify the index, pick the newest release of
the channel that is newer, not failed before, has an image and allows `min_upgrade_from`. In `auto`
mode, inside the maintenance window:

1. **backup** — `pg_dump -Fc` via `docker exec` in the postgres container into `/backups`; keep the newest N. A failed backup aborts.
2. **pull** — the image **by digest** from the signed manifest (a tag cannot be swapped under it).
3. **migrate** — a one-off container of the new image runs `openlog-migrate` with the environment and network of the running `openlog` container. Only expand (and eligible contract) migrations run, so the running release keeps working even if this fails.
4. **recreate** — for each service container: stop it (its stop timeout), rename it to `<name>-pre-update`, create a new container with the same config, env, labels, mounts, port bindings, restart policy, networks and aliases and only the image replaced, start it.
5. **health** — every health URL must answer 200 with the target version before `OPENLOG_UPDATER_HEALTH_TIMEOUT`; a container that exits without a restart policy fails immediately.
6. **success** — remove the `-pre-update` containers, rewrite `OPENLOG_IMAGE` in `.env`, run `openlog-migrate` again for contract migrations that were waiting for the old containers.
7. **failure after step 3** — remove the new containers, rename and start the previous ones, wait until they report the previous version: state `rolled_back` (or `rollback_failed`). The version is recorded in `failed_versions` and not retried; a newer release is.

The status is kept in `/backups/updater-status.json` and in PostgreSQL (shown as `updater` in
`GET /api/v1/version`); events go to the audit log (`updater.update_started`, `…_succeeded`,
`…_rolled_back`, `…_failed`, `…_available`, actor `openlog-updater`). If the updater dies mid-update,
it finds the `-pre-update` container on its next start and restores it.

**Why the Docker Engine API instead of `docker compose up`.** The updater talks to `/var/run/docker.sock`
with plain HTTP and recreates containers from their own inspect data. This needs no compose CLI or
compose file inside the image, no host-identical project path (compose resolves relative bind mounts
against the host path), and no copy of the variables that were in your shell when the stack was
started; the previous container stays intact as the rollback target, which makes rollback a rename
instead of a second deployment. The cost is drift from the compose file, which the updater closes by
rewriting `OPENLOG_IMAGE` in `.env`: a later `docker compose up -d` keeps the new version (it may
recreate the container once because the stored config hash no longer matches). Settings that came from
the old image (its `ENV`, `LABEL`, entrypoint) are dropped so the new image's defaults apply.

**Security.** The Docker socket is root-equivalent; the updater runs as root in its container. Only
images whose digest is in a manifest signed by a trusted key are ever run. Registry credentials are not
supported (public images or a mirror reachable without auth).

**Not updated automatically:** the `openlog-updater` container itself (it runs the image from `.env` on
the next `docker compose up -d`), `bootstrap`, `loadgen`, and third-party images (Kafka, ClickHouse, PostgreSQL).

## 4. Kubernetes (Helm)

### Recommended: GitOps

Pin the chart and image version in Git and let Argo CD or Flux apply it; the chart's `pre-upgrade`
migrate hook runs `openlog-migrate` with the new image before Deployments roll (expand first). Watch
`GET /api/v1/version` (or the UI banner) for new releases, e.g. with Renovate on the chart version.

```yaml
# Argo CD Application (excerpt)
source:
  repoURL: https://github.com/onuragtas/openlog
  path: deploy/helm/openlog
  targetRevision: v0.9.1
  helm: {valueFiles: [values-production.yaml], parameters: [{name: image.tag, value: "0.9.1"}]}
```

### Manual

```sh
helm upgrade openlog deploy/helm/openlog -n observability -f values.yaml --set image.tag=0.9.1 --wait
kubectl -n observability logs job/openlog-migrate
```

Contract migrations skipped during the hook (old pods still alive) are applied by the next migrate run
(next upgrade, or `kubectl -n observability create job --from=cronjob/…` / a manual `openlog-migrate` Job).
Back up PostgreSQL first (CloudNativePG: `spec.backup`; external: `pg_dump`).

### Optional: updater CronJob

```yaml
updater:
  enabled: true
  mode: auto                     # notify | auto
  schedule: "17 3 * * *"
  maintenanceWindow: "sat,sun 02:00-05:00"
```

`openlog-updater -k8s -once` (in-cluster service account, plain REST, no client-go):

1. verifies the index and manifest and picks the target (same rules as Compose);
2. creates a Job `<release>-api-updater-migrate-*` from the api Deployment's pod settings (service account, env, security context) with the new image, `openlog-migrate` and `OPENLOG_MIGRATE_SKIP_KAFKA=true`, and waits for it;
3. patches the image of the ingest, processor and api Deployments (strategic merge patch, normal rolling update) and waits until each rollout is complete;
4. waits until the api reports the target version;
5. on failure in 3–4 patches the previous images back and waits for that rollout (`rolled_back`).

RBAC (Role in the release namespace): `get`,`patch` on the three Deployments by name; `create`,`get`
Jobs; `get`,`list` pods and `get` pods/log. The status goes to PostgreSQL (`GET /api/v1/version`) and
the audit log. There is no backup step on Kubernetes: configure CloudNativePG backups or your managed
database's snapshots.

"Check now" / "Update now" in the UI: the CronJob has no long-running process to poll, so each run first handles
the requests queued since the previous run (an `apply` request installs in `notify` mode too). To act on a request
right away, start a run from the CronJob:

```sh
kubectl -n <namespace> create job --from=cronjob/<fullname>-updater openlog-updater-now-$(date +%s)
```

Caveat: the updater changes Deployments outside Helm. The next `helm upgrade` sets the image from
`image.tag` again — set it to the running version (or use GitOps, where the updater should stay in
`notify` mode).

## 5. Backups and restore

Compose backups are custom-format dumps of the PostgreSQL database (tenancy, users, keys, fleet state).
Telemetry lives in ClickHouse and Kafka volumes, which upgrades never delete; expand migrations only add.

```sh
cd deploy/compose
docker compose stop openlog
docker compose exec -T postgres pg_restore -U openlog -d openlog --clean --if-exists < backups/openlog-20260913T030000Z-0.9.0.dump
sed -i.bak 's|^OPENLOG_IMAGE=.*|OPENLOG_IMAGE=<previous image>|' .env
docker compose up -d openlog
```

Restoring a dump taken before an upgrade also restores `schema_migrations` of that time, so migrations
of the newer release run again on the next upgrade.

## 6. Troubleshooting

| Symptom | Check |
|---|---|
| `update_check: failed` | `GET /api/v1/version`; api leader logs `update check failed` (network to GitHub, mirror URL, "no trusted release keys" on self-built images → `OPENLOG_RELEASE_TRUSTED_KEYS_FILE`). |
| Updater `state: error` | `error` field: current version not readable (`OPENLOG_UPDATER_HEALTH_URLS`), index/manifest signature, Docker socket not mounted. A failed run is retried after `min(OPENLOG_UPDATER_INTERVAL, 1m)`, so the error right after `docker compose up` (`lookup openlog … no such host`: the updater starts before `openlog`) clears within a minute. |
| `waiting_for_maintenance_window` | `OPENLOG_UPDATER_MAINTENANCE_WINDOW` is UTC. |
| `up_to_date` but a newer release exists | `message` lists skipped releases: failed before (remove from `failed_versions` by deleting `/backups/updater-status.json` and `DELETE FROM system_state WHERE key='updater'`), `min_upgrade_from`, missing image. |
| `rolled_back` | `steps[]` shows the failing step; `docker compose logs openlog-updater` includes the tail of the failed container's log. The previous version keeps running. |
| `rollback_failed` | Manual action: `docker ps -a` — start `<name>-pre-update` after renaming it back, or restore from backup. |
| Contract migration never runs | `openlog-migrate -plan` names the old instance; stop it, or wait 5 minutes after it died without a graceful shutdown. |
| UI keeps asking to reload | A load balancer still sends some requests to an older pod; finish the rollout. |
| Acceptance tests | `make mixed-version` (N and N+1 side by side, contract gating), `make updater-acceptance` (Compose 0.9.0 → 0.9.1 with test expand + contract migrations → broken 0.9.2 rolled back; see below). |

### What `make updater-acceptance` proves

`test/autoupdate` (compose project `openlog-updtest`, host ports 25xxx, a local registry and a release index signed with a
throwaway key) runs the real `deploy/compose` stack and `openlog-updater` in `auto` mode:

1. **0.9.0 → 0.9.1**: every step `ok` (backup → pull → migrate → recreate → health → cleanup → contract-migrate); the test
   expand migration 9001 runs in the migrate step, the contract migration 9002 (`requires-all-at-least 0.9.1`) only in
   contract-migrate after the old container stopped; hosts, log/span/metric row counts, the logs and APM queries, the
   `pg_dump` backup and the rewritten `OPENLOG_IMAGE` are checked.
2. **Broken 0.9.2** (a real build with one more expand migration, 9003, whose allinone never becomes ready): the migrate
   step applies 9003, health fails (`not healthy within …: connection refused`), the containers are rolled back to 0.9.1
   (`rolled_back`, recorded in `failed_versions` and the audit log). **The expand migration stays applied**: 0.9.1 serves
   the same rows, answers queries and ingests new data with it — the reason expand migrations must stay compatible with
   the previous release. The next check shows `up_to_date` with `no eligible release newer than 0.9.1: 0.9.2: failed
   before on this installation`, which is what Settings → Organization → Version and updates displays.

`UPDTEST_FROM_IMAGE=ghcr.io/onuragtas/openlog:<published version>` starts from a published release instead of 0.9.0
(its own `openlog-updater` performs the update; `UPDTEST_UPDATER=to` uses the new one), so the update also applies every
real migration added since that release. `UPDTEST_KEEP=1` keeps the stack.

Measured 2026-09-14 (`UPDTEST_FROM_IMAGE=ghcr.io/onuragtas/openlog:0.1.9`, 212 s): the published 0.1.9 updater installed
the working-tree build, the migrate step applied PostgreSQL 0010…0056 (16 migrations) and ClickHouse 0020…0050 (7) plus the
test migrations before the new container started, every step was `ok`, and hosts and telemetry rows were kept; the broken
release was then rolled back as above. The default run (0.9.0 built from the tree) took 240 s.
