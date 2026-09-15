-- openlog:phase expand
-- 0090_apm_agent_versions: language agent identity per service per hour (docs/contracts/api.md "APM"
-- GET /api/v1/apm/agents, D-124). The API shows which services run an outdated openlog agent without reading the
-- resource attribute maps of raw spans: telemetry.distro.name/version, telemetry.sdk.name/language/version, distinct
-- instances (service.instance.id, else host_id) and the openlog Go instrumentation modules recognisable from the
-- instrumentation scope (otelgin, otelecho, otelgrpc).
--
-- Same sharding rules as 0006_apm: the MV on spans_local writes partial aggregates on every shard; every read
-- re-aggregates with GROUP BY through the Distributed table. Fixed 30-day TTL. No backfill: services appear with the
-- first spans ingested after this migration.
--
-- Mixed versions: older API servers do not read the table; the MV is independent of the processor version.

CREATE TABLE IF NOT EXISTS openlog.apm_agent_versions_1h_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    service_name           LowCardinality(String),
    service_namespace      LowCardinality(String),
    deployment_environment LowCardinality(String),
    timestamp              DateTime('UTC'),
    distro_name            LowCardinality(String),
    distro_version         LowCardinality(String),
    sdk_name               LowCardinality(String),
    sdk_language           LowCardinality(String),
    sdk_version            LowCardinality(String),
    span_count             SimpleAggregateFunction(sum, UInt64),
    last_seen              SimpleAggregateFunction(max, DateTime64(9, 'UTC')),
    instances              AggregateFunction(uniq, String),
    go_modules             AggregateFunction(groupUniqArrayArray(16), Array(String))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/apm_agent_versions_1h_local', '{replica}')
PARTITION BY toYYYYMM(timestamp)
ORDER BY (tenant_id, service_name, service_namespace, deployment_environment, timestamp, distro_name, distro_version, sdk_name, sdk_language, sdk_version)
TTL timestamp + INTERVAL 30 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.apm_agent_versions_1h_mv ON CLUSTER '{cluster}'
TO openlog.apm_agent_versions_1h_local
AS SELECT
    tenant_id,
    service_name,
    service_namespace,
    deployment_environment,
    toStartOfHour(ts) AS timestamp,
    d_name AS distro_name,
    d_version AS distro_version,
    s_name AS sdk_name,
    s_lang AS sdk_language,
    s_version AS sdk_version,
    toUInt64(count()) AS span_count,
    max(ts) AS last_seen,
    uniqState(instance) AS instances,
    groupUniqArrayArrayState(16)(module) AS go_modules
FROM
(
    SELECT tenant_id, service_name, service_namespace, deployment_environment, timestamp AS ts,
           resource_attributes['telemetry.distro.name'] AS d_name,
           resource_attributes['telemetry.distro.version'] AS d_version,
           resource_attributes['telemetry.sdk.name'] AS s_name,
           resource_attributes['telemetry.sdk.language'] AS s_lang,
           resource_attributes['telemetry.sdk.version'] AS s_version,
           if(resource_attributes['service.instance.id'] != '', resource_attributes['service.instance.id'], host_id) AS instance,
           if(s_lang = 'go',
              multiIf(position(scope_name, 'otelgin') > 0, ['gin'], position(scope_name, 'otelecho') > 0, ['echo'],
                      position(scope_name, 'otelgrpc') > 0, ['grpc'], CAST([], 'Array(String)')),
              CAST([], 'Array(String)')) AS module
    FROM openlog.spans_local
    WHERE service_name != ''
)
GROUP BY tenant_id, service_name, service_namespace, deployment_environment, timestamp, distro_name, distro_version, sdk_name, sdk_language, sdk_version;

CREATE TABLE IF NOT EXISTS openlog.apm_agent_versions_1h ON CLUSTER '{cluster}'
AS openlog.apm_agent_versions_1h_local
ENGINE = Distributed('{cluster}', openlog, apm_agent_versions_1h_local, cityHash64(tenant_id, service_name));
