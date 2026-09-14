-- openlog:phase expand
-- 0055_dashboard_reports: scheduled e-mail reports of dashboards (docs/contracts/api.md "Dashboards" › "Scheduled
-- reports", postgres.md, D-087). The api leader sends each schedule at most once per period: a run row is claimed
-- (primary key report_id, period) before any e-mail is sent.

CREATE TABLE IF NOT EXISTS dashboard_reports (
    id           uuid PRIMARY KEY,
    org_id       uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    dashboard_id uuid NOT NULL REFERENCES dashboards (id) ON DELETE CASCADE,
    name         text NOT NULL DEFAULT '' CHECK (length(name) <= 100),
    frequency    text NOT NULL CHECK (frequency IN ('daily', 'weekly')),
    -- 0 = Sunday … 6 = Saturday (weekly)
    weekday      smallint NOT NULL DEFAULT 1 CHECK (weekday BETWEEN 0 AND 6),
    hour         smallint NOT NULL CHECK (hour BETWEEN 0 AND 23),
    minute       smallint NOT NULL DEFAULT 0 CHECK (minute BETWEEN 0 AND 59),
    timezone     text NOT NULL DEFAULT 'UTC' CHECK (length(timezone) BETWEEN 1 AND 64),
    recipients   text[] NOT NULL CHECK (cardinality(recipients) BETWEEN 1 AND 20),
    language     text NOT NULL DEFAULT 'en' CHECK (language IN ('en', 'tr')),
    -- relative range of the report ("24h", "7d")
    time_range   text NOT NULL CHECK (length(time_range) BETWEEN 2 AND 8),
    variables    jsonb NOT NULL DEFAULT '{}',
    enabled      boolean NOT NULL DEFAULT true,
    created_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS dashboard_reports_dashboard ON dashboard_reports (dashboard_id, created_at);
CREATE INDEX IF NOT EXISTS dashboard_reports_enabled ON dashboard_reports (enabled) WHERE enabled;

CREATE TABLE IF NOT EXISTS dashboard_report_runs (
    report_id   uuid NOT NULL REFERENCES dashboard_reports (id) ON DELETE CASCADE,
    -- local date of the scheduled send (YYYY-MM-DD in the schedule's time zone)
    period      text NOT NULL CHECK (length(period) = 10),
    status      text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'sent', 'partial', 'failed', 'skipped')),
    error       text NOT NULL DEFAULT '' CHECK (length(error) <= 1000),
    recipients  integer NOT NULL DEFAULT 0,
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    PRIMARY KEY (report_id, period)
);

CREATE INDEX IF NOT EXISTS dashboard_report_runs_started ON dashboard_report_runs (started_at);
