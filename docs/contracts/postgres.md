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
| Ingest license key | `olk_` + 48 hex chars (192 random bits), or an imported value (16–256 printable ASCII chars, see [api.md](api.md#post-apiv1license-keys-name-key)) | `license_keys.key_hash = HMAC-SHA256(OPENLOG_KEY_HASH_SECRET, key)`, or `sha256(key)` without a secret; `key_prefix` = first 12 chars (at most half of an imported value) |
| API key | `ola_` + 48 hex chars | `api_keys.key_hash`, hashed like license keys; `key_prefix` = first 12 chars |
| Invitation token | `oli_` + 48 hex chars | `invitations.token_hash = sha256(token)` |
| E-mail verification token | `olv_` + 48 hex chars | `email_verifications.token_hash = sha256(token)` |
| Session token (cookie) | 32 random bytes, base64url | `sessions.token_hash = sha256(token)` |
| SCIM token | `ols_` + 48 hex chars | `scim_tokens.key_hash`, hashed like API keys (D-044); `key_prefix` = first 12 chars |
| SSO sign-in state / binding cookie | 32 random bytes each, base64url | `sso_login_states.state_hash` / `binding_hash = sha256(…)`; the PKCE verifier and nonce are stored for 10 minutes |
| Domain verification e-mail token | `oldv_` + 48 hex chars | `sso_domains.email_token_hash = sha256(token)` |
| Dashboard share link token | `olds_` + 43 base64url chars (256 random bits) | `dashboard_shares.token_hash = sha256(token)` (`0054_dashboard_shares`, D-087) |
| OIDC client secret, SAML SP private key | IdP-issued / generated RSA 2048 | `sso_connections.secret_enc` / `sp_key_enc` = AES-256-GCM with a key from `OPENLOG_SSO_SECRET_KEY` (else derived from `OPENLOG_KEY_HASH_SECRET`; `_PREVIOUS` values decrypt), associated data = purpose + connection id. Format `0x01 ‖ key id (4) ‖ nonce ‖ ciphertext`; `0x00 ‖ plaintext` without any key (warning) |
| Password | user-chosen, 8–256 chars | `users.password_hash` = argon2id PHC string (`m=19456,t=2,p=1`, 16-byte salt, 32-byte key) |

Keys are high-entropy random values, so an unsalted hash is sufficient and allows an indexed lookup.
Keys are shown once, in the response that creates them.

**Server-side key hash secret (D-044).** With `OPENLOG_KEY_HASH_SECRET` the stored hash of license and API keys
is an HMAC, so a database dump alone does not allow offline guessing of (possibly low-entropy) imported values.
A lookup sends every candidate hash in one query — HMAC with the current secret, HMAC with
`OPENLOG_KEY_HASH_SECRET_PREVIOUS`, plain SHA-256 — ordered by preference
(`key_hash = ANY($1) ORDER BY array_position($1, key_hash)`); a row found by a later candidate is rewritten to the
current hash in the same statement (data-modifying CTE, skipped if that hash already exists, active keys only).
Ingest does this only on a cache miss, so the hot path is unchanged. Creating an imported or bootstrap key also
refuses a value whose older-format hash exists (active or revoked). Revoked rows keep their old hash. Rotation:
set the new secret and the old one as `_PREVIOUS` on every service at once; drop `_PREVIOUS` once every active
key has `last_used_at` after the rotation. Session, invitation and verification tokens stay SHA-256 (random,
short-lived, never operator-chosen). Operator-chosen keys — imported under
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
| `locale` | text | `0060_language_preferences`: default e-mail language (`''` = none, `en`, `tr`; CHECK), set by admins/owners; used for organization e-mails when the recipient has no preference (D-095) |

### `users`
| Column | Type | Notes |
|---|---|---|
| `id` | uuid PK | |
| `email` | text UNIQUE | stored lower-case (CHECK) |
| `name` | text | display name, ≤ 200 chars |
| `password_hash` | text NULL | NULL = no password sign-in (reserved for SSO, M4) |
| `created_at`, `updated_at`, `last_login_at` | timestamptz | |
| `disabled_at` | timestamptz NULL | disabled users cannot sign in; their sessions stop working |
| `email_verified_at` | timestamptz NULL | `0011_email_verification`: NULL only for self-service sign-ups that have not confirmed their address (`OPENLOG_SIGNUP_REQUIRE_VERIFICATION`); they cannot create license/API keys or invite. Existing rows were backfilled with `created_at`; the column default `now()` keeps users created by older binaries verified |
| `locale` | text | `0050_email_locale`: e-mail language preferred when the user was created (sign-up, invitation acceptance; `Accept-Language` reduced to a supported language: `en`, `tr`), `''` = English. Used for usage notifications to owners. With `locale_explicit` it is the language the user chose (Settings → Profile) |
| `locale_explicit` | boolean | `0060_language_preferences`: `true` = `locale` is the user's preference (web UI and e-mails to the user); `false` = automatic (D-095) |

### `email_verifications` (`0011_email_verification`)
`id`, `user_id`, `email`, `token_hash` (UNIQUE), `created_at`, `expires_at` (`OPENLOG_EMAIL_VERIFICATION_TTL`),
`used_at`, `locale` (`0050_email_locale`: language of the e-mail that carried the link). Opening the link sets `used_at` and `users.email_verified_at` in one transaction (only while the
user still has that e-mail). Every resend creates a new row; older unused links stay valid until they expire. Rows
expired or used more than a day ago are deleted by the hourly auth cleanup.

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
Like `license_keys` plus `role`, `scope` and `expires_at` (NULL = never). `role`
(`0091_api_key_roles`, D-133) is `viewer`, `member` or `admin` (`owner` is not a key role: the
owner-only operations are account and organization lifecycle and stay with a human); every row that
existed before the migration got `viewer`, so no key gained a permission from it. `scope` (`read`,
`write`) is derived from the role and kept for older clients. API keys authenticate the Query API
only and act with their role ([api.md](api.md#roles)); they never authenticate ingest, and ingest
license keys never authenticate the API.

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
created. Accepting marks the invitation and inserts the membership in one transaction. `last_sent_at` and
`send_count` (`0010_invitation_email`) record invitation e-mails. Resending replaces `token_hash` and
`expires_at` of an invitation that is neither accepted nor revoked (also after expiry), so the previous link stops
working. `locale` (`0050_email_locale`) is the inviter's language when the invitation was created; resent e-mails keep
it (`''`: the resending admin's language).

### `audit_log`
`id` (identity), `org_id` (NULL for user-level events such as sign-in), `actor_user_id`, `actor_email`,
`actor_api_key_id` (`REFERENCES api_keys ON DELETE SET NULL`) and `actor_api_key_name` (`0091_api_key_roles`,
D-133), `action`, `target_type`, `target_id`, `details` (jsonb), `ip`, `created_at`.

A change made with an API key has `actor_user_id` NULL and an empty `actor_email` — no user is invented for it:
the key is the actor, by id and by the name it had at the time (the name is copied rather than joined, so the
row keeps its meaning after the key is renamed or deleted).

| Action | Target |
|---|---|
| `org.create`, `org.rename`, `org.language_change` (`details.from`/`to`), `bootstrap` | organization |
| `member.role_change` (`details.from`/`to`), `member.remove` (`details.role`, `details.revoked_api_keys` when the removed user's API keys were revoked, `details.report_recipient_removed`) | user |
| `dashboard.report.recipient_remove` (`details.user_id`, `details.reports`, `details.disabled_reports`, `details.via`; written with a member removal that changed report recipient lists, D-096) | user |
| `user.language_change` (`details.from`/`to`; no organization) | user |
| `invitation.create`, `invitation.resend` (`details.email_sent`), `invitation.revoke`, `invitation.accept` | invitation |
| `user.email_verified`, `user.verification_resend` (no organization) | user |
| `license_key.create`, `license_key.revoke` | license_key (`details.name`, `details.prefix`; `details.custom = true` for an imported value) |
| `api_key.create` (`details.name`, `details.prefix`, `details.role`), `api_key.revoke` | api_key |
| `user.login`, `user.logout`, `user.password_change`, `user.password_reset`, `session.revoke` | session / user |
| `fleet.policy.update` (`details.from`/`to`) | policy (organization id) |
| `fleet.host_override.set` (`details.action`/`version`), `fleet.host_override.delete` | agent_host |
| `fleet.rollout.pause`, `fleet.rollout.resume`, `fleet.rollout.deploy_now` (`details.from_wave`/`wave`), `fleet.rollback` (`details.from_version`/`to_version`) | rollout |
| `update.check_requested`, `update.apply_requested` (`details.from`/`to`/`ignore_maintenance_window`/`engine`) | update_request |
| `fleet.rollout.create`, `fleet.rollout.advance`, `fleet.rollout.halt`, `fleet.rollout.complete`, `fleet.rollout.supersede` (actor email `openlog-controller`, no user) | rollout |
| `integration_setting.create`, `integration_setting.update`, `integration_setting.delete` (`details.integration`/`host_id`/`changed`/`password_changed`) | integration_setting |
| `apm.error_group.update` (`details.service_name`/`service_namespace`/`environment`, `status` and `assignee_user_id` from/to, `resolved_in_version`), `apm.error_group.comment`, `apm.error_group.comment_delete` (`details.comment_id`) | apm_error_group (target id = 16 hex digit group id) |
| `apm.error_group.regressed` (actor email `openlog-apm`, no user; `details.resolved_at`/`resolved_in_version`/`occurrence_at`/`version`) | apm_error_group |

Audit writes are best effort: a failed write is logged (`cannot write audit log`) and does not fail the
operation.

### `rate_limit_counters` (`0061_rate_limit_counters`)
UNLOGGED. `key` (bytea, 16 bytes = sha256(purpose, subject); client IPs and tokens are not stored), `bucket`
(timestamptz, minute start), `n`; PK `(key, bucket)`, index on `bucket`. Cluster-wide sliding-window counters of the
public dashboard share link endpoints (api.md "Public share link endpoints", `internal/ratelimit`, D-096): every api pod
adds its counts with one `INSERT … SELECT FROM unnest(…) ON CONFLICT (key, bucket) DO UPDATE SET n = n + excluded.n`
per second (keys sorted, ≤ 1000 per statement) or per request near a limit, and reads the current and previous bucket in
the same statement. The api leader (task `rate-limit-prune`) deletes buckets older than the previous minute every
minute. Being UNLOGGED, the counters start from zero after a crash or failover and are not on standbys.

### `login_failures`
`key_hash = sha256(lower(email) + "|" + client IP)`, `attempted_at`. Sign-in (and password checks
during invitation acceptance and password change) is refused with `429 resource_exhausted` after
`OPENLOG_LOGIN_MAX_FAILURES` failures within `OPENLOG_LOGIN_WINDOW`; a successful sign-in clears the
counter. Shared by all API pods. Rows older than a day are deleted hourly.
The same table holds the other API rate-limit counters, with `key_hash = sha256(purpose, subject)`: sign-up
attempts per client IP and per e-mail (`OPENLOG_SIGNUP_MAX_PER_IP`/`_PER_EMAIL`), invitation e-mails per
organization and per invited address, and verification e-mails per user (windows ≤ 24 h).

Listing (`GET /audit-log`) filters by `actor_email` or `actor_api_key_name` substring, `action` prefix (`starts_with`) and time, newest
first with a keyset cursor on `(created_at, id)`, served by `audit_log_org_idx`.

## Single sign-on and SCIM (`0030_sso`, `0056_sso_connections_slo`, `0063_sso_sessions_index`)

OIDC/SAML connections, claimed domains, role mappings, sign-in and logout states, the SAML replay cache, single
logout session data and SCIM provisioning ([api.md](api.md#single-sign-on), D-077, D-078, D-088, D-089, code
`internal/sso`, `internal/scim`, store `internal/store/postgres/sso.go`).

### `sso_connections`
`id` (PK), `org_id` (FK organizations; index `(org_id, created_at, id)`; `0056` dropped the UNIQUE: up to 10
connections, enforced by the api; the oldest is the **default connection**), `protocol` (`oidc`·`saml`),
`name`, `enabled`, `config` (jsonb `{"oidc": {issuer, client_id, scopes, require_email_verified}}` or
`{"saml": {idp_metadata_url, idp_metadata_xml, idp_entity_id, idp_sso_url, idp_certificates, idp_cert_not_after,
allow_idp_initiated, relay_state_allowlist, sign_authn_requests, sp_certificate_pem, idp_slo_url, idp_slo_binding,
metadata_signing_certificates, allow_unsigned_metadata, pending_metadata}}` plus `logout_redirect_allowlist`; the
metadata trust fields of D-098 — pinned PEM certificates, the unsigned choice and a refreshed change awaiting
confirmation `{digest, reason, detected_at, idp_certificates, idp_sso_url, idp_slo_url, signer_certificate}` — need no
migration: older api binaries ignore them and refresh unsigned metadata as before), `secret_enc`, `sp_key_enc`
(sealed, see "Secrets"), `email_attribute`, `name_attribute`, `groups_attribute` (empty = defaults), `jit_enabled`,
`default_role` (admin·member·viewer), `session_max_age_seconds` (0 = session TTL), `enforce`
(CHECK: only when `enabled`), `break_glass_user_ids uuid[]`, `config_version` (incremented by every settings save),
`tested_version` (set by a successful test sign-in of that version), `last_test_at`, `last_test_ok`,
`last_test_error`, `last_test_details` (jsonb: email, groups, role), `created_by`, `updated_by`, timestamps;
`0056`: `allow_external_invitations` (default true, claimed-domain invitations), background refresh state
`idp_refreshed_at`, `idp_refresh_ok` (NULL = never), `idp_refresh_error`, `idp_refresh_failures` (consecutive),
`idp_next_refresh_at` (partial index `WHERE enabled`), `idp_cache` (jsonb `{fetched_at, issuer, oidc_discovery,
oidc_jwks, saml_valid_until}` of the last successful fetch; written only by the refresh, never by a settings save).
Refreshed SAML metadata is written into `config.saml` with `WHERE config_version = <read version>` and does not
change the version. The session policy reads the session's connection (`enabled`, `session_max_age_seconds`) and the
connection the user's verified e-mail domain routes to (`enabled AND enforce`, `break_glass_user_ids`) in **one**
statement per session-authenticated request (primary key, `sso_domains (org_id, domain)`, `sso_connections_org_idx`).

### `sso_domains`
`id`, `org_id`, `domain` (lower case), `dns_token` (public TXT value), `email_token_hash`/`email_address`/
`email_expires_at` (pending e-mail verification, 24 h), `verified_at`, `verification_method` (`dns_txt`·`email`),
`last_checked_at`, `created_by`, `created_at`, `connection_id` (`0056`, FK `sso_connections` **ON DELETE SET NULL**;
NULL = the default connection). UNIQUE `(org_id, domain)`; **partial UNIQUE `(domain) WHERE verified_at IS NOT
NULL`**: a domain is verified by one organization only (a second verification → `already_exists`). A connection id of
another organization is refused by the store.

### `sso_role_mappings`
`org_id`, `connection_id` (`0056`, FK, cascade; NULL = organization-wide), `group_name`, `role` (admin·member·viewer;
owners are never mapped), `created_at`. UNIQUE `(org_id, coalesce(connection_id, 00000000-…), group_name)` (replaces
the former PK). Organization-wide mappings are used by SCIM groups and by connections without own mappings; each scope
is replaced as a whole in one transaction.

### `sso_login_states`
In-flight sign-ins: `state_hash` (UNIQUE), `binding_hash` (NULL for IdP-initiated SAML), `org_id`, `connection_id`
(FK, cascade), `purpose` (`login`·`test`·`logout`), `idp_initiated`, `nonce`, `pkce_verifier`, `saml_request_id`
(the AuthnRequest, or the LogoutRequest of an SP-initiated logout),
`redirect_to`, `actor_user_id` (test), `config_version` (a settings change during the sign-in expires it), `result`
(jsonb verified identity between SAML ACS and completion), `created_at`, `expires_at` (`OPENLOG_SSO_LOGIN_TTL`;
IdP-initiated 1 min), `consumed_at`. Consumption is a single `UPDATE … WHERE consumed_at IS NULL AND expires_at > now
RETURNING`, so a state completes once across all pods; `result` is written only while NULL.

### `sso_saml_assertions`
PK `(connection_id, assertion_id)`, `expires_at` (latest `NotOnOrAfter` + clock skew, at least 10 min). `INSERT … ON
CONFLICT DO UPDATE … WHERE expires_at < now()`: an id is accepted once until it expires (replay protection across
pods).

### `sso_sessions` (`0056`)
The IdP session of an SSO session: `session_id` (PK, FK `sessions`, cascade), `connection_id` (FK, cascade), `org_id`,
`user_id`, `subject` (SAML NameID value / OIDC `sub`), `name_id_format`, `name_qualifier`, `sp_name_qualifier`,
`session_index` (SAML SessionIndex / OIDC `sid`), `id_token_enc` (OIDC ID token for `id_token_hint`, sealed like
client secrets with AAD `oidc-id-token:<session id>`), `created_at`. Index `(connection_id, subject)`: an IdP
LogoutRequest lists the active sessions (joined with `sessions`: not revoked, not expired) of the connection with the
NameID; index `user_id`; `0063`: partial index `(connection_id, session_index) WHERE session_index <> ''` for OIDC
back-channel/front-channel logout by `sid` (D-098). The rows of LogoutRequest IDs and OIDC logout token `jti`s share the
replay cache (`sso_saml_assertions`, `slo:<id>`, `oidc-logout:<jti>`).

### `scim_tokens`
Like `api_keys`: `id`, `org_id`, `name`, `key_prefix`, `key_hash` (UNIQUE, HMAC/SHA-256 per D-044; rehashed on lookup),
`created_by`, `created_at`, `last_used_at` (≤ 1 write/min), `expires_at`, `revoked_at`.

### `scim_users`
PK `(org_id, user_id)` (FK users, cascade), `user_name` (UNIQUE per organization, case-insensitive), `external_id`
(UNIQUE per organization when not empty), `active`, `display_name`, `given_name`, `family_name`, timestamps. The row
stays when SCIM deactivates the user (the membership is removed), which keeps JIT sign-in from re-adding them.

### `scim_groups`, `scim_group_members`
`scim_groups`: `id`, `org_id`, `display_name` (UNIQUE per organization, case-insensitive), `external_id`, timestamps.
`scim_group_members`: PK `(group_id, user_id)`, index on `user_id`; deleting a SCIM user removes it from the
organization's groups.

### `sessions` (columns added by `0030_sso`)
`auth_method` (`password`·`oidc`·`saml`, default `password`), `org_id` (FK organizations, cascade; SSO sessions act
only in this organization), `sso_connection_id` (FK `sso_connections`, **ON DELETE CASCADE**: deleting a connection
deletes its sessions). Older api binaries insert password sessions with the defaults.

Cleanup (api, every 10 minutes, idempotent): login and logout states expired more than an hour ago, expired assertion
and LogoutRequest ids, expired domain e-mail tokens. `users.email` is changed by SCIM (`SetUserEmail`, UNIQUE →
`already_exists`).

Mixed versions (`0056`, expand): an older api reads one connection per organization (the first row it gets), treats
connection-specific role mappings as organization-wide (its replace deletes them) and creates no `sso_sessions`
rows, so its sessions cannot be ended by an IdP LogoutRequest until the rollout finishes.

## Fleet (agent updates, `0002_fleet`)

Agent update policy, host exceptions, rollouts and the agents that sync with ingest
([releases-updates.md](releases-updates.md) §3–§4, API: [api.md](api.md#fleet-agent-updates), code: `internal/fleet`).

### `agent_update_policies`
`org_id` (PK, FK organizations), `mode` (`off`·`notify`·`auto`), `channel` (`stable`·`beta`), `target`
(`latest`·`patch`·`pinned`), `pinned_version` (text, required when `target = pinned`), `waves` (int[], strictly
increasing percentages ending at 100), `wave_soak_minutes` (0–43200), `halt_failure_rate` (0–1),
`maintenance_windows` (jsonb `[{"days": ["mon",…], "start": "HH:MM", "end": "HH:MM"}]`, UTC; `[]` = always;
`end <= start` spans midnight), `updated_at`, `updated_by`, `php_agent` (`0045_php_agent_fleet`, jsonb
`{"mode": "off|manual|auto", "version": "agent|<semver>", "reload": "none|graceful", "exclude_bins": [], "changed_at"}`;
`{}` = defaults: `manual`, `agent`, `none`). **No row = the default policy** (`auto`, `stable`, `latest`,
`[10,50,100]`, 60 min, 0.05, no windows, PHP agent `manual`). Validation is done by the API. `java_agent`
(`0086_java_agent_fleet`, jsonb `{"mode": "off|manual|auto", "version": "agent|<semver>", "changed_at"}`; `{}` =
defaults `manual`, `agent`; java-agent.md §2.6).

### `agent_java_host_overrides` (`0086_java_agent_fleet`)
`(org_id, host_id)` PK, `mode` (`off`·`manual`·`auto`: the host's Java agent mode instead of the policy's; an `auto`
override skips Java agent waves), `updated_at`, `updated_by`. Rows are kept when the host disappears.

### `agent_php_host_overrides` (`0045_php_agent_fleet`)
`(org_id, host_id)` PK, `mode` (`off`·`manual`·`auto`: the host's PHP agent mode instead of the policy's; an `auto`
override skips PHP agent waves), `updated_at`, `updated_by`. Rows are kept when the host disappears.

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
turned off on the host), `php_agent` (`0045_php_agent_fleet`, jsonb: the `php_agent` section of the last sync request —
PHP runtimes, installed PHP agent version, `managed_by`, last operation — bounded by ingest; NULL when the agent does
not report one), `php_access` (`0064_php_access`, jsonb: the `php_access` section of the last sync request — socket
group, PHP-FPM pools and whether their users may write `php.sock`, php-agent.md §1 — at most 512 pools; NULL when the
agent does not report one), `java_agent` (`0086_java_agent_fleet`, jsonb: the `java_agent` section of the last sync
request — managed jar version, link state, JVMs with loaded version and `restart_pending`, last operation — at most 64
JVMs; NULL when the agent does not report one).

Written **only by ingest, asynchronously**: sync requests are answered from memory; reports are queued per pod
(coalesced per host, bounded by `OPENLOG_FLEET_REPORT_QUEUE_SIZE`, the oldest report is dropped when full:
`openlog_fleet_host_reports_dropped_total`) and upserted every second in statements of at most 200 rows
(`INSERT … SELECT FROM unnest(…) JOIN organizations ON tenant_id … ON CONFLICT DO UPDATE`). A failed batch is
dropped; agents report again on their next sync.

### Access pattern and leader
- Ingest reads, per tenant and pod at most once per `OPENLOG_FLEET_POLICY_CACHE_TTL` (≤ 30 s, singleflight):
  the organization by `tenant_id`, its policy, overrides, PHP agent overrides, current rollout and integration settings. While PostgreSQL is unreachable,
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

### `tail_sampling_policies` (`0040_tail_sampling`)
One tail sampling policy per organization ([apm.md](apm.md) §4.2, D-075). Primary key `org_id`; `policy` jsonb object
(validated by the api: `internal/tailsampling.ParsePolicy`), `version` (starts at 1, +1 per save; `PUT
/api/v1/apm/sampling` sends the edited version and gets 409 on mismatch), `updated_at`, `updated_by` (user, nullable).
Written together with the audit event `apm.tail_sampling.update` (target `tail_sampling_policy` = org id,
`details.version`/`enabled`/`baseline_ratio`/`max_spans_per_second`/`rules` count) in one transaction.
`openlog-sampler` reads all rows joined with `organizations.tenant_id` every `OPENLOG_TAILSAMPLING_POLICY_REFRESH`;
invalid documents are skipped (the default policy applies).

## SLOs (`0087_slo`)

### `slos`
Service level objectives of APM services ([slo.md](slo.md), [api.md](api.md#service-level-objectives), D-125).
`id` (uuid), `org_id` (cascade), `name` (1–200), `description` (≤ 2000), the target service
(`service_name` 1–512; `service_namespace` and `deployment_environment` nullable: NULL = every
namespace/environment, `''` = exactly the services without the attribute), `sli_type`
(`availability`/`latency`), `latency_threshold_ms` (1–600000, set exactly for `latency`, CHECK), `objective`
(percent, 50 ≤ x < 100), `window_days` (7, 28 or 30), `created_by`/`updated_by` (`ON DELETE SET NULL`),
`created_at`, `updated_at`. Indexes: (`org_id`, `lower(name)`), (`org_id`, `service_name`). At most 200 rows
per organization (checked in the insert statement). Written by `/api/v1/slos` together with the audit event
`slo.create`/`slo.update`/`slo.delete` (target type `slo`) in one transaction. Budgets are **not** stored:
they are computed from `apm_transactions_1m` at query time.

The migration also extends `alert_rules_type_check` with `slo_burn` (alerting.md §2.11); such rules keep the
`slo_id` of a deleted SLO and then report an evaluation error.

## Job monitoring (`0096_job_monitors`)

`job_monitors` is the definition (name, schedule kind, cron expression and its time zone or the reporting
interval, grace period, tags) plus `ping_token`, the value in the URL the job calls. `job_monitor_state` is
one row per monitor: what the last ping said and when the next run is due. The sweeper claims overdue rows
with `FOR UPDATE ... SKIP LOCKED` and advances `expected_at` in the same statement, so a missed run is
concluded once (D-141). The runs themselves live in ClickHouse (`job_runs`, 90 days).

The token is stored as it is rather than hashed: the URL lives in a crontab and has to stay re-readable, and
what it authorizes is one monitor's own status — the threat model is in [api.md](api.md#job-monitoring).

## Synthetic monitoring (`0090_synthetics`)

Extended by `0095_synthetic_check_types` (D-140): `type` accepts `http`, `tcp`, `dns` and `tls`, and the
non-HTTP kinds use `target` (`host:port`, or the name a dns check resolves) with `dns_record_type`,
`dns_expected` and `tls_warning_days`. `synthetic_checks_target_per_type` is the constraint that keeps a row
describing one kind: an `http` check has a `url` and an empty `target`, every other kind the reverse.

### `synthetic_checks`
Scheduled outside-in HTTP checks ([api.md](api.md#synthetic-monitoring), D-132). `id` (uuid), `org_id`
(cascade), `name` (1–200), `type` (`http`; the CHECK is where further check types are added), `enabled`,
`url` (1–2048), `method` (CHECK, seven verbs), `headers` (jsonb object, ≤ 20 entries), `body` (≤ 65536),
`expected_status` (`integer[]`, 1–10 codes), `assertion_type`
(`none`/`contains`/`not_contains`/`json_path`), `assertion_path` and `assertion_value` (≤ 1024 each),
`timeout_ms` (500–60000), `interval_seconds` (30–86400), `locations` (`text[]`, 1–10; built-in `local` =
the openlog server), `created_by`/`updated_by` (`ON DELETE SET NULL`), `created_at`, `updated_at`. A table
CHECK keeps `timeout_ms <= interval_seconds * 1000`, so a slow target cannot pile runs up. Index:
(`org_id`, `lower(name)`). At most 100 rows per organization (checked in the insert statement). Written by
`/api/v1/synthetics/checks` together with the audit event
`synthetic_check.create`/`update`/`delete` (target type `synthetic_check`) in one transaction.

### `synthetic_check_schedule`
One row per check and location, primary key (`check_id`, `location`), `check_id` cascading from
`synthetic_checks`. `next_run_at` (indexed) is when the run is due; `last_run_at`, `last_success` (NULL
until the first run), `last_status_code`, `last_duration_ms`, `last_error_kind` and `last_error` are the
last outcome, which is what the check list shows without querying ClickHouse. The api leader claims due rows
with `FOR UPDATE ... SKIP LOCKED` and advances `next_run_at` by the check's interval **in the same
statement**, so two schedulers (a leadership handover) never run the same check twice. Adding a location
needs no migration: it is a value in `synthetic_checks.locations` plus a row here. The runs themselves are
**not** stored here; they go to ClickHouse `synthetic_runs` (30 days) and are mirrored as metric data points.

## Cloud connections (`0092_cloud_connections`)

### `cloud_connections`
One cloud account an organization lets openlog read metrics from ([api.md](api.md#cloud-connections), D-135).
`id` (uuid, generated by the API because it is part of the credential AAD), `org_id` (cascade), `name`
(1–200), `provider` (CHECK `aws`/`azure`/`gcp`), `ingest_mode` (CHECK `poll`/`push`, default `poll`),
`enabled`, `credentials_enc`, `credentials_key_id`, `scopes` (`text[]`, 1–50: AWS regions, Azure
subscriptions, GCP projects), `services` (`text[]`, 1–50; the known values are a list in
`internal/cloudconnect`, not a CHECK, so a new service needs no migration), `poll_interval_seconds`
(60–86400), `max_metrics_per_poll` (100–200000), `max_api_calls_per_poll` (1–5000),
`created_by`/`updated_by` (`ON DELETE SET NULL`), `created_at`, `updated_at`. Index: (`org_id`,
`lower(name)`). At most 50 rows per organization (checked in the insert statement). Written by
`/api/v1/cloud/connections` together with the audit event `cloud_connection.create`/`update`/`delete`
(target type `cloud_connection`) in one transaction.

`credentials_enc` is `ol1:<key id>:<AES-256-GCM>` of a provider-specific JSON credential document with
`OPENLOG_SECRETS_KEY`, AAD `org_id/id` — the same mechanism as `integration_settings.password_enc`. It is
never selected by the list and detail queries (only by the credential read the poller and the test action
use), never returned by the API, and never part of a log line or an audit detail; the audit records only
`credentials_changed`. `openlog-alert rotate-secrets` does not re-encrypt these rows yet, so keep a previous
key in `OPENLOG_SECRETS_KEY_PREVIOUS` until each connection has been saved again. `ingest_mode` `push` is
reserved for provider-side delivery: such a row keeps its credentials, scopes and services and simply has no
schedule rows, so adding push later needs no migration.

### `cloud_connection_schedule`
One row per connection and scope, primary key (`connection_id`, `scope`), cascading from `cloud_connections`.
`next_run_at` (indexed) is when the poll is due; `last_run_at`, `last_status` (NULL until the first poll, then
`ok`/`partial`/`error`), `last_error`, `last_metrics`, `last_api_calls`, `last_duration_ms` are the last
outcome, which is what the connection list shows without reading the run history. `consecutive_errors` drives
the exponential backoff of a failing scope (reset to 0 by any non-error poll). The api leader claims due rows
with `FOR UPDATE ... SKIP LOCKED` and advances `next_run_at` by the connection's interval **in the same
statement**, so two schedulers (a leadership handover) never poll the same scope twice — which here means the
provider is never billed twice for the same poll. Only rows of enabled `poll` connections that have stored
credentials are claimed. One row per scope is also what isolates failures: a region that is throttled or
misconfigured carries its own status and backoff and never stops the others.

### `cloud_collection_runs`
The recent polls, for the honest history behind the status badge: `connection_id` (cascade), `scope`,
`started_at`, `duration_ms`, `status` (CHECK `ok`/`partial`/`error`), `metrics` (data points written),
`api_calls` (provider requests made — what the provider bills for), `throttled`, `error`, and `services` (a
JSON array `[{"service","metrics","error"}]`, so a failure names the service it belongs to). Index
(`connection_id`, `started_at DESC`). The API keeps the newest 200 rows per connection and prunes the rest in
the same transaction as the write, so the table stays small without a retention job. The metrics themselves
are **not** here: they go to ClickHouse `metrics` like any other data point.

## APM error workflow (`0025_apm_error_workflow`)

Error inbox state ([apm.md](apm.md) §3.4). A group without a row is unresolved and unassigned.

### `apm_error_group_states`
Primary key (`org_id`, `group_id`); `group_id` = the 16 lower-case hex digit ClickHouse error group id (it already
encodes the service); `service_name`, `service_namespace`, `deployment_environment` for filtering; `status`
(`unresolved`/`resolved`/`ignored`), `assignee_user_id` (member; `ON DELETE SET NULL`), `resolved_at` (set exactly
while resolved, CHECK), `resolved_in_version` (≤ 256 bytes, only while resolved, CHECK), `resolved_by`,
`regressed_at`, `regression_count`, `created_at`, `updated_at`, `updated_by` (NULL for automatic reopenings). Indexes:
(`org_id`, `status`, `updated_at DESC`), (`org_id`, `assignee_user_id`) where assigned, (`org_id`, service triple).
Written by `PATCH /api/v1/apm/errors/groups` (upsert under `SELECT … FOR UPDATE` per group, audit event in the same
transaction; the assignee must have a `memberships` row) and by regression detection (conditional
`UPDATE … WHERE status = 'resolved' AND resolved_at < occurrence`, audit `apm.error_group.regressed`).

### `apm_error_group_comments`
`id` (uuid), `org_id`, `group_id`, `author_user_id` (`ON DELETE SET NULL`), `author_email`, `body` (1–4000),
`created_at`; index (`org_id`, `group_id`, `created_at`). Deleted by the author or an admin/owner.

The migration also adds `audit_log_target_idx` (`org_id`, `target_type`, `target_id`, `created_at DESC`) for a group's
activity and extends `alert_rules_type_check` with `apm_error` (and `oql`, so the order of the parallel expand
migrations does not matter).

## Dashboards (`0020_dashboards`)

Custom dashboards of OQL widgets ([api.md](api.md#dashboards), [oql.md](oql.md), code: `internal/dashboard`, D-064).

| Table | Key | Content |
|---|---|---|
| `dashboards` | `id` | `org_id` (cascade), `name` (1–200), `description` (≤ 2000), `visibility` (`org`·`private`), `variables` (jsonb: `[{name, label, type, query, values, default, multi, include_all}]`), `version` (+1 on every save), `created_by`/`updated_by` (SET NULL), timestamps. Index `(org_id, lower(name))` |
| `dashboard_pages` | `id` | `dashboard_id` (cascade), `position` (unique per dashboard), `name` (1–100) |
| `dashboard_widgets` | `id` | `dashboard_id`, `page_id` (both cascade), `position` (unique per page), `title` (≤ 200), `visualization` (`line`·`area`·`bar`·`table`·`billboard`·`pie`·`heatmap`·`markdown`), `x`/`y`/`w`/`h` (12-column grid: `0 ≤ x ≤ 11`, `1 ≤ w ≤ 12`, `x + w ≤ 12`, `0 ≤ y ≤ 10000`, `1 ≤ h ≤ 50`), `query` (≤ 8192), `markdown` (≤ 20000), `unit`, `thresholds` (jsonb `[{value, severity}]`), `options` (jsonb `{stacked, legend}`) |

A save replaces the whole document in one transaction: the dashboard row is locked `FOR UPDATE`, its `version` compared
with the version the client read (mismatch → `409`), then pages (and, by cascade, widgets) are deleted and re-inserted
with the kept ids. Reads of one dashboard use a repeatable-read, read-only transaction. Visibility is filtered in SQL
(`visibility = 'org' OR created_by = viewer OR (created_by IS NULL AND admin)`). Audit: `dashboard.{create,update,delete,duplicate,import}`.
`0021_alert_oql` adds `oql` to `alert_rules.type` ([alerting.md](alerting.md) §2.10).

Version history, sharing and scheduled reports (`0053_dashboard_versions`, `0054_dashboard_shares`,
`0055_dashboard_reports`; [api.md](api.md#dashboards), D-086, D-087):

| Table | Key | Content |
|---|---|---|
| `dashboard_versions` | `(dashboard_id, version)` | `dashboard_id` (cascade), `document` (jsonb `{name, description, visibility, variables, pages}` with page and widget ids), `author_id` (SET NULL), `restored_from`, `created_at`. The newest 50 per dashboard are kept (pruned in the saving transaction); 0053 backfills the current version of every existing dashboard |
| `dashboard_org_settings` | `org_id` | `shares_enabled` (default false), `report_domains` (text[] ≤ 20), `updated_by` (SET NULL), `updated_at`. No row = defaults |
| `dashboard_shares` | `id` | `org_id`, `dashboard_id` (both cascade), `token_hash` (SHA-256 of the token, unique), `label` (≤ 100), `time_range` (relative) or `range_from`/`range_to` (fixed; CHECK exactly one), `variables` (jsonb), `created_by`/`revoked_by` (SET NULL), `created_at`, `expires_at`, `revoked_at`, `last_used_at`, `use_count`. Index `(dashboard_id, created_at DESC)` |
| `dashboard_reports` | `id` | `org_id`, `dashboard_id` (both cascade), `name` (≤ 100), `frequency` (`daily`·`weekly`), `weekday` (0 = Sunday), `hour`, `minute`, `timezone`, `recipients` (text[] ≤ 20; `0062_report_recipients_cleanup` allows an empty list, the API requires 1–20), `language` (`en`·`tr`), `time_range`, `variables` (jsonb), `enabled`, `created_by` (SET NULL), timestamps. Partial index on enabled. Removing a member (`internal/store/postgres` `RemoveMemberWithCleanup`, API and SCIM) runs `array_remove` of the user's address in the membership's transaction and disables reports left empty (D-096) |
| `dashboard_report_runs` | `(report_id, period)` | `report_id` (cascade), `period` (local date `YYYY-MM-DD`), `status` (`running`·`sent`·`partial`·`failed`·`skipped`), `error` (≤ 1000), `recipients` (sent count), `started_at`, `finished_at`; runs older than 90 days are deleted when a new run of the report is claimed |

Every save inserts its version row in the same transaction as the document. A share link lookup joins the organization
(tenant id), `dashboard_org_settings.shares_enabled` and the creator's membership; `last_used_at`/`use_count` are
updated at most once a minute per link. Creating a share link or report locks the dashboard row `FOR UPDATE` to enforce
the per-dashboard limits. The report job claims `(report_id, period)` with `INSERT … ON CONFLICT DO NOTHING` before
sending. Audit: `dashboard.{restore,settings.update,share.create,share.revoke,share.access,report.create,report.update,report.delete}`.

## Alerting (`0004_alerting`, `0088_alert_routing`)

Rules, evaluation state, incidents, channels, routing rules, mutes and the notification outbox ([alerting.md](alerting.md), API:
[api.md](api.md#alerting), code: `internal/alert/pgstore_*.go`).

| Table | Key | Content |
|---|---|---|
| `alert_rules` | `id` | `org_id`, `name`, `description`, `type` (`apm_no_data` allowed since `0006_alerting_m2`), `severity`, `enabled`, `interval_seconds`, `for_seconds`, `recovery_for_seconds`, `condition` (jsonb, validated canonical form), `renotify_interval_seconds`, `flapping` (jsonb), `runbook_url`, `labels` (jsonb), `version` (+1 on every change), `created_by`, `updated_by`, timestamps |
| `alert_rule_channels` | (`rule_id`, `channel_id`) | rule → channel (both cascade) |
| `alert_channels` | `id` | `org_id`, `name`, `type` (`slack`·`email`·`webhook`·`teams`, plus `pagerduty`·`opsgenie` since `0088_alert_routing`), `enabled`, `config` (jsonb, non-secret: e-mail recipients/SMTP override, `pagerduty` and `opsgenie` sections), `secrets` (`ol1:<key id>:<AES-256-GCM>` of the secret JSON — `url`, `hmac_secret`, `smtp_password`, `routing_key`, `api_key` —, AAD `org_id/channel_id`), `secrets_key_id`, `secret_hints` (masked), `created_by`, timestamps |
| `alert_evaluators` | `instance_id` | evaluator heartbeats (`last_seen`, database clock); rows older than 1 h are pruned |
| `alert_rule_leases` | `rule_id` | `org_id`, `owner` (`''` = free), `lease_until` (`-infinity` = released), `claimed_at`, `next_eval_at`, `last_eval_end`, `last_evaluated_at`, `last_result`, `last_error`, `last_duration_ms`. One row per rule (created with it; missing rows are added by evaluators) |
| `alert_series_state` | (`rule_id`, `series_key`) | non-ok series (and ok series with flapping history): `labels`, `state`, `pending_since`, `firing_since`, `recovering_since`, `last_value`, `last_seen_at`, `incident_id`, `transitions` (timestamptz[] ≤ 10), `last_notified_at`, `renotify_count` |
| `alert_incidents` | `id` | `org_id`, `rule_id` (SET NULL on delete), `rule_name`/`rule_type`/`severity` snapshot, `series_key`, `labels`, `summary`, `state` (`open`·`acknowledged`·`resolved`), `value`, `last_value`, `threshold`, `channel_ids` (uuid[] notified at open), `flapping`, `opened_at`, `acknowledged_at/by`, `resolved_at/by`, `resolve_reason`. Partial unique index `alert_incidents_open_uniq (rule_id, series_key) WHERE state <> 'resolved'` |
| `alert_incident_events` | `id` (identity) | timeline: `incident_id`, `at`, `kind`, `actor_user_id`, `actor_email`, `message` (≤ 4000), `details` |
| `alert_mutes` | `id` | `org_id`, `name`, `comment`, `starts_at`, `ends_at`, `rule_ids` (uuid[], empty = all), `matchers` (jsonb), `schedule` (jsonb, `0006_alerting_m2`; NULL = one-off; recurring mutes keep the current or next occurrence in `starts_at`/`ends_at`, rolled forward by dispatchers), `created_by`, timestamps |
| `alert_holiday_calendars` | `id` | `0015_alert_holiday_calendars`: `org_id`, `name` (unique per org), `description`, `dates` (text[], `YYYY-MM-DD` or `MM-DD`), `created_by`, timestamps; referenced by id from `alert_mutes.schedule->holiday_calendar_ids` (no FK: delete is refused while referenced) |
| `alert_routing_rules` | `id` | `0088_alert_routing`: ordered routing rules (alerting.md §5.6): `org_id`, `name`, `"position"` (0 first), `enabled`, `is_default` (partial unique index `alert_routing_rules_default_uniq (org_id) WHERE is_default`), `match` (jsonb: severities, services, rule types, label matchers, local time window), `created_by`, timestamps |
| `alert_routing_rule_channels` | (`route_id`, `channel_id`) | routing rule → channel (both cascade) |
| `alert_notifications` | `id` (+ `seq` identity for ordering) | outbox: `org_id`, `incident_id`, `rule_id`, `channel_id` (SET NULL), `channel_type`, `kind` (`opened`·`resolved`·`renotify`·`test`, plus `acknowledged` since `0088_alert_routing`), `idempotency_key` (UNIQUE), `payload` (jsonb event), `status` (`pending`·`sending`·`delivered`·`failed`·`suppressed`), `attempts`, `next_attempt_at`, `claimed_by`, `claimed_until`, `muted_logged`, `last_error`, `created_at`, `finished_at`. Finished rows older than 30 days are pruned |
| `alert_delivery_attempts` | `id` (identity) | delivery log: `notification_id` (cascade), `attempt`, `instance_id`, `started_at`, `duration_ms`, `success`, `status_code`, `error` (≤ 2000) |

**Concurrency.** Leases are claimed with `SELECT … FOR UPDATE SKIP LOCKED` and renewed by the owner; every evaluation
commit locks the rule's lease row `FOR UPDATE` and checks owner, expiry, `last_eval_end` and the rule `version`
(`FOR SHARE`) before writing series state, incidents, events and outbox rows in the same transaction. API changes to a
rule or incident lock the same lease row first (same lock order), so they serialize with evaluations. Dispatchers claim
outbox rows with `FOR UPDATE SKIP LOCKED` and finish them conditionally (`status = 'sending' AND claimed_by = me`);
expired claims return to `pending`. Audit: `alert.rule.*`, `alert.channel.*`, `alert.mute.*`,
`alert.routing_rule.{create,update,delete,reorder}`, `alert.holiday_calendar.*`,
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

## Usage, plans and billing (`0035_usage_plans`)

Plan definitions are configuration (`OPENLOG_PLANS`, [usage.md](usage.md) §4); these tables hold assignments and the
bookkeeping of quota evaluation, notifications, billing pushes and per-tenant retention (D-079–D-081).

### `org_plans`
One row per organization with an explicit assignment (none = `OPENLOG_DEFAULT_PLAN`): `plan_id` (catalog id; unknown ids
fall back to the default plan), `overrides` jsonb (partial limits, `quota.Overrides`), `billing_provider`,
`billing_customer_id`, `billing_subscription_id` (empty until connected; partial index on provider + customer),
`note`, `updated_by`, `updated_at`. Written only by superadmins (`PUT /api/v1/admin/orgs/{org}/plan`) and billing
webhooks, each change with audit action `plan.update` (target `organization`).

### `org_query_limits` (`0051_org_query_limits`)
Organization layer of the ClickHouse query limits ([usage.md](usage.md) §4.5). PK `org_id` (cascade):
`max_memory_usage`, `max_rows_to_read`, `max_bytes_to_read` (bigint `>= 0`; NULL = inherit the plan/defaults, `0` = not
set by openlog), `updated_by`, `updated_at`. No row = nothing set; clearing every value deletes the row. Written through
`PUT`/`DELETE /api/v1/usage/query-limits` with audit actions `query_limits.update` / `query_limits.delete`; read by
every api and alert pod every 30 s.

### `tenant_quota_status`
Latest evaluation per tenant (PK `tenant_id`), upserted by the api leader every `OPENLOG_USAGE_EVALUATION_INTERVAL`:
`org_id`, `plan_id`, `period_start`, `level` (`ok`/`warning`/`exceeded`), `ingest_bytes`, `ingest_limit_bytes`,
`ingest_blocked`, `rate_bytes_per_second`, `burst_bytes`, `metrics` jsonb, `evaluated_at`. Every ingest pod reads the rows
evaluated within the last hour that are blocked or rate limited (`OPENLOG_QUOTA_REFRESH_INTERVAL`); the UI banner reads
its organization's row. Rows of deleted organizations cascade.

### `usage_notifications`
PK `(org_id, period_start, metric, threshold)`: the insert claims an owner e-mail for a billing period; released
(deleted) when no e-mail could be sent, `recipients`/`sent_at` updated after sending.

### `billing_usage_pushes`
PK `idempotency_key` (`openlog:<org_id>:<day>:<metric>`, also sent to the provider): `provider`, `metric`, `day`,
`quantity`, `status` (`pending`/`pushed`/`failed`), `attempts`, `error`. A failed record is re-claimed until 5 attempts; a
pending claim older than 15 minutes is taken over.

### `usage_retention_mutations`
PK `(table_name, partition_id, tenants_hash)`: ClickHouse `ALTER … DELETE IN PARTITION` mutations submitted by the
per-tenant retention job (`retention_days`, `tenants`, `submitted_at`); not re-submitted for the same tenant set within
24 h; rows older than 30 days are pruned.

## SaaS operations (`0065_saas_operator`)

Operator console, organization lifecycle, support access, abuse flags and hard host limits (D-105, D-106,
[saas.md](../operations/saas.md) §8–§11). All tables cascade with their organization. Older binaries ignore them.

### `org_saas_state`
PK `org_id`; no row = active, no trial, no support access. Suspension: `suspended_at`, `suspended_by`, `suspend_reason`
(read by every ingest pod every `OPENLOG_QUOTA_REFRESH_INTERVAL` and every api pod every 15 s). Trial: `trial_plan_id`,
`trial_started_at`, `trial_ends_at`, `trial_ended_at` (set by the leader when it assigns the fallback plan; partial index
on running trials). Support access granted by an owner: `support_access_until`, `support_access_granted_by`,
`support_access_granted_at`. `updated_at`.

### `trial_notifications`
PK `(org_id, trial_ends_at, days_left)`: claim of one trial e-mail per step (`days_left` 7/3/1, `0` = ended); released
when no e-mail could be sent. Extending a trial changes `trial_ends_at`, so reminders are sent again for the new end.

### `support_sessions`
`id`, `org_id`, `operator_user_id`, `operator_email`, `auth_session_id` (the operator's `sessions` row, cascade: signing
out ends the support view), `reason`, `started_at`, `expires_at`, `ended_at`. Index `(org_id, started_at DESC)`.

### `abuse_flags`
`id` identity, `org_id`, `kind` (`ingest_spike`, `new_org_hosts`, `ingest_source_ips`), `status`
(`open`/`dismissed`/`actioned`), `details` jsonb, `occurrences`, `first_seen_at`, `last_seen_at`, `auto_suspended`,
`resolved_by`, `resolved_by_email`, `resolved_at`, `resolution_note`. Unique partial index: one open flag per
`(org_id, kind)` — the detector refreshes it instead of adding rows.

### `tenant_host_limits`, `tenant_known_hosts`
Written by the api leader (`OPENLOG_SAAS_HOST_SYNC_INTERVAL`) for tenants whose effective plan limits hosts:
`tenant_host_limits` (PK `tenant_id` → `organizations.tenant_id`): `host_limit`, `active_hosts`, `evaluated_at` (rows older
than an hour are not enforced); `tenant_known_hosts` (PK `(tenant_id, host_id)`): `last_seen` date of hosts active
today or yesterday (older rows deleted). Ingest pods load both.

### `ingest_source_counts`
PK `(tenant_id, hour, instance)`: `distinct_ips` — the number of distinct client addresses of authenticated OTLP requests
per ingest instance and hour (addresses themselves are never stored), `updated_at`. Read by the abuse detector (max over
instances); rows older than 48 hours are deleted.

## Data subject requests (`0070_data_subject_requests`)

Data exports, organization soft deletion and hard deletion bookkeeping, deletion certificates (D-107,
[saas.md](../operations/saas.md) §12).

### `organizations.deleted_at`
Set when an owner or operator schedules the organization's deletion, cleared by a cancellation. While set, the store
hides the organization from memberships (sessions lose access on the next request), API keys of the organization do not
authenticate, license keys are not found by ingest, alert rules are not claimed for evaluation and scheduled reports
are not sent. Partial index on non-null values.

### `data_exports`
Export jobs. `kind` `organization` (owners; `org_id` cascade, `user_id` = requester, SET NULL) or `user` (personal;
`user_id` = subject). `status` `pending` → `running` → `completed` | `failed`; `completed` → `expired` when `expires_at`
passes (the archive is deleted, `object_key` and `download_token_hash` cleared). `signals` (`logs`, `traces`,
`metrics`), `range_from`/`range_to`, `locale` (e-mail), `storage` (`local`/`s3`), `object_key`
(`exports/<kind>/<org or user id>/<id>.zip`), `size_bytes`, `telemetry_rows`, `truncated`, `manifest` jsonb (the
archive's manifest.json), `download_token_hash` (sha256 of the e-mailed link token, unique), `attempts`, `error`
(safe for the requester), `created_at`, `started_at`, `heartbeat_at`, `completed_at`, `expires_at`. The api leader
claims the oldest pending row with `FOR UPDATE SKIP LOCKED`; a running row without a heartbeat for 5 minutes is retried
(3 attempts). Creation is serialized per organization/user with `pg_advisory_xact_lock(hashtext('data_export:…'))`:
one pending or running export at a time and at most 5 per 24 hours. Failed and expired rows are pruned after 90 days.

### `org_deletions`
One row per scheduling: `org_id` (SET NULL once the organization is gone), `tenant_id`, `status` `scheduled` →
`cancelled` | `deleting` → `completed`, `initiator` (`owner`/`operator`), `reason` (operators), `requested_by`,
`requested_by_email`, `notify` jsonb (organization name and owner addresses for the completion e-mail),
`revoked_license_key_ids` / `revoked_scim_token_ids` (keys revoked by the scheduling and restored by a cancellation),
`requested_at`, `purge_after` (= requested_at + grace), `cancelled_at`, `started_at`, `completed_at`, `progress` jsonb
(ClickHouse row counts before, submitted mutations per table and partition, remaining rows, `verified`), `attempts`,
`last_error`, `certificate_id`. Unique partial index: one `scheduled`/`deleting` row per organization. When a deletion
completes, `requested_by`, `requested_by_email`, `reason` and `notify` are cleared. A deletion becomes `deleting` (no
longer cancellable) when the leader starts it after `purge_after`.

### `license_key_tombstones` (`0075_license_key_tombstones`)
License keys revoked by an organization deletion, so ingest answers `403 org_deleted` instead of `401` (D-115):
`key_hash` (primary key; `license_keys.key_hash` at scheduling time — a hash, no personal data), `reason`
(`org_deleted`), `org_deletion_id` (no foreign key; not an `org_id` column, so the purge keeps the row), `created_at`,
`expires_at`. Written by the scheduling for the keys it revokes (keys revoked earlier by users get none), deleted by a
cancellation, given `expires_at` = purge time + 365 days by the purge; the purge also deletes expired rows. The license
key lookup consults it only when no active key matches a candidate hash (D-044 candidates, same as `license_keys`) and
ignores it while that hash belongs to a key of an organization that is not deleted.

### `deletion_certificates`
Proof of a completed hard deletion without personal data, kept indefinitely: `subject_type` (`organization`/`user`),
`subject_hash` (hex sha256 of the tenant id or user id), `initiator` (`owner`/`operator`/`self`), `requested_at`,
`grace_ended_at`, `started_at`, `completed_at`, `postgres_rows` jsonb (rows deleted or pseudonymized per table; for
organizations also `users_deleted`: accounts left without any organization), `clickhouse_rows` jsonb (the tenant's rows
per ClickHouse table before deletion), `verified` (every ClickHouse table re-counted with zero rows). Listed by
superadmins (`GET /api/v1/admin/deletion-certificates`).

### Account deletion
`DELETE`s the `users` row in one transaction (cascade: memberships, sessions, `sso_sessions`, `sso_login_states`,
`email_verifications`, `scim_users`, `scim_group_members`), deletes `invitations` to the address, API keys the user
created and personal `data_exports`, removes the address from `dashboard_reports.recipients` (reports left without
recipients are disabled) and `sso_domains.email_address`, and replaces the user in retained records with a random
stable pseudonym `deleted-user-<10 hex>`: `audit_log` (`actor_email`, `actor_user_id` NULL, `ip` cleared, `target_id`
of `user` targets, occurrences of the address and id inside `details`), `alert_incident_events.actor_email`,
`apm_error_group_comments.author_email`, `update_requests.requested_by_email`, `org_deletions.requested_by_email`.
Refused while the user is the only owner of an organization that is not scheduled for deletion.

## Status page (`0071_status_page`)

Public status page data (D-108); no tenant data.

### `status_incidents`
`kind` (`incident`/`maintenance`), `title`, `status` (incident: `investigating`, `identified`, `monitoring`,
`resolved`; maintenance: `scheduled`, `in_progress`, `completed`), `impact` (`none`, `minor`, `major`, `critical`),
`components` text[] (`ingest`, `query_api`, `alerting`, `processing`), `starts_at`, `ends_at` (maintenance end or
resolution time, set on `resolved`/`completed`), `created_by` (SET NULL), `created_at`, `updated_at`. Written by
superadmins (audit actions `status.incident_create`, `status.incident_update`, `status.incident_delete` with
`org_id NULL`).

### `status_incident_updates`
Timeline entries: `incident_id` (cascade), `status`, `message` (1–5000 characters), `created_at`.

### `status_checks_daily`
PK `(component, day)`: `checks`, `operational` (operational or under maintenance), `degraded`, `outage` (partial or
major outage). The api leader adds one check per component per minute; uptime = `(checks - outage) / checks`. Rows
older than 400 days are pruned. The latest snapshot is the `system_state` document `status_page`
(`checked_at`, `components`, `processing_lag_seconds`).

## Saved views (`0080_saved_views`)

Saved Logs/Metrics Explorer views ([api.md](api.md#saved-views), code: `internal/savedview`, D-118).

| Table | Key | Content |
|---|---|---|
| `saved_views` | `id` | `org_id` (cascade), `signal` (`logs`·`metrics`·`traces`), `name` (1–200), `description` (≤ 2000), `visibility` (`private`·`org`), `state` (jsonb object ≤ 64 KiB; the API accepts ≤ 32 KiB: filters, groups, columns, time range …), `created_by` (SET NULL), `created_at`, `updated_at`. Index `(org_id, signal, lower(name))` |

Visibility is filtered in SQL (`visibility = 'org' OR created_by = viewer OR (created_by IS NULL AND admin)`). The limit of
500 views per organization is checked in the insert statement (`INSERT … SELECT … WHERE count < 500`; concurrent creates
may exceed it by a few rows). Audit: `saved_view.{create,update,delete}`.

## Sizing and operations

- Connections: every ingest and api pod opens a pool of at most `OPENLOG_POSTGRES_MAX_CONNS` (default 10).
  Ingest queries PostgreSQL only on license-key cache misses, so its steady-state load is about
  `(distinct keys × pods) / OPENLOG_AUTH_CACHE_TTL` indexed lookups.
- The API performs 2–3 indexed queries per authenticated request (session or API key, membership).
- Backups: PostgreSQL is the system of record for access control; back it up (CloudNativePG: configure
  `backup` on the `Cluster`). Losing it does not lose telemetry, but all keys and users must be recreated.
