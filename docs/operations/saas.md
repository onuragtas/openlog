# Operating openlog as SaaS

How to run openlog for many paying organizations: plans, quotas, usage, per-tenant retention and the billing
integration. The contract is [docs/contracts/usage.md](../contracts/usage.md); decisions D-079–D-081.

Self-hosted installations need none of this: usage metering is always on and Settings → Usage & plan shows the
numbers against the built-in `unlimited` plan; nothing is ever rejected.

## 1. Checklist

| Setting | Why |
|---|---|
| `OPENLOG_AUTH_MODE=postgres` | required by SaaS mode |
| `OPENLOG_SAAS_MODE=true` on **ingest, api and migrate** (allinone: once) | hard enforcement, per-tenant retention default |
| `OPENLOG_PLANS_FILE=/etc/openlog/plans.json` (or `OPENLOG_PLANS`) on **api and migrate** | plan catalog; migrate needs it for table TTLs (§4) |
| `OPENLOG_DEFAULT_PLAN=free` | plan of new sign-ups |
| `OPENLOG_SUPERADMIN_EMAILS=ops@example.com,…` on api | who may assign plans |
| `OPENLOG_SMTP_*`, `OPENLOG_PUBLIC_URL` on api | owner e-mails at 80 % / 100 % |
| `OPENLOG_SIGNUP_ENABLED=true`, `OPENLOG_KEY_HASH_SECRET`, sign-up protection | multi-tenant sign-up (config.md) |
| `OPENLOG_CLICKHOUSE_READ_USER` | tenant queries without write access (D-047) |
| `OPENLOG_BILLING_PROVIDER` | `none` until a provider is implemented (§6) |
| `OPENLOG_SAAS_SIGNUP_TRIAL_PLAN=pro` (plan with `trial_days`) on api | automatic trials of new sign-ups (§9) |
| `OPENLOG_SAAS_AUTO_SUSPEND` (default `false`) on api | only if flagged organizations must be suspended without a human (§10) |

Apply PostgreSQL migration `0065_saas_operator` before rolling out the operator console, suspension and host limits.

Apply ClickHouse schema `0050_usage` and PostgreSQL migration `0035_usage_plans` with openlog-migrate before rolling out
the new binaries (both are expand-only; older binaries ignore them).

## 2. Plans

Write the catalog as JSON (format and fields: usage.md §4.1). Keep prices in the billing provider; openlog only needs
limits and `billing.plan_ref`.

```json
{
  "default": "free",
  "plans": [
    {"id": "free", "name": "Free",
     "limits": {"ingest_gb_month": 100, "hosts": 5, "users": 3,
                "retention_days": {"logs": 7, "traces": 7, "metrics": 30},
                "ingest_bytes_per_second": 2097152, "ingest_burst_bytes": 20971520},
     "enforcement": {"hard_ingest_limit": true, "grace_percent": 10}},
    {"id": "pro", "name": "Pro",
     "limits": {"ingest_gb_month": 1000, "retention_days": {"logs": 30, "traces": 14, "metrics": 90},
                "query": {"max_memory_usage": 8589934592}},
     "enforcement": {"hard_ingest_limit": true, "grace_percent": 20},
     "billing": {"plan_ref": "price_pro_monthly"}},
    {"id": "enterprise", "name": "Enterprise", "limits": {"retention_days": {"logs": 90}}}
  ]
}
```

The catalog is read at startup: change it by rolling api (and ingest does not need it — ingest reads the evaluated
status from PostgreSQL). Removing a plan id that organizations use makes them fall back to the default plan.

### Assigning plans and overrides

Superadmins use Settings → Usage & plan → "Plan assignment (operator)" while switched into the organization, or the API:

```sh
curl -s -b cookies.txt -H 'X-CSRF-Token: …' -H 'Content-Type: application/json' \
  -X PUT https://openlog.example.com/api/v1/admin/orgs/acme/plan \
  -d '{"plan_id":"pro","overrides":{"ingest_gb_month":2500,"hosts":400},"note":"contract #42"}'
```

`{org}` is the organization id or tenant id. Every change is in the organization's audit log (`plan.update`).

## 3. Quotas and enforcement

- The api leader evaluates every organization every minute and stores the result in `tenant_quota_status`.
- Ingest pods reload that table every 30 s. Over `ingest_gb_month × (1 + grace_percent/100)` a plan with
  `hard_ingest_limit` gets `429` / `RESOURCE_EXHAUSTED` with `Retry-After: 300`; agents keep retrying and resume once
  the plan is upgraded (within ~1.5 minutes) or the month rolls over.
