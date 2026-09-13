-- openlog:phase expand
-- Application and system logs. Inventory events are routed to the inventory tables and are NOT stored here.

CREATE TABLE IF NOT EXISTS openlog.logs_local ON CLUSTER '{cluster}'
(
    tenant_id           LowCardinality(String),
    timestamp           DateTime64(9, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    observed_timestamp  DateTime64(9, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    service_name        LowCardinality(String),
    host_id             LowCardinality(String),
    host_name           LowCardinality(String),
    severity_text       LowCardinality(String),
    severity_number     UInt8,
    trace_id            String CODEC(ZSTD(1)),
    span_id             String CODEC(ZSTD(1)),
    trace_flags         UInt8,
    event_name          LowCardinality(String),
    body                String CODEC(ZSTD(3)),
    resource_attributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    scope_name          LowCardinality(String),
    attributes          Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    INDEX idx_trace_id trace_id TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_body lower(body) TYPE tokenbf_v1(32768, 3, 0) GRANULARITY 1
)
ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/openlog/logs_local', '{replica}')
PARTITION BY toDate(timestamp)
ORDER BY (tenant_id, service_name, host_id, timestamp)
TTL toDateTime(timestamp) + INTERVAL 14 DAY
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS openlog.logs ON CLUSTER '{cluster}'
AS openlog.logs_local
ENGINE = Distributed('{cluster}', openlog, logs_local, cityHash64(tenant_id, host_id));
