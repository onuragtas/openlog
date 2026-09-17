-- openlog:phase expand
-- 0092_cloud_connections: managed cloud service metrics (docs/contracts/api.md "Cloud connections",
-- postgres.md "Cloud connections", D-135). A connection is one cloud account an organization lets openlog
-- read metrics from; the api leader polls the due ones (internal/cloudconnect) and writes the data points
-- through the same path as agent metrics, so the Metrics Explorer, alert rules and dashboards see a managed
-- database like any other source. No telemetry is stored here: only the definition, the schedule and the
-- last runs (the operator-visible status).
--
-- Mixed versions (releases-updates.md §6): older binaries do not use the tables, so a connection created
-- here is simply not polled until the api pod that holds the leader lock is upgraded.

CREATE TABLE IF NOT EXISTS cloud_connections (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name                  text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    provider              text NOT NULL CHECK (provider IN ('aws', 'azure', 'gcp')),
    -- How data reaches openlog. Only 'poll' exists; 'push' is reserved for provider-side delivery
    -- (Kinesis Firehose, Event Hubs, Pub/Sub), which adds rows here instead of a new table: a pushing
    -- connection keeps its credentials, scopes and services and simply has no schedule rows.
    ingest_mode           text NOT NULL DEFAULT 'poll' CHECK (ingest_mode IN ('poll', 'push')),
    enabled               boolean NOT NULL DEFAULT true,
    -- Credentials as one encrypted JSON document (the fields differ per provider): 'ol1:<key id>:<AES-256-GCM>'
    -- with OPENLOG_SECRETS_KEY, AAD org_id/id, exactly like integration_settings.password_enc. Never returned
    -- by the API, never logged, never part of an audit detail.
    credentials_enc       text NOT NULL DEFAULT '',
    -- Key id of credentials_enc, so an operator can see which rows still use a retired key.
    credentials_key_id    text NOT NULL DEFAULT '',
    -- What to cover: AWS regions, Azure subscriptions, GCP projects. One generic column, because the
    -- scheduler treats them the same: one schedule row per scope, claimed and run on its own, so a region
    -- that fails or throttles never stops the others.
    scopes                text[] NOT NULL DEFAULT '{}'
                          CHECK (array_length(scopes, 1) BETWEEN 1 AND 50),
    -- Which managed services to collect ('rds', 's3', 'lambda', 'azure_sql', 'cloud_sql', …). Empty is
    -- rejected by the API; the known values are a list in internal/cloudconnect, not a CHECK constraint, so
    -- a new service needs no migration.
    services              text[] NOT NULL DEFAULT '{}'
                          CHECK (array_length(services, 1) BETWEEN 1 AND 50),
    poll_interval_seconds integer NOT NULL DEFAULT 300 CHECK (poll_interval_seconds BETWEEN 60 AND 86400),
    -- Guardrails per poll and scope: these APIs cost money per request and rate-limit hard, so a
    -- misconfigured connection must not be able to spend without bound.
    max_metrics_per_poll  integer NOT NULL DEFAULT 5000 CHECK (max_metrics_per_poll BETWEEN 100 AND 200000),
    max_api_calls_per_poll integer NOT NULL DEFAULT 200 CHECK (max_api_calls_per_poll BETWEEN 1 AND 5000),
    created_by            uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by            uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS cloud_connections_org_name_idx ON cloud_connections (org_id, lower(name));

-- One row per connection and scope (region/subscription/project): when the next poll is due and what the
-- last one did. The api leader claims due rows with FOR UPDATE ... SKIP LOCKED and advances next_run_at in
-- the same statement, so two schedulers never poll the same scope twice per interval. This is also what the
-- connection list shows without reading the run history.
CREATE TABLE IF NOT EXISTS cloud_connection_schedule (
    connection_id      uuid NOT NULL REFERENCES cloud_connections (id) ON DELETE CASCADE,
    scope              text NOT NULL CHECK (length(scope) BETWEEN 1 AND 200),
    next_run_at        timestamptz NOT NULL DEFAULT now(),
    last_run_at        timestamptz,
    -- NULL until the first poll; then 'ok', 'partial' (some services or pages failed, or a cap was hit) or
    -- 'error' (nothing was collected).
    last_status        text CHECK (last_status IN ('ok', 'partial', 'error')),
    last_error         text NOT NULL DEFAULT '',
    last_metrics       integer NOT NULL DEFAULT 0,
    last_api_calls     integer NOT NULL DEFAULT 0,
    last_duration_ms   double precision NOT NULL DEFAULT 0,
    -- Consecutive failures of this scope; the scheduler backs the next run off exponentially, so a broken
    -- region stops costing a request every interval.
    consecutive_errors integer NOT NULL DEFAULT 0,
    PRIMARY KEY (connection_id, scope)
);

CREATE INDEX IF NOT EXISTS cloud_connection_schedule_due_idx ON cloud_connection_schedule (next_run_at);

-- Recent polls, per connection and scope: the honest history behind the status badge, including what failed
-- and whether a guardrail stopped the run. Bounded per connection by the API (the newest rows are kept), so
-- the table stays small without a retention job; the metrics themselves live in ClickHouse.
CREATE TABLE IF NOT EXISTS cloud_collection_runs (
    id             bigserial PRIMARY KEY,
    connection_id  uuid NOT NULL REFERENCES cloud_connections (id) ON DELETE CASCADE,
    scope          text NOT NULL,
    started_at     timestamptz NOT NULL DEFAULT now(),
    duration_ms    double precision NOT NULL DEFAULT 0,
    status         text NOT NULL CHECK (status IN ('ok', 'partial', 'error')),
    -- Data points written to ClickHouse, provider API calls made, and how often the provider throttled us.
    metrics        integer NOT NULL DEFAULT 0,
    api_calls      integer NOT NULL DEFAULT 0,
    throttled      integer NOT NULL DEFAULT 0,
    -- '' when the run succeeded; otherwise the failure, already truncated by internal/cloudconnect. Never
    -- contains credentials: the provider clients build these messages from the status and the API error code.
    error          text NOT NULL DEFAULT '',
    -- Per-service outcome of this run as a JSON array [{"service","metrics","error"}], so the detail view can
    -- say which service failed without one row per service.
    services       jsonb NOT NULL DEFAULT '[]'::jsonb
);

CREATE INDEX IF NOT EXISTS cloud_collection_runs_conn_idx ON cloud_collection_runs (connection_id, started_at DESC);
