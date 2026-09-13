# Contract: PostgreSQL schema (M1)

PostgreSQL 16+ holds tenancy and access control: organizations (= tenants), users, memberships and
roles, ingest license keys, API keys, sessions, invitations, the audit log and login rate limiting.
Telemetry stays in ClickHouse; the only link is `organizations.tenant_id`, the first `ORDER BY` column
of every ClickHouse table.

Services use it only in `OPENLOG_AUTH_MODE=postgres` (the default; see [config.md](config.md)).
`OPENLOG_AUTH_MODE=static` (development and tests) never connects to PostgreSQL.

## Migrations

- Files: `migrations/postgres/NNNN_name.sql`, embedded in the binaries (`migrations.Postgres`).
- Applied by `openlog-migrate` (before the ClickHouse schema), `openlog-allinone` on start
  (`OPENLOG_MIGRATE_ON_START`) and every `openlog-admin` command.
- Each file runs in one transaction and is recorded in `schema_migrations (version, name, applied_at)`.
  Concurrent migrators are serialized with `pg_advisory_lock(0x6f70656e6c6f67)`.
- Changes are forward-only and backward compatible with the previous release (add tables/columns,
  never rename or drop in the same release), so pods of two versions can run during a rollout.
- The first line of every file is `-- openlog:phase expand` or `-- openlog:phase contract`
  (docs/contracts/releases-updates.md §6). Contract files also declare
  `-- openlog:requires-all-at-least <version>` in the leading comment block and run only when every live
  instance in `component_heartbeats` (last 5 minutes) and the migrator are at least that version; skipped
  contract migrations stay unrecorded and are retried by the next run, so they must not be prerequisites of
  later expand migrations. `openlog-migrate -plan` shows the decision. The same rules apply to
  `schema/clickhouse`, gated by the same PostgreSQL table.

## Secrets

Nothing that grants access is stored in plaintext:

