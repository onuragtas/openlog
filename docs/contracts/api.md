# Contract: Query and management API (M1)

Base path `/api/v1`. JSON responses. Times accepted as RFC3339 or unix milliseconds; returned as RFC3339 with nanoseconds (UTC).
Machine-readable spec: [openapi.yaml](openapi.yaml). Data model: [postgres.md](postgres.md).

Errors: HTTP status + `{"error": {"code": "invalid_argument", "message": "…"}}`. Codes: `invalid_argument` (400),
`unauthenticated` (401), `permission_denied` (403), `not_found` (404), `already_exists` (409),
`failed_precondition` (409), `resource_exhausted` (429, with `Retry-After`), `internal` (500),
`unavailable` (503, with `Retry-After`), `timeout` (504).

## Authentication

`OPENLOG_AUTH_MODE=postgres` (default). Every endpoint except the public ones below needs one of:

| Credential | How | Acts as | Notes |
|---|---|---|---|
| Session | cookie `openlog_session` from `POST /auth/login` | the user's role in the selected organization | `HttpOnly`, `SameSite=Strict`, `Path=/api`, `Secure` (unless `OPENLOG_COOKIE_SECURE=false`). Server-side in PostgreSQL; ends after `OPENLOG_SESSION_TTL` or `OPENLOG_SESSION_IDLE_TIMEOUT` of inactivity, on logout, on revocation, or when the password changes (other sessions) |
| API key | `Authorization: Bearer ola_…` | `viewer` of the key's organization | Read-only: telemetry endpoints, `GET /auth/me`, `GET /orgs/current`. Every other management endpoint answers `403`. Revocation is immediate |

Ingest license keys (`olk_…`) are **not** API credentials, and API keys are not ingest credentials.

**CSRF.** A cookie-authenticated request with a method other than `GET`/`HEAD`/`OPTIONS` must send the session's
token in `X-CSRF-Token` (returned as `csrf_token` by `/auth/login`, `/auth/signup`, `/invitations/accept` and
`GET /auth/me`); otherwise `403 permission_denied`. Bearer requests do not need it. Endpoints with a JSON body
require `Content-Type: application/json` (`400` otherwise), which also protects the unauthenticated sign-in
endpoints against cross-site form posts. The API never sends CORS headers.

**Organization.** The tenant of every telemetry query is taken **only** from the authenticated principal and
injected by the query layer; no endpoint accepts a tenant parameter. A user who belongs to several organizations
selects one per request with the header `X-Openlog-Org-Id: <organization id>`; without it the user's oldest
membership is used. A header naming an organization the user is not a member of → `403`. For API keys the header
is optional and must match the key's organization. A signed-in user without any membership gets `403` on
telemetry endpoints.

**Rate limiting.** Failed password checks (sign-in, invitation acceptance by an existing user, password change)
are counted per (email, client IP) in PostgreSQL; after `OPENLOG_LOGIN_MAX_FAILURES` within `OPENLOG_LOGIN_WINDOW`
the request gets `429 resource_exhausted`, even with the right password. The client IP is the TCP peer, or the
right-most untrusted `X-Forwarded-For` hop when the peer is in `OPENLOG_API_TRUSTED_PROXIES`. Unknown emails and
wrong passwords return the same `401` with equal timing.

`OPENLOG_AUTH_MODE=static` (development/tests): the license keys of `OPENLOG_LICENSE_KEYS` authenticate the query
endpoints (`openlog-license-key` header or `Authorization: Bearer`) as `viewer`; only `GET /auth/config` and
`GET /auth/me` exist besides the telemetry endpoints (all other paths below → `404`).

## Roles

| Operation | viewer | member | admin | owner |
|---|:-:|:-:|:-:|:-:|
| Telemetry, `GET /orgs/current`, `GET /members`, `GET /fleet/*`, own sessions, leave organization | ✓ | ✓ | ✓ | ✓ |
| `GET /license-keys`, `GET /api-keys`, `POST /api-keys`, revoke own API keys | | ✓ | ✓ | ✓ |
| Alerting reads (`GET /alerts/*`) and rule preview | ✓ | ✓ | ✓ | ✓ |
| Create alert rules and mutes, change/delete **own** rules and mutes, acknowledge/resolve/annotate incidents | | ✓ | ✓ | ✓ |
| `GET /integrations/settings` | ✓ | ✓ | ✓ | ✓ |
| `PATCH /orgs/current`, invitations, create/revoke license keys, revoke any API key, change roles/remove members (not owners), `GET /audit-log`, fleet changes (`PUT /fleet/policy`, host overrides, pause/resume, deploy now, rollback), integration setting changes, any alert rule or mute, alert channels and test sends, `POST /version/check` and `POST /version/update` (signed-in users only; refused for everyone when `OPENLOG_SIGNUP_ENABLED=true`) | | | ✓ | ✓ |
| Grant or remove the owner role, invite owners, remove owners | | | | ✓ |

An organization always keeps at least one owner (`409 failed_precondition`).

## Auth endpoints

### `GET /api/v1/auth/config` (public)
`{"mode": "postgres", "signup_enabled": false, "password_min_length": 8}`

### `POST /api/v1/auth/login` (public)
Body `{"email": "…", "password": "…"}`. `200` sets the session cookie and returns the **Me** object. `401` for any
wrong email/password combination, `429` when rate limited.

### `POST /api/v1/auth/signup` (public, `OPENLOG_SIGNUP_ENABLED=true`)
Body `{"email", "password", "name", "organization_name"}` → `201` + cookie + **Me**. Creates a new organization
(generated tenant id) owned by the new user. `403` when disabled, `409` when the email exists.

### `GET /api/v1/auth/me`
**Me** object:
```json
{"auth": "session", "user": {"id": "…", "email": "ada@example.com", "name": "Ada"},
 "organization": {"id": "…", "tenant_id": "default", "name": "Default"}, "role": "owner",
 "organizations": [{"id": "…", "tenant_id": "default", "name": "Default", "role": "owner"}],
 "csrf_token": "…"}
```
For API keys: `"auth": "api_key"`, `user`/`csrf_token` `null`, `organizations: []`, `role: "viewer"`.

