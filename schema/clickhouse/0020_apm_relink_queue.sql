-- openlog:phase expand
-- Late-span re-link queue (docs/contracts/apm.md §6 "Late-span re-link"). The processor writes one row per
-- (tenant, client minute) of a chunk when spans that can change trace-linked edges arrive later than
-- OPENLOG_APM_RELINK_AFTER; the edge-linking job on the api leader reads new rows through the Distributed table
-- (by enqueued_at) and re-links those minutes on every shard.
--
-- Plain MergeTree: duplicates (several chunks, re-delivery without deduplication) are harmless, the reader groups by
-- minute. trace_id is the first late trace of the minute in the chunk; the sharding key is the one of spans, so a row
-- lands on the shard of that trace. The TTL (8 days on enqueued_at) exceeds OPENLOG_APM_RELINK_MAX_AGE (≤ 7 days by
-- default, the spans TTL).

CREATE TABLE IF NOT EXISTS openlog.apm_relink_queue_local ON CLUSTER '{cluster}'
(
    tenant_id   LowCardinality(String),
    minute      DateTime('UTC'),
    trace_id    String CODEC(ZSTD(1)),
    spans       UInt32,
    enqueued_at DateTime64(3, 'UTC')
)
ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/openlog/apm_relink_queue_local', '{replica}')
PARTITION BY toYYYYMMDD(enqueued_at)
ORDER BY (enqueued_at, tenant_id, minute)
TTL toDateTime(enqueued_at) + INTERVAL 8 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS openlog.apm_relink_queue ON CLUSTER '{cluster}'
AS openlog.apm_relink_queue_local
ENGINE = Distributed('{cluster}', openlog, apm_relink_queue_local, cityHash64(tenant_id, trace_id));
