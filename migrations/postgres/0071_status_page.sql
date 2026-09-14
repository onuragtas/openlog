-- openlog:phase expand
-- 0071_status_page: the public status page (GET /api/v1/status, /status; docs/contracts/postgres.md "Status page",
-- D-108): incidents and maintenance windows written by operators, and daily counts of the api leader's self-checks
-- for the 90-day uptime history. No tenant data.
--
-- Mixed versions: older binaries do not use the tables.

CREATE TABLE IF NOT EXISTS status_incidents (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind        text NOT NULL CHECK (kind IN ('incident', 'maintenance')),
    title       text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    -- incident: investigating | identified | monitoring | resolved; maintenance: scheduled | in_progress | completed
    status      text NOT NULL CHECK (status IN ('investigating', 'identified', 'monitoring', 'resolved', 'scheduled', 'in_progress', 'completed')),
    impact      text NOT NULL DEFAULT 'minor' CHECK (impact IN ('none', 'minor', 'major', 'critical')),
    -- ingest, query_api, alerting, processing
    components  text[] NOT NULL DEFAULT '{}',
    starts_at   timestamptz NOT NULL DEFAULT now(),
    -- end of the maintenance window, or when the incident was resolved
    ends_at     timestamptz,
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS status_incidents_starts_idx ON status_incidents (starts_at DESC);

CREATE TABLE IF NOT EXISTS status_incident_updates (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    incident_id  uuid NOT NULL REFERENCES status_incidents (id) ON DELETE CASCADE,
    status       text NOT NULL,
    message      text NOT NULL CHECK (length(message) BETWEEN 1 AND 5000),
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS status_incident_updates_incident_idx ON status_incident_updates (incident_id, created_at);

-- One row per component and UTC day; the api leader adds one check per minute. Rows older than 400 days are pruned.
CREATE TABLE IF NOT EXISTS status_checks_daily (
    component    text NOT NULL CHECK (length(component) BETWEEN 1 AND 64),
    day          date NOT NULL,
    checks       integer NOT NULL DEFAULT 0 CHECK (checks >= 0),
    operational  integer NOT NULL DEFAULT 0 CHECK (operational >= 0),
    degraded     integer NOT NULL DEFAULT 0 CHECK (degraded >= 0),
    outage       integer NOT NULL DEFAULT 0 CHECK (outage >= 0),
    PRIMARY KEY (component, day)
);