- Rate limits are per pod (`rate / live ingest pods`). Check `openlog_quota_ingest_pods` matches your ingest replica
  count; with a load balancer that does not spread connections evenly (long-lived gRPC connections) set
  `OPENLOG_QUOTA_INGEST_PODS` explicitly or use L7 gRPC balancing.
- Ingest fails open when PostgreSQL is unreachable for more than 15 minutes.

### Runbook: unblock a tenant immediately

1. Raise the limit: `PUT /api/v1/admin/orgs/<tenant>/plan` with `{"plan_id": "<current>", "overrides": {"ingest_gb_month": <higher>}}`
   (or `"hard_ingest_limit": false`).
2. Wait for the next evaluation (`OPENLOG_USAGE_EVALUATION_INTERVAL`, 1m) and the ingest refresh (30s).
3. Verify `GET /api/v1/usage/status` as the organization shows `"ingest_blocked": false` and
   `openlog_quota_ingest_rejected_total{reason="quota_exceeded"}` stops increasing.

### Runbook: find the tenants being rejected

```sql
-- PostgreSQL
SELECT tenant_id, plan_id, level, ingest_bytes / 1073741824.0 AS gib, ingest_limit_bytes / 1073741824.0 AS limit_gib, evaluated_at
FROM tenant_quota_status WHERE ingest_blocked OR level <> 'ok' ORDER BY level DESC, ingest_bytes DESC;
```

## 4. Per-tenant retention

With `OPENLOG_QUOTA_RETENTION_ENABLED=true` (default in SaaS mode):

1. openlog-migrate raises the raw tables' delete TTL to the longest plan retention per signal (logs, traces, metrics).
   Run `openlog-migrate -plan` to see the `ALTER TABLE … MODIFY TTL` statements before applying; `openlog-admin storage status`
   shows pending TTL changes. **Plan the disk:** the longest plan retention now applies to every tenant's raw data until
   the deletion job removes it.
2. The api leader submits partition-scoped `ALTER TABLE … DELETE IN PARTITION ID … WHERE tenant_id IN (…)` mutations for
   tenants with shorter retention (at most `OPENLOG_QUOTA_RETENTION_MAX_MUTATIONS` per hourly run).

Watch the mutations:

```sql
SELECT table, command, create_time, parts_to_do, is_done, latest_fail_reason
FROM clusterAllReplicas('openlog', system.mutations) WHERE database = 'openlog' AND NOT is_done;
```

A long mutation backlog means too many distinct retentions or a downgrade catch-up; raise the per-run limit only if
merges keep up. Disabling the feature later leaves the widened TTL in place until migrate runs without it.

## 5. Usage data

- Metering tables keep 400 days (`usage_*` in ClickHouse, usage.md §2).
- Query compute comes from `system.query_log`: keep `query_log` enabled on every server with at least 4 hours of
  retention, and give the writer user read access to it.
- Exports: `GET /api/v1/usage/export?period=2026-09&format=csv` (organization admins/owners) is the invoice-period
  record to reconcile provider invoices.

## 6. Billing provider

The provider is not chosen yet. `internal/billing` defines what openlog needs; `OPENLOG_BILLING_PROVIDER=noop` runs the
daily push and its bookkeeping (`billing_usage_pushes`) without an external call.

### Adding a provider (Stripe, iyzico, Paddle, …)

1. Implement `billing.Provider` in `internal/billing/<provider>` (HTTP client with the provider's secret key from
   `OPENLOG_BILLING_<PROVIDER>_*` variables — never in the repository):
   - `EnsureCustomer`: create/find the customer for the organization (store its id with `PUT /api/v1/admin/orgs/{org}/plan`
     `billing.customer_id`, or call it from a sign-up/checkout flow).
   - `PushUsage`: report `ingest_gb`, `hosts`, `users` for a day and pass `UsageRecord.IdempotencyKey` as the provider's
     idempotency key (Stripe: meter event `identifier`; Paddle: transaction/adjustment custom data; iyzico has no metered
     billing — compute the invoice amount from the export instead and return `ErrNotSupported`).
   - `ParseWebhook`: verify the signature header with the webhook secret and map provider events to
     `subscription.updated` (with `PlanRef` = the price/plan id), `subscription.canceled`, `payment.failed`, `invoice.paid`.
