-- openlog:phase expand
-- Usage metering (docs/contracts/usage.md, D-079). Everything billable is derived from stored rows, never from
-- ingest-side counters alone:
--
--   usage_signals_1h   items and estimated uncompressed bytes per tenant / hour / signal / service / host, filled by
--                      materialized views on spans_local, logs_local and metrics_local.
--   usage_entities_1d  distinct hosts, containers and APM services per tenant / day (same MVs' sources).
--   usage_ingest_1h    uncompressed OTLP protobuf bytes and export requests per tenant / hour / signal, written by the
--                      processor with the chunk's insert_deduplication_token (like every other table it writes).
--   usage_queries_1h   query compute per tenant / hour / component, collected from system.query_log (log_comment
--                      tenant_id) by the api leader and re-collected (ReplacingMergeTree, version collected_at).
--
-- Idempotency: the processor inserts with insert_deduplication_token and
-- deduplicate_blocks_in_dependent_materialized_views=1 (internal/store/clickhouse InsertSettings), so a re-delivered
-- chunk that the source table drops is not counted again by the MVs. Sharding: MVs write partial aggregates on every
-- shard; every read re-aggregates with GROUP BY (sum / uniqExact) through the Distributed tables.
-- Retention: 400 days (fixed; not managed by openlog-migrate TTL ownership, D-067).

CREATE TABLE IF NOT EXISTS openlog.usage_signals_1h_local ON CLUSTER '{cluster}'
(
    tenant_id    LowCardinality(String),
    hour         DateTime('UTC'),
    -- traces | logs | metrics (queue.Signal)
    signal       LowCardinality(String),
    service_name LowCardinality(String),
    host_id      String,
    items        UInt64,
    bytes        UInt64
)
ENGINE = ReplicatedSummingMergeTree('/clickhouse/tables/{shard}/openlog/usage_signals_1h_local', '{replica}', (items, bytes))
PARTITION BY toYYYYMM(hour)
ORDER BY (tenant_id, hour, signal, service_name, host_id)
TTL hour + INTERVAL 400 DAY;

CREATE TABLE IF NOT EXISTS openlog.usage_signals_1h ON CLUSTER '{cluster}'
AS openlog.usage_signals_1h_local
ENGINE = Distributed('{cluster}', openlog, usage_signals_1h_local, cityHash64(tenant_id, host_id));

-- Estimated uncompressed size of a row: byteSize of the variable-size columns plus the fixed-size ones.
CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.usage_signals_spans_mv ON CLUSTER '{cluster}'
TO openlog.usage_signals_1h_local
AS SELECT
    tenant_id,
    toStartOfHour(ts) AS hour,
    'traces' AS signal,
    svc AS service_name,
    hid AS host_id,
    toUInt64(count()) AS items,
    toUInt64(sum(sz)) AS bytes
FROM
(
    SELECT tenant_id, timestamp AS ts, service_name AS svc, toString(host_id) AS hid,
           byteSize(trace_id, span_id, parent_span_id, trace_state, name, status_message, service_name, host_id,
                    resource_attributes, scope_name, attributes, events_timestamp, events_name, events_attributes,
                    links_trace_id, links_span_id) + 18 AS sz
    FROM openlog.spans_local
)
GROUP BY tenant_id, hour, service_name, host_id;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.usage_signals_logs_mv ON CLUSTER '{cluster}'
TO openlog.usage_signals_1h_local
AS SELECT
    tenant_id,
    toStartOfHour(ts) AS hour,
    'logs' AS signal,
    svc AS service_name,
    hid AS host_id,
    toUInt64(count()) AS items,
    toUInt64(sum(sz)) AS bytes
FROM
(
    SELECT tenant_id, timestamp AS ts, service_name AS svc, toString(host_id) AS hid,
           byteSize(service_name, host_id, host_name, severity_text, trace_id, span_id, event_name, body,
                    resource_attributes, scope_name, attributes) + 18 AS sz
    FROM openlog.logs_local
)
GROUP BY tenant_id, hour, service_name, host_id;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.usage_signals_metrics_mv ON CLUSTER '{cluster}'
TO openlog.usage_signals_1h_local
AS SELECT
    tenant_id,
    toStartOfHour(ts) AS hour,
    'metrics' AS signal,
    svc AS service_name,
    hid AS host_id,
    toUInt64(count()) AS items,
    toUInt64(sum(sz)) AS bytes
FROM
(
    SELECT tenant_id, timestamp AS ts, service_name AS svc, toString(host_id) AS hid,
           byteSize(metric_name, unit, description, service_name, host_id, host_name, resource_attributes, scope_name,
                    attributes, bucket_counts, explicit_bounds) + 55 AS sz
    FROM openlog.metrics_local
)
GROUP BY tenant_id, hour, service_name, host_id;

-- ---- active entities per day ----

CREATE TABLE IF NOT EXISTS openlog.usage_entities_1d_local ON CLUSTER '{cluster}'
(
    tenant_id LowCardinality(String),
    day       Date,
    -- host | container | service
    kind      LowCardinality(String),
    entity    String
)
ENGINE = ReplicatedReplacingMergeTree('/clickhouse/tables/{shard}/openlog/usage_entities_1d_local', '{replica}')
PARTITION BY toYYYYMM(day)
ORDER BY (tenant_id, day, kind, entity)
TTL day + INTERVAL 400 DAY;

CREATE TABLE IF NOT EXISTS openlog.usage_entities_1d ON CLUSTER '{cluster}'
AS openlog.usage_entities_1d_local
ENGINE = Distributed('{cluster}', openlog, usage_entities_1d_local, cityHash64(tenant_id, entity));

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.usage_entities_metrics_mv ON CLUSTER '{cluster}'
TO openlog.usage_entities_1d_local
AS SELECT tenant_id, d AS day, e.1 AS kind, e.2 AS entity
FROM
(
    SELECT tenant_id, toDate(timestamp) AS d,
           arrayJoin(arrayFilter(x -> x.2 != '', [('host', toString(host_id)), ('container', lower(attributes['container.id']))])) AS e
    FROM openlog.metrics_local
)
GROUP BY tenant_id, day, kind, entity;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.usage_entities_spans_mv ON CLUSTER '{cluster}'
TO openlog.usage_entities_1d_local
AS SELECT tenant_id, d AS day, e.1 AS kind, e.2 AS entity
FROM
(
    SELECT tenant_id, toDate(timestamp) AS d,
           arrayJoin(arrayFilter(x -> x.2 != '', [('host', toString(host_id)), ('service', toString(service_name)),
                                                  ('container', lower(resource_attributes['container.id']))])) AS e
    FROM openlog.spans_local
)
GROUP BY tenant_id, day, kind, entity;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.usage_entities_logs_mv ON CLUSTER '{cluster}'
TO openlog.usage_entities_1d_local
AS SELECT tenant_id, d AS day, e.1 AS kind, e.2 AS entity
FROM
(
    SELECT tenant_id, toDate(timestamp) AS d,
           arrayJoin(arrayFilter(x -> x.2 != '', [('host', toString(host_id)), ('container', lower(resource_attributes['container.id']))])) AS e
    FROM openlog.logs_local
)
GROUP BY tenant_id, day, kind, entity;

-- ---- ingested OTLP bytes (processor) ----

CREATE TABLE IF NOT EXISTS openlog.usage_ingest_1h_local ON CLUSTER '{cluster}'
(
    tenant_id LowCardinality(String),
    hour      DateTime('UTC'),
    signal    LowCardinality(String),
    requests  UInt64,
    bytes     UInt64
)
ENGINE = ReplicatedSummingMergeTree('/clickhouse/tables/{shard}/openlog/usage_ingest_1h_local', '{replica}', (requests, bytes))
PARTITION BY toYYYYMM(hour)
ORDER BY (tenant_id, hour, signal)
TTL hour + INTERVAL 400 DAY;

CREATE TABLE IF NOT EXISTS openlog.usage_ingest_1h ON CLUSTER '{cluster}'
AS openlog.usage_ingest_1h_local
ENGINE = Distributed('{cluster}', openlog, usage_ingest_1h_local, cityHash64(tenant_id, signal));

-- ---- query compute (api leader, from system.query_log) ----

CREATE TABLE IF NOT EXISTS openlog.usage_queries_1h_local ON CLUSTER '{cluster}'
(
    tenant_id        LowCardinality(String),
    hour             DateTime('UTC'),
    -- api | alert
    component        LowCardinality(String),
    queries          UInt64,
    failed           UInt64,
    read_rows        UInt64,
    read_bytes       UInt64,
    cpu_microseconds UInt64,
    memory_bytes     UInt64,
    collected_at     DateTime64(3, 'UTC')
)
ENGINE = ReplicatedReplacingMergeTree('/clickhouse/tables/{shard}/openlog/usage_queries_1h_local', '{replica}', collected_at)
PARTITION BY toYYYYMM(hour)
ORDER BY (tenant_id, hour, component)
TTL hour + INTERVAL 400 DAY;

CREATE TABLE IF NOT EXISTS openlog.usage_queries_1h ON CLUSTER '{cluster}'
AS openlog.usage_queries_1h_local
ENGINE = Distributed('{cluster}', openlog, usage_queries_1h_local, cityHash64(tenant_id));
