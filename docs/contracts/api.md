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
| `PATCH /orgs/current`, invitations, create/revoke license keys, revoke any API key, change roles/remove members (not owners), `GET /audit-log`, fleet changes (`PUT /fleet/policy`, host overrides, pause/resume, rollback) | | | ✓ | ✓ |
| Grant or remove the owner role, invite owners, remove owners | | | | ✓ |

An organization always keeps at least one owner (`409 failed_precondition`).

## Auth endpoints

### `GET /api/v1/auth/config` (public)
`{"mode": "postgres", "signup_enabled": false, "password_min_length": 12}`

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
New user: creates the account with `password` (policy: 12–256 characters). Existing user (`user_exists`): `password`
must be their current password. Adds the membership, signs in (cookie) and returns **Me** with the user's default
organization (send `X-Openlog-Org-Id` to switch).

## Ingest license keys

### `GET /api/v1/license-keys`
`{"license_keys": [{"id", "name", "prefix": "olk_1a2b3c4d", "created_by_email", "created_at", "last_used_at", "revoked_at"}]}`
(revoked keys included). `last_used_at` is updated at most once a minute and may lag by up to a minute per ingest pod.

### `POST /api/v1/license-keys` `{"name"}`
`201 {"license_key": {…}, "key": "olk_…"}` — **the key is shown only in this response**.

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
Every API response (including errors and the UI) carries `X-Openlog-Version`.

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

## Metrics

### `GET /api/v1/metrics/names?host_id=&from=&to=`
`{"names": [{"name": "system.cpu.utilization", "type": "gauge", "unit": "1"}]}`

### `GET /api/v1/hosts/{host_id}/metrics?name=&from=&to=&step=&agg=&group_by=`

| Param | Default | Notes |
|---|---|---|
| `name` | required | metric name |
| `step` | auto (≈ 300 points) | Go duration, min `10s` |
| `agg` | `avg` for gauges, `rate` for monotonic sums, `last` for non-monotonic sums | `avg`, `min`, `max`, `sum`, `last`, `rate` |
| `group_by` | all attributes | comma-separated attribute keys; series are merged by these keys |

`rate` = per-second increase per series per step (counter resets clamp to 0), then summed across merged series.
Ranges longer than 6h read from the 1-minute rollup table.

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

### `GET /api/v1/logs?host_id=&service=&q=&severity_min=&trace_id=&attr.<key>=&from=&to=&limit=`
Excludes inventory events (`event.name` starting with `openlog.inventory.`). `q` is a case-insensitive substring match on body. Newest first.

`attr.<key>=<value>` is an exact-match filter on a log record attribute (AND-ed, one value per key, bound as a query
parameter). Allowed keys — the attributes the infra agent sets (semantic-conventions §4):
`openlog.log.source` (`file` / `journald`), `log.file.path`, `log.file.name`, `openlog.discovery.id`,
`openlog.systemd.unit`, `openlog.syslog.identifier`. Any other `attr.*` key, an empty or repeated value, or a value
longer than 1024 bytes → `400 invalid_argument`. Agent log records have an empty `service_name`, so host log views use
`host_id` plus these filters, e.g. `?host_id=…&attr.openlog.discovery.id=nginx&attr.log.file.path=/var/log/nginx/error.log`.
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

### `POST /api/v1/fleet/rollback` `{"to_version"}`
Creates a rollback rollout (`201`, Rollout) from the current upgrade rollout's target (or the newest running version)
down to `to_version`, with the policy's waves; it supersedes the open rollout. `400`: not a verified release or not
lower than that version. `409`: catalog unavailable, no agent runs a newer version, or `to_version` is below the
`rollback_floor` of the version rolled back from. The controller does not start a new upgrade to the version rolled
back from; a newer release does.

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
