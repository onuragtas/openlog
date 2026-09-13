-- openlog:phase expand
-- One row per OTLP data point. series_id is computed by the processor as a stable 64-bit hash of
-- (tenant_id, metric_name, resource_attributes, attributes).

CREATE TABLE IF NOT EXISTS openlog.metrics_local ON CLUSTER '{cluster}'
(
    tenant_id           LowCardinality(String),
    metric_name         LowCardinality(String),
    metric_type         Enum8('gauge' = 1, 'sum' = 2, 'histogram' = 3, 'exponential_histogram' = 4, 'summary' = 5),
    temporality         Enum8('unspecified' = 0, 'delta' = 1, 'cumulative' = 2),
    is_monotonic        Bool,
    unit                LowCardinality(String),
    description         String CODEC(ZSTD(1)),
    service_name        LowCardinality(String),
    host_id             LowCardinality(String),
    host_name           LowCardinality(String),
    series_id           UInt64,
    resource_attributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    scope_name          LowCardinality(String),
    attributes          Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    start_timestamp     DateTime64(9, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    timestamp           DateTime64(9, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    value               Float64 CODEC(Gorilla, ZSTD(1)),
    -- histogram / summary
    count               UInt64 CODEC(T64, ZSTD(1)),
    sum                 Float64 CODEC(Gorilla, ZSTD(1)),
    bucket_counts       Array(UInt64) CODEC(ZSTD(1)),
    explicit_bounds     Array(Float64) CODEC(ZSTD(1)),
    flags               UInt32
)
ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/openlog/metrics_local', '{replica}')
PARTITION BY toDate(timestamp)
ORDER BY (tenant_id, metric_name, host_id, series_id, timestamp)
TTL toDateTime(timestamp) + INTERVAL 30 DAY
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS openlog.metrics ON CLUSTER '{cluster}'
AS openlog.metrics_local
ENGINE = Distributed('{cluster}', openlog, metrics_local, cityHash64(tenant_id, host_id));

-- 1-minute rollup for gauges and sums; used by the API for ranges longer than 6h.
CREATE TABLE IF NOT EXISTS openlog.metrics_1m_local ON CLUSTER '{cluster}'
(
    tenant_id    LowCardinality(String),
    metric_name  LowCardinality(String),
    host_id      LowCardinality(String),
    series_id    UInt64,
    timestamp    DateTime('UTC'),
    metric_type  SimpleAggregateFunction(any, Enum8('gauge' = 1, 'sum' = 2, 'histogram' = 3, 'exponential_histogram' = 4, 'summary' = 5)),
    is_monotonic SimpleAggregateFunction(any, Bool),
    unit         SimpleAggregateFunction(any, LowCardinality(String)),
    service_name SimpleAggregateFunction(any, LowCardinality(String)),
    -- Map(String, String): ClickHouse rejects LowCardinality keys inside SimpleAggregateFunction(any, Map(...)).
    attributes   SimpleAggregateFunction(any, Map(String, String)),
    value_min    SimpleAggregateFunction(min, Float64),
    value_max    SimpleAggregateFunction(max, Float64),
    value_sum    SimpleAggregateFunction(sum, Float64),
    value_count  SimpleAggregateFunction(sum, UInt64),
    value_last   AggregateFunction(argMax, Float64, DateTime64(9, 'UTC'))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/metrics_1m_local', '{replica}')
PARTITION BY toYYYYMM(timestamp)
ORDER BY (tenant_id, metric_name, host_id, series_id, timestamp)
TTL timestamp + INTERVAL 395 DAY;

-- The source columns are renamed in a sub-select (ts, attrs) because output aliases such as
-- `any(metric_type) AS metric_type` would otherwise shadow them in WHERE / argMaxState.
CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.metrics_1m_mv ON CLUSTER '{cluster}'
TO openlog.metrics_1m_local
AS SELECT
    tenant_id,
    metric_name,
    host_id,
    series_id,
    toStartOfMinute(ts) AS timestamp,
    any(metric_type)    AS metric_type,
    any(is_monotonic)   AS is_monotonic,
    any(unit)           AS unit,
    any(service_name)   AS service_name,
    any(attrs)          AS attributes,
    min(value)          AS value_min,
    max(value)          AS value_max,
    sum(value)          AS value_sum,
    toUInt64(count())   AS value_count,
    argMaxState(value, ts) AS value_last
FROM
(
    SELECT *, timestamp AS ts, CAST(attributes, 'Map(String, String)') AS attrs
    FROM openlog.metrics_local
    WHERE metric_type IN ('gauge', 'sum')
)
GROUP BY tenant_id, metric_name, host_id, series_id, timestamp;

CREATE TABLE IF NOT EXISTS openlog.metrics_1m ON CLUSTER '{cluster}'
AS openlog.metrics_1m_local
ENGINE = Distributed('{cluster}', openlog, metrics_1m_local, cityHash64(tenant_id, host_id));
