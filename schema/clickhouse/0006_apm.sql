-- openlog:phase expand
-- APM (docs/contracts/apm.md §8): derived span columns written by the processor, and
-- per-minute aggregates maintained by materialized views on spans_local.
--
-- Sharding: spans are sharded by cityHash64(tenant_id, trace_id), so the MVs below write
-- partial aggregates of one key (service, minute, ...) on every shard. All columns are
-- mergeable (sum/min/max/sumMap/argMax) and the API always re-aggregates with GROUP BY
-- through the Distributed tables, which makes the result independent of placement.
-- apm_service_links_1m is filled per shard by the edge-linking job (internal/apm) and
-- read with FINAL (per shard). Nothing inserts through the Distributed APM tables; their
-- sharding key is unused.
--
-- Latency histogram bucket (apm.md §4.1, Go: apm.Bucket):
--   toInt16(if(duration_ns <= 1000, -80, greatest(-80, least(200, ceil(8 * log2(duration_ns / 1e6))))))

ALTER TABLE openlog.spans_local ON CLUSTER '{cluster}'
    ADD COLUMN IF NOT EXISTS service_namespace       LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS deployment_environment  LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS is_entry                Bool DEFAULT false,
    ADD COLUMN IF NOT EXISTS transaction_type        LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS transaction_name        LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS is_error                Bool DEFAULT false,
    ADD COLUMN IF NOT EXISTS http_status_code        UInt16 DEFAULT 0,
    ADD COLUMN IF NOT EXISTS sample_weight           Float64 DEFAULT 1,
    ADD COLUMN IF NOT EXISTS peer_type               LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS peer_name               LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS db_system               LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS db_name                 LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS db_operation            LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS db_statement_normalized String DEFAULT '' CODEC(ZSTD(1)),
    ADD COLUMN IF NOT EXISTS error_group_id          UInt64 DEFAULT 0,
    ADD COLUMN IF NOT EXISTS error_type              LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS error_message           String DEFAULT '' CODEC(ZSTD(1));

ALTER TABLE openlog.spans ON CLUSTER '{cluster}'
    ADD COLUMN IF NOT EXISTS service_namespace       LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS deployment_environment  LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS is_entry                Bool DEFAULT false,
    ADD COLUMN IF NOT EXISTS transaction_type        LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS transaction_name        LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS is_error                Bool DEFAULT false,
    ADD COLUMN IF NOT EXISTS http_status_code        UInt16 DEFAULT 0,
    ADD COLUMN IF NOT EXISTS sample_weight           Float64 DEFAULT 1,
    ADD COLUMN IF NOT EXISTS peer_type               LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS peer_name               LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS db_system               LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS db_name                 LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS db_operation            LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS db_statement_normalized String DEFAULT '',
    ADD COLUMN IF NOT EXISTS error_group_id          UInt64 DEFAULT 0,
    ADD COLUMN IF NOT EXISTS error_type              LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS error_message           String DEFAULT '';

-- ---- transactions (entry spans) ----

CREATE TABLE IF NOT EXISTS openlog.apm_transactions_1m_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    service_name           LowCardinality(String),
    service_namespace      LowCardinality(String),
    deployment_environment LowCardinality(String),
    timestamp              DateTime('UTC'),
    transaction_type       LowCardinality(String),
    transaction_name       LowCardinality(String),
    requests               SimpleAggregateFunction(sum, Float64),
    samples                SimpleAggregateFunction(sum, UInt64),
    errors                 SimpleAggregateFunction(sum, Float64),
    duration_sum_ms        SimpleAggregateFunction(sum, Float64),
    duration_max_ms        SimpleAggregateFunction(max, Float64),
    -- (bucket, weight) of all requests / of requests without error (Apdex)
    duration_hist          SimpleAggregateFunction(sumMap, Tuple(Array(Int16), Array(Float64))),
    ok_hist                SimpleAggregateFunction(sumMap, Tuple(Array(Int16), Array(Float64)))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/apm_transactions_1m_local', '{replica}')
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (tenant_id, service_name, service_namespace, deployment_environment, timestamp, transaction_type, transaction_name)
TTL timestamp + INTERVAL 30 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.apm_transactions_1m_mv ON CLUSTER '{cluster}'
TO openlog.apm_transactions_1m_local
AS SELECT
    tenant_id,
    service_name,
    service_namespace,
    deployment_environment,
    toStartOfMinute(ts) AS timestamp,
    transaction_type,
    transaction_name,
    sum(w) AS requests,
    toUInt64(count()) AS samples,
    sumIf(w, err) AS errors,
    sum(w * dur_ms) AS duration_sum_ms,
    max(dur_ms) AS duration_max_ms,
    sumMap([bucket], [w]) AS duration_hist,
    sumMapIf([bucket], [w], NOT err) AS ok_hist
