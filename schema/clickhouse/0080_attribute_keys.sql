-- openlog:phase expand
-- 0080_attribute_keys: hourly index of attribute keys per tenant and signal for the query builders' key autocomplete
-- (GET /api/v1/fields/keys, GET /api/v1/metrics/{name}; docs/contracts/api.md "Fields", D-118). One materialized view per
-- signal table explodes the attribute and resource attribute maps of every inserted block and aggregates per
-- (tenant, signal, metric, hour, source, key): records with the key, records whose value parses as a number / is a
-- boolean (type inference) and an approximate distinct value count. Values themselves are not stored (they are read
-- from a bounded sample of the raw table, GET /api/v1/fields/values).
--
-- Rows written before this migration are not indexed (no backfill: the API samples the raw tables when the index has
-- no rows for a range). Mixed versions: older binaries do not read the table; the views only add work on insert.

CREATE TABLE IF NOT EXISTS openlog.attribute_keys_local ON CLUSTER '{cluster}'
(
    tenant_id      LowCardinality(String),
    signal         LowCardinality(String),
    -- metrics: the metric name; '' for logs and traces
    metric_name    LowCardinality(String),
    -- attribute | resource
    source         LowCardinality(String),
    key            String,
    hour           DateTime('UTC'),
    events         SimpleAggregateFunction(sum, UInt64),
    numeric_events SimpleAggregateFunction(sum, UInt64),
    bool_events    SimpleAggregateFunction(sum, UInt64),
    value_uniq     AggregateFunction(uniqCombined(12), String)
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/attribute_keys_local', '{replica}')
PARTITION BY toDate(hour)
ORDER BY (tenant_id, signal, metric_name, hour, source, key)
TTL hour + INTERVAL 30 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS openlog.attribute_keys ON CLUSTER '{cluster}'
AS openlog.attribute_keys_local
ENGINE = Distributed('{cluster}', openlog, attribute_keys_local, cityHash64(tenant_id));

-- Keys longer than 256 bytes are not indexed (the API rejects them as filter keys).
CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.attribute_keys_logs_mv ON CLUSTER '{cluster}'
TO openlog.attribute_keys_local
AS SELECT
    tenant_id,
    'logs' AS signal,
    '' AS metric_name,
    tupleElement(kv, 1) AS source,
    tupleElement(kv, 2) AS key,
    toStartOfHour(toDateTime(ts)) AS hour,
    toUInt64(count()) AS events,
    toUInt64(countIf(isNotNull(toFloat64OrNull(tupleElement(kv, 3))))) AS numeric_events,
    toUInt64(countIf(tupleElement(kv, 3) IN ('true', 'false'))) AS bool_events,
    uniqCombinedState(12)(tupleElement(kv, 3)) AS value_uniq
FROM
(
    SELECT
        tenant_id,
        timestamp AS ts,
        arrayJoin(arrayConcat(
            arrayMap((k, v) -> ('attribute', k, v), mapKeys(CAST(attributes, 'Map(String, String)')), mapValues(CAST(attributes, 'Map(String, String)'))),
            arrayMap((k, v) -> ('resource', k, v), mapKeys(CAST(resource_attributes, 'Map(String, String)')), mapValues(CAST(resource_attributes, 'Map(String, String)')))
        )) AS kv
    FROM openlog.logs_local
)
WHERE length(tupleElement(kv, 2)) BETWEEN 1 AND 256
GROUP BY tenant_id, signal, metric_name, source, key, hour;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.attribute_keys_spans_mv ON CLUSTER '{cluster}'
TO openlog.attribute_keys_local
AS SELECT
    tenant_id,
    'traces' AS signal,
    '' AS metric_name,
    tupleElement(kv, 1) AS source,
    tupleElement(kv, 2) AS key,
    toStartOfHour(toDateTime(ts)) AS hour,
    toUInt64(count()) AS events,
    toUInt64(countIf(isNotNull(toFloat64OrNull(tupleElement(kv, 3))))) AS numeric_events,
    toUInt64(countIf(tupleElement(kv, 3) IN ('true', 'false'))) AS bool_events,
    uniqCombinedState(12)(tupleElement(kv, 3)) AS value_uniq
FROM
(
    SELECT
        tenant_id,
        timestamp AS ts,
        arrayJoin(arrayConcat(
            arrayMap((k, v) -> ('attribute', k, v), mapKeys(CAST(attributes, 'Map(String, String)')), mapValues(CAST(attributes, 'Map(String, String)'))),
            arrayMap((k, v) -> ('resource', k, v), mapKeys(CAST(resource_attributes, 'Map(String, String)')), mapValues(CAST(resource_attributes, 'Map(String, String)')))
        )) AS kv
    FROM openlog.spans_local
)
WHERE length(tupleElement(kv, 2)) BETWEEN 1 AND 256
GROUP BY tenant_id, signal, metric_name, source, key, hour;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.attribute_keys_metrics_mv ON CLUSTER '{cluster}'
TO openlog.attribute_keys_local
AS SELECT
    tenant_id,
    'metrics' AS signal,
    mname AS metric_name,
    tupleElement(kv, 1) AS source,
    tupleElement(kv, 2) AS key,
    toStartOfHour(toDateTime(ts)) AS hour,
    toUInt64(count()) AS events,
    toUInt64(countIf(isNotNull(toFloat64OrNull(tupleElement(kv, 3))))) AS numeric_events,
    toUInt64(countIf(tupleElement(kv, 3) IN ('true', 'false'))) AS bool_events,
    uniqCombinedState(12)(tupleElement(kv, 3)) AS value_uniq
FROM
(
    SELECT
        tenant_id,
        metric_name AS mname,
        timestamp AS ts,
        arrayJoin(arrayConcat(
            arrayMap((k, v) -> ('attribute', k, v), mapKeys(CAST(attributes, 'Map(String, String)')), mapValues(CAST(attributes, 'Map(String, String)'))),
            arrayMap((k, v) -> ('resource', k, v), mapKeys(CAST(resource_attributes, 'Map(String, String)')), mapValues(CAST(resource_attributes, 'Map(String, String)')))
        )) AS kv
    FROM openlog.metrics_local
)
WHERE length(tupleElement(kv, 2)) BETWEEN 1 AND 256
GROUP BY tenant_id, signal, metric_name, source, key, hour;