2. Register the name in `billing.New` and in `config.validateUsage` (`OPENLOG_BILLING_PROVIDER`).
3. Put the provider's price/plan ids into the catalog as `billing.plan_ref`.
4. Point the provider's webhook at `https://<api>/api/v1/billing/webhooks/<provider>`.
5. Add a checkout/customer-portal link to the UI (not implemented; Settings → Usage & plan is the natural place).

## 7. Metrics to alert on

| Metric | Alert when |
|---|---|
| `openlog_quota_evaluations_total{result="error"}` | increasing for 10m (limits are stale; blocked tenants stay blocked for up to an hour) |
| `openlog_quota_ingest_rejected_total` | sudden jumps (a large customer hit its quota) |
| `openlog_quota_ingest_pods` | differs from the ingest replica count |
| `openlog_processor_rows_inserted_total{table="usage_ingest_1h"}` | zero while other tables insert (metering broken) |
| `openlog_saas_ingest_rejected_total{reason="org_suspended"\|"host_limit"}` | host limit rejections of a customer that should have room (stale host sync) |

## 8. Operator console (D-105)

Superadmins (`OPENLOG_SUPERADMIN_EMAILS`, signed in with a verified address) see **Operator** in the navigation (`/operator`);
nobody else does, and the API answers `403`. The console has three tabs:

- **Organizations**: search by name, tenant id, organization id or member e-mail; filter by effective plan and state
  (active, trial, suspended, flagged); sort by creation, name, ingest this period, members or last ingest. Numbers come
  from PostgreSQL (latest quota evaluation, license key `last_used_at`), so the list stays fast with many tenants.
- **Organization detail**: members (role, last login, verified), plan and overrides (the existing plan editor), trial
  (the plan picker lists only plans with `trial_days`, from `GET /api/v1/plans`), organization deletion (§12), usage
  chart of the current period, quota status, the 50 newest audit events, SSO connections (protocol, enabled, enforced,
  JIT, last test), counts of license keys/API keys/SCIM tokens and verified domains, flags and support sessions. No
  secret, key value, SSO configuration or IP address is shown.
- **Flagged**: open abuse flags (§10) with dismiss / actioned (note required) and links to the organization.

Every action asks for confirmation and a reason; the reason is stored in the organization's audit log, where the
customer's admins can read it:

| Action | Effect |
|---|---|
| Suspend | ingest `403 org_suspended` within `OPENLOG_QUOTA_REFRESH_INTERVAL`; the UI shows a banner and turns read-only (create/edit/delete actions of dashboards, alerts, fleet, integrations and settings are hidden or disabled with an explanation; a mutation answered `403 org_suspended` refetches the state at once); members' changes are refused (`403 org_suspended`) but they can sign in, query and export/delete their data |
| Unsuspend | back to normal immediately on the api pod that handled it, within 15 s on the others |
| Change plan / overrides | `PUT /api/v1/admin/orgs/{org}/plan` (§2) |
| Start / extend trial | §9 |
| Reset quota notifications | the 80 %/100 % e-mails of the current period can be sent again (e.g. after raising a limit) |
| Force logout | every active session of the members is revoked (their other organizations too); API keys keep working — revoke them as the organization if needed |
| Resend verification | a new verification e-mail to each owner without a confirmed address |

### Runbook: suspend an abusive organization

1. Open the organization from **Flagged** or search for it; check usage, recent audit and flags.
2. **Suspend** with a reason that names the ticket. Ingest stops within ~30 s; confirm
   `openlog_saas_ingest_rejected_total{reason="org_suspended"}` increases.
3. If credentials may be compromised, **Force logout**; ask the owner to rotate license keys.
4. Mark the flag **actioned** with the ticket number.

## 9. Trials (D-106)

Add `trial_days` (and optionally `trial_fallback_plan`) to plans that can be trialled:

```json
{"id": "pro", "name": "Pro", "trial_days": 14, "trial_fallback_plan": "free", "limits": {"ingest_gb_month": 1000}}
```

- **Operator**: organization detail → *Start trial* (plan, days) assigns the plan and records the end; *Extend trial*
  moves the end (days are added). Audit `plan.update`, `trial.start`, `trial.extend`.
- **Sign-up**: with `OPENLOG_SAAS_SIGNUP_TRIAL_PLAN=pro`, organizations created after the api first ran with the setting and
  without a plan assignment start a trial within `OPENLOG_SAAS_LIFECYCLE_INTERVAL` (they are on the default plan until
  then). Existing organizations are never put on a trial automatically.