### `POST /api/v1/auth/logout`
Revokes the session and clears the cookie → `204`.

### `POST /api/v1/auth/password`
Body `{"current_password", "new_password"}` → `204`; revokes the user's other sessions. Wrong current password → `403`.

## Organization and members

### `GET /api/v1/orgs/current` · `PATCH /api/v1/orgs/current` `{"name"}`
`{"id", "tenant_id", "name", "role", "created_at"}` (`role` = caller's role).

### `GET /api/v1/members`
`{"members": [{"user_id", "email", "name", "role", "joined_at"}]}`

### `PATCH /api/v1/members/{user_id}` `{"role"}` · `DELETE /api/v1/members/{user_id}`
`204`. A user may always remove themselves (leave).

## Invitations

Invitations are not emailed in M1: the inviter gets a one-time token and shares the accept link
(`/invite?token=<token>` in the web UI).

### `GET /api/v1/invitations` · `POST /api/v1/invitations` `{"email", "role"}` · `DELETE /api/v1/invitations/{id}`
List: `{"invitations": [{"id", "email", "role", "invited_by_email", "created_at", "expires_at"}]}` (pending only).
Create → `201 {"invitation": {…}, "token": "oli_…"}` (**token shown once**); `409` if the email is already a member
or has a pending invitation.

### `POST /api/v1/invitations/lookup` (public) `{"token"}`
`{"organization_name", "email", "role", "expires_at", "user_exists"}`; `404` when invalid, used, revoked or expired.

### `POST /api/v1/invitations/accept` (public) `{"token", "password", "name"}`
New user: creates the account with `password` (policy: 8–256 characters). Existing user (`user_exists`): `password`
must be their current password. Adds the membership, signs in (cookie) and returns **Me** with the user's default
organization (send `X-Openlog-Org-Id` to switch).

## Ingest license keys

### `GET /api/v1/license-keys`
`{"license_keys": [{"id", "name", "prefix": "olk_1a2b3c4d", "custom": false, "created_by_email", "created_at", "last_used_at", "revoked_at"}]}`
(revoked keys included). `last_used_at` is updated at most once a minute and may lag by up to a minute per ingest pod.

### `POST /api/v1/license-keys` `{"name", "key"?}`
Without `key`: `201 {"license_key": {…, "custom": false}, "key": "olk_…"}` — **the key is shown only in this response**.

With `key` an existing value is **imported**, so senders that already use it (e.g. an `x-api-key` header configured
for another backend) keep working without being redeployed:
- Surrounding whitespace is trimmed; the value must be 16–256 characters of printable ASCII without spaces, quotes
  or backslashes (`[\x21-\x7e]` minus `"`, `'`, `\`), so it fits HTTP headers, gRPC metadata and `install.sh`.
  Otherwise `400 invalid_argument` (an empty `key` is also rejected; omit it to generate a key).
- Only `sha256(key)` and a display prefix revealing at most half of the value are stored.
- A value already used by **any** organization, active or revoked, fails with `409 already_exists`; the response
  does not say which organization has it.
- `201 {"license_key": {…, "custom": true}, "key": null}` (the caller already knows the value). Audit:
  `license_key.create` with `details.custom = true`.

### `DELETE /api/v1/license-keys/{id}`
Revokes → `204` (idempotent). Ingest pods keep accepting the key until their cache entry expires, i.e. for up to
`OPENLOG_AUTH_CACHE_TTL` (default 60 s).

## API keys

### `GET /api/v1/api-keys`
`{"api_keys": [{"id", "name", "prefix": "ola_…", "scope": "read", "created_by_user_id", "created_by_email", "created_at", "last_used_at", "expires_at", "revoked_at"}]}`

### `POST /api/v1/api-keys` `{"name", "expires_at"?}`
`201 {"api_key": {…}, "key": "ola_…"}` — **shown once**. `expires_at` (optional) must be in the future.

### `DELETE /api/v1/api-keys/{id}`
Revokes → `204`; effective immediately.

## Sessions and audit log

### `GET /api/v1/sessions` · `DELETE /api/v1/sessions/{id}`
The caller's active sessions: `{"sessions": [{"id", "created_at", "last_seen_at", "expires_at", "ip", "user_agent", "current"}]}`.
Revoking the current session also clears the cookie.

### `GET /api/v1/audit-log?limit=`
Newest first (default 100, max 500): `{"events": [{"id", "actor_email", "action", "target_type", "target_id", "details", "ip", "created_at"}]}`.
Actions: see [postgres.md](postgres.md#audit_log).

## Version

### `GET /api/v1/version` (any authenticated principal, both auth modes)
`{"version", "commit", "date", "latest_available": {"version", "notes_url", "checked_at"} | null,
"update_check": "enabled|disabled|failed", "updater": {…} | null}`. `latest_available` is the newest verified
release of `OPENLOG_UPDATE_CHANNEL` when it is newer than the answering pod. `updater` is the status document of
`openlog-updater` (`engine`, `mode`, `state`: `off|error|up_to_date|available|waiting_for_maintenance_window|updating|succeeded|failed|rolled_back|rollback_failed`,
`current_version`, `target_version`, `steps[]`, `failed_versions[]`, `history[]`, …; see openapi `UpdaterStatus`).
`update_requests` (null in static mode): `{"can_request", "updater_listening", "updater_polled_at", "latest": UpdateRequest | null}`;
`can_request` = the caller may use the two endpoints below; `latest.requested_by_email` only when `can_request`.
Every API response (including errors and the UI) carries `X-Openlog-Version`.

### `POST /api/v1/version/check` (admin, owner; postgres auth mode)
"Check now": queues an update request `action=check` for `openlog-updater` and runs the api release check
immediately (`OPENLOG_UPDATE_CHECK=enabled`), then returns `200` with the `GET /version` body. At most one check
request per 30 s for the whole installation: `429 resource_exhausted` with `Retry-After` (seconds). Audit
`update.check_requested` (target `update_request`). API keys, lower roles, and every caller when
`OPENLOG_SIGNUP_ENABLED=true` (organization admins are not server operators) → `403`.

### `POST /api/v1/version/update` `{"target_version", "ignore_maintenance_window"?: false}` (admin, owner)
"Update now": `202` with the queued `UpdateRequest` `{"id", "action": "apply", "target_version",
"ignore_maintenance_window", "state": "pending|running|done|failed|expired", "message", "requested_by_email",
"requested_at", "picked_at", "finished_at"}`. The updater installs the release also in `notify` mode, only when it
still selects `target_version`, and outside its maintenance window only with `ignore_maintenance_window: true`
(releases-updates.md §5.1). `400` invalid version; `409 failed_precondition` when the version is not newer than the
answering pod, no updater reports, `OPENLOG_UPDATER_MODE=off`, an update is running or an apply request is already
open; `429` within 30 s of the previous apply request. Audit `update.apply_requested` (`details.from`, `to`,
`ignore_maintenance_window`, `engine`). Progress: poll `GET /version` (`update_requests.latest`, `updater.state`,
`updater.steps`); the Compose updater picks requests up within `OPENLOG_UPDATER_REQUEST_POLL` (10 s), the Kubernetes
CronJob at its next run.

## Hosts

### `GET /api/v1/hosts?limit=`
Hosts seen in the last 24h, ordered by `host_name`.
```json
{"hosts": [{"host_id": "…", "host_name": "web-1", "os_description": "Ubuntu 24.04 LTS", "arch": "amd64",
            "agent_version": "0.1.0", "last_seen": "2026-09-13T10:00:00Z", "resource_attributes": {"env": "prod"}}]}
```

### `GET /api/v1/hosts/{host_id}`
Single host object (same shape) or `404`.

Every `/hosts/{host_id}/…` endpoint (`metrics`, `inventory`, `services`) first checks that the host has a host record
in the caller's organization and returns `404 not_found` (`"host not found"`) otherwise. A host of another
organization is indistinguishable from an unknown one. Parameter errors (`400`) are reported before this check.

## Containers

Containers reported by the infra agent: cgroup metrics, `openlog.container.status` and `container.restarts` data points
(semantic-conventions.md §2 "Container metrics"), kept in the `containers` table (0009_containers, 30 days after the last
data point). A container is identified by `container_id` (64 hex); `reporting` is false when its last data point is older
than 5 minutes. Telemetry permissions (any role, API keys too); every query is tenant-scoped. `container_id` path
parameters that are not 64 hex characters → `400 invalid_argument`.

**Container object** (`Container`):
```json
{"container_id": "3f4e…", "name": "openlog-agents-orders-1", "image_name": "openlog-apmdemo/orders", "image_tags": ["1"],
 "runtime": "docker", "host_id": "…", "host_name": "docker-desktop", "compose_project": "openlog-agents",
 "compose_service": "orders", "k8s_pod_name": "", "k8s_namespace_name": "", "k8s_container_name": "",
 "state": "running", "health": "healthy", "started_at": "2026-09-14T09:00:00.5Z", "restart_count": 0,
 "first_seen": "…", "last_seen": "…", "reporting": true,
 "cpu_utilization": 0.012, "memory_usage": 18350080, "memory_limit": 8233447424,
 "cpu_sparkline": [[1757757600000, 0.011]], "memory_sparkline": [[1757757600000, 18350080]]}
```
`state` is the Docker state (`running`, `paused`, `restarting`, `exited`, `created`, `dead`) or `""` when the agent sends
none (no Docker API access); `health` is `healthy`/`unhealthy`/`starting` or `""`; `started_at` is `null` when unknown.
`cpu_utilization` (0..1 of the host), `memory_usage` and `memory_limit` (bytes; host memory when unlimited) are the latest
bucket of the range, `null` without data points.

### `GET /api/v1/containers?host_id=&compose_project=&compose_service=&state=&q=&from=&to=&limit=`
Containers with data in `[from, to]` (default last hour), ordered by compose project, compose service and name:
`{"containers": [Container…], "total": 12, "step": "120s"}`. `total` counts matches before `limit`; sparklines have
≈ 30 buckets of `step`. `compose_project`/`compose_service` are exact matches, present but empty = containers without
one; `state` is one of the states above or `unknown` (`400` otherwise); `q` (≤ 256 bytes) matches every whitespace-separated
term, case-insensitive, against id, name, image, tags, host, compose and Kubernetes names.

### `GET /api/v1/containers/groups?host_id=&state=&q=&from=&to=`
The same containers grouped by compose project and service. `running` counts reporting containers in state `running`;
`cpu_utilization` and `memory_usage` sum the latest values of reporting containers (`null` without data; not computed
above `OPENLOG_API_MAX_ROWS` containers).
```json
{"projects": [{"compose_project": "openlog-agents", "host_ids": ["…"], "containers": 9, "running": 9,
  "services": [{"compose_service": "orders", "containers": 1, "running": 1, "cpu_utilization": 0.01, "memory_usage": 18350080}]}]}
```

### `GET /api/v1/containers/{container_id}?from=&to=`
The container's latest record (the host it was seen on last) with stats over the range, plus `attributes` (data point
attributes of its latest status point), or `404`.

### `GET /api/v1/containers/{container_id}/timeseries?from=&to=&step=`
Chart series from raw data points (`step`: Go duration ≥ `10s`, default ≈ 300 points; 30-day retention), `404` for an
unknown container. Gauges are averaged per bucket; `network_*` and `blockio_*` are per-second rates of the cumulative
counters per series (resets clamp to 0), summed over series.
```json
{"container_id": "…", "host_id": "…", "step": "20s", "from": 1757757600000, "to": 1757761200000,
 "series": {"cpu_utilization": [[1757757600000, 0.01]], "memory_usage": [], "memory_limit": [], "network_receive": [],
            "network_transmit": [], "blockio_read": [], "blockio_write": []}}
```

### `GET /api/v1/containers/{container_id}/services?from=&to=`
APM services whose span resources carried this `container.id` (`apm_service_containers`, apm.md §1), with the RED object
over the range: `{"services": [{"service_name", "service_namespace", "environment", "first_seen", "last_seen", "apdex_t_ms", …ApmRed}]}`.

Container logs: `GET /api/v1/logs?container_id=…` (see Logs).

## Metrics

### `GET /api/v1/metrics/names?host_id=&from=&to=`
`{"names": [{"name": "system.cpu.utilization", "type": "gauge", "unit": "1"}]}`

### `GET /api/v1/hosts/{host_id}/metrics?name=&from=&to=&step=&agg=&group_by=`

| Param | Default | Notes |
|---|---|---|
| `name` | required | metric name |
| `step` | auto (≈ 300 points) | Go duration, min `10s` |
| `agg` | `avg` for gauges, `rate` for monotonic sums, `last` for non-monotonic sums | `avg`, `min`, `max`, `sum`, `last`, `rate` |
| `group_by` | all attributes | comma-separated attribute keys; series are merged by these keys. `resource.<key>` (allowed keys below) groups by a resource attribute, reported as `resource.<key>` in `attributes` |
| `resource.<key>` | — | exact-match resource attribute filter (AND-ed, one value per key, at most 4) |

`rate` = per-second increase per series per step (counter resets clamp to 0), then summed across merged series.
Ranges longer than 6h read from the 1-minute rollup table, except requests with `resource.*` filters or groupings
(the rollup has no resource attributes), which read raw data points (30-day retention).

Allowed `resource.<key>` keys — integration instance identity and PostgreSQL entities (semantic-conventions §6.1, §6.5):
`openlog.discovery.id`, `openlog.discovery.instance`, `openlog.integration.id`, `service.instance.id`, `server.address`,
`server.port`, `postgresql.database.name`, `postgresql.table.name`, `postgresql.index.name`. Any other key, an empty or
repeated value, or a value longer than 1024 bytes → `400 invalid_argument` (reported before the host check). Values are
bound query parameters. Integration panels select one instance with
`?resource.openlog.discovery.id=redis&resource.openlog.discovery.instance=/usr/bin/redis-server`.

```json
{"metric": {"name": "system.cpu.utilization", "type": "gauge", "unit": "1"}, "step": "60s",
 "series": [{"attributes": {"cpu.mode": "user"}, "points": [[1757757600000, 0.12]]}]}
```

## Inventory and discovery

### `GET /api/v1/hosts/{host_id}/inventory?category=`
Items of the host's latest **complete** snapshot. A known host without a complete snapshot yet returns `200` with `snapshot_id: ""` and empty `items`.
```json
{"snapshot_id": "…", "snapshot_time": "…", "items": [{"category": "package", "key": "dpkg:openssl", "data": {"name": "openssl", "version": "3.0.13"}}]}
```

### `GET /api/v1/hosts/{host_id}/services`
Shortcut for `category=discovered_service`; `data` is the discovered service body.

### `GET /api/v1/inventory/search?category=&q=&limit=`
Across all hosts' latest complete snapshots: items in `category` (required) whose key contains `q` (case-insensitive).
`{"items": [{"host_id": "…", "host_name": "…", "category": "package", "key": "dpkg:openssl", "data": {…}}]}`

## Logs

### `GET /api/v1/logs?host_id=&service=&container_id=&compose_project=&compose_service=&q=&severity_min=&trace_id=&attr.<key>=&from=&to=&limit=`
Excludes inventory events (`event.name` starting with `openlog.inventory.`). `q` is a case-insensitive substring match on body. Newest first.

`attr.<key>=<value>` is an exact-match filter on a log record attribute (AND-ed, one value per key, bound as a query
parameter). Allowed keys — the attributes the infra agent sets (semantic-conventions §4):
`openlog.log.source` (`file` / `journald` / `container`), `log.file.path`, `log.file.name`, `openlog.discovery.id`,
`openlog.systemd.unit`, `openlog.syslog.identifier`, `log.iostream` (`stdout` / `stderr`). Any other `attr.*` key, an empty or repeated value, or a value
longer than 1024 bytes → `400 invalid_argument`. Agent log records have an empty `service_name`, so host log views use
`host_id` plus these filters, e.g. `?host_id=…&attr.openlog.discovery.id=nginx&attr.log.file.path=/var/log/nginx/error.log`.
`container_id`, `compose_project` and `compose_service` are exact matches on the resource attributes `container.id`
(lower-cased), `docker.compose.project` and `docker.compose.service` of container logs (semantic-conventions §4).
```json
{"logs": [{"timestamp": "…", "severity_text": "ERROR", "severity_number": 17, "body": "…", "host_id": "…",
           "service_name": "…", "trace_id": "…", "span_id": "…", "attributes": {}, "resource_attributes": {}}]}
```

## Traces

### `GET /api/v1/traces/{trace_id}`
All spans of the trace ordered by start time, or `404`.
```json
{"trace_id": "…", "spans": [{"span_id": "…", "parent_span_id": "…", "name": "GET /users", "kind": "server",
  "service_name": "…", "start": "…", "duration_ns": 1234567, "status_code": "ok", "status_message": "",
  "attributes": {}, "resource_attributes": {}, "events": [{"timestamp": "…", "name": "…", "attributes": {}}]}]}
```

## APM

Application performance data derived from traces. Definitions (transactions, error groups, sampling weights,
latency histogram, Apdex, service map merge rules) are in [apm.md](apm.md); this section lists the endpoints.
Telemetry permissions (any role, API keys too) except `PUT …/settings`.

Common parameters: `from`/`to` (default last hour), `step` (Go duration ≥ `60s`, default ≈ 60 points, rounded up to
whole minutes; `400` below 60s). On `/apm/services/{service_name}/…`: `namespace`, `environment` — omitted = every
namespace/environment of the service, present (also empty) = exact match. Data is per minute: the first bucket starts
at `from` truncated to the minute. Durations are milliseconds; `throughput` is requests (or calls) per minute;
`error_rate` and `apdex` are 0..1; `avg_ms`, `p50_ms`, `p95_ms`, `p99_ms`, `apdex` are `null` without requests. All
counts are weighted by the sampling probability (apm.md §4).

**RED object** (`ApmRed`): `{"requests", "throughput", "errors", "error_rate", "avg_ms", "p50_ms", "p95_ms", "p99_ms", "apdex"}`.

### `GET /api/v1/apm/services?from=&to=&step=&namespace=&environment=&q=`
Services with spans since `from` (`q`: case-insensitive substring of the name), ordered by name:
```json
{"step": "60s", "services": [{"service_name": "orders", "service_namespace": "shop", "environment": "prod",
  "language": "go", "version": "1.4.2", "last_seen": "…", "apdex_t_ms": 500, "requests": 5400, "throughput": 90,
  "errors": 27, "error_rate": 0.005, "avg_ms": 41.2, "p50_ms": 18.3, "p95_ms": 120.4, "p99_ms": 480.0, "apdex": 0.97,
  "sparkline": [[1757757600000, 88.0]]}]}
```

### `GET /api/v1/apm/services/{service_name}`
`404` when the service has no data within retention.
```json
{"service_name": "orders", "apdex_t_ms": 500, "apdex_t_default": true,
 "instances": [{"service_namespace": "shop", "environment": "prod", "first_seen": "…", "last_seen": "…", "version": "1.4.2",
                "language": "go", "sdk_name": "opentelemetry", "resource_attributes": {"host.id": "…"}}],
 "hosts": [{"host_id": "…", "host_name": "web-1", "first_seen": "…", "last_seen": "…", "known": true}]}
```
`known`: the host has a host record (`GET /hosts/{host_id}` works).

### `GET /api/v1/apm/services/{service_name}/overview?transaction=&type=`
`{"step": "60s", "apdex_t_ms": 500, "totals": ApmRed, "series": [{"t": 1757757600000, …ApmRed}]}`; `transaction`/`type`
restrict to one transaction. Buckets without requests are omitted.

### `GET /api/v1/apm/services/{service_name}/transactions?sort=&type=&limit=`
`sort` = `time` (default: most time consumed) | `throughput` | `slowest` (p95) | `errors`; `limit` default 100, max 1000.
`{"apdex_t_ms": 500, "transactions": [{"transaction_type": "web", "transaction_name": "GET /orders/{id}", "time_consumed_ms", "time_share", "max_ms", …ApmRed}]}`

### `GET /api/v1/apm/services/{service_name}/transaction?name=&type=`
`name` required.
```json
{"transaction_name": "GET /orders/{id}", "transaction_type": "", "step": "60s", "apdex_t_ms": 500, "totals": ApmRed,
 "max_ms": 1840.2, "series": [ApmPoint], "histogram": [{"from_ms": 17.4, "to_ms": 19.0, "count": 42}],
 "slowest": [{"trace_id", "span_id", "timestamp", "duration_ms", "is_error", "http_status_code", "service_name", "transaction_name"}]}
```
`histogram` lists the non-empty latency buckets (apm.md §4.1); `slowest` the 10 slowest entry spans in the range.

### `GET /api/v1/apm/services/{service_name}/errors?limit=`
Error groups with occurrences in the range, most frequent first (`limit` default 50, max 500):
```json
{"step": "60s", "groups": [{"group_id": "9f3c2a71d4b84e0f", "error_type": "*errors.errorString",
  "message": "order <n>: inventory shard <n> unavailable", "count": 12, "total_count": 480, "first_seen": "…",
  "last_seen": "…", "last_trace_id": "…", "last_span_name": "GET /orders/{id}", "sparkline": [[1757757600000, 2]]}]}
```
`count` is in the range, `total_count`, `first_seen`, `last_seen` over retention.

### `GET /api/v1/apm/services/{service_name}/errors/{group_id}`
`400` unless `group_id` is 16 hex digits, `404` for an unknown group. Group fields above plus `last_message` (raw),
`stacktrace` (newest sample), `last_span_id`, `series` (`[[ms, count]]`) and `samples` (newest 20 error spans in the
range: `{"trace_id", "span_id", "timestamp", "span_name", "transaction_name", "duration_ms", "message"}`).

### `GET /api/v1/apm/services/{service_name}/databases?sort=&db_system=&limit=`
`sort` = `time` (default) | `calls` | `slowest` (avg) | `errors`.
`{"queries": [{"db_system": "postgresql", "db_name": "orders", "db_operation": "SELECT", "statement": "SELECT … WHERE id = ?", "calls", "throughput", "errors", "error_rate", "avg_ms", "p95_ms", "max_ms", "time_consumed_ms", "time_share"}]}`

### `GET /api/v1/apm/services/{service_name}/hosts`
`{"hosts": [...]}` (shape as in the service object).

### `GET /api/v1/apm/services/{service_name}/containers?from=`
Containers whose span resources carried `container.id` for the service since `from` (apm.md §1), newest first. `known`:
the container has infra agent data (Containers); `state`, `reporting` and the latest `cpu_utilization`, `memory_usage`,
`memory_limit` are set only then.
`{"containers": [{"container_id", "name", "host_id", "host_name", "first_seen", "last_seen", "known", "state", "reporting", "cpu_utilization", "memory_usage", "memory_limit"}]}`

### `GET /api/v1/apm/services/{service_name}/settings` · `PUT …/settings` `{"apdex_t_ms": 300}`
`{"service_name", "service_namespace", "environment", "apdex_t_ms", "is_default", "updated_at", "updated_by_email"}`.
The key is (`service_name`, `namespace`, `environment` query parameters, missing = `''`); a row with empty namespace and
environment applies to all of them unless a more specific row exists. `PUT` needs a signed-in admin or owner (`403`
otherwise; CSRF as usual), `apdex_t_ms` an integer 1..600000 (`400`), writes the audit event
`apm.service_settings.update`; not available with `OPENLOG_AUTH_MODE=static` (`404`, `GET` returns the default).

### `GET /api/v1/apm/hosts/{host_id}/services?from=`
Services whose resources carried `host.id` since `from` (default 24h ago):
`{"services": [{"service_name", "service_namespace", "environment", "first_seen", "last_seen"}]}`. Unknown hosts → empty list.

### `GET /api/v1/apm/map?service=&namespace=&environment=`
```json
{"nodes": [{"id": "service:orders|shop|prod", "type": "service", "name": "orders", "service_namespace": "shop",
            "environment": "prod", "requests", "throughput", "error_rate", "avg_ms", "p95_ms", "apdex"},
           {"id": "db:postgresql/orders", "type": "db", "name": "postgresql/orders", "service_namespace": "", "environment": "", …}],
 "edges": [{"id": "service:frontend|shop|prod->service:orders|shop|prod", "source": "…", "target": "…", "target_type": "service",
            "calls", "throughput", "errors", "error_rate", "avg_ms", "p95_ms"}]}
```
Node `type` ∈ `service`, `db`, `external`, `messaging`; dependency node RED is the sum of its incoming edges. `service`
restricts the map to edges touching that service.

### `GET /api/v1/apm/traces?service=&namespace=&environment=&transaction=&type=&min_duration_ms=&max_duration_ms=&error=&attr.<key>=&sort=&limit=`
Entry spans in the range (`sort` = `timestamp` (default, newest first) | `duration`; `limit` default 50, max 500). Without
`service`, one row per trace. `error` = `true`|`false`. `attr.<key>=<value>`: exact match on a span attribute (at most 10;
key ≤ 256 bytes, one value ≤ 1024 bytes; `400` otherwise). Open results with `GET /traces/{trace_id}` and correlated
logs with `GET /logs?trace_id=`.
`{"traces": [{"trace_id", "span_id", "timestamp", "duration_ms", "is_error", "http_status_code", "service_name", "service_namespace", "environment", "transaction_type", "transaction_name"}]}`

## Fleet (agent updates)

Agent update policy, rollouts and agents of the caller's organization ([releases-updates.md](releases-updates.md)
§3–§4, tables: [postgres.md](postgres.md#fleet-agent-updates-0002_fleet)). Reads need any role (API keys too);
changes need a signed-in admin or owner (`403` otherwise, CSRF as usual). Not available with
`OPENLOG_AUTH_MODE=static` (`404`). Changes reach ingest within `OPENLOG_FLEET_POLICY_CACHE_TTL` and the rollout
controller within `OPENLOG_FLEET_CONTROLLER_INTERVAL`. Every change is written to the audit log (`fleet.*`).

### `GET /api/v1/fleet/policy` · `PUT /api/v1/fleet/policy`
```json
{"mode": "auto", "channel": "stable", "target": "latest", "pinned_version": null, "waves": [10, 50, 100],
 "wave_soak_minutes": 60, "halt_failure_rate": 0.05,
 "maintenance_windows": [{"days": ["sat", "sun"], "start": "02:00", "end": "05:00"}],
 "is_default": false, "updated_at": "…", "updated_by_email": "…"}
```
`PUT` takes the policy fields and returns the stored policy. `400 invalid_argument`: unknown mode/channel/target,
`waves` not strictly increasing in 1..100 or not ending at 100 (1–20 waves), `wave_soak_minutes` outside 0..43200,
`halt_failure_rate` outside 0..1, bad windows (`days` ∈ `mon`…`sun`, empty = every day; `HH:MM`, `end` may be
`24:00`, `end <= start` spans midnight; at most 50), `target=pinned` without `pinned_version`, or a pinned version
that is not a verified release (checked when the release catalog is loaded). `pinned_version` is ignored (null)
for other targets; a leading `v` is removed.

### `GET /api/v1/fleet/summary`
Counts over agents that synced within `OPENLOG_FLEET_HOST_STALE_AFTER` (`total_hosts` counts all):
```json
{"total_hosts": 52, "active_hosts": 50, "update_capable": 46,
 "not_update_capable": [{"reason": "container", "hosts": 4}], "outdated": 30, "unsupported": 2,
 "in_progress": 3, "failed": 1, "held": 1, "pinned": 0,
 "versions": [{"version": "0.4.0", "hosts": 20, "latest": true, "outdated": false, "supported": true}],
 "latest": {"stable": {"version": "0.4.0", "channel": "stable", "released_at": "…", "notes_url": "…"}, "beta": null},
 "target": {"version": "0.4.0", …} | null, "oldest_supported_version": "0.2.0", "policy_mode": "auto",
 "update_available": true, "stale_after_seconds": 86400,
 "catalog": {"status": "ok|pending|error|disabled", "source": "…", "checked_at": "…", "last_success_at": "…",
             "error": "", "releases": 7, "warnings": []},
 "current_rollout": Rollout | null}
```
- `not_update_capable.reason` is the agent's `install_method` (`container`, `dev`, …; `unknown` when empty).
- `outdated`: running version below the policy target (`target=patch`: below the newest patch of its minor).
  `unsupported`: below `compatibility.oldest_supported_agent` of this backend's release (or of the newest stable
  release when this version is not in the catalog); unparsable versions are unsupported.
- `target` is null for `target=patch` or when the target is not in the catalog. `update_available` = `outdated > 0`
  (shown by the UI in `notify` mode).

### `GET /api/v1/fleet/hosts?version=&state=&q=&limit=&cursor=`
Ordered by host name then id; `limit` default 100, max 1000; `next_cursor` (opaque) or null. `version` matches
the reported version exactly, `state` the reported update state, `q` a substring of host name or id.
```json
{"hosts": [{"host_id": "…", "host_name": "web-1",
  "agent": {"name": "openlog-infra-agent", "version": "0.3.0", "commit": "…", "os": "linux", "arch": "amd64",
            "install_method": "tarball", "update_capable": true},
  "update": {"state": "failed", "from_version": "0.3.0", "to_version": "0.4.0", "error": "self-test failed", "changed_at": "…"},
  "first_seen_at": "…", "last_sync_at": "…", "rollout_id": "…" | null,
  "override": {"action": "pin", "version": "0.3.1", "updated_at": "…"} | null,
  "outdated": true, "supported": true, "status": "already_failed", "status_target": "0.4.0" | null}],
 "next_cursor": null}
```
`status` is the update decision for the host right now: `offer` (will get `status_target` on its next sync),
`not_in_wave`, `outside_window`, `hold`, `not_capable`, `up_to_date`, `already_failed`, `incompatible` (no upgrade
path from its version / below the rollback floor), `no_artifact` (platform), `rollout_paused`, `rollout_halted`,
`rollout_outdated`, `not_in_rollout`, `no_rollout`, `notify_only`, `mode_off`, `no_catalog`, `target_unavailable`,
`invalid_version`.

### `PUT /api/v1/fleet/hosts/{host_id}/override` `{"action": "hold"|"pin", "version"}` · `DELETE …/override`
`PUT` returns `{"action", "version", "updated_at"}`; `404` for a host that never synced; `400` for another action or
a pin version that is not a verified release. `DELETE` is idempotent (`204`). A pinned host moves to its version
(upgrade or, down to the rollback floor, rollback) regardless of waves, within maintenance windows, and is not part
of fleet rollouts.

### `GET /api/v1/fleet/rollouts?limit=`
Newest first (default 20, max 100): `{"rollouts": [Rollout]}`.
```json
{"id": "…", "action": "upgrade", "from_version": "0.3.0", "to_version": "0.4.0", "targets": {},
 "waves": [10, 50, 100], "current_wave": 1, "wave_percent": 50, "wave_started_at": "…", "next_wave_at": "…" | null,
 "wave_soak_minutes": 60, "halt_failure_rate": 0.05, "state": "halted",
 "state_reason": "failure rate 4/30 reached the halt threshold 5%",
 "counters": {"pending": 20, "attempted": 30, "succeeded": 26, "failed": 3, "rolled_back": 1},
 "created_by_email": null, "created_at": "…", "updated_at": "…", "ended_at": null}
```
`to_version` is null for `target=patch` rollouts (per-minor `targets`). `created_by_email` null = created by the
rollout controller. `next_wave_at` is set for an active rollout that has further waves (the wave advances then if
the failure rate allows).

### `POST /api/v1/fleet/rollouts/{id}/pause` · `POST /api/v1/fleet/rollouts/{id}/resume`
Return the rollout. Pause: only `active` (`409 failed_precondition` otherwise). Resume: `paused` or `halted`; the
soak time of the current wave restarts, and for a halted rollout the failures so far are acknowledged (no longer
counted towards the halt threshold). `404` for unknown ids.

### `POST /api/v1/fleet/rollouts/{id}/deploy-now`
"Deploy now": returns the rollout moved to its last wave (100 %), `wave_started_at` = now, `next_wave_at` null.
Only an `active` rollout that is not in its last wave (`409 failed_precondition` otherwise). The halt threshold and
the policy's maintenance windows still apply; agents get the update at their next sync, and agents waiting for a
wave are told to sync every `OPENLOG_FLEET_ROLLOUT_SYNC_INTERVAL` (60 s). Audit `fleet.rollout.deploy_now`
(`details.from_wave`, `wave`, `percent`).

### `POST /api/v1/fleet/rollback` `{"to_version"}`
Creates a rollback rollout (`201`, Rollout) from the current upgrade rollout's target (or the newest running version)
down to `to_version`, with the policy's waves; it supersedes the open rollout. `400`: not a verified release or not
lower than that version. `409`: catalog unavailable, no agent runs a newer version, or `to_version` is below the
`rollback_floor` of the version rolled back from. The controller does not start a new upgrade to the version rolled
back from; a newer release does.

## Integration settings

Endpoints and credentials of infra agent integrations (`nginx`, `redis`, `mysql`, `postgresql`, `docker`) for all hosts
of the caller's organization or for one host, delivered to agents through sync ([releases-updates.md](releases-updates.md)
§3, table: [postgres.md](postgres.md#integration-settings-0008_integration_settings)). Same permissions as Fleet: reads
need any role (API keys too); changes need a signed-in admin or owner (`403` otherwise, CSRF as usual). Not available with
`OPENLOG_AUTH_MODE=static` (`404`). Changes reach agents within `OPENLOG_FLEET_POLICY_CACHE_TTL` plus one sync interval.
Every change is audited (`integration_setting.*`). `host_id` is the agent's host id (the `host_id` of `/hosts` and
`/fleet/hosts`, the `host.id` resource attribute).

IntegrationSetting:
```json
{"id": "…", "host_id": "…" | null, "integration": "redis",
 "match": {"port": 6380 | null, "container": "", "endpoint": "", "instance": ""},
 "enabled": true, "endpoint": "127.0.0.1:6380", "username": "default", "password_set": true,
 "database": "", "databases": [], "created_at": "…", "updated_at": "…", "updated_by_email": "admin@example.com"}
```
The password is write-only: it is never returned (`password_set` tells whether one is stored) or logged.

### `GET /api/v1/integrations/settings?host_id=`
Without `host_id`: every setting of the organization (all-hosts settings first, then by host id). With `host_id`: the
settings that apply to that host, in the order the agent applies them (all-hosts settings, then the host's; without a
match first; then by creation time), and the host's revisions:
```json
{"items": [IntegrationSetting],
 "host": {"host_id": "h1", "revision": "sha256:…", "applied_revision": "sha256:…", "applied_at": "…",
          "remote_config_disabled": false} | null}
```
- `revision`: the revision sync sends this host now. `applied_revision`: the revision the agent reported in its last
  sync (`""` = none yet, or the host never synced); `applied_at`: that sync's time (null when `applied_revision` is
  `""`). The agent runs the current settings when `applied_revision == revision`. A saved change is pending until the
  agent's next sync.
- `remote_config_disabled`: the agent reported `"disabled"` (remote config turned off in its config file).
- `400` for a `host_id` over 256 bytes.

### `POST /api/v1/integrations/settings` · `PUT /api/v1/integrations/settings/{id}` · `DELETE …/{id}`
```json
{"host_id": "h1" | null, "integration": "postgresql",
 "match": {"port": 5432, "container": "", "endpoint": "", "instance": ""},
 "enabled": true, "endpoint": "127.0.0.1:5432", "username": "monitor", "password": "…" | null,
 "database": "postgres", "databases": ["app"]}
```
`POST` → `201` IntegrationSetting; `PUT` → `200` (replaces every non-secret field: omitted fields become empty,
`enabled` defaults to true); `DELETE` → `204`. Only `integration` is required; `host_id` null or `""` = all hosts.
`password`: omitted or null keeps the stored password on `PUT` (none on `POST`), `""` clears it, a value replaces it;
changing to an integration without passwords clears it. `404` for an unknown id.

- `400 invalid_argument`: unknown integration; a non-empty field the integration does not use (nginx: `endpoint`;
  redis, mysql: `endpoint`, `username`, `password`; postgresql: also `database`, `databases`; docker: only `enabled` and
  `match`); nginx `endpoint` not an `http(s)` URL with a host; other endpoints not `host:port` or
  `unix:/absolute/path`; `match.port` outside 1–65535; a string over 512 bytes or with control characters;
  more than 64 `databases` or an empty entry; `host_id` over 256 bytes.
- `409 already_exists`: another setting has the same host scope, integration and match.
- `409 failed_precondition`: a password was given but `OPENLOG_SECRETS_KEY` is not configured.

## Alerting

Rules, incidents, notification channels, mute windows and the delivery log of the caller's organization. Semantics
(rule types, evaluation, state machine, notifications, payloads, secrets): [alerting.md](alerting.md); shapes:
[openapi.yaml](openapi.yaml) tag `alerts`; tables: [postgres.md](postgres.md#alerting-0004_alerting). Reads need any role
(API keys too); writes need a signed-in user (CSRF as usual) with the role in [Roles](#roles): members create rules and
mutes and change only those they created (`403 permission_denied` otherwise), work on incidents; admins and owners change
everything and manage channels. Not available with `OPENLOG_AUTH_MODE=static` (`404`). Every write is audited
(`alert.*`, alerting.md §7).

| Endpoint | Notes |
|---|---|
| `GET /api/v1/alerts/rule-types` | `{"types": [{"type", "available", "reason"}]}` |
| `GET /api/v1/alerts/rules?type=&enabled=` · `POST /api/v1/alerts/rules` | list ordered by name; create → `201` Rule. `409` at `OPENLOG_ALERT_MAX_RULES_PER_ORG` |
| `GET /api/v1/alerts/rules/{id}` | Rule + `series` (non-ok series states) |
| `PUT /api/v1/alerts/rules/{id}` · `DELETE …` | full replacement (optional `version` → `409` when stale); changing `type`/`condition` resolves open incidents (`rule_changed`); delete resolves them (`rule_deleted`), incidents are kept |
| `POST /api/v1/alerts/rules/{id}/enable` · `…/disable` | disable resolves open incidents (`rule_disabled`) |
| `POST /api/v1/alerts/rules/preview` `{"rule": RuleInput, "hours": 1..24}` | evaluates the unsaved definition over the last hours through the tenant-scoped query layer (viewer): `{"from", "to", "step_seconds", "operator", "threshold", "recovery_threshold", "unit", "series": [{"key", "labels", "points": [[ms, value\|null]], "transitions": [{"at", "state", "value"}], "incidents": [{"opened_at", "resolved_at", "peak"}]}], "truncated", "approximate"}` |
| `GET /api/v1/alerts/rules/{id}/evaluations?from=&to=` | evaluation history from ClickHouse (alerting.md §3.6; default last 24 h, ≤ 30 days, ≤ 500 buckets): `{"from", "to", "step_seconds", "evaluations": [{"at", "firing_series", "evaluations", "errors", "duration_ms", "max_duration_ms"}], "series": [{"series_key", "labels", "points": [[ms, value\|null, state]]}], "truncated"}` |
| `GET /api/v1/alerts/incidents?state=open,acknowledged&rule_id=&severity=&limit=&cursor=` | newest first (default 50, max 500), `next_cursor`, `counts {open, acknowledged, resolved (7 d)}` |
| `GET /api/v1/alerts/incidents/{id}` | Incident + `events` (timeline) + `deliveries` |
| `POST /api/v1/alerts/incidents/{id}/acknowledge` · `…/resolve` `{"note"?}` · `…/notes` `{"text"}` | ack is idempotent (`409` when resolved; stops re-notifications); resolve enqueues resolve notifications; note → `201` event |
| `GET /api/v1/alerts/channels` · `POST` · `GET/PUT/DELETE /api/v1/alerts/channels/{id}` | secrets are write-only (`secret_hints` masked; omitted secret fields keep stored values; `generated_secrets` once on create); list carries `secrets_configured`. `409` without `OPENLOG_SECRETS_KEY` |
| `POST /api/v1/alerts/channels/{id}/test` | synchronous test send: `{"success", "status_code", "error", "duration_ms", "notification_id"}` (always `200` when the channel exists) |
| `GET /api/v1/alerts/mutes?include_expired=` · `POST` · `PUT/DELETE /api/v1/alerts/mutes/{id}` | `starts_at`/`ends_at` (≤ 90 days), `rule_ids`, label `matchers`; `active` computed. Recurring: `schedule {timezone, days\|rrule, start_time, end_time, from, until}` (alerting.md §5.2); responses then carry the current or next occurrence in `starts_at`/`ends_at` |
| `GET /api/v1/alerts/deliveries?channel_id=&incident_id=&status=&limit=` | delivery log newest first (default 100, max 500) with `attempt_log` |

## Edge cases (machine-readable spec: [openapi.yaml](openapi.yaml))

- Inventory without a complete snapshot: `snapshot_id: ""`, `snapshot_time: null`, empty `items`.
- Unknown host (no host record in the caller's organization, including another organization's host): `404 not_found` on `GET /hosts/{host_id}`, `/inventory`, `/services` and `/metrics`. An unknown **metric** on a known host is `200` with empty `series` and `metric.type`/`unit` = `""`.
- `/metrics/names?host_id=` and `/logs?host_id=` are filters, not host lookups: an unknown host gives empty lists (`200`).
- `attr.*` log filters outside the allowlist → `400` (see Logs).
- Inventory search omits `host_name` when the host is not in the hosts table.
- Malformed `trace_id` (not 32 hex chars) → `400 invalid_argument`.
- Unknown `/api/*` routes → `404` before authentication.
- `severity_min` accepts OTel severity names (`TRACE`…`FATAL`, plus `WARNING` as alias of `WARN`) or a number.
- `/metrics/names` has no `limit`; capped at `OPENLOG_API_MAX_ROWS`.
- For ranges longer than 6h, `step` is rounded up to whole minutes (rollup resolution).
- Non-`/api` paths serve the embedded UI when `OPENLOG_API_UI_ENABLED=true`.
- Every management response carries `Cache-Control: no-store`.
