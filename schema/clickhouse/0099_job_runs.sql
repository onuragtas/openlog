-- openlog:phase expand
-- 0099_job_runs: one row per concluded run of a job monitor (D-141, migrations/postgres/0096_job_monitors).
-- Written by the api pod that received the ping, and by the leader's sweeper for a run that never reported,
-- with direct inserts into the _local table of the shard chosen by cityHash64(tenant_id, monitor_id) (D-018;
-- all runs of a monitor are on one shard). Read only through the Distributed table and the tenant-scoped
-- query layer.
--
-- Every run is mirrored as gauge data points in `metrics` (jobs.run.success, jobs.run.missed, jobs.run.late
-- and, when openlog saw both ends of the run, jobs.run.duration), so a metric alert rule watches a job like
-- any other signal and the 1-minute rollup keeps it for 395 days. This table is the detail the rollup cannot
-- hold: the exit code, the output the job attached and where the ping came from.
--
-- A run is written when it is concluded, never while it is running: `running` is a state of the monitor
-- (PostgreSQL), not a row of history.
--
-- TTL 90 days, the class of the alert evaluations (internal/migrate TTLTables, class "alerts"): operational
-- results of the installation, not tenant telemetry, and not widened by per-tenant retention (D-081). Ninety
-- days rather than thirty because a monthly job needs three occurrences to show a pattern.

CREATE TABLE IF NOT EXISTS openlog.job_runs_local ON CLUSTER '{cluster}'
(
    tenant_id     LowCardinality(String),
    monitor_id    String,
    monitor_name  String CODEC(ZSTD(1)),
    -- When the run was concluded (the finishing ping, or the moment the sweeper gave up on it).
    timestamp     DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    -- success, failure, missed or overrun.
    status        LowCardinality(String),
    -- The start ping's time; epoch 0 when openlog never saw one (a job that only reports when it finishes).
    started_at    DateTime('UTC') CODEC(DoubleDelta, ZSTD(1)),
    duration_ms   Float32 CODEC(Gorilla, ZSTD(1)),
    exit_code     Int32 CODEC(T64, ZSTD(1)),
    -- How far past the expected time the run was concluded, in seconds; negative means early.
    late_seconds  Float32 CODEC(Gorilla, ZSTD(1)),
    -- The output the job attached to its ping (stdout tail, an error line), at most 4 KiB.
    message       String CODEC(ZSTD(1)),
    -- The address the ping came from, so a job running on the wrong host is visible.
    source        String CODEC(ZSTD(1))
)
ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/openlog/job_runs_local', '{replica}')
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (tenant_id, monitor_id, timestamp)
TTL toDateTime(timestamp) + INTERVAL 90 DAY
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS openlog.job_runs ON CLUSTER '{cluster}'
AS openlog.job_runs_local
ENGINE = Distributed('{cluster}', openlog, job_runs_local, cityHash64(tenant_id, monitor_id));
