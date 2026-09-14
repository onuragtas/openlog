-- openlog:phase expand
-- 0035_usage_plans: plans, quotas and billing bookkeeping (docs/contracts/usage.md, postgres.md "Usage, plans and
-- billing", D-079..D-081).
--
-- Plan definitions are configuration (OPENLOG_PLANS / OPENLOG_PLANS_FILE), not rows: org_plans only assigns a plan id
-- and per-organization overrides. An organization without a row has OPENLOG_DEFAULT_PLAN.
--
-- Mixed versions: older binaries ignore these tables (no quota enforcement, no usage page).

CREATE TABLE org_plans (
    org_id                  uuid PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    plan_id                 text NOT NULL CHECK (plan_id ~ '^[a-z0-9][a-z0-9_-]{0,62}$'),
    -- Partial limits (quota.Overrides JSON) replacing the plan's values for this organization.
    overrides               jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- Billing provider ids (empty until the organization is connected to a provider).
    billing_provider        text NOT NULL DEFAULT '',
    billing_customer_id     text NOT NULL DEFAULT '',
    billing_subscription_id text NOT NULL DEFAULT '',
    note                    text NOT NULL DEFAULT '' CHECK (length(note) <= 1000),
    updated_by              uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at              timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX org_plans_customer_idx ON org_plans (billing_provider, billing_customer_id) WHERE billing_customer_id <> '';

-- Latest quota evaluation per tenant, written by the api leader (quota.Evaluator) and read by every ingest pod
-- (quota.IngestLimiter) for enforcement. One small row per organization.
CREATE TABLE tenant_quota_status (
    tenant_id             text PRIMARY KEY,
    org_id                uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    plan_id               text NOT NULL,
    period_start          date NOT NULL,
    -- ok | warning (>= first threshold) | exceeded (>= 100 %)
    level                 text NOT NULL DEFAULT 'ok' CHECK (level IN ('ok', 'warning', 'exceeded')),
    ingest_bytes          bigint NOT NULL DEFAULT 0,
    ingest_limit_bytes    bigint NOT NULL DEFAULT 0,
    -- true only in SaaS mode for plans with hard ingest enforcement over limit + grace
    ingest_blocked        boolean NOT NULL DEFAULT false,
    rate_bytes_per_second bigint NOT NULL DEFAULT 0,
    burst_bytes           bigint NOT NULL DEFAULT 0,
    -- [{metric, used, limit, percent, level}]
    metrics               jsonb NOT NULL DEFAULT '[]'::jsonb,
    evaluated_at          timestamptz NOT NULL DEFAULT now()
);

-- One row per (organization, billing period, metric, threshold) whose notification was sent: the insert is the claim,
-- so several leaders or restarts never send the same e-mail twice.
CREATE TABLE usage_notifications (
    org_id       uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    period_start date NOT NULL,
    metric       text NOT NULL,
    threshold    integer NOT NULL CHECK (threshold BETWEEN 1 AND 1000),
    recipients   integer NOT NULL DEFAULT 0,
    sent_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, period_start, metric, threshold)
);

-- Usage records pushed to the billing provider; idempotency_key is also sent to the provider.
CREATE TABLE billing_usage_pushes (
    idempotency_key text PRIMARY KEY,
    org_id          uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    provider        text NOT NULL,
    metric          text NOT NULL,
    day             date NOT NULL,
    quantity        double precision NOT NULL,
    status          text NOT NULL CHECK (status IN ('pending', 'pushed', 'failed')),
    attempts        integer NOT NULL DEFAULT 0,
    error           text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX billing_usage_pushes_org_idx ON billing_usage_pushes (org_id, day);

-- Per-tenant retention deletions submitted to ClickHouse (D-081), so a partition is not mutated again for the same
-- tenant set within a day.
CREATE TABLE usage_retention_mutations (
    table_name     text NOT NULL,
    partition_id   text NOT NULL,
    tenants_hash   text NOT NULL,
    retention_days integer NOT NULL,
    tenants        text[] NOT NULL,
    submitted_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (table_name, partition_id, tenants_hash)
);
