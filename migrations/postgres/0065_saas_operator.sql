-- openlog:phase expand
-- 0065_saas_operator: SaaS operator console, organization lifecycle (suspension, trials), time-boxed support access,
-- abuse flags and hard host limits (docs/contracts/postgres.md "SaaS operations", docs/operations/saas.md, D-105, D-106).
--
-- Every table is used only with OPENLOG_SAAS_MODE=true (the operator console also works without it, read-mostly).
-- Mixed versions: older binaries ignore these tables (no suspension, host limit or trial enforcement until upgraded).

-- One row per organization that has any SaaS lifecycle state. No row = active, no trial, no support access.
CREATE TABLE org_saas_state (
    org_id                    uuid PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    -- Suspension: ingest answers 403, the api is read-only for members (sessions stay valid for data export).
    suspended_at              timestamptz,
    suspended_by              uuid REFERENCES users (id) ON DELETE SET NULL,
    suspend_reason            text NOT NULL DEFAULT '' CHECK (length(suspend_reason) <= 1000),
    -- Trial of trial_plan_id until trial_ends_at; the leader moves the organization to the plan's fallback afterwards
    -- and sets trial_ended_at.
    trial_plan_id             text NOT NULL DEFAULT '' CHECK (trial_plan_id = '' OR trial_plan_id ~ '^[a-z0-9][a-z0-9_-]{0,62}$'),
    trial_started_at          timestamptz,
    trial_ends_at             timestamptz,
    trial_ended_at            timestamptz,
    -- Support access granted by an owner: operators may open a read-only support view until then.
    support_access_until      timestamptz,
    support_access_granted_by uuid REFERENCES users (id) ON DELETE SET NULL,
    support_access_granted_at timestamptz,
    updated_at                timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX org_saas_state_trial_idx ON org_saas_state (trial_ends_at) WHERE trial_ends_at IS NOT NULL AND trial_ended_at IS NULL;

-- Trial ending e-mails: the insert is the claim (one e-mail per organization, trial end and days-left step).
CREATE TABLE trial_notifications (
    org_id        uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    trial_ends_at timestamptz NOT NULL,
    days_left     integer NOT NULL CHECK (days_left BETWEEN 0 AND 365),
    recipients    integer NOT NULL DEFAULT 0,
    sent_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, trial_ends_at, days_left)
);

-- Read-only support views of an operator into an organization that granted support access. Bound to the operator's
-- session; every request is audited in the organization's audit log.
CREATE TABLE support_sessions (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    operator_user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    operator_email   text NOT NULL,
    auth_session_id  uuid NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    reason           text NOT NULL CHECK (length(reason) BETWEEN 1 AND 1000),
    started_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    ended_at         timestamptz
);
CREATE INDEX support_sessions_org_idx ON support_sessions (org_id, started_at DESC);

-- Organizations flagged by the abuse detector (leader job). At most one open flag per (organization, kind).
CREATE TABLE abuse_flags (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id            uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    kind              text NOT NULL CHECK (kind IN ('ingest_spike', 'new_org_hosts', 'ingest_source_ips')),
    status            text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'dismissed', 'actioned')),
    details           jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurrences       integer NOT NULL DEFAULT 1,
    first_seen_at     timestamptz NOT NULL DEFAULT now(),
    last_seen_at      timestamptz NOT NULL DEFAULT now(),
    auto_suspended    boolean NOT NULL DEFAULT false,
    resolved_by       uuid REFERENCES users (id) ON DELETE SET NULL,
    resolved_by_email text NOT NULL DEFAULT '',
    resolved_at       timestamptz,
    resolution_note   text NOT NULL DEFAULT '' CHECK (length(resolution_note) <= 1000)
);
CREATE UNIQUE INDEX abuse_flags_open_uniq ON abuse_flags (org_id, kind) WHERE status = 'open';
CREATE INDEX abuse_flags_status_idx ON abuse_flags (status, last_seen_at DESC);

-- Hard host limits (SaaS mode): the api leader writes the limit and the active hosts of every tenant whose plan
-- limits hosts; ingest pods load both and reject data of hosts beyond the limit.
CREATE TABLE tenant_host_limits (
    tenant_id    text PRIMARY KEY REFERENCES organizations (tenant_id) ON DELETE CASCADE,
    host_limit   bigint NOT NULL CHECK (host_limit > 0),
    active_hosts bigint NOT NULL DEFAULT 0,
    evaluated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE tenant_known_hosts (
    tenant_id text NOT NULL REFERENCES organizations (tenant_id) ON DELETE CASCADE,
    host_id   text NOT NULL CHECK (length(host_id) BETWEEN 1 AND 512),
    last_seen date NOT NULL,
    PRIMARY KEY (tenant_id, host_id)
);

-- Distinct client addresses per tenant, hour and ingest instance (counts only, no addresses), for the abuse detector.
CREATE TABLE ingest_source_counts (
    tenant_id    text NOT NULL,
    hour         timestamptz NOT NULL,
    instance     text NOT NULL CHECK (length(instance) <= 255),
    distinct_ips integer NOT NULL CHECK (distinct_ips >= 0),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, hour, instance)
);