FROM
(
    SELECT tenant_id, service_name, service_namespace, deployment_environment, transaction_type, transaction_name,
           timestamp AS ts, sample_weight AS w, is_error AS err, duration_ns / 1e6 AS dur_ms,
           toInt16(if(duration_ns <= 1000, -80, greatest(-80, least(200, ceil(8 * log2(duration_ns / 1e6)))))) AS bucket
    FROM openlog.spans_local
    WHERE is_entry AND service_name != '' AND sample_weight > 0
)
GROUP BY tenant_id, service_name, service_namespace, deployment_environment, timestamp, transaction_type, transaction_name;

CREATE TABLE IF NOT EXISTS openlog.apm_transactions_1m ON CLUSTER '{cluster}'
AS openlog.apm_transactions_1m_local
ENGINE = Distributed('{cluster}', openlog, apm_transactions_1m_local, cityHash64(tenant_id, service_name));

-- ---- service map: attribute edges of client/producer spans ----

CREATE TABLE IF NOT EXISTS openlog.apm_service_edges_1m_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    service_name           LowCardinality(String),
    service_namespace      LowCardinality(String),
    deployment_environment LowCardinality(String),
    timestamp              DateTime('UTC'),
    target_type            LowCardinality(String),
    target_name            LowCardinality(String),
    calls                  SimpleAggregateFunction(sum, Float64),
    samples                SimpleAggregateFunction(sum, UInt64),
    errors                 SimpleAggregateFunction(sum, Float64),
    duration_sum_ms        SimpleAggregateFunction(sum, Float64),
    duration_hist          SimpleAggregateFunction(sumMap, Tuple(Array(Int16), Array(Float64)))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/apm_service_edges_1m_local', '{replica}')
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (tenant_id, service_name, service_namespace, deployment_environment, timestamp, target_type, target_name)
TTL timestamp + INTERVAL 30 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.apm_service_edges_1m_mv ON CLUSTER '{cluster}'
TO openlog.apm_service_edges_1m_local
AS SELECT
    tenant_id,
    service_name,
    service_namespace,
    deployment_environment,
    toStartOfMinute(ts) AS timestamp,
    peer_type AS target_type,
    peer_name AS target_name,
    sum(w) AS calls,
    toUInt64(count()) AS samples,
    sumIf(w, err) AS errors,
    sum(w * dur_ms) AS duration_sum_ms,
    sumMap([bucket], [w]) AS duration_hist
FROM
(
    SELECT tenant_id, service_name, service_namespace, deployment_environment, peer_type, peer_name,
           timestamp AS ts, sample_weight AS w, is_error AS err, duration_ns / 1e6 AS dur_ms,
           toInt16(if(duration_ns <= 1000, -80, greatest(-80, least(200, ceil(8 * log2(duration_ns / 1e6)))))) AS bucket
    FROM openlog.spans_local
    WHERE kind IN ('client', 'producer') AND peer_type != '' AND service_name != '' AND sample_weight > 0
)
GROUP BY tenant_id, service_name, service_namespace, deployment_environment, timestamp, target_type, target_name;

CREATE TABLE IF NOT EXISTS openlog.apm_service_edges_1m ON CLUSTER '{cluster}'
AS openlog.apm_service_edges_1m_local
ENGINE = Distributed('{cluster}', openlog, apm_service_edges_1m_local, cityHash64(tenant_id, service_name));

