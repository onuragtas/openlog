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

- Those who may request updates (`update_requests.can_request`: superadmins on `OPENLOG_SIGNUP_ENABLED=true`
  installations, admins and owners otherwise; admins and owners when there is no request channel) see a banner
  "openlog 0.9.1 is available — Release notes" (dismissed per version in the browser).
- **Settings → Organization → Version and updates** has two buttons for admins and owners. On multi-tenant
  installations (`OPENLOG_SIGNUP_ENABLED=true`) the owners and admins of self-signup organizations are not server
  operators: there the buttons are shown only to superadmins (`OPENLOG_SUPERADMIN_EMAILS`, signed in with a verified
  e-mail), whatever their role in the organization they are signed in to; everyone else gets `403` (the audit entry of a
  superadmin request has `superadmin: true`). The section itself, with the updater state and notices, is visible to
  every member.
  - **Check now** runs the release check immediately and asks `openlog-updater` to check too (at most once per
    30 s); no need to wait for the daily check or `OPENLOG_UPDATER_INTERVAL`.
  - **Update now** (shown when a newer release is known and an updater reports) asks the updater to install that
    release now — also in `notify` mode, since an admin confirmed it. The confirmation has a checkbox
    "Install now, also outside the maintenance window"; without it the request fails outside
    `OPENLOG_UPDATER_MAINTENANCE_WINDOW`. The section shows the updater's maintenance window in UTC and in the browser's
    time with its next opening ("any time" without one; on Kubernetes the CronJob schedule decides when the updater
    runs); in `waiting_for_maintenance_window` the next opening is shown next to the state. The page shows the request state and the updater's steps (backup, pull,
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
| `OPENLOG_UPDATER_ENV_FILE` | `/compose/.env` | settings of recreated containers ([below](#settings-from-env)); `OPENLOG_IMAGE=` is rewritten after a verified update |
| `OPENLOG_UPDATER_COMPOSE_DIR` | directory of `OPENLOG_UPDATER_ENV_FILE` (`/compose`) | the compose project directory as mounted in the updater |
| `OPENLOG_UPDATER_COMPOSE_SYNC` | `auto` | `auto`: install-server.sh installations get the verified compose files of each installed version ([below](#compose-files)); `off`: compose files are never changed |
| `OPENLOG_UPDATER_SELF_UPDATE` | `auto` | after a successful update the updater replaces its own container with the installed image ([below](#updater-self-update)): `auto` install-server.sh installations (`.bundle-version`), `on` every Compose installation, `off` never (the notice `updater_outdated*` remains) |
| `OPENLOG_UPDATER_IMAGE_REPOSITORY` | – | pull the manifest digest from a mirror repository |
| `OPENLOG_UPDATER_COMPOSE_PROJECT` | detected from the updater container | |

Each run: read the version the stack reports (`/readyz`), verify the index, pick the newest release of
the channel that is newer, not failed before, has an image and allows `min_upgrade_from`. In `auto`
mode, inside the maintenance window:

1. **backup** — `pg_dump -Fc` via `docker exec` in the postgres container into `/backups`; keep the newest N. A failed backup aborts.
2. **pull** — the image **by digest** from the signed manifest (a tag cannot be swapped under it).
3. **compose-bundle** (install-server.sh installations only) — download `openlog-compose-<v>.tar.gz` listed in the signed manifest, check size and sha256, extract it into `.bundle-staging/` (regular files only, no `.env`). A failure here stops the update before anything changed ([Compose files](#compose-files)).
4. **migrate** — a one-off container of the new image runs `openlog-migrate` with the network and the (refreshed, [below](#settings-from-env)) environment of the running `openlog` container. Only expand (and eligible contract) migrations run, so the running release keeps working even if this fails.
5. **recreate** — for each service container: stop it (its stop timeout), rename it to `<name>-pre-update`, create a new container with the same config, labels, mounts, port bindings, restart policy, networks and aliases, the image replaced and the environment refreshed from `.env` and the compose files, start it. The step detail lists the refreshed variable names (never values).
6. **health** — every health URL must answer 200 with the target version before `OPENLOG_UPDATER_HEALTH_TIMEOUT`; a container that exits without a restart policy fails immediately.
7. **success** (step `cleanup`) — remove the `-pre-update` containers, install the staged compose bundle, rewrite `OPENLOG_IMAGE` in `.env`, then (step `contract-migrate`) run `openlog-migrate` again for contract migrations that were waiting for the old containers.
8. **failure after step 4** — remove the new containers, rename and start the previous ones, wait until they report the previous version: state `rolled_back` (or `rollback_failed`). The staged compose bundle is deleted; the compose files were never touched. The version is recorded in `failed_versions` and not retried; a newer release is.
9. **self-update** (only after success, once the result and a UI request are recorded; never after a failure or rollback) — an updater older than the installed release replaces its own container with that release's image ([Updater self-update](#updater-self-update)). A failure here does not change the result of the update.

The status is kept in `/backups/updater-status.json` and in PostgreSQL (shown as `updater` in
`GET /api/v1/version`); events go to the audit log (`updater.update_started`, `…_succeeded`,
`…_rolled_back`, `…_failed`, `…_available`, `updater.self_update_started` / `…_succeeded` / `…_failed`, actor
`openlog-updater`). The document also carries `updater_version` (the updater's own version) and `self_update` (its last
self-update attempt). If the updater dies mid-update,
it finds the `-pre-update` container on its next start and restores it (and an interrupted compose file replacement,
see below).

### Settings from `.env`

A container created from an older compose file does not have the settings that file did not list (for example
`OPENLOG_SAAS_MODE` in `.env` with a compose file from before that setting: the image was updated, the setting never
reached the container). The updater therefore refreshes the environment of every container it recreates from `.env`
and the compose files the container was created from (labels `com.docker.compose.project.config_files` and
`working_dir`, mapped into `/compose`; for install-server.sh installations the new bundle's `docker-compose.yml`),
with the precedence of `docker compose up`:

| # | Variable | Value in the recreated container |
|---|---|---|
| 1 | in the service's `environment`, every referenced variable set in `.env` (or a literal) | computed like compose: `${VAR:-default}` with the `.env` value, literals as written (a compose literal wins over `.env`) |
| 2 | in the service's `environment`, a referenced variable **not** in `.env` | the container's current value (it came from the shell or another env file when the container was created) |
| 3 | not in the service's `environment`, set in `.env` | set when the compose files are older than the installed version (their `x-openlog-compose-version` / `.bundle-version`; none = older) **and** do not reference the variable at all — the settings those files predate. Never image, `OPENLOG_UPDATER_*`, listen address (`*_ADDR`) or bundled-dependency TLS/SASL variables. With current compose files such variables are intentionally not passed. |
| 4 | anything else in the container (a `docker-compose.override.yml` value, …) | kept |

Values are parsed with compose's `.env` rules (quotes, `export`, `#` comments, `${VAR}` references). When the
compose files cannot be read (a file outside the project directory, a YAML error, a service using `extends`), only
`OPENLOG_*` variables of `.env` that the container lacks or has empty are added. An unreadable or unparsable `.env`
never blocks an update: the environment is then left as it was and the `recreate` step says so. Shell variables that
were exported when `docker compose up` ran are invisible to the updater: keep settings in `.env`.

The compose file passes every backend setting explicitly (the `x-openlog-settings` anchor, enforced by
`internal/config/envcoverage_test.go`); it deliberately has no `env_file: .env`, which would also hand PostgreSQL,
bootstrap and S3 credentials of other services to `openlog` (and switch data exports to the tiered storage bucket).

### Compose files

**install-server.sh installations** (`/opt/openlog-server/.bundle-version` exists) are kept at the running version:

- Every release has the asset `openlog-compose-<v>.tar.gz` (manifest component `compose`,
  [releases-updates.md §2](../contracts/releases-updates.md)): `docker-compose.yml` (with
  `x-openlog-compose-version: "<v>"`), `.env.example`, `clickhouse/`, `README.md`.
- It is verified and staged before `migrate` and installed only after the new version is healthy: the files it
  replaces (and `.bundle-version`) are copied to `.bundle-previous/`, a journal `.bundle-swap.json` is written, each
  file is renamed into place, settings that `.env` lacks are appended from the new `.env.example` (same rule as
  install-server.sh: active lines only, never credentials or `OPENLOG_IMAGE`; `.env` keeps its mode 0600), and
  `.bundle-version` is written last. On any error the previous files are restored from `.bundle-previous/` and the
  error is shown in the `cleanup` step; the updater restores them also when it finds the journal at start.
- Manual rollback of the compose files: `cp -R /opt/openlog-server/.bundle-previous/. /opt/openlog-server/`
  (restores the files and `.bundle-version`; settings appended to `.env` stay, they are defaults).
- The containers are recreated through the Docker API, so only the image and the environment change. Changes the
  updater does not apply — new or changed volumes, ports, networks, new services, or a changed mounted file such as
  `clickhouse/*.xml` — are reported as the notice `compose_changes_pending` ("… re-run install-server.sh or
  docker compose up -d") in Settings → Organization → Version and updates and in the updater log, until the service
  was recreated by compose (its `com.docker.compose.config-hash` changed) or, for mounted files, restarted.
  Re-running install-server.sh applies them (`docker compose up -d` with the new files). The updater does not run
  the compose CLI itself: that would need the compose plugin in the image and the project mounted at its host path
  (compose resolves relative bind mounts on the host), and it would recreate the updater's own container.
- A release without the asset (older than 0.1.22) or `OPENLOG_UPDATER_COMPOSE_SYNC=off`: the files stay, the step
  says why, and the notice `compose_outdated_bundle` asks to re-run install-server.sh.

**Other installations** (a git clone, your own directory): the updater never changes files. When the compose file's
`x-openlog-compose-version` (none: a file older than the updater's release) is older than the running version, it logs a
warning and shows the notice `compose_outdated`: "the compose files (0.1.21) are older than the running version
0.1.22: … update them (git checkout v0.1.22, then docker compose up -d) or migrate to install-server.sh". Settings
added to `.env` still reach recreated containers (rule 3 above), but volumes and new services do not until the files
are updated — or [migrate to install-server.sh](#migrating-from-a-git-clone-installation).

The first update that brings this behaviour is still performed by the updater container you run now: an updater older than
0.1.26 neither updates itself nor reports that it is old (the api adds the notice `updater_outdated` for it). Run
`install-server.sh` once (or `docker compose --profile updater up -d openlog-updater` with the new files) so later updates
use the current updater, which then keeps itself current ([below](#updater-self-update)).

### Notices

Shown in Settings → Organization → Version and updates (translated), in `GET /api/v1/version` (`updater.notices[]`) and
as warnings in the updater log; refreshed on every run.

| Code | Meaning | Fix |
|---|---|---|
| `compose_outdated` | compose files of a git clone / own directory older than the running version | `git checkout v<running>` + `docker compose up -d`, or migrate to install-server.sh |
| `compose_outdated_bundle` | install-server.sh compose files older than the running version (no bundle in the release, `OPENLOG_UPDATER_COMPOSE_SYNC=off`, failed install) | re-run install-server.sh |
| `compose_changes_pending` | compose changes the updater cannot apply (volumes, ports, new services, mounted files) | re-run install-server.sh or `docker compose up -d` |
| `updater_outdated_bundle` | the openlog-updater container (install-server.sh installation) is older than the running version: self-update off, failed or not yet possible | re-run install-server.sh |
| `updater_outdated` | the same for other Compose installations — also added by the api for status documents of updaters older than 0.1.26 (`updater_version: "< <running>"`), which cannot report it | `docker compose --profile updater up -d openlog-updater` in the compose directory (git clone), or re-run install-server.sh |
| `updater_outdated_kubernetes` | the updater CronJob runs an image older than the Deployments it updated | `helm upgrade … --set image.tag=<running version>` (§4) |
| `updater_self_update_failed` | the self-update to `version` failed; the previous updater keeps running and it is not retried for that version | read `reason` and the updater log; re-run install-server.sh (or `docker compose --profile updater up -d openlog-updater`) |

### Updater self-update

The `openlog-updater` container runs the image `.env` selected when it was created
(`OPENLOG_UPDATER_IMAGE`, default `OPENLOG_IMAGE`). Without help it would keep that image forever, so improvements of the
updater itself would never reach an installation. After a successful update to `V` (health check passed, compose files
installed, the UI request finished) an updater older than `V` therefore replaces its own container with `V`'s image — the
image the update already pulled by its signed digest. `OPENLOG_UPDATER_SELF_UPDATE=auto` (default) does this for
install-server.sh installations; `on` also for a git clone or your own directory (the container is recreated through the
Docker API like `openlog`, so your compose file is not needed, but a `docker-compose.override.yml` setting that is not in
the container stays as it is); `off` never (notice only). The Kubernetes CronJob runs the chart's image and never updates
itself.

The container name (e.g. `openlog-openlog-updater-1`) is the ownership token: Docker renames are atomic and names are
unique, so only one container can hold it, and only the holder acts on the stack.

1. The old updater writes `updater-handover.json` into the backup directory (mounted into both containers), rewrites
   `OPENLOG_UPDATER_IMAGE` in `.env` when `.env` pins it to an image (not when the updater follows `OPENLOG_IMAGE`, which
   the update already rewrote), and creates `<name>-self-update-new` from its own container: same labels (including
   `com.docker.compose.*`, so compose still owns it), mounts, networks, user and entrypoint, the environment refreshed
   from `.env` and the compose files like any recreated container, the new image — and **restart policy `no`**.
2. The new updater starts, finds the marker with its own container id, and runs a self-test: it reports the expected
   version, reaches Docker, reads the status file and the compose directory. It writes `updater-handover-ready.json` and
   waits; it does nothing else.
3. The old updater waits up to 2 minutes for that file. If the new container does not start, exits, fails the self-test or
   stays silent, the old updater removes it, restores `.env`, deletes the marker, records the step `self-update` as failed
   and the notice `updater_self_update_failed`, and keeps running. The self-update is not retried for `V` (the next
   release tries again).
4. On success the old updater renames itself `<name>-self-update-old` and stops acting for good.
5. The new updater takes the canonical name, applies the old restart policy (`unless-stopped`) to itself — the commit —,
   stops and removes the old container, deletes the marker, marks the step `self-update` ok (Settings shows
   "Updater self-update") and starts its normal loop (first run immediately).

Log lines to look for: `openlog-updater replaces its own container…`, `the new updater passed its self-test`, `handed
over to the new updater container`, then in the new container `self-update: took over the container name` and
`openlog-updater replaced its own container`.

**Crash safety.** Every updater reads the marker at start, before it acts:

| Interrupted | After a host or daemon restart | Result |
|---|---|---|
| before the old updater renamed itself | only the old container restarts (the new one has restart policy `no`); it still holds the name | it removes the new container, restores `.env`: failed, old updater runs |
| after the rename, before the new updater took the name | only the old one restarts (named `…-self-update-old`); the new one is stopped | it removes the stopped, uncommitted new container and takes its name back: failed, old updater runs |
| new updater took the name, not yet committed | same: the new container is stopped with policy `no` | as above |
| after the commit | both restart; the old one sees a committed new updater and only waits | the new one removes the old container: succeeded |
| the new updater hangs | — | it exits when the handover is 3 minutes old unless it holds the name; the old one then reclaims |
| `docker compose up -d openlog-updater` during the handover | the container compose creates holds the name | it removes both handover containers and deletes the marker |

The old updater reclaims the name only when the new container is gone, or stopped **and** still has restart policy `no`:
a stopped container with that policy can never act again, and a committed one is never touched. So at most one updater
acts at any time, and after any restart exactly one remains.

**Disable:** `OPENLOG_UPDATER_SELF_UPDATE=off` in `.env`, then `docker compose --profile updater up -d openlog-updater`
(or re-run install-server.sh).

**Manual recovery** (should not be needed): `docker ps -a --filter name=self-update` lists leftovers. Keep the container
named `<project>-openlog-updater-1` (rename one back with `docker rename`), `docker rm -f` the others, delete
`backups/updater-handover.json` and `backups/updater-handover-ready.json`, and re-run install-server.sh (or
`docker compose --profile updater up -d openlog-updater`).

### Migrating from a git clone installation

A stack started from a clone (`/opt/openlog/deploy/compose`, project `openlog`) moves to the install-server.sh layout
without data loss: the named volumes (`openlog_postgres-data`, `openlog_clickhouse-data`, `openlog_kafka-data`, …)
belong to the project name, not to the directory.

```sh
sudo mkdir -p /opt/openlog-server/releases
sudo cp /opt/openlog/deploy/compose/.env /opt/openlog-server/.env && sudo chmod 600 /opt/openlog-server/.env
sudo cp /opt/openlog/deploy/compose/releases/plans.json /opt/openlog-server/releases/ 2>/dev/null || true   # if you use a plan catalog
# optional: keep the updater's dumps
sudo cp -R /opt/openlog/deploy/compose/backups /opt/openlog-server/ 2>/dev/null || true
curl -fsSL https://github.com/onuragtas/openlog/releases/latest/download/install-server.sh |
  sudo sh -s -- --project openlog --dir /opt/openlog-server
docker compose -p openlog -f /opt/openlog-server/docker-compose.yml --env-file /opt/openlog-server/.env ps
```

The installer keeps the copied `.env` (secrets are never regenerated; missing settings are appended), writes the
compose files of the running or newer release, recreates the containers from them in the same project and starts the
updater from the new directory. Settings from a `docker-compose.override.yml` must be moved into `.env` first
(install-server.sh and the updater use only `docker-compose.yml` and `.env`; data exports have the volume
`data-exports`, so no override is needed for them). Check the UI and `ps` (all services healthy), then remove the old
directory (`sudo rm -rf /opt/openlog` — never `docker compose down -v`, which deletes the volumes).

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

**Updated automatically:** the services in `OPENLOG_UPDATER_SERVICES` (`openlog`), `OPENLOG_IMAGE` in `.env`, the compose
files of install-server.sh installations, and — since 0.1.26, with `OPENLOG_UPDATER_SELF_UPDATE` — the `openlog-updater`
container itself ([Updater self-update](#updater-self-update)).

**Not updated automatically:** `bootstrap`, `loadgen`, `openlog-renderer`, third-party images (Kafka, ClickHouse,
PostgreSQL), the updater container with `OPENLOG_UPDATER_SELF_UPDATE=off` (or `auto` outside install-server.sh
installations: notice `updater_outdated`), and updater containers older than 0.1.26 (run install-server.sh or
`docker compose --profile updater up -d openlog-updater` once).

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
`notify` mode). The CronJob itself keeps running the chart's `image.tag`, so after it updated the Deployments it reports
the notice `updater_outdated_kubernetes` until the next `helm upgrade --set image.tag=<running version>`.

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
| `waiting_for_maintenance_window` | `OPENLOG_UPDATER_MAINTENANCE_WINDOW` is UTC; Settings → Version and updates (`updater.maintenance_window.next_open_at`) shows the next opening. To install earlier: **Update now** with "Install now, also outside the maintenance window". |
| `up_to_date` but a newer release exists | `message` lists skipped releases: failed before (remove from `failed_versions` by deleting `/backups/updater-status.json` and `DELETE FROM system_state WHERE key='updater'`), `min_upgrade_from`, missing image. |
| `rolled_back` | `steps[]` shows the failing step; `docker compose logs openlog-updater` includes the tail of the failed container's log. The previous version keeps running. |
| `rollback_failed` | Manual action: `docker ps -a` — start `<name>-pre-update` after renaming it back, or restore from backup. |
| A setting in `.env` has no effect | `docker inspect <container> --format '{{json .Config.Env}}'`; the notice `compose_outdated` / `compose_outdated_bundle` means the compose files predate the setting: update them (install-server.sh, or `git checkout v<running version>` + `docker compose up -d`). The next updater run adds unreferenced settings ([Settings from .env](#settings-from-env)). |
| `compose_changes_pending` | Re-run install-server.sh (or `docker compose -p openlog -f … --env-file … up -d`); for a changed mounted file a restart of the named service is enough. |
| `compose-bundle` step failed | Download or sha256 of `openlog-compose-<v>.tar.gz` (network, mirror without the asset: `OPENLOG_UPDATER_COMPOSE_SYNC=off` and re-run install-server.sh for each version). Nothing was changed. |
| `cleanup` step: compose files restored | The update itself succeeded; the files stayed at the previous version (notice `compose_outdated_bundle`): re-run install-server.sh. |
| No "Check now" / "Update now" with `OPENLOG_SIGNUP_ENABLED=true` | Only superadmins see them: the e-mail must be in `OPENLOG_SUPERADMIN_EMAILS` of `openlog` and verified. |
| `updater_outdated*` | The updater container is older than the running version ([Notices](#notices)); `docker inspect <project>-openlog-updater-1 --format '{{.Config.Image}}'`. |
| `self-update` step failed / `updater_self_update_failed` | `reason` and `docker compose logs openlog-updater` (the tail of the new container's log is included). The previous updater keeps running; fix the cause and re-run install-server.sh. |
| Containers `…-self-update-new` / `…-self-update-old` stay | A handover is in progress (up to ~3 min) or was left by a manual intervention: [manual recovery](#updater-self-update). |
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
   **Self-update** (`OPENLOG_UPDATER_SELF_UPDATE=on`; skipped with `UPDTEST_FROM_IMAGE` or `UPDTEST_UPDATER=to`): the 0.9.0
   updater hands over to a 0.9.1 container — `self_update.state=succeeded`, step `self-update` ok, `updater_version`
   0.9.1, `openlog-updtest-openlog-updater-1` runs the 0.9.1 digest with restart policy `unless-stopped` and its compose
   service label, no `-self-update-` container is left — and that new updater performs step 2.
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
