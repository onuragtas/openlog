-- openlog:phase expand
-- 0092_metric_exemplars: OTLP metric exemplars (D-130). An exemplar is the trace a metric data point came from:
-- the SDK attaches a trace id, span id, value, timestamp and its own filtered attributes to a data point, which is
-- the one correlation hop metrics could not make before this migration (the processor dropped them).
--
-- They live in their own table, not in a column on metrics_local, for two reasons. (1) Retention: an exemplar is a
-- pointer into a trace, and traces are kept 7 days while metrics are kept 30 (and the 1-minute rollup 395). On the
-- metric row every exemplar would outlive its trace by 23 days and link to a trace that no longer exists; here the
-- TTL is the traces one and the two expire together (internal/migrate TTLTables, class "traces", so a changed trace
-- retention moves the exemplars with it). (2) The 1-minute rollup: metrics_1m aggregates gauges and sums per series
-- and minute and has nowhere to put a per-point trace id, so a column on the raw row would silently disappear from
-- every rolled-up range.
--
-- The row repeats the identifying columns of its data point (name, type, unit, service, host, scope, attributes,
-- resource attributes) so POST /api/v1/metrics/exemplars applies exactly the filter conditions of
-- POST /api/v1/metrics/query — the dots always mean the same series the chart drew. The repetition is cheap: the
-- processor caps exemplars hard (at most 5 per series and minute per chunk, at most 10000 per chunk), so this table
-- holds a small fraction of the rows metrics_local does, and every repeated column is LowCardinality or ZSTD.
--
-- Exemplars without a trace id are not stored: there would be nothing to open. Summary data points carry no
-- exemplars in OTLP, so only gauges, sums, histograms and exponential histograms produce rows here.
--
-- Sharded by trace_id like spans: trace ids are uniformly random, so the shards fill evenly, while host_id (the
-- metrics sharding key) is empty for application metrics and would concentrate them on one shard.

CREATE TABLE IF NOT EXISTS openlog.metric_exemplars_local ON CLUSTER '{cluster}'
(
    tenant_id           LowCardinality(String),
    metric_name         LowCardinality(String),
    metric_type         Enum8('gauge' = 1, 'sum' = 2, 'histogram' = 3, 'exponential_histogram' = 4, 'summary' = 5),
    unit                LowCardinality(String),
    service_name        LowCardinality(String),
    host_id             LowCardinality(String),
    host_name           LowCardinality(String),
    scope_name          LowCardinality(String),
    -- Same series id as the data point in metrics_local (the processor's hash of tenant, name, resource, attributes).
    series_id           UInt64,
    resource_attributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    attributes          Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    timestamp           DateTime64(9, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    -- The exemplar's own value, not the data point's: for a histogram it is the single measurement, not the mean.
    value               Float64 CODEC(Gorilla, ZSTD(1)),
    trace_id            String CODEC(ZSTD(1)),
    span_id             String CODEC(ZSTD(1)),
    -- Attributes the SDK recorded on the measurement but dropped from the metric's own attributes.
    filtered_attributes Map(LowCardinality(String), String) CODEC(ZSTD(1))
)
ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/openlog/metric_exemplars_local', '{replica}')
PARTITION BY toDate(timestamp)
ORDER BY (tenant_id, metric_name, timestamp, series_id)
TTL toDateTime(timestamp) + INTERVAL 7 DAY
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS openlog.metric_exemplars ON CLUSTER '{cluster}'
AS openlog.metric_exemplars_local
ENGINE = Distributed('{cluster}', openlog, metric_exemplars_local, cityHash64(tenant_id, trace_id));