-- ---- service map: trace-linked edges (client span -> child server span of another service) ----
-- Written by the edge-linking job on each shard; a re-run replaces the previous result of the
-- same key on that shard (computed_at). Read with FINAL.

CREATE TABLE IF NOT EXISTS openlog.apm_service_links_1m_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    service_name           LowCardinality(String),
    service_namespace      LowCardinality(String),
    deployment_environment LowCardinality(String),
    timestamp              DateTime('UTC'),
    target_service         LowCardinality(String),
    target_namespace       LowCardinality(String),
    target_environment     LowCardinality(String),
    via                    LowCardinality(String),
    calls                  Float64,
    samples                UInt64,
    errors                 Float64,
    duration_sum_ms        Float64,
    duration_hist          Tuple(Array(Int16), Array(Float64)),
    computed_at            DateTime64(3, 'UTC')
)
ENGINE = ReplicatedReplacingMergeTree('/clickhouse/tables/{shard}/openlog/apm_service_links_1m_local', '{replica}', computed_at)
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (tenant_id, service_name, service_namespace, deployment_environment, timestamp, target_service, target_namespace, target_environment, via)
TTL timestamp + INTERVAL 30 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS openlog.apm_service_links_1m ON CLUSTER '{cluster}'
AS openlog.apm_service_links_1m_local
ENGINE = Distributed('{cluster}', openlog, apm_service_links_1m_local, cityHash64(tenant_id, service_name));

-- ---- database queries ----