- **E-mails**: owners get a TR/EN reminder `OPENLOG_SAAS_TRIAL_NOTIFY_DAYS` (7, 3, 1) days before the end and a notice
  when it ended (needs `OPENLOG_SMTP_*`; without SMTP trials still end). A reminder that could not be sent is retried on
  the next run; extending a trial re-arms the reminders.
- **End**: the leader sets `trial_ended_at` and assigns the fallback plan (overrides and billing ids stay) unless an
  operator assigned another plan during the trial. Audit `trial.end` (actor `system:trial`). The Settings → Usage & plan
  banner shows the running trial.

Runbook: *a customer paid during the trial* — assign the paid plan with `PUT /api/v1/admin/orgs/{org}/plan`; the trial
still ends at its date but keeps the assigned plan.

## 10. Abuse detection (D-106)

The api leader evaluates every organization every `OPENLOG_SAAS_ABUSE_INTERVAL` (10 min) and raises flags:

| Kind | Condition (defaults) |
|---|---|
| `ingest_spike` | ingest of the previous or current hour > `OPENLOG_SAAS_ABUSE_INGEST_MULTIPLIER` (10) × `ingest_gb_month / 730`; plans without an ingest limit are skipped |
| `new_org_hosts` | organization younger than `OPENLOG_SAAS_ABUSE_NEW_ORG_DAYS` (7) with more than `OPENLOG_SAAS_ABUSE_NEW_ORG_HOSTS` (50) active hosts |
| `ingest_source_ips` | license keys used from more than `OPENLOG_SAAS_ABUSE_SOURCE_IPS` (200) distinct client addresses within an hour on one ingest instance |

- One open flag per organization and kind; repeated detections update `occurrences`, `details` and `last_seen_at`.
- Client addresses are counted in memory by each ingest pod; only the per-hour count is written (`ingest_source_counts`,
  48 h). Ingest sees the peer address: behind a load balancer that does not preserve client addresses (no PROXY protocol
  / L4 pass-through) the count is the number of balancer addresses and this flag never fires. Counts are per instance, so
  with request-level balancing across N pods the value is a lower bound.
- Nothing is suspended unless `OPENLOG_SAAS_AUTO_SUSPEND=true`; then a **new** flag suspends its organization (audit
  `org.suspend` by `system:abuse-detector`, flag `auto_suspended`). Review auto-suspensions daily: the thresholds are
  coarse (a customer onboarding a large fleet trips `new_org_hosts`).
- Existing sign-up protection (rate limits per IP/e-mail, CAPTCHA, disposable domain blocklist, e-mail verification;
  D-044–D-046) stays the first line; the detector catches what gets through.

## 11. Support access (D-105)

openlog staff never use customer credentials. An **owner** allows support in Settings → Organization → *openlog support
access* for 24 hours or 7 days (audit `support_access.grant`) and can revoke it at any time (`support_access.revoke`,
which also ends open support views).

While access is granted, an operator opens the organization → *Open support view* with a reason (audit
`support.session_start`). The browser switches into a read-only view of the organization with a red banner showing the
organization, the time left and *Exit support view*:

- The operator acts as a **viewer**: dashboards, hosts, logs, traces, alerts etc. are visible; any change is refused
  (`403 support_read_only`), including the viewer's usual self-service actions. Queries and previews work.
- The view is bound to the operator's own session and ends after `OPENLOG_SAAS_SUPPORT_SESSION_TTL` (2 h), when the grant
  expires or is revoked, or when the operator signs out.
- Every page the operator opens (`support.page_view`) and every distinct API request (`support.request`, method and
  path) is written to the organization's audit log, so the customer sees exactly what support looked at.

Security notes for operators: keep `OPENLOG_SUPERADMIN_EMAILS` short, require SSO with MFA for those accounts where
possible, and treat the operator console like production database access.

## 12. Data subject requests (KVKK/GDPR; D-107)

Customers exercise their rights themselves; operators only step in for abuse or when a customer cannot sign in.
Everything below works in every postgres-mode installation (`OPENLOG_DATA_EXPORT_*`, `OPENLOG_ORG_DELETION_GRACE`,
config.md "Data subject requests and status page"). E-mails need `OPENLOG_SMTP_*` and `OPENLOG_PUBLIC_URL`.

### Data portability: exports

