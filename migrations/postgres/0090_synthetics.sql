-- openlog:phase expand
-- 0090_synthetics: scheduled outside-in checks (docs/contracts/api.md "Synthetic monitoring",
-- postgres.md "Synthetic monitoring", D-132). A check is an HTTP request an organization wants openlog to
-- make on schedule; the api leader runs the due ones (internal/synthetics), stores every run in ClickHouse
-- (schema 0093_synthetic_runs) and mirrors it as metric data points, so the existing metric alert rules and
-- dashboards watch a check like any other metric. Nothing about a run is stored here except its last
-- outcome, which is what the list view shows.
--
-- Mixed versions (releases-updates.md §6): older binaries do not use the tables, so checks created here are
-- simply not run until the api pod that holds the leader lock is upgraded.

CREATE TABLE IF NOT EXISTS synthetic_checks (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name              text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    -- Only http exists; the check constraint is where tcp/dns/browser checks are added later.
    type              text NOT NULL DEFAULT 'http' CHECK (type IN ('http')),
    enabled           boolean NOT NULL DEFAULT true,
    url               text NOT NULL CHECK (length(url) BETWEEN 1 AND 2048),
    method            text NOT NULL DEFAULT 'GET'
                      CHECK (method IN ('GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS')),
    -- Request headers as a flat JSON object (at most 20 entries; internal/synthetics validates names/values).
    headers           jsonb NOT NULL DEFAULT '{}'::jsonb,
    body              text NOT NULL DEFAULT '' CHECK (length(body) <= 65536),
    -- A run succeeds only with one of these status codes.
    expected_status   integer[] NOT NULL DEFAULT '{200}'
                      CHECK (array_length(expected_status, 1) BETWEEN 1 AND 10),
    assertion_type    text NOT NULL DEFAULT 'none'
                      CHECK (assertion_type IN ('none', 'contains', 'not_contains', 'json_path')),
    -- json_path only: a dotted path into the decoded body (data.items.0.status).
    assertion_path    text NOT NULL DEFAULT '' CHECK (length(assertion_path) <= 1024),
    assertion_value   text NOT NULL DEFAULT '' CHECK (length(assertion_value) <= 1024),
    timeout_ms        integer NOT NULL DEFAULT 10000 CHECK (timeout_ms BETWEEN 500 AND 60000),
    interval_seconds  integer NOT NULL DEFAULT 300 CHECK (interval_seconds BETWEEN 30 AND 86400),
    -- Built-in location 'local' = the openlog server itself. Further locations need no migration: they are
    -- values in this array plus one schedule row each.
    locations         text[] NOT NULL DEFAULT '{local}'
                      CHECK (array_length(locations, 1) BETWEEN 1 AND 10),
    created_by        uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by        uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    -- A run must finish before the next one is due, so a slow target cannot pile runs up.
    CONSTRAINT synthetic_checks_timeout_fits CHECK (timeout_ms <= interval_seconds * 1000)
);

CREATE INDEX IF NOT EXISTS synthetic_checks_org_name_idx ON synthetic_checks (org_id, lower(name));

-- One row per check and location: when the next run is due, and what the last one did (the list view's
-- current status without a ClickHouse query). The scheduler claims due rows with FOR UPDATE SKIP LOCKED and
-- advances next_run_at in the same statement, so two schedulers never run the same check twice.
CREATE TABLE IF NOT EXISTS synthetic_check_schedule (
    check_id          uuid NOT NULL REFERENCES synthetic_checks (id) ON DELETE CASCADE,
    location          text NOT NULL CHECK (length(location) BETWEEN 1 AND 64),
    next_run_at       timestamptz NOT NULL DEFAULT now(),
    last_run_at       timestamptz,
    -- NULL until the first run.
    last_success      boolean,
    last_status_code  integer NOT NULL DEFAULT 0,
    last_duration_ms  double precision NOT NULL DEFAULT 0,
    last_error_kind   text NOT NULL DEFAULT '',
    last_error        text NOT NULL DEFAULT '',
    PRIMARY KEY (check_id, location)
);

CREATE INDEX IF NOT EXISTS synthetic_check_schedule_due_idx ON synthetic_check_schedule (next_run_at);
