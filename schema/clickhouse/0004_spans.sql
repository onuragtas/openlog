-- openlog:phase expand
-- Spans are sharded by trace_id so a whole trace lives on one shard.
-- trace_id / span_id are lowercase hex strings.

CREATE TABLE IF NOT EXISTS openlog.spans_local ON CLUSTER '{cluster}'
(
    tenant_id           LowCardinality(String),
    timestamp           DateTime64(9, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    duration_ns         UInt64 CODEC(T64, ZSTD(1)),
    trace_id            String CODEC(ZSTD(1)),
    span_id             String CODEC(ZSTD(1)),
    parent_span_id      String CODEC(ZSTD(1)),
    trace_state         String CODEC(ZSTD(1)),
    name                LowCardinality(String),
    kind                Enum8('unspecified' = 0, 'internal' = 1, 'server' = 2, 'client' = 3, 'producer' = 4, 'consumer' = 5),
    status_code         Enum8('unset' = 0, 'ok' = 1, 'error' = 2),
    status_message      String CODEC(ZSTD(1)),
    service_name        LowCardinality(String),
    host_id             LowCardinality(String),
    resource_attributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    scope_name          LowCardinality(String),
    attributes          Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    events_timestamp    Array(DateTime64(9, 'UTC')) CODEC(ZSTD(1)),
    events_name         Array(LowCardinality(String)) CODEC(ZSTD(1)),
    events_attributes   Array(Map(LowCardinality(String), String)) CODEC(ZSTD(1)),
    links_trace_id      Array(String) CODEC(ZSTD(1)),
    links_span_id       Array(String) CODEC(ZSTD(1)),
    INDEX idx_trace_id trace_id TYPE bloom_filter(0.001) GRANULARITY 1
)
ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/openlog/spans_local', '{replica}')
PARTITION BY toDate(timestamp)
ORDER BY (tenant_id, service_name, name, timestamp)
TTL toDateTime(timestamp) + INTERVAL 7 DAY
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS openlog.spans ON CLUSTER '{cluster}'
AS openlog.spans_local
ENGINE = Distributed('{cluster}', openlog, spans_local, cityHash64(tenant_id, trace_id));

-- trace_id -> time range lookup so fetching a trace does not scan every partition.
CREATE TABLE IF NOT EXISTS openlog.trace_index_local ON CLUSTER '{cluster}'
(
    tenant_id  LowCardinality(String),
    trace_id   String,
    start      SimpleAggregateFunction(min, DateTime64(9, 'UTC')),
    end        SimpleAggregateFunction(max, DateTime64(9, 'UTC')),
    span_count SimpleAggregateFunction(sum, UInt64)
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/trace_index_local', '{replica}')
PARTITION BY toDate(start)
ORDER BY (tenant_id, trace_id)
TTL toDateTime(start) + INTERVAL 7 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.trace_index_mv ON CLUSTER '{cluster}'
TO openlog.trace_index_local
AS SELECT
    tenant_id,
    trace_id,
    min(timestamp) AS start,
    max(timestamp + toIntervalNanosecond(duration_ns)) AS end,
    toUInt64(count()) AS span_count
FROM openlog.spans_local
GROUP BY tenant_id, trace_id;

CREATE TABLE IF NOT EXISTS openlog.trace_index ON CLUSTER '{cluster}'
AS openlog.trace_index_local
ENGINE = Distributed('{cluster}', openlog, trace_index_local, cityHash64(tenant_id, trace_id));