| Request | Who | Where | Contents |
|---|---|---|---|
| Organization export | owners | Settings → Organization → *Data export* (`POST /api/v1/data-exports`) | `organization/*.json`: organization, members, invitations, license/API key metadata, dashboards, reports, alert rules, channels (no secrets; masked hints), mutes, incidents, SSO connections/domains/role mappings/SCIM token metadata (no client secrets or keys), plan and limits, audit log; optional telemetry of a time range (logs, traces, metrics) as `telemetry/<signal>/<day>-NNNN.ndjson.gz` |
| Personal export | every user | Settings → Profile → *Your data* (`POST /api/v1/account/data-exports`) | `user/*.json`: profile, memberships, session metadata (IP, user agent, times; no tokens), API keys created, invitations to the address, SCIM identities, SSO sessions, comments, audit entries by or about the user |

- The api leader builds one export at a time: a ZIP archive with `manifest.json` (files, row counts, limits,
  `truncated`, `complete_until` per signal). Telemetry is streamed from ClickHouse day by day with `max_threads=2` and
  `OPENLOG_DATA_EXPORT_ROWS_PER_SECOND`; it stops cleanly at `OPENLOG_DATA_EXPORT_MAX_ROWS` or
  `OPENLOG_DATA_EXPORT_MAX_BYTES` (the customer requests the rest with a shorter range). The range is bounded by
  `OPENLOG_DATA_EXPORT_MAX_RANGE`. One pending export per organization/user, at most 5 per 24 hours.
- Storage: S3 (`OPENLOG_DATA_EXPORT_S3_URL`, or the tiered-storage bucket under `openlog-exports/`) or a local directory
  (`OPENLOG_DATA_EXPORT_LOCAL_PATH`; single api pod or a shared volume). Give the bucket prefix a lifecycle rule a little
  longer than `OPENLOG_DATA_EXPORT_TTL` as a safety net.
- S3 credentials: static keys, or (`OPENLOG_DATA_EXPORT_S3_CREDENTIALS=auto`, the default without keys) an IAM role
  (D-116): EKS IRSA (annotate the api service account with `eks.amazonaws.com/role-arn`), EKS Pod Identity, ECS task
  role or the EC2 instance profile (IMDSv2; on EKS nodes the hop limit must allow pods to reach it). The role needs
  `s3:PutObject`, `s3:GetObject` and `s3:DeleteObject` on the export prefix. Credentials files, SSO and
  `credential_process` are not read.
- The requester gets an e-mail with a download link (`/api/v1/data-exports/download?token=…`, 256-bit random token,
  only its hash is stored) valid until `OPENLOG_DATA_EXPORT_TTL`; owners can also download from the settings page.
  Archives are deleted when they expire. Audit: `data_export.request`, `data_export.download` (organization),
  `user.data_export_request` (installation level).

### Erasure: account deletion

Settings → Profile → *Delete account* (`POST /api/v1/account/delete`): the user types their e-mail address and their
password (users without a password — single sign-on — sign in again and confirm within 10 minutes). Refused while the
user is the only owner of an organization; they add another owner or delete the organization first. In one
transaction the account, its sessions, SSO/SCIM identities, API keys it created, invitations and personal exports are
removed, the address leaves report recipient lists, and audit/incident/comment records keep a random pseudonym
(`deleted-user-<hex>`) instead of name, address and IP (details: postgres.md "Account deletion"). A confirmation
e-mail goes to the old address; a deletion certificate (`subject_type=user`, sha256 of the user id) is recorded.

### Erasure: organization deletion

1. An owner opens Settings → Organization → *Delete organization*, types the organization name and confirms with the
   password (or a recent SSO sign-in). The organization is soft-deleted at once: members lose access, API keys and
   license keys stop working (ingest answers `403` with reason `org_deleted` for the revoked keys, also for 365 days
   after the purge, so agents left running show a clear error; D-115), SCIM tokens are revoked, alerts are no longer evaluated and
   reports no longer sent. Owners get an e-mail.
2. During `OPENLOG_ORG_DELETION_GRACE` (default 7 days) any owner can cancel it under Settings → Profile; keys revoked by
   the deletion work again.
3. After the grace period the api leader deletes the tenant's rows from **every ClickHouse table with a `tenant_id`
   column** (raw signals, rollups, inventories, containers, Kubernetes, `usage_*`) with partition-scoped
   `ALTER TABLE … ON CLUSTER … DELETE IN PARTITION ID … WHERE tenant_id = …` mutations (≤ 50 per run), waits for them,
   re-counts and requires zero rows. Parts on S3 (tiered storage) are rewritten on S3 and the old objects are removed
   with the old parts. Then one PostgreSQL transaction deletes the organization (all rows with its `org_id`), accounts
   that belong to no other organization afterwards (as in account deletion) and records the deletion certificate
   (`subject_type=organization`, sha256 of the tenant id, row counts per table, timestamps, `verified`). Owners get a
   completion e-mail; the deletion row keeps no personal data.

