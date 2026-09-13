-- openlog:phase expand
-- Alert evaluation summaries (docs/contracts/alerting.md §3.6). Written by the alert evaluators (openlog-alert,
-- allinone) after an evaluation committed in PostgreSQL, in batches, with direct inserts into the _local table
-- of the shard chosen by cityHash64(tenant_id, rule_id) (D-018; all rows of a rule are on one shard). Read only
-- through the Distributed table and the tenant-scoped query layer. Losing rows (process death before a flush)
-- only leaves a gap in the history; PostgreSQL stays the source of truth for state.
--
-- One row per series per evaluation (series_key != '', state after the evaluation) and one rule row
-- (series_key = '': value = number of firing series, result ok|error, duration of the evaluation).

CREATE TABLE IF NOT EXISTS openlog.alert_evaluations_local ON CLUSTER '{cluster}'
(
    tenant_id    LowCardinality(String),
    rule_id      String,
    series_key   String,
    evaluated_at DateTime64(3, 'UTC'),
    labels       Map(String, String) CODEC(ZSTD(1)),
    value        Nullable(Float64),
    state        LowCardinality(String),
    result       LowCardinality(String),
    rule_type    LowCardinality(String),
    duration_ms  UInt32
)
ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/openlog/alert_evaluations_local', '{replica}')
PARTITION BY toYYYYMMDD(evaluated_at)
ORDER BY (tenant_id, rule_id, evaluated_at, series_key)
TTL toDateTime(evaluated_at) + INTERVAL 30 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS openlog.alert_evaluations ON CLUSTER '{cluster}'
AS openlog.alert_evaluations_local
ENGINE = Distributed('{cluster}', openlog, alert_evaluations_local, cityHash64(tenant_id, rule_id));
