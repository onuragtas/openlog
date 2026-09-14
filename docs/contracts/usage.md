# Contract: Usage metering, plans, quotas and billing (v1)

Decisions: D-079 (metering), D-080 (plans and quotas), D-081 (per-tenant retention). Operator guide:
[docs/operations/saas.md](../operations/saas.md). API: [api.md "Usage and plans"](api.md#usage-and-plans), configuration:
[config.md "Usage, plans and billing"](config.md#usage-plans-and-billing), PostgreSQL tables:
[postgres.md "Usage, plans and billing"](postgres.md#usage-plans-and-billing-0035_usage_plans).

## 1. Modes

| | Self-hosted (default) | SaaS (`OPENLOG_SAAS_MODE=true`) |
|---|---|---|
| Metering (§2) | on | on |
| Plans, evaluation, banners, owner e-mails (§4) | on (catalog defaults to one `unlimited` plan) | on |
| Ingest 429 over the monthly quota, ingest rate limits (§4.4) | **off** | on (plans with `hard_ingest_limit`, `ingest_bytes_per_second`) |
| Per-tenant retention (§5) | off (`OPENLOG_QUOTA_RETENTION_ENABLED` can enable it) | on by default |
| Billing provider push/webhooks (§8) | `OPENLOG_BILLING_PROVIDER` | `OPENLOG_BILLING_PROVIDER` |
| Hard host limit (new hosts beyond `limits.hosts` rejected, §4.6) | **off** (warning only) | on |
| Users limit on invitations, SSO JIT and SCIM (§4.6) | **off** (warning only) | on |
| Suspension, trials, abuse flags (§4.7) | **off** (operator console read-only views work) | on |

### 4.6 Hard host and user limits (D-105)

In SaaS mode the hosts and users metrics of §4 are enforced, not only warned about:

- **Hosts**: the window is the one of the hosts metric — hosts that reported today or yesterday (`usage_entities_1d`).
  Hosts in that window keep reporting even above the limit (after a downgrade). A `host.id` not seen yet is admitted
  while the known hosts plus the hosts newly admitted by the ingest pod are below the limit, otherwise its resources are
  rejected (`429 quota_exceeded`, or OTLP partial success when the request also carries admitted hosts; api.md "Ingest:
  suspended organization and host limit"). Data without `host.id` (APM services without a host) is not limited. The
  known host list is refreshed by the api leader every `OPENLOG_SAAS_HOST_SYNC_INTERVAL`; between refreshes several
  ingest pods can each admit up to the remaining room, so the limit can be exceeded by at most (pods − 1) × room for one
  interval. Fail open like §4.4 when PostgreSQL is unreachable for more than 15 minutes.
- **Users**: members (enabled users) plus pending, unexpired invitations may not exceed `limits.users` when an invitation
  is created; accepting an invitation, SSO just-in-time provisioning and SCIM need members < limit. Rejections are
  `403 quota_exceeded`.

### 4.7 Lifecycle (D-106)

Plans may define `trial_days` and `trial_fallback_plan` (§4.1 catalog fields; the fallback defaults to the catalog
default and must differ from the plan). Trials, suspension, support access and abuse flags are described in
[saas.md](../operations/saas.md) §8–§11.

SaaS mode requires `OPENLOG_AUTH_MODE=postgres`. In static auth mode only the usage read endpoints work (no plans in
PostgreSQL, no leader jobs except query collection).

## 2. Metering (D-079)

Everything billable is derived from **stored rows**, not from ingest-side counters, so the numbers are exactly what
the tenant can query and are idempotent under Kafka re-delivery and insert retries.

ClickHouse schema `0050_usage` (all `ReplicatedMergeTree` family, `TTL 400 DAY`, not managed by openlog-migrate's TTL
ownership):

| Table | Engine / key | Filled by | Content |
|---|---|---|---|
| `usage_signals_1h` | SummingMergeTree `(tenant_id, hour, signal, service_name, host_id)` | MVs on `spans_local`, `logs_local`, `metrics_local` | `items` (spans, log records, metric data points), `bytes` (estimated uncompressed row size: `byteSize` of the variable-size columns + fixed-size columns) |
| `usage_entities_1d` | ReplacingMergeTree `(tenant_id, day, kind, entity)` | MVs on the same tables | one row per distinct `host` (host_id), `container` (lower-cased `container.id` of metric data point or resource attributes), `service` (span `service_name`) per day |
| `usage_ingest_1h` | SummingMergeTree `(tenant_id, hour, signal)` | openlog-processor (`internal/processor/usage.go`) | `requests`, `bytes` = uncompressed OTLP protobuf bytes of each Kafka record (the payload ingest produced; OTLP/JSON is converted to protobuf at ingest) in the hour ingest received it |
| `usage_queries_1h` | ReplacingMergeTree(`collected_at`) `(tenant_id, hour, component)` | api leader job `usage-query-collection` | `queries`, `failed`, `read_rows`, `read_bytes`, `cpu_microseconds` (User+System time), `memory_bytes` (sum of per-query peaks) from `system.query_log` |

**Idempotency.**

- The processor inserts every table of a chunk with `insert_deduplication_token = <topic>:<partition>:<first>-<last>:<table>`
  and `deduplicate_blocks_in_dependent_materialized_views = 1` (kafka.md "Consumption semantics"). A re-delivered chunk
  produces the same tokens, the source table drops the block, and the MVs do not see it. `usage_ingest_1h` rows are
  aggregated per chunk and use the same token scheme. Timing-dependent chunk cuts (idle, memory, shutdown) can produce
  different tokens on re-delivery; they double-store the telemetry rows too, so usage stays equal to what is stored.
- Query compute is recomputed for the last `3h` every `15m`; the newest `collected_at` wins, so re-runs and parallel
  collectors never double count.
- Integration test: `internal/usage/integration_test.go` inserts chunks, retries them with identical tokens and
  compares `usage_*` with `count()` / `uniqExact()` on the raw tables.

**Sharding.** MVs write partial aggregates on every shard; every read re-aggregates with `sum` / `uniqExact` through the
Distributed tables. `usage_queries_1h` is sharded by `tenant_id` only, so `FINAL` works per shard.

**Definitions.**

| Metric | Definition |
|---|---|
| ingest bytes | `sum(usage_ingest_1h.bytes)` in the period. With tail sampling (D-075) the traces value is the sampled traces record (what is stored). |
| items per signal | `sum(usage_signals_1h.items)` |
| stored bytes (estimate) | `sum(usage_signals_1h.bytes)` of hours inside the signal's effective retention; the compressed estimate multiplies it by `data_compressed_bytes / data_uncompressed_bytes` of the raw table's active parts |
| active hosts | `max(uniqExact(host) of the day of "to", of the day before)` — what the `hosts` limit is evaluated against |
| hosts / containers / services in a range | `uniqExact(entity)` over the days of the range |
| users | enabled members of the organization (PostgreSQL) at evaluation time |
| query compute | initial queries (`is_initial_query`) with `log_comment.tenant_id` — every api/alert query carries `{"component","tenant_id"}` (D-047). Distributed sub-queries are included in the initial query's counters. Usage endpoints' own queries have no `log_comment` and are not counted. |

## 3. Billing period and projection

A billing period is a UTC calendar month (`YYYY-MM`). The projection of cumulative metrics at period end is
`used + recent_daily_average × remaining_days`, where the average covers the last 7 complete days of the period; without a
complete day it is `used × period_length / elapsed` (not before one hour has elapsed). Past periods are not projected.

## 4. Plans and quotas (D-080)

### 4.1 Catalog

Plans are configuration (`OPENLOG_PLANS` inline JSON or `OPENLOG_PLANS_FILE`), never code; prices live in the billing
provider. Unknown fields are rejected. `0` or an absent limit means unlimited; an absent retention keeps the table
default.

```json
{
  "default": "free",
  "plans": [
    {
      "id": "free", "name": "Free", "description": "Evaluation",
      "limits": {
        "ingest_gb_month": 100, "hosts": 5, "users": 3,
        "retention_days": { "logs": 7, "traces": 7, "metrics": 30 },
        "query": { "max_memory_usage": 2147483648, "max_rows_to_read": 500000000 },
        "ingest_bytes_per_second": 2097152, "ingest_burst_bytes": 20971520
      },
      "enforcement": { "hard_ingest_limit": true, "grace_percent": 10 },
      "billing": { "plan_ref": "price_free" }
    },
    { "id": "pro", "name": "Pro", "limits": { "ingest_gb_month": 1000, "retention_days": { "logs": 30 } }, "billing": { "plan_ref": "price_pro" } },
    { "id": "enterprise", "name": "Enterprise", "limits": {} }
  ]
}
```

- `ingest_gb_month` is GiB (2^30 bytes) of ingest bytes (§2) per billing period.
- `billing` is opaque to openlog except `plan_ref`, which maps provider subscriptions to plans (§8).
- Without a catalog the only plan is `unlimited`. `OPENLOG_DEFAULT_PLAN` overrides `default`; without both the first
  plan is the default. An organization without an `org_plans` row has the default plan.

### 4.2 Per-organization overrides

`org_plans.overrides` (set by superadmins, §7) replaces individual fields: `ingest_gb_month`, `hosts`, `users`,
`retention_days.<signal>`, `query`, `ingest_bytes_per_second`, `ingest_burst_bytes`, `hard_ingest_limit`,
`grace_percent`. A retention override may not exceed the table retention of §5 (the API rejects it).

### 4.3 Evaluation

The api leader job `usage-quota-evaluation` (every `OPENLOG_USAGE_EVALUATION_INTERVAL`, default 1m) evaluates every
organization and upserts `tenant_quota_status`:

| Metric | Used | Limit |
|---|---|---|
| `ingest_bytes` | period ingest bytes | `ingest_gb_month × 2^30` |
| `hosts` | active hosts | `hosts` |
| `users` | members | `users` |

`percent = used / limit × 100`; level `exceeded` at ≥ 100 %, `warning` at ≥ the lowest configured threshold below 100
(`OPENLOG_USAGE_NOTIFY_THRESHOLDS`, default `80,100`), else `ok`. The organization's level is its worst metric's.
`ingest_blocked` is true only in SaaS mode for plans with `hard_ingest_limit` once
`used ≥ limit + round(limit × grace_percent / 100)`.

**Soft enforcement** (all modes): UI banner (`GET /api/v1/usage/status`), Settings → Usage & plan, and e-mail to the
organization's owners when a threshold is crossed (§6).

### 4.4 Hard enforcement in ingest (SaaS mode)

Every ingest pod loads `tenant_quota_status` rows (evaluated within the last hour, blocked or rate limited) every
`OPENLOG_QUOTA_REFRESH_INTERVAL` (30s) and checks each authenticated export request after decoding (size = uncompressed
protobuf bytes; OTLP/JSON: its protobuf size):

| Case | HTTP | gRPC | Retry hint | Body message |
|---|---|---|---|---|
| monthly quota used up (`ingest_blocked`) | `429` | `RESOURCE_EXHAUSTED` | `Retry-After` / `RetryInfo` = `OPENLOG_QUOTA_BLOCKED_RETRY_AFTER` (5m) | `monthly ingest quota of plan "free" is used up (110.00 of 100.00 GiB); upgrade the plan or wait for the next billing period` |
| rate limit | `429` | `RESOURCE_EXHAUSTED` | time until the bucket admits a request (≥ 1 s) | `ingest rate limit of plan "free" exceeded (2097152 bytes/s); retry after 3 s` |

This is the tenant-limit case of D-014 (`503`/`UNAVAILABLE` stays reserved for backend outages). The HTTP body is a
`google.rpc.Status` in the request's encoding. Unauthenticated requests are rejected before the limiter.

**Rate limiting without a new dependency.** Each pod runs a token bucket per tenant with
`rate = ingest_bytes_per_second / pods` and `burst = ingest_burst_bytes / pods` (burst defaults to one second of rate).
`pods` is the number of live `openlog-ingest` and `openlog-allinone` instances in `component_heartbeats` (seen within
90 s), or `OPENLOG_QUOTA_INGEST_PODS` when set. A request is admitted while the bucket holds any tokens and takes its
full size (the bucket can go into debt), so requests larger than the burst are never starved and the long-run rate stays
bounded. The per-pod split is approximate: with uneven load balancing a tenant is limited below its rate on busy pods
(documented trade-off versus a shared Redis counter).

**Failure behaviour.** Ingest fails open: before the first successful load, or when PostgreSQL was unreachable for more
than 15 minutes, every request is admitted. Evaluation stops when ClickHouse is unavailable (status rows age out after an
hour and stop being enforced).

### 4.5 Query limits

ClickHouse `max_memory_usage`, `max_rows_to_read` and `max_bytes_to_read` of an organization's api queries and alert
evaluations are resolved per setting from four layers, lowest to highest:

| # | Layer | Set by | Value semantics |
|---|---|---|---|
| 1 | `OPENLOG_QUERY_MAX_*` defaults | environment | `0` = openlog does not set it |
| 2 | plan `limits.query` (with `org_plans.overrides.query`) | plan catalog / superadmin | only values `> 0` replace layer 1 |
| 3 | organization setting (`org_query_limits`, Settings → Usage & plan → Query limits, `PUT /api/v1/usage/query-limits`) | owner (self-hosted) or superadmin (SaaS mode) | a set value (including `0`) replaces layers 1–2; unset inherits |
| 4 | `OPENLOG_QUERY_TENANT_LIMITS` entry of the tenant | environment | replaces all three settings (the operator's last word) |

Who may change layer 3: superadmins always; organization owners (session users) only without `OPENLOG_SAAS_MODE`, because
in SaaS mode the limits are part of the plan an operator sells (D-080). Every change writes audit action
`query_limits.update` / `query_limits.delete`. Every api and alert pod caches layers 2–3 for all tenants and reloads them
every 30 s (`quota.QueryLimitsResolver`; the pod that handled the change reloads at once), so queries never read
PostgreSQL; while PostgreSQL is unreachable the last loaded layers stay in use (before the first load: layers 1 and 4).

## 5. Per-tenant retention (D-081)

Options evaluated:

1. **Partition by (retention tier, day)** — a new partition key means re-creating every raw table; plan changes move
   tenants between tiers only for new data.
2. **Row TTL with a tenant-dependent column** (`TTL ts + toIntervalDay(retention_days)`) — the retention is stamped at
   insert time (downgrades/upgrades do not apply to stored data), and with mixed tenants per daily partition parts expire
   only via TTL merges that rewrite parts (`ttl_only_drop_parts` cannot be used).
3. **Table TTL = longest retention + daily partition-scoped deletes** — chosen.

Mechanics:

- With `OPENLOG_QUOTA_RETENTION_ENABLED`, openlog-migrate sets the delete TTL of the `logs`, `traces` (`spans_local`,
  `trace_index_local`) and `metrics` (`metrics_local`) classes to the longest effective plan retention, where a plan without a
  retention keeps the schema default (logs 14, traces 7, metrics 30 days). This goes through the TTL ownership of D-067
  (`migrate.TTLOptions.ClassRetentionDays`); tiered storage moves are unaffected.
- The api leader job `usage-tenant-retention` (hourly) groups tenants by effective retention `N` per signal (only
  `N` shorter than the table TTL). A daily partition `D` of a table is due for a group once `D + N + 1 days ≤ today`
  and while the table TTL has not dropped it. For each due (table, partition, group) with rows of those tenants it runs
  `ALTER TABLE openlog.<table>_local ON CLUSTER '<cluster>' DELETE IN PARTITION ID '<YYYYMMDD>' WHERE tenant_id IN (…)`
  and records it in `usage_retention_mutations` (not re-submitted for the same tenant set within 24 h), at most
  `OPENLOG_QUOTA_RETENTION_MAX_MUTATIONS` per run, oldest partition first.
- Steady state rewrites one daily partition per (table, distinct retention) per day. A downgrade catches up over several
  runs.
- Aggregates (`metrics_1m`, APM tables) keep their global retention. Rows with timestamps far in the past that arrive after
  their partition was processed stay until the table TTL.

## 6. Notifications

For every organization and metric the evaluator e-mails the enabled owners once per (billing period, metric, threshold):
the claim is the `usage_notifications` insert, released again if no e-mail could be sent. When a higher threshold is
crossed first, lower ones are claimed silently (no 80 % mail after the 100 % mail). E-mail uses `OPENLOG_SMTP_*`; without
SMTP only banners show. The text links to `<OPENLOG_PUBLIC_URL>/settings/usage`.

## 7. Superadmins

`OPENLOG_SUPERADMIN_EMAILS` lists the SaaS operators. A superadmin is a session user with a verified e-mail address in
the list; membership in the organization is not required. Only superadmins can read or change plan assignments of any
organization (`GET/PUT /api/v1/admin/orgs/{org}/plan`, audit action `plan.update`). API keys never qualify.

## 8. Billing integration

`internal/billing` is provider-agnostic; openlog meters and enforces itself, the provider owns customers,
subscriptions, invoices and payments.

```go
type Provider interface {
    Name() string
    EnsureCustomer(ctx, Customer) (customerID string, err error)
    GetSubscription(ctx, customerID string) (Subscription, error)
    PushUsage(ctx, UsageRecord) error            // idempotent on UsageRecord.IdempotencyKey
    ParseWebhook(payload []byte, header http.Header) (Event, error) // verifies the signature
}
```

- `OPENLOG_BILLING_PROVIDER=none` (default) disables pushes and webhooks; `noop` accepts everything (exercises the job
  and bookkeeping). No real provider is implemented yet (the provider is not chosen).
- **Daily push** (api leader job `billing-usage-push`, at `OPENLOG_BILLING_PUSH_AT` UTC, plus the 3 previous days on start
  and every run): for organizations with `billing_provider = <provider>` and a customer id, one record per metric per
  day: `ingest_gb` (GiB ingested that day), `hosts` (distinct hosts that day), `users` (members at push time). Idempotency
  key `openlog:<org_id>:<YYYY-MM-DD>:<metric>`; `billing_usage_pushes` claims each key (pending → pushed/failed, at most 5
  attempts, stale pending claims taken over after 15 minutes).
- **Webhooks** `POST /api/v1/billing/webhooks/{provider}` (public; the provider verifies the signature): normalized
  `subscription.updated` assigns the plan whose `billing.plan_ref` equals the subscription's plan reference;
  `subscription.canceled` returns the organization to the default plan; other events are accepted and ignored.
  Overrides are kept; changes are audited with actor `billing:<provider>`.

## 9. Export

`GET /api/v1/usage/export?period=YYYY-MM&format=csv|json` (admin, owner). CSV is long format:

```
tenant_id,period,day,metric,value
acme,2026-09,2026-09-01,traces.items,120034
acme,2026-09,2026-09-01,traces.bytes,98123456
acme,2026-09,2026-09-01,traces.ingest_bytes,51234567
acme,2026-09,2026-09-01,traces.ingest_requests,4521
…
acme,2026-09,2026-09-01,ingest_bytes,151234567
acme,2026-09,2026-09-01,hosts,12
acme,2026-09,2026-09-01,containers,40
acme,2026-09,2026-09-01,services,7
acme,2026-09,2026-09-01,queries,321
acme,2026-09,2026-09-01,query_failed,2
acme,2026-09,2026-09-01,query_read_rows,123456789
acme,2026-09,2026-09-01,query_read_bytes,4567890123
acme,2026-09,2026-09-01,query_cpu_seconds,12.5
```

JSON contains the organization, plan id, period, totals and the days of `GET /api/v1/usage/daily`.

## 10. Metrics

| Metric | Component |
|---|---|
| `openlog_quota_ingest_rejected_total{reason=quota_exceeded\|rate_limited}` | ingest |
| `openlog_quota_ingest_pods` | ingest |
| `openlog_quota_evaluations_total{result}` | api (leader) |
| `openlog_processor_rows_inserted_total{table="usage_ingest_1h"}` | processor |
