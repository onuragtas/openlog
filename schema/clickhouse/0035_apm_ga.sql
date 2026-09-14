-- openlog:phase expand
-- APM GA (docs/contracts/apm.md §3.3, §12): error group occurrences per dimension (affected versions, hosts,
-- containers, transactions; regression detection) and span counts per service.version per minute (deployments).
--
-- Same sharding rules as 0006_apm: MVs on spans_local write partial aggregates on every shard; every read
-- re-aggregates with GROUP BY through the Distributed tables. Fixed 30-day TTL (not OPENLOG_APM_RETENTION_DAYS).

CREATE TABLE IF NOT EXISTS openlog.apm_error_group_dims_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    service_name           LowCardinality(String),
    service_namespace      LowCardinality(String),
    deployment_environment LowCardinality(String),
    error_group_id         UInt64,
    -- version (resource service.version), host (host_id), container (lower-cased container.id),
    -- transaction (transaction_name, else the span name)
    dim                    LowCardinality(String),
    value                  String,
    first_seen             SimpleAggregateFunction(min, DateTime64(9, 'UTC')),
    last_seen              SimpleAggregateFunction(max, DateTime64(9, 'UTC')),
    count                  SimpleAggregateFunction(sum, Float64),
    samples                SimpleAggregateFunction(sum, UInt64)
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/apm_error_group_dims_local', '{replica}')
ORDER BY (tenant_id, service_name, service_namespace, deployment_environment, error_group_id, dim, value)
TTL toDateTime(last_seen) + INTERVAL 30 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.apm_error_group_dims_mv ON CLUSTER '{cluster}'
TO openlog.apm_error_group_dims_local
AS SELECT
    tenant_id,
    service_name,
    service_namespace,
    deployment_environment,
    error_group_id,
    tupleElement(d, 1) AS dim,
    tupleElement(d, 2) AS value,
    min(ts) AS first_seen,
    max(ts) AS last_seen,
    sum(w) AS count,
    toUInt64(count()) AS samples
FROM
(
    SELECT tenant_id, service_name, service_namespace, deployment_environment, error_group_id,
           timestamp AS ts, sample_weight AS w,
           arrayJoin([
               ('version', resource_attributes['service.version']),
               ('host', toString(host_id)),
               ('container', lower(resource_attributes['container.id'])),
               ('transaction', if(transaction_name != '', toString(transaction_name), toString(name)))
           ]) AS d
    FROM openlog.spans_local
    WHERE error_group_id != 0 AND service_name != ''
)
WHERE tupleElement(d, 2) != ''
GROUP BY tenant_id, service_name, service_namespace, deployment_environment, error_group_id, dim, value;

CREATE TABLE IF NOT EXISTS openlog.apm_error_group_dims ON CLUSTER '{cluster}'
AS openlog.apm_error_group_dims_local
ENGINE = Distributed('{cluster}', openlog, apm_error_group_dims_local, cityHash64(tenant_id, service_name));

CREATE TABLE IF NOT EXISTS openlog.apm_service_versions_1m_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    service_name           LowCardinality(String),
    service_namespace      LowCardinality(String),
    deployment_environment LowCardinality(String),
    timestamp              DateTime('UTC'),
    service_version        String,
    span_count             SimpleAggregateFunction(sum, UInt64),
    first_seen             SimpleAggregateFunction(min, DateTime64(9, 'UTC')),
    last_seen              SimpleAggregateFunction(max, DateTime64(9, 'UTC'))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/apm_service_versions_1m_local', '{replica}')
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (tenant_id, service_name, service_namespace, deployment_environment, timestamp, service_version)
TTL timestamp + INTERVAL 30 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.apm_service_versions_1m_mv ON CLUSTER '{cluster}'
TO openlog.apm_service_versions_1m_local
AS SELECT
    tenant_id,
    service_name,
    service_namespace,
    deployment_environment,
    toStartOfMinute(ts) AS timestamp,
    ver AS service_version,
    toUInt64(count()) AS span_count,
    min(ts) AS first_seen,
    max(ts) AS last_seen
FROM
(
    SELECT tenant_id, service_name, service_namespace, deployment_environment, timestamp AS ts,
           resource_attributes['service.version'] AS ver
    FROM openlog.spans_local
    WHERE service_name != ''
)
GROUP BY tenant_id, service_name, service_namespace, deployment_environment, timestamp, service_version;

CREATE TABLE IF NOT EXISTS openlog.apm_service_versions_1m ON CLUSTER '{cluster}'
AS openlog.apm_service_versions_1m_local
ENGINE = Distributed('{cluster}', openlog, apm_service_versions_1m_local, cityHash64(tenant_id, service_name));