Operators delete abusive organizations with `POST /api/v1/admin/orgs/{org}/deletion` `{"reason", "immediate"?}`
(superadmin; `immediate` skips the grace period; owners are informed but cannot cancel; installation-level audit
`admin.org_deletion_schedule`). `GET /api/v1/admin/org-deletions` shows progress and `last_error`;
`POST /api/v1/admin/org-deletions/{id}/cancel` cancels during the grace period. The operator console's organization
detail has the same controls in *Organization deletion*: schedule (reason required; *Delete immediately* also requires
typing the tenant id), the pending deletion with its purge time, initiator, reason and last error, cancel, earlier
requests and the tenant's deletion certificates (matched by the sha256 of the tenant id).

Watch a running deletion:

```sql
SELECT table, partition_id, command, parts_to_do, is_done, latest_fail_reason
FROM clusterAllReplicas('openlog', system.mutations) WHERE database = 'openlog' AND NOT is_done AND position(command, '<tenant_id>') > 0;
```

### Procedure for requests that arrive by e-mail

1. Verify the requester controls the account or is an owner of the organization (reply from the registered address,
   or have them sign in). Record the request in your ticket system with the date (KVKK: answer within 30 days; GDPR:
   one month).
2. Access/portability: ask them to use the export buttons; if they cannot sign in, sign in as operator only after
   identity verification and use an owner's support access (§11), never their credentials.
3. Erasure of an account: they delete it themselves; if they cannot sign in, disable sign-in and schedule it through a
   remaining owner of their organizations. Erasure of an organization: an owner deletes it, or an operator uses the admin
   endpoint with the ticket reference as `reason`.
4. Send the certificate id and `subject_hash` from `GET /api/v1/admin/deletion-certificates?subject_hash=<sha256>` as
   confirmation.

### What is not deleted immediately, and retention of certificates

- **Kafka**: raw OTLP records stay until `OPENLOG_KAFKA_RETENTION_MS` (default 24 h) — keep the grace period longer than
  the Kafka retention so no in-flight record of the tenant is written after the ClickHouse deletion.
- **Backups** (`pg_dump`, ClickHouse backups, S3 versioning): deleted data remains in backups until they rotate out;
  state the backup retention in your privacy notice and do not restore a deleted tenant from a backup.
- **Detached ClickHouse parts** (manual `DETACH`): not visible to the job; check `system.detached_parts` before
  certifying an unusual cluster.
- **Operator records**: installation-level audit entries (`admin.*`, `user.delete` with the pseudonym) and the
  certificates are kept as proof of compliance; certificates contain only hashes, counts and timestamps. Keep them for
  as long as you must be able to demonstrate the deletion (we recommend the statutory limitation period, e.g. 10 years
  under Turkish commercial law), then delete rows from `deletion_certificates` manually.

## 13. Status page (D-108)

`OPENLOG_STATUS_PAGE_ENABLED` (default: `OPENLOG_SAAS_MODE`) serves the public page `/status` and
`GET /api/v1/status` (no authentication, `Cache-Control: public, max-age=30`, CORS `*`). It shows no tenant data.

- **Components** from the api leader's one-minute self-checks: `ingest` (live ingest/allinone instances in
  `component_heartbeats`), `query_api` (PostgreSQL and ClickHouse reachable), `alerting` (live evaluators; degraded when
  more than 10 % of enabled rules are over 5 minutes late), `processing` (live processors; degraded when the newest
  stored metric is older than 10 minutes, partial outage after 30). A snapshot older than 5 minutes shows `unknown`.
- **Uptime**: 90 days per component from `status_checks_daily` (a day is an outage day when any check reported a
  partial or major outage; uptime = checks without outage / checks).
- **Incidents and maintenance**: superadmins manage them in Settings → Status page (or
  `/api/v1/admin/status/incidents`): create with components, impact and a first message, add timeline updates, resolve.
  Active maintenance windows show the affected components as `maintenance`; open incidents raise the overall status by
  impact (minor → degraded, major → partial outage, critical → major outage). Resolved items stay on the page for 14 days.
- Put the page on a separate host name through your proxy (`status.example.com` → `/status`) and keep a static fallback
  outside the cluster for full outages: the page itself is served by openlog.
