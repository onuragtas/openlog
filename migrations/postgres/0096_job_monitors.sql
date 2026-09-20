-- openlog:phase expand
-- 0096_job_monitors: cron and heartbeat monitoring (docs/contracts/api.md "Job monitoring", postgres.md,
-- D-141). A monitor is a promise about a schedule: the job pings openlog when it runs, and the api leader
-- concludes a missed run when the ping does not arrive within the grace period. The history of runs lives
-- in ClickHouse (schema 0099_job_runs); this table keeps the definition and the current state, which is
-- what the list view shows without a ClickHouse query.
--
-- Mixed versions (releases-updates.md §6): older binaries do not use the table, so a monitor created here
-- simply has no sweeper until the api pod holding the leader lock is upgraded. Pings are accepted by any
-- pod that has this migration.

CREATE TABLE IF NOT EXISTS job_monitors (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name             text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description      text NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    -- 'cron': the crontab expression that starts the job. 'interval': the job reports at least this often,
    -- which is what a daemon's heartbeat is.
    kind             text NOT NULL DEFAULT 'cron' CHECK (kind IN ('cron', 'interval')),
    cron             text NOT NULL DEFAULT '' CHECK (length(cron) <= 200),
    -- The IANA zone the cron expression is read in ('' = UTC): a nightly job stays at its local hour across
    -- a daylight-saving change instead of drifting by an hour.
    time_zone        text NOT NULL DEFAULT '' CHECK (length(time_zone) <= 64),
    interval_seconds integer NOT NULL DEFAULT 0 CHECK (interval_seconds = 0 OR interval_seconds BETWEEN 60 AND 7776000),
    -- How long after the expected time a run may still arrive before it counts as missed.
    grace_seconds    integer NOT NULL DEFAULT 300 CHECK (grace_seconds BETWEEN 0 AND 86400),
    enabled          boolean NOT NULL DEFAULT true,
    tags             text[] NOT NULL DEFAULT '{}' CHECK (coalesce(array_length(tags, 1), 0) <= 10),
    -- The value in the ping URL. Stored as it is rather than hashed: the URL lives in a crontab and has to
    -- stay re-readable, and what it authorizes is one monitor's own status. A copy can report a run that
    -- did not happen — the same capability as simply not pinging (D-141, threat model in api.md).
    ping_token       text NOT NULL UNIQUE CHECK (ping_token ~ '^olj_[a-z2-7]{32}$'),
    created_by       uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by       uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    -- A definition names exactly one schedule.
    CONSTRAINT job_monitors_schedule_per_kind CHECK (
        (kind = 'cron' AND length(cron) > 0 AND interval_seconds = 0)
        OR (kind = 'interval' AND cron = '' AND interval_seconds > 0)
    )
);

CREATE INDEX IF NOT EXISTS job_monitors_org_name_idx ON job_monitors (org_id, lower(name));

-- The state of a monitor: what the last ping said and when the next run is due. One row per monitor,
-- created with it. The sweeper claims overdue rows with FOR UPDATE ... SKIP LOCKED and advances
-- expected_at in the same statement, so two api pods never report the same missed run twice.
CREATE TABLE IF NOT EXISTS job_monitor_state (
    monitor_id        uuid PRIMARY KEY REFERENCES job_monitors (id) ON DELETE CASCADE,
    -- NULL until the first run is concluded; otherwise running, success, failure, missed or overrun.
    status            text NOT NULL DEFAULT '' CHECK (status IN ('', 'running', 'success', 'failure', 'missed', 'overrun')),
    last_ping_at      timestamptz,
    last_started_at   timestamptz,
    last_finished_at  timestamptz,
    last_duration_ms  double precision NOT NULL DEFAULT 0,
    last_exit_code    integer NOT NULL DEFAULT 0 CHECK (last_exit_code BETWEEN 0 AND 255),
    last_message      text NOT NULL DEFAULT '' CHECK (length(last_message) <= 4096),
    -- When the next run is due. A monitor that has never reported is due from the moment it was created,
    -- so a job that is registered but never runs is still noticed.
    expected_at       timestamptz NOT NULL DEFAULT now(),
    consecutive_failures integer NOT NULL DEFAULT 0
);

-- The sweeper's scan: enabled monitors whose expected time has passed.
CREATE INDEX IF NOT EXISTS job_monitor_state_expected_idx ON job_monitor_state (expected_at);