CREATE TABLE IF NOT EXISTS openlog.apm_db_queries_1m_local ON CLUSTER '{cluster}'
(
    tenant_id               LowCardinality(String),
    service_name            LowCardinality(String),
    service_namespace       LowCardinality(String),
    deployment_environment  LowCardinality(String),
    timestamp               DateTime('UTC'),
    db_system               LowCardinality(String),
    db_name                 LowCardinality(String),
    db_statement_normalized String CODEC(ZSTD(1)),
    db_operation            SimpleAggregateFunction(anyLast, LowCardinality(String)),
    calls                   SimpleAggregateFunction(sum, Float64),
    samples                 SimpleAggregateFunction(sum, UInt64),
    errors                  SimpleAggregateFunction(sum, Float64),
    duration_sum_ms         SimpleAggregateFunction(sum, Float64),
    duration_max_ms         SimpleAggregateFunction(max, Float64),
    duration_hist           SimpleAggregateFunction(sumMap, Tuple(Array(Int16), Array(Float64)))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/apm_db_queries_1m_local', '{replica}')
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (tenant_id, service_name, service_namespace, deployment_environment, timestamp, db_system, db_name, db_statement_normalized)
TTL timestamp + INTERVAL 30 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.apm_db_queries_1m_mv ON CLUSTER '{cluster}'
TO openlog.apm_db_queries_1m_local
AS SELECT
    tenant_id,
    service_name,
    service_namespace,
    deployment_environment,
    toStartOfMinute(ts) AS timestamp,
    db_system,
    db_name,
    stmt AS db_statement_normalized,
    anyLast(op) AS db_operation,
    sum(w) AS calls,
    toUInt64(count()) AS samples,
    sumIf(w, err) AS errors,
    sum(w * dur_ms) AS duration_sum_ms,
    max(dur_ms) AS duration_max_ms,
    sumMap([bucket], [w]) AS duration_hist
FROM
(
    SELECT tenant_id, service_name, service_namespace, deployment_environment, db_system, db_name,
           db_statement_normalized AS stmt, db_operation AS op,
           timestamp AS ts, sample_weight AS w, is_error AS err, duration_ns / 1e6 AS dur_ms,
           toInt16(if(duration_ns <= 1000, -80, greatest(-80, least(200, ceil(8 * log2(duration_ns / 1e6)))))) AS bucket
    FROM openlog.spans_local
    WHERE db_system != '' AND kind = 'client' AND service_name != '' AND sample_weight > 0
)
GROUP BY tenant_id, service_name, service_namespace, deployment_environment, timestamp, db_system, db_name, db_statement_normalized;

CREATE TABLE IF NOT EXISTS openlog.apm_db_queries_1m ON CLUSTER '{cluster}'
AS openlog.apm_db_queries_1m_local
ENGINE = Distributed('{cluster}', openlog, apm_db_queries_1m_local, cityHash64(tenant_id, service_name));

-- ---- errors: per-minute counts and group summaries ----

CREATE TABLE IF NOT EXISTS openlog.apm_errors_1m_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    service_name           LowCardinality(String),
    service_namespace      LowCardinality(String),
    deployment_environment LowCardinality(String),
    timestamp              DateTime('UTC'),
    error_group_id         UInt64,
    count                  SimpleAggregateFunction(sum, Float64),
    samples                SimpleAggregateFunction(sum, UInt64)
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/apm_errors_1m_local', '{replica}')
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (tenant_id, service_name, service_namespace, deployment_environment, timestamp, error_group_id)
TTL timestamp + INTERVAL 30 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.apm_errors_1m_mv ON CLUSTER '{cluster}'
TO openlog.apm_errors_1m_local
AS SELECT
    tenant_id,
    service_name,
    service_namespace,
    deployment_environment,
    toStartOfMinute(ts) AS timestamp,
    error_group_id,
    sum(w) AS count,
    toUInt64(count()) AS samples
FROM
(
    SELECT tenant_id, service_name, service_namespace, deployment_environment, error_group_id,
           timestamp AS ts, sample_weight AS w
    FROM openlog.spans_local
    WHERE error_group_id != 0 AND service_name != '' AND sample_weight > 0
)
GROUP BY tenant_id, service_name, service_namespace, deployment_environment, timestamp, error_group_id;

CREATE TABLE IF NOT EXISTS openlog.apm_errors_1m ON CLUSTER '{cluster}'
AS openlog.apm_errors_1m_local
ENGINE = Distributed('{cluster}', openlog, apm_errors_1m_local, cityHash64(tenant_id, service_name));

CREATE TABLE IF NOT EXISTS openlog.apm_error_groups_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    service_name           LowCardinality(String),
    service_namespace      LowCardinality(String),
    deployment_environment LowCardinality(String),
    error_group_id         UInt64,
    first_seen             SimpleAggregateFunction(min, DateTime64(9, 'UTC')),
    last_seen              SimpleAggregateFunction(max, DateTime64(9, 'UTC')),
    count                  SimpleAggregateFunction(sum, Float64),
    error_type             SimpleAggregateFunction(anyLast, String),
    error_message          SimpleAggregateFunction(anyLast, String),
    -- newest sample of the group
    last_trace_id          AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    last_span_id           AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    last_span_name         AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    last_message           AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    last_stacktrace        AggregateFunction(argMax, String, DateTime64(9, 'UTC'))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/apm_error_groups_local', '{replica}')
ORDER BY (tenant_id, service_name, service_namespace, deployment_environment, error_group_id)
TTL toDateTime(last_seen) + INTERVAL 30 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.apm_error_groups_mv ON CLUSTER '{cluster}'
TO openlog.apm_error_groups_local
AS SELECT
    tenant_id,
    service_name,
    service_namespace,
    deployment_environment,
    error_group_id,
    min(ts) AS first_seen,
    max(ts) AS last_seen,
    sum(w) AS count,
    anyLast(etype) AS error_type,
    anyLast(emsg) AS error_message,
    argMaxState(tid, ts) AS last_trace_id,
    argMaxState(sid, ts) AS last_span_id,
    argMaxState(sname, ts) AS last_span_name,
    argMaxState(raw_message, ts) AS last_message,
    argMaxState(stack, ts) AS last_stacktrace
FROM
(
    SELECT tenant_id, service_name, service_namespace, deployment_environment, error_group_id,
           timestamp AS ts, sample_weight AS w, toString(error_type) AS etype, error_message AS emsg,
           trace_id AS tid, span_id AS sid, toString(name) AS sname,
           arrayLastIndex(x -> x = 'exception', events_name) AS exc,
           if(exc > 0, events_attributes[exc]['exception.message'], status_message) AS raw_message,
           if(exc > 0, events_attributes[exc]['exception.stacktrace'], '') AS stack
    FROM openlog.spans_local
    WHERE error_group_id != 0 AND service_name != ''
)
GROUP BY tenant_id, service_name, service_namespace, deployment_environment, error_group_id;

CREATE TABLE IF NOT EXISTS openlog.apm_error_groups ON CLUSTER '{cluster}'
AS openlog.apm_error_groups_local
ENGINE = Distributed('{cluster}', openlog, apm_error_groups_local, cityHash64(tenant_id, service_name));

-- ---- services and service <-> host links ----

CREATE TABLE IF NOT EXISTS openlog.apm_services_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    service_name           LowCardinality(String),
    service_namespace      LowCardinality(String),
    deployment_environment LowCardinality(String),
    first_seen             SimpleAggregateFunction(min, DateTime64(9, 'UTC')),
    last_seen              SimpleAggregateFunction(max, DateTime64(9, 'UTC')),
    spans                  SimpleAggregateFunction(sum, UInt64),
    service_version        AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    sdk_language           AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    sdk_name               AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    resource_attributes    AggregateFunction(argMax, Map(String, String), DateTime64(9, 'UTC'))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/apm_services_local', '{replica}')
ORDER BY (tenant_id, service_name, service_namespace, deployment_environment)
TTL toDateTime(last_seen) + INTERVAL 30 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.apm_services_mv ON CLUSTER '{cluster}'
TO openlog.apm_services_local
AS SELECT
    tenant_id,
    service_name,
    service_namespace,
    deployment_environment,
    min(ts) AS first_seen,
    max(ts) AS last_seen,
    toUInt64(count()) AS spans,
    argMaxState(res['service.version'], ts) AS service_version,
    argMaxState(res['telemetry.sdk.language'], ts) AS sdk_language,
    argMaxState(res['telemetry.sdk.name'], ts) AS sdk_name,
    argMaxState(res, ts) AS resource_attributes
FROM
(
    SELECT tenant_id, service_name, service_namespace, deployment_environment,
           timestamp AS ts, CAST(resource_attributes, 'Map(String, String)') AS res
    FROM openlog.spans_local
    WHERE service_name != ''
)
GROUP BY tenant_id, service_name, service_namespace, deployment_environment;

CREATE TABLE IF NOT EXISTS openlog.apm_services ON CLUSTER '{cluster}'
AS openlog.apm_services_local
ENGINE = Distributed('{cluster}', openlog, apm_services_local, cityHash64(tenant_id, service_name));

CREATE TABLE IF NOT EXISTS openlog.apm_service_hosts_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    host_id                String,
    service_name           LowCardinality(String),
    service_namespace      LowCardinality(String),
    deployment_environment LowCardinality(String),
    first_seen             SimpleAggregateFunction(min, DateTime64(9, 'UTC')),
    last_seen              SimpleAggregateFunction(max, DateTime64(9, 'UTC')),
    host_name              AggregateFunction(argMax, String, DateTime64(9, 'UTC'))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/apm_service_hosts_local', '{replica}')
ORDER BY (tenant_id, host_id, service_name, service_namespace, deployment_environment)
TTL toDateTime(last_seen) + INTERVAL 30 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.apm_service_hosts_mv ON CLUSTER '{cluster}'
TO openlog.apm_service_hosts_local
AS SELECT
    tenant_id,
    host_id,
    service_name,
    service_namespace,
    deployment_environment,
    min(ts) AS first_seen,
    max(ts) AS last_seen,
    argMaxState(hname, ts) AS host_name
FROM
(
    SELECT tenant_id, host_id, service_name, service_namespace, deployment_environment,
           timestamp AS ts, resource_attributes['host.name'] AS hname
    FROM openlog.spans_local
    WHERE host_id != '' AND service_name != ''
)
GROUP BY tenant_id, host_id, service_name, service_namespace, deployment_environment;

CREATE TABLE IF NOT EXISTS openlog.apm_service_hosts ON CLUSTER '{cluster}'
AS openlog.apm_service_hosts_local
ENGINE = Distributed('{cluster}', openlog, apm_service_hosts_local, cityHash64(tenant_id, host_id));
