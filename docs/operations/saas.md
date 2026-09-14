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
