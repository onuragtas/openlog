-- openlog:phase expand
-- Continuous profiling (OTLP profiles). One row per sample: the resolved call stack and the value measured
-- on it, so answering "which function burned the CPU" is a GROUP BY rather than five joins.
--
-- Why the stack is stored expanded rather than as the wire's dictionary indices: OTLP profiles are a
-- dictionary plus indices (sample -> stack -> location -> line -> function -> string), which is the right
-- shape for a payload and the wrong one for a column store. The dictionary is small and the expansion is
-- done once, at ingest (internal/profiles), so a flame graph is `SELECT stack, sum(value) GROUP BY stack`.
--
-- Sharded by cityHash64(tenant_id, service_name): a flame graph is always one service over one window, so
-- keeping a service's samples together makes it a single-shard read. Sharding by host would scatter every
-- service across the cluster and make each flame graph a fan-out.

CREATE TABLE IF NOT EXISTS openlog.profiles_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    timestamp              DateTime64(9, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    service_name           LowCardinality(String),
    service_namespace      LowCardinality(String),
    deployment_environment LowCardinality(String),
    host_id                LowCardinality(String),
    -- The profile's own sample type and unit ("cpu"/"nanoseconds", "alloc_space"/"bytes"): never invented
    -- here, so a chart can say what its numbers mean.
    profile_type           LowCardinality(String),
    unit                   LowCardinality(String),
    -- Root first, the way a flame graph reads. LowCardinality on the element: a process has thousands of
    -- distinct frames, not millions, and the same names repeat in every sample.
    stack                  Array(LowCardinality(String)) CODEC(ZSTD(1)),
    -- The innermost frame, stored rather than derived: "self time by function" is the first question asked
    -- of a profile, and arrayElement(stack, -1) in a GROUP BY would defeat the index.
    leaf                   LowCardinality(String),
    value                  Int64 CODEC(T64, ZSTD(1)),
    -- Wall time the profile covers, so a rate can be computed without a second query.
    duration_ns            UInt64 CODEC(T64, ZSTD(1)),
    resource_attributes    Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    attributes             Map(LowCardinality(String), String) CODEC(ZSTD(1))
)
ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/openlog/profiles_local', '{replica}')
PARTITION BY toDate(timestamp)
ORDER BY (tenant_id, service_name, profile_type, timestamp)
TTL toDateTime(timestamp) + INTERVAL 7 DAY
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS openlog.profiles ON CLUSTER '{cluster}'
AS openlog.profiles_local
ENGINE = Distributed('{cluster}', openlog, profiles_local, cityHash64(tenant_id, service_name));
