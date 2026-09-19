-- openlog:phase expand
-- 0096_db_monitoring: database query performance monitoring (docs/contracts/db-monitoring.md, D-138).
--
-- The infra agent's database integrations (PostgreSQL, MySQL/MariaDB, SQL Server) send three kinds of OTLP log
-- records that the processor routes here instead of into `logs`, like the inventory events:
--
--   openlog.db.query_stats     → db_query_stats      per statement per collection interval: calls, time, rows, …
--   openlog.db.session_sample  → db_session_samples  every non-idle session at a sampling instant (waits, blocking)
--   openlog.db.query_plan      → db_query_plans      an execution plan of a top statement, at most once an hour
--
-- Why dedicated tables and not metrics. The question the product answers is "which statements cost the most on
-- this server, and who runs them". As metrics that is a rate over thousands of per-statement cumulative series
-- whose resource carries the statement text; here it is `sum(total_time_ms) GROUP BY fingerprint` over rows that
-- are already deltas (the agent differences the server's cumulative counters, handles resets and drops the first
-- collection). A plan is a document and a session sample an event; neither is a number over time.
--
-- The statement text is stored **normalized** by the processor with apm.NormalizeStatement — the function that
-- also normalizes db.statement of APM client spans into apm_db_queries_1m.db_statement_normalized (apm.md §7).
-- The agent only redacts literals before sending; the canonical form exists in exactly one place. So the join
-- "which services run this statement" is an equality on the normalized text; `fingerprint` (FNV-1a 64 of the
-- normalized text, computed by the processor: processor.DBFingerprint) is only its compact key for grouping and URLs.
--
-- Retention: statement statistics and plans follow the metrics class (30 days, internal/migrate TTLTables); the
-- session samples are high-volume diagnostic events like spans and follow the traces class (7 days).
--
-- Sharding: cityHash64(tenant_id, instance). Every read of the UI is per database instance, so one instance's rows
-- sit on one shard, and instances spread evenly.

-- ---- statement statistics ----

CREATE TABLE IF NOT EXISTS openlog.db_query_stats_local ON CLUSTER '{cluster}'
(
    tenant_id        LowCardinality(String),
    -- End of the collection interval the deltas cover.
    timestamp        DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    interval_seconds UInt32,
    host_id          LowCardinality(String),
    host_name        LowCardinality(String),
    -- postgresql, mysql, mssql (OTel db.system.name values).
    db_system        LowCardinality(String),
    -- service.instance.id of the integration instance (host:port of the server, or the socket): the entity key.
    instance         String CODEC(ZSTD(1)),
    server_address   LowCardinality(String),
    server_port      UInt16,
    db_name          LowCardinality(String),
    db_user          LowCardinality(String),
    -- The server's own id: pg_stat_statements queryid, performance_schema DIGEST, SQL Server query_hash.
    query_id         String CODEC(ZSTD(1)),
    query_text       String CODEC(ZSTD(3)),
    fingerprint      UInt64,
    calls            UInt64,
    total_time_ms    Float64,
    -- Rows returned/affected; rows_examined only where the server reports it (MySQL).
    rows             UInt64,
    rows_examined    UInt64,
    errors           UInt64,
    -- Executions without a usable index (MySQL SUM_NO_INDEX_USED + SUM_NO_GOOD_INDEX_USED).
    no_index_used    UInt64,
    -- Buffer pages found in the cache / read from disk (PostgreSQL shared blocks, SQL Server logical/physical reads).
    blocks_hit       UInt64,
    blocks_read      UInt64
)
ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/openlog/db_query_stats_local', '{replica}')
PARTITION BY toDate(timestamp)
ORDER BY (tenant_id, instance, timestamp, fingerprint)
TTL toDateTime(timestamp) + INTERVAL 30 DAY
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS openlog.db_query_stats ON CLUSTER '{cluster}'
AS openlog.db_query_stats_local
ENGINE = Distributed('{cluster}', openlog, db_query_stats_local, cityHash64(tenant_id, instance));

-- ---- session samples (active session history) ----

CREATE TABLE IF NOT EXISTS openlog.db_session_samples_local ON CLUSTER '{cluster}'
(
    tenant_id            LowCardinality(String),
    timestamp            DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    host_id              LowCardinality(String),
    host_name            LowCardinality(String),
    db_system            LowCardinality(String),
    instance             String CODEC(ZSTD(1)),
    db_name              LowCardinality(String),
    db_user              LowCardinality(String),
    -- PostgreSQL pid, MySQL processlist id, SQL Server session_id.
    session_id           String CODEC(ZSTD(1)),
    -- active, idle in transaction, … (PostgreSQL state; MySQL command; SQL Server request status).
    state                LowCardinality(String),
    -- The wait class and event; both empty while the session is on the CPU.
    wait_event_type      LowCardinality(String),
    wait_event           LowCardinality(String),
    query_id             String CODEC(ZSTD(1)),
    query_text           String CODEC(ZSTD(3)),
    fingerprint          UInt64,
    -- How long the current statement (or, idle in transaction, the transaction) has been running.
    duration_ms          Float64,
    -- Sessions this one waits for (lock holders): the edges of the blocking tree.
    blocking_session_ids Array(String) CODEC(ZSTD(1)),
    application          LowCardinality(String),
    client_address       LowCardinality(String)
)
ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/openlog/db_session_samples_local', '{replica}')
PARTITION BY toDate(timestamp)
ORDER BY (tenant_id, instance, timestamp)
TTL toDateTime(timestamp) + INTERVAL 7 DAY
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS openlog.db_session_samples ON CLUSTER '{cluster}'
AS openlog.db_session_samples_local
ENGINE = Distributed('{cluster}', openlog, db_session_samples_local, cityHash64(tenant_id, instance));

-- ---- execution plans ----
--
-- One row per capture the agent decided to send: it explains the top statements periodically but sends a plan only
-- when its shape (plan_hash: the plan without costs and row estimates) changed for that statement, or once a day
-- to show it is still current. So a plan change — the event worth seeing after an index was dropped or statistics
-- changed — is a new plan_hash, and "first seen" is min(captured_at) of that hash. Partitioned by day like the other
-- tables, so the per-tenant retention (D-081) can drop a tenant's old captures partition by partition.

CREATE TABLE IF NOT EXISTS openlog.db_query_plans_local ON CLUSTER '{cluster}'
(
    tenant_id     LowCardinality(String),
    captured_at   DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    instance      String CODEC(ZSTD(1)),
    fingerprint   UInt64,
    plan_hash     String,
    db_system     LowCardinality(String),
    host_id       LowCardinality(String),
    db_name       LowCardinality(String),
    query_text    String CODEC(ZSTD(3)),
    -- json (PostgreSQL EXPLAIN FORMAT JSON, MySQL EXPLAIN FORMAT=JSON) or xml (SQL Server showplan).
    plan_format   LowCardinality(String),
    plan          String CODEC(ZSTD(3)),
    total_cost    Float64
)
ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/openlog/db_query_plans_local', '{replica}')
PARTITION BY toDate(captured_at)
ORDER BY (tenant_id, instance, fingerprint, plan_hash, captured_at)
TTL toDateTime(captured_at) + INTERVAL 30 DAY
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS openlog.db_query_plans ON CLUSTER '{cluster}'
AS openlog.db_query_plans_local
ENGINE = Distributed('{cluster}', openlog, db_query_plans_local, cityHash64(tenant_id, instance));