| Secret | Format | Stored as |
|---|---|---|
| Ingest license key | `olk_` + 48 hex chars (192 random bits), or an imported value (16–256 printable ASCII chars, see [api.md](api.md#post-apiv1license-keys-name-key)) | `license_keys.key_hash = sha256(key)`, `key_prefix` = first 12 chars (at most half of an imported value) |
| API key | `ola_` + 48 hex chars | `api_keys.key_hash = sha256(key)`, `key_prefix` = first 12 chars |
| Invitation token | `oli_` + 48 hex chars | `invitations.token_hash = sha256(token)` |
| Session token (cookie) | 32 random bytes, base64url | `sessions.token_hash = sha256(token)` |
| Password | user-chosen, 8–256 chars | `users.password_hash` = argon2id PHC string (`m=19456,t=2,p=1`, 16-byte salt, 32-byte key) |

Keys are high-entropy random values, so an unsalted SHA-256 is sufficient and allows an indexed lookup.
Keys are shown once, in the response that creates them. Operator-chosen keys — imported under
Settings → License keys, or given to `openlog-admin bootstrap` (`OPENLOG_BOOTSTRAP_LICENSE_KEY`) — are
hashed the same way; their displayed prefix reveals at most half of the key. An imported value should
itself be high-entropy (e.g. an existing 32-character random key). Bootstrap keys follow the same character
rules but may be as short as 8 characters, so development defaults such as `dev-license-key` keep working.

## Tables

### `organizations`
| Column | Type | Notes |
|---|---|---|
| `id` | uuid PK | used by the API (`X-Openlog-Org-Id`) |
| `tenant_id` | text UNIQUE | `^[a-z0-9][a-z0-9_-]{0,62}$`; ClickHouse tenant key; immutable. Generated as `t` + 20 hex chars on sign-up, chosen by the operator for bootstrap/`create-owner` |
| `name` | text | 1–200 chars |
| `created_at`, `updated_at` | timestamptz | |

### `users`
| Column | Type | Notes |
|---|---|---|
| `id` | uuid PK | |
| `email` | text UNIQUE | stored lower-case (CHECK) |
| `name` | text | display name, ≤ 200 chars |
| `password_hash` | text NULL | NULL = no password sign-in (reserved for SSO, M4) |
| `created_at`, `updated_at`, `last_login_at` | timestamptz | |
| `disabled_at` | timestamptz NULL | disabled users cannot sign in; their sessions stop working |

### `memberships`
`(org_id, user_id)` PK, `role` ∈ `owner`, `admin`, `member`, `viewer`, `created_at`. A user's default
organization is their oldest membership. Every organization keeps at least one owner: demoting or
removing the last owner fails (the owner rows are locked `FOR UPDATE`, so concurrent demotions cannot
both succeed).

### `license_keys` (ingest)
`id`, `org_id`, `name`, `key_prefix`, `key_hash` (UNIQUE, 32 bytes), `custom` (boolean, `0007_license_key_custom`;
true = operator-chosen value, imported or bootstrap; rows from before the migration are backfilled from
`key_prefix NOT LIKE 'olk\_%'`), `created_by` (user, NULL after the user is deleted), `created_at`,
`last_used_at`, `revoked_at`, `revoked_by`. The unique `key_hash` makes a value usable by one organization
only, and a revoked value can never be imported again.
Lookup: `WHERE key_hash = $1 AND revoked_at IS NULL`. Revocation is a soft delete (the row stays for
the audit trail and to keep the hash unusable). `last_used_at` is written asynchronously by ingest, at
most once per minute per key per pod.

### `api_keys`
Like `license_keys` plus `scope` (`read`, the only scope in M1) and `expires_at` (NULL = never).
API keys authenticate the Query API only and act as `viewer`; they never authenticate ingest, and
ingest license keys never authenticate the API.

### `sessions`
`id`, `user_id`, `token_hash` (UNIQUE), `csrf_token`, `created_at`, `last_seen_at`, `expires_at`,
`revoked_at`, `ip`, `user_agent`. A session is valid while not revoked, `now < expires_at`
(`OPENLOG_SESSION_TTL`) and `now - last_seen_at < OPENLOG_SESSION_IDLE_TIMEOUT`. `last_seen_at` is
updated at most once per minute. The API deletes sessions that expired or were revoked more than a day
ago (hourly, from every API pod; idempotent).

### `invitations`
`id`, `org_id`, `email`, `role`, `token_hash` (UNIQUE), `invited_by`, `created_at`, `expires_at`
(`OPENLOG_INVITATION_TTL`), `accepted_at`, `accepted_by`, `revoked_at`. At most one pending invitation
per `(org_id, email)` (partial unique index); an expired pending invitation is revoked when a new one is
created. Accepting marks the invitation and inserts the membership in one transaction.

### `audit_log`
`id` (identity), `org_id` (NULL for user-level events such as sign-in), `actor_user_id`, `actor_email`,
`action`, `target_type`, `target_id`, `details` (jsonb), `ip`, `created_at`.

| Action | Target |
|---|---|
| `org.create`, `org.rename`, `bootstrap` | organization |
| `member.role_change` (`details.from`/`to`), `member.remove` | user |
| `invitation.create`, `invitation.revoke`, `invitation.accept` | invitation |
| `license_key.create`, `license_key.revoke` | license_key (`details.name`, `details.prefix`; `details.custom = true` for an imported value) |
| `api_key.create`, `api_key.revoke` | api_key |
| `user.login`, `user.logout`, `user.password_change`, `user.password_reset`, `session.revoke` | session / user |
| `fleet.policy.update` (`details.from`/`to`) | policy (organization id) |
| `fleet.host_override.set` (`details.action`/`version`), `fleet.host_override.delete` | agent_host |
| `fleet.rollout.pause`, `fleet.rollout.resume`, `fleet.rollout.deploy_now` (`details.from_wave`/`wave`), `fleet.rollback` (`details.from_version`/`to_version`) | rollout |
| `update.check_requested`, `update.apply_requested` (`details.from`/`to`/`ignore_maintenance_window`/`engine`) | update_request |
| `fleet.rollout.create`, `fleet.rollout.advance`, `fleet.rollout.halt`, `fleet.rollout.complete`, `fleet.rollout.supersede` (actor email `openlog-controller`, no user) | rollout |
| `integration_setting.create`, `integration_setting.update`, `integration_setting.delete` (`details.integration`/`host_id`/`changed`/`password_changed`) | integration_setting |

Audit writes are best effort: a failed write is logged (`cannot write audit log`) and does not fail the
operation.

### `login_failures`
`key_hash = sha256(lower(email) + "|" + client IP)`, `attempted_at`. Sign-in (and password checks
during invitation acceptance and password change) is refused with `429 resource_exhausted` after
`OPENLOG_LOGIN_MAX_FAILURES` failures within `OPENLOG_LOGIN_WINDOW`; a successful sign-in clears the
counter. Shared by all API pods. Rows older than a day are deleted hourly.

## Fleet (agent updates, `0002_fleet`)

Agent update policy, host exceptions, rollouts and the agents that sync with ingest
([releases-updates.md](releases-updates.md) §3–§4, API: [api.md](api.md#fleet-agent-updates), code: `internal/fleet`).

### `agent_update_policies`
`org_id` (PK, FK organizations), `mode` (`off`·`notify`·`auto`), `channel` (`stable`·`beta`), `target`
(`latest`·`patch`·`pinned`), `pinned_version` (text, required when `target = pinned`), `waves` (int[], strictly
increasing percentages ending at 100), `wave_soak_minutes` (0–43200), `halt_failure_rate` (0–1),
`maintenance_windows` (jsonb `[{"days": ["mon",…], "start": "HH:MM", "end": "HH:MM"}]`, UTC; `[]` = always;
`end <= start` spans midnight), `updated_at`, `updated_by`. **No row = the default policy** (`auto`, `stable`,
`latest`, `[10,50,100]`, 60 min, 0.05, no windows). Validation is done by the API.

### `agent_update_host_overrides`
`(org_id, host_id)` PK, `action` (`hold` = never update; `pin` = move this host to `version`, up or down, outside
of fleet rollouts), `version`, `updated_at`, `updated_by`. Rows are kept when the host disappears.

### `agent_rollouts`
`id`, `org_id`, `action` (`upgrade`·`rollback`), `from_version` (informational: most common running version, or
the version rolled back from), `to_version` (NULL for `target = patch`, whose per-minor targets are in
`targets` jsonb `{"0.3": "0.3.4"}`), `waves`/`wave_soak_minutes`/`halt_failure_rate` (copied from the policy at
creation), `current_wave` (index into `waves`), `wave_started_at`, `state`, `state_reason`, `hosts_pending`,
`hosts_attempted`, `hosts_succeeded`, `hosts_failed`, `hosts_rolled_back` (refreshed by the controller),
`ack_attempted`/`ack_failed` (counts acknowledged by an admin resume of a halted rollout; excluded from the halt
rate), `created_by` (NULL = controller), `created_at`, `updated_at`, `ended_at`.

- States: `active` → `paused` (admin) / `halted` (failure rate) → `active` (admin resume); `completed` (no host
  pending); `superseded` (replaced by a newer rollout or no longer targeted by the policy). A partial unique index
  allows **one open (`active`, `paused`, `halted`) rollout per organization**; creating a rollout supersedes the
  open one in the same transaction.
- The **current rollout** of an organization is its newest rollout that is not `superseded`. Sync hands out its
  update to hosts in the current wave (`fnv32a(host_id + ":" + id) % 100 < waves[current_wave]`); a `completed`
  rollout serves every host (agents installed later catch up).
- Host classification (controller, hosts synced within `OPENLOG_FLEET_HOST_STALE_AFTER`): *succeeded* = offered by
  this rollout and running its target; *failed*/*rolled back* = offered by this rollout and reporting that state
  since the rollout started (not offered again by this rollout); *in progress* counts as attempted and pending;
  *pending* = would receive the update ignoring waves and maintenance windows (held and pinned hosts excluded).

### `agent_hosts`
`(org_id, host_id)` PK, `host_name`, `agent_name`, `agent_version`, `agent_commit`, `agent_os`, `agent_arch`,
`install_method`, `update_capable`, `update_state`, `update_from`, `update_to`, `update_error`,
`update_changed_at`, `config_hash`, `first_seen_at`, `last_sync_at`, `rollout_id` (last rollout that offered this
host an update; soft reference, never cleared by later syncs), `integrations_config_revision` (`0008_integration_settings`;
the remote integration config revision the agent reported in its last sync: `''` = none, `disabled` = remote config
turned off on the host).

Written **only by ingest, asynchronously**: sync requests are answered from memory; reports are queued per pod
(coalesced per host, bounded by `OPENLOG_FLEET_REPORT_QUEUE_SIZE`, the oldest report is dropped when full:
`openlog_fleet_host_reports_dropped_total`) and upserted every second in statements of at most 200 rows
(`INSERT … SELECT FROM unnest(…) JOIN organizations ON tenant_id … ON CONFLICT DO UPDATE`). A failed batch is
dropped; agents report again on their next sync.

### Access pattern and leader
- Ingest reads, per tenant and pod at most once per `OPENLOG_FLEET_POLICY_CACHE_TTL` (≤ 30 s, singleflight):
  the organization by `tenant_id`, its policy, overrides, current rollout and integration settings. While PostgreSQL is unreachable,
  cached state is served for 5 minutes; uncached organizations get no updates (sync still answers `200`).
- The rollout controller runs on the api leader (session advisory lock `postgres.LeaderLock`, shared with the
  update check) every `OPENLOG_FLEET_CONTROLLER_INTERVAL`: per organization with agents it reads the policy,
  overrides, current rollout and the recently synced `agent_hosts` rows, then updates counters and state with
  conditional writes (`WHERE state = <expected>`), so concurrent admin actions win.

## Integration settings (`0008_integration_settings`)

### `integration_settings`
Infra agent integration settings edited in the UI and delivered through agent sync ([releases-updates.md](releases-updates.md)
§3, API: [api.md](api.md#integration-settings), code: `internal/intsettings`).

| Column | Type | Notes |
|---|---|---|
| `id` | uuid PK | generated by the API (it is part of the password AAD) |
| `org_id` | uuid FK organizations | cascade |
| `host_id` | text NULL | agent `host_id` (1–256 bytes); NULL = every host of the organization |
| `integration` | text | `nginx`·`redis`·`mysql`·`postgresql`·`docker` |
| `match_port` | int NULL | 1–65535 |
| `match_container`, `match_endpoint`, `match_instance` | text, default `''` | discovered service selectors (≤ 512 bytes, validated by the API); all empty = every instance |
| `enabled` | boolean | default true |
| `endpoint`, `username`, `database` | text, default `''` | fields an integration does not use stay empty |
| `password_enc` | text, default `''` | `ol1:<key id>:<AES-256-GCM>` of the password with `OPENLOG_SECRETS_KEY`, AAD `org_id/id`; `''` = no password |
| `databases` | text[], default `{}` | ≤ 64 entries |
| `created_at`, `updated_at` | timestamptz | |
| `updated_by` | uuid NULL | user (SET NULL on delete) |

Unique index `integration_settings_scope_uniq (org_id, coalesce(host_id, ''), integration, coalesce(match_port, 0),
match_container, match_endpoint, match_instance)`. Audit: `integration_setting.create`, `integration_setting.update`,
`integration_setting.delete` (target `integration_setting` = id; `details.integration`, `host_id`, `changed` (field
names), `password_changed`; update also `previous_host_id`/`previous_integration` when they change). Passwords never
appear in responses, logs or audit details. Ingest decrypts them only to answer sync. `openlog-alert rotate-secrets` does not
re-encrypt these rows yet, so keep a previous key in `OPENLOG_SECRETS_KEY_PREVIOUS` until each password has been saved
again.

## APM (`0005_apm`)

### `apm_service_settings`
Per-service APM settings ([apm.md](apm.md) §4). Primary key (`org_id`, `service_name`, `service_namespace`,
`deployment_environment`); `apdex_t_ms` 1..600000; `updated_at`, `updated_by` (user, nullable). A row with empty
namespace and environment applies to every namespace/environment of the service unless a more specific row exists;
services without a row use `OPENLOG_APM_DEFAULT_APDEX_T`. Written by `PUT /api/v1/apm/services/{service_name}/settings`
together with the audit event `apm.service_settings.update` (target `apm_service` = service name,
`details.service_namespace`/`environment`/`apdex_t_ms`/`previous_apdex_t_ms`) in one transaction.

## Alerting (`0004_alerting`)

Rules, evaluation state, incidents, channels, mutes and the notification outbox ([alerting.md](alerting.md), API:
[api.md](api.md#alerting), code: `internal/alert/pgstore_*.go`).

| Table | Key | Content |
|---|---|---|
| `alert_rules` | `id` | `org_id`, `name`, `description`, `type` (`apm_no_data` allowed since `0006_alerting_m2`), `severity`, `enabled`, `interval_seconds`, `for_seconds`, `recovery_for_seconds`, `condition` (jsonb, validated canonical form), `renotify_interval_seconds`, `flapping` (jsonb), `runbook_url`, `labels` (jsonb), `version` (+1 on every change), `created_by`, `updated_by`, timestamps |
| `alert_rule_channels` | (`rule_id`, `channel_id`) | rule → channel (both cascade) |
| `alert_channels` | `id` | `org_id`, `name`, `type` (`slack`·`email`·`webhook`·`teams`), `enabled`, `config` (jsonb, non-secret), `secrets` (`ol1:<key id>:<AES-256-GCM>` of the secret JSON, AAD `org_id/channel_id`), `secrets_key_id`, `secret_hints` (masked), `created_by`, timestamps |
| `alert_evaluators` | `instance_id` | evaluator heartbeats (`last_seen`, database clock); rows older than 1 h are pruned |
| `alert_rule_leases` | `rule_id` | `org_id`, `owner` (`''` = free), `lease_until` (`-infinity` = released), `claimed_at`, `next_eval_at`, `last_eval_end`, `last_evaluated_at`, `last_result`, `last_error`, `last_duration_ms`. One row per rule (created with it; missing rows are added by evaluators) |
| `alert_series_state` | (`rule_id`, `series_key`) | non-ok series (and ok series with flapping history): `labels`, `state`, `pending_since`, `firing_since`, `recovering_since`, `last_value`, `last_seen_at`, `incident_id`, `transitions` (timestamptz[] ≤ 10), `last_notified_at`, `renotify_count` |
| `alert_incidents` | `id` | `org_id`, `rule_id` (SET NULL on delete), `rule_name`/`rule_type`/`severity` snapshot, `series_key`, `labels`, `summary`, `state` (`open`·`acknowledged`·`resolved`), `value`, `last_value`, `threshold`, `channel_ids` (uuid[] notified at open), `flapping`, `opened_at`, `acknowledged_at/by`, `resolved_at/by`, `resolve_reason`. Partial unique index `alert_incidents_open_uniq (rule_id, series_key) WHERE state <> 'resolved'` |
| `alert_incident_events` | `id` (identity) | timeline: `incident_id`, `at`, `kind`, `actor_user_id`, `actor_email`, `message` (≤ 4000), `details` |
| `alert_mutes` | `id` | `org_id`, `name`, `comment`, `starts_at`, `ends_at`, `rule_ids` (uuid[], empty = all), `matchers` (jsonb), `schedule` (jsonb, `0006_alerting_m2`; NULL = one-off; recurring mutes keep the current or next occurrence in `starts_at`/`ends_at`, rolled forward by dispatchers), `created_by`, timestamps |
| `alert_notifications` | `id` (+ `seq` identity for ordering) | outbox: `org_id`, `incident_id`, `rule_id`, `channel_id` (SET NULL), `channel_type`, `kind`, `idempotency_key` (UNIQUE), `payload` (jsonb event), `status` (`pending`·`sending`·`delivered`·`failed`·`suppressed`), `attempts`, `next_attempt_at`, `claimed_by`, `claimed_until`, `muted_logged`, `last_error`, `created_at`, `finished_at`. Finished rows older than 30 days are pruned |
| `alert_delivery_attempts` | `id` (identity) | delivery log: `notification_id` (cascade), `attempt`, `instance_id`, `started_at`, `duration_ms`, `success`, `status_code`, `error` (≤ 2000) |

**Concurrency.** Leases are claimed with `SELECT … FOR UPDATE SKIP LOCKED` and renewed by the owner; every evaluation
commit locks the rule's lease row `FOR UPDATE` and checks owner, expiry, `last_eval_end` and the rule `version`
(`FOR SHARE`) before writing series state, incidents, events and outbox rows in the same transaction. API changes to a
rule or incident lock the same lease row first (same lock order), so they serialize with evaluations. Dispatchers claim
outbox rows with `FOR UPDATE SKIP LOCKED` and finish them conditionally (`status = 'sending' AND claimed_by = me`);
expired claims return to `pending`. Audit: `alert.rule.*`, `alert.channel.*`, `alert.mute.*`,
`alert.incident.{acknowledge,resolve}` (details: names, `condition_changed`, `success` of test sends).

## Versions and updates (`0003_component_heartbeats`)

### `component_heartbeats`
`(component, instance_id)` primary key, `version`, `started_at`, `last_seen` (database clock). Upserted every
30 s by each ingest, processor, api and allinone process (`component` = binary name, `instance_id` = host name +
random suffix, so a restart is a new row); deleted on graceful shutdown; rows not seen for 24 h are pruned. Read
by `openlog-migrate` (live = `last_seen` within 5 minutes). Index on `last_seen`.

### `system_state`
`key` primary key, `value jsonb`, `updated_at`. Small cluster-wide documents:

| key | Written by | Content |
|---|---|---|
| `update_check` | api leader (advisory lock `postgres.LeaderLock` = `0x6f6c2d6c656164`, shared with other leader jobs) | `{channel, checked_at, last_success_at, error, latest: {version, notes_url, released_at}}` |
| `updater` | `openlog-updater` | the `UpdaterStatus` of GET /api/v1/version |
| `updater_poll` | Compose `openlog-updater`, every 30 s | `{engine, mode, poll_seconds, polled_at}` (`update_requests.updater_listening`) |

### `update_requests` (`0009_update_requests`)
"Check now" / "Update now" from the UI to `openlog-updater` (releases-updates.md §5.1, D-041). `id` uuid, `action`
(`check|apply`), `target_version`, `ignore_maintenance_window`, `state` (`pending|running|done|failed|expired`),
`message` (≤ 2000), `org_id` and `requested_by` (SET NULL on delete), `requested_by_email`, `requested_at`,
`picked_at`, `finished_at`. Indexes: `requested_at DESC`; partial `(requested_at) WHERE state = 'pending'` for the
updater's poll. Inserts are serialized by `pg_advisory_xact_lock(0x6f6c2d7570647271)` (rate limit: one request per
action per 30 s; one open `apply`). The table stays small (a few rows per admin action); rows are not pruned.

Updater events are also written to `audit_log` with `org_id NULL`, `actor_email = 'openlog-updater'`,
`action` `updater.update_{available,started,succeeded,failed,rolled_back,rollback_failed}`,
`target_type = 'openlog_release'`, `target_id` = target version.

## Sizing and operations

- Connections: every ingest and api pod opens a pool of at most `OPENLOG_POSTGRES_MAX_CONNS` (default 10).
  Ingest queries PostgreSQL only on license-key cache misses, so its steady-state load is about
  `(distinct keys × pods) / OPENLOG_AUTH_CACHE_TTL` indexed lookups.
- The API performs 2–3 indexed queries per authenticated request (session or API key, membership).
- Backups: PostgreSQL is the system of record for access control; back it up (CloudNativePG: configure
  `backup` on the `Cluster`). Losing it does not lose telemetry, but all keys and users must be recreated.
