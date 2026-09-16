-- openlog:phase expand
-- 0091_log_patterns: Drain-style log patterns (D-128). The processor derives a template ("user <*> logged in from
-- <*>") and its stable id (the 64-bit hash of the template text) for every log record (internal/logpattern) and
-- writes both on the row, so POST /api/v1/logs/patterns can group records by pattern with the explorer's ordinary
-- filter conditions, and POST /api/v1/logs/query can filter by pattern_id like any other field.
--
-- The template is stored on the row rather than only in a lookup table: it is the same short string on every record
-- of a pattern, so ZSTD compresses the column to almost nothing, and both the grouping query and the unfiltered
-- rollup below read it without a join.
--
-- log_patterns_1h is the unfiltered path of the patterns list (as attribute_keys is for field keys, D-118): a
-- materialized view aggregates records, the severity mix, the newest sample body and first/last seen per
-- (tenant, hour, pattern, service). A filtered request reads the raw table instead, so the two never disagree about
-- what a filter means.
--
-- Rows written before this migration have pattern_id = 0 and an empty template; they are simply not part of any
-- pattern (the API reports them as "unclassified") and the view skips them. No backfill: logs have a 14-day TTL.
-- Mixed versions: older processors do not write the columns, older API servers do not read them.

ALTER TABLE openlog.logs_local ON CLUSTER '{cluster}'
    ADD COLUMN IF NOT EXISTS pattern_id       UInt64 CODEC(ZSTD(1)) AFTER body,
    ADD COLUMN IF NOT EXISTS pattern_template String CODEC(ZSTD(3)) AFTER pattern_id;

ALTER TABLE openlog.logs ON CLUSTER '{cluster}'
    ADD COLUMN IF NOT EXISTS pattern_id       UInt64 AFTER body,
    ADD COLUMN IF NOT EXISTS pattern_template String AFTER pattern_id;

-- Records of one pattern are spread over the whole range, so a skip index only pays off for the drill-down query
-- ("the records of this pattern"), which is exactly what the UI does. Parts written before this migration are not
-- indexed (no MATERIALIZE INDEX: logs have a 14-day TTL).
ALTER TABLE openlog.logs_local ON CLUSTER '{cluster}'
    ADD INDEX IF NOT EXISTS idx_pattern_id pattern_id TYPE bloom_filter(0.01) GRANULARITY 1;

CREATE TABLE IF NOT EXISTS openlog.log_patterns_1h_local ON CLUSTER '{cluster}'
(
    tenant_id     LowCardinality(String),
    hour          DateTime('UTC'),
    pattern_id    UInt64,
    service_name  LowCardinality(String),
    -- The id is the hash of the template, so every row of a pattern carries the same text: any() is exact.
    template      SimpleAggregateFunction(any, String),
    records       SimpleAggregateFunction(sum, UInt64),
    -- Severity mix, by the OpenTelemetry severity number ranges (1-4 trace … 21-24 fatal; 0 = unspecified).
    unspecified   SimpleAggregateFunction(sum, UInt64),
    traces        SimpleAggregateFunction(sum, UInt64),
    debugs        SimpleAggregateFunction(sum, UInt64),
    infos         SimpleAggregateFunction(sum, UInt64),
    warns         SimpleAggregateFunction(sum, UInt64),
    errors        SimpleAggregateFunction(sum, UInt64),
    fatals        SimpleAggregateFunction(sum, UInt64),
    max_severity  SimpleAggregateFunction(max, UInt8),
    first_seen    SimpleAggregateFunction(min, DateTime64(9, 'UTC')),
    last_seen     SimpleAggregateFunction(max, DateTime64(9, 'UTC')),
    -- Newest record of the pattern, as the sample shown in the list.
    sample_body   AggregateFunction(argMax, String, DateTime64(9, 'UTC'))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/log_patterns_1h_local', '{replica}')
PARTITION BY toDate(hour)
ORDER BY (tenant_id, hour, pattern_id, service_name)
TTL hour + INTERVAL 30 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS openlog.log_patterns_1h ON CLUSTER '{cluster}'
AS openlog.log_patterns_1h_local
ENGINE = Distributed('{cluster}', openlog, log_patterns_1h_local, cityHash64(tenant_id));

-- Inventory events are not application logs and are excluded here exactly as in the log endpoints.
CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.log_patterns_1h_mv ON CLUSTER '{cluster}'
TO openlog.log_patterns_1h_local
AS SELECT
    tenant_id,
    toStartOfHour(toDateTime(timestamp)) AS hour,
    pattern_id,
    service_name,
    any(pattern_template) AS template,
    toUInt64(count()) AS records,
    toUInt64(countIf(severity_number = 0)) AS unspecified,
    toUInt64(countIf(severity_number BETWEEN 1 AND 4)) AS traces,
    toUInt64(countIf(severity_number BETWEEN 5 AND 8)) AS debugs,
    toUInt64(countIf(severity_number BETWEEN 9 AND 12)) AS infos,
    toUInt64(countIf(severity_number BETWEEN 13 AND 16)) AS warns,
    toUInt64(countIf(severity_number BETWEEN 17 AND 20)) AS errors,
    toUInt64(countIf(severity_number >= 21)) AS fatals,
    max(severity_number) AS max_severity,
    min(timestamp) AS first_seen,
    max(timestamp) AS last_seen,
    argMaxState(body, timestamp) AS sample_body
FROM openlog.logs_local
WHERE pattern_id != 0 AND NOT startsWith(event_name, 'openlog.inventory.')
GROUP BY tenant_id, hour, pattern_id, service_name;
