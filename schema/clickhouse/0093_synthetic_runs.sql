-- openlog:phase expand
-- 0093_synthetic_runs: one row per run of a synthetic check (D-132, migrations/postgres/0090_synthetics).
-- Written by the api leader's scheduler (internal/synthetics) in batches, with direct inserts into the
-- _local table of the shard chosen by cityHash64(tenant_id, check_id) (D-018; all runs of a check are on one
-- shard). Read only through the Distributed table and the tenant-scoped query layer. Losing rows (process
-- death before a flush) only leaves a gap in the history; PostgreSQL keeps the last outcome of every check.
--
-- Every run is mirrored as two gauge data points in `metrics` (synthetics.check.success,
-- synthetics.check.duration), so metric alert rules (alerting.md §2.2) and dashboards use a check like any
-- other metric and the 1-minute rollup keeps it for 395 days. This table is the detail the rollup cannot
-- hold: the error message, the status code and the phase timings of each individual run.
--
-- The definition is repeated on the row (check_name, url, method) so the history keeps describing what ran
-- after the check was edited or deleted; the columns are LowCardinality or ZSTD, and a check produces at
-- most one row per 30 seconds and location, so the repetition is cheap.
--
-- TTL 30 days, the class of the alert evaluations (internal/migrate TTLTables, class "alerts"): operational
-- results of the installation, not tenant telemetry, and not widened by per-tenant retention (D-081).

CREATE TABLE IF NOT EXISTS openlog.synthetic_runs_local ON CLUSTER '{cluster}'
(
    tenant_id      LowCardinality(String),
    check_id       String,
    check_name     String CODEC(ZSTD(1)),
    -- Built-in location 'local' = the openlog server itself (further locations need no migration).
    location       LowCardinality(String),
    timestamp      DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    success        Bool,
    -- 0 when the run failed before a response (dns, connect, tls, timeout, blocked).
    status_code    UInt16 CODEC(T64, ZSTD(1)),
    -- '' on success; otherwise dns, connect, tls, timeout, blocked, redirect, status, assertion, body, request.
    error_kind     LowCardinality(String),
    error_message  String CODEC(ZSTD(1)),
    duration_ms    Float32 CODEC(Gorilla, ZSTD(1)),
    -- Phase timings of the first connection of the run; 0 when the phase did not happen (a cached DNS
    -- answer, a plain http target) or the run failed before it.
    dns_ms         Float32 CODEC(Gorilla, ZSTD(1)),
    connect_ms     Float32 CODEC(Gorilla, ZSTD(1)),
    tls_ms         Float32 CODEC(Gorilla, ZSTD(1)),
    first_byte_ms  Float32 CODEC(Gorilla, ZSTD(1)),
    response_bytes UInt32 CODEC(T64, ZSTD(1)),
    url            String CODEC(ZSTD(1)),
    method         LowCardinality(String)
)
ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/openlog/synthetic_runs_local', '{replica}')
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (tenant_id, check_id, timestamp)
TTL toDateTime(timestamp) + INTERVAL 30 DAY
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS openlog.synthetic_runs ON CLUSTER '{cluster}'
AS openlog.synthetic_runs_local
ENGINE = Distributed('{cluster}', openlog, synthetic_runs_local, cityHash64(tenant_id, check_id));
