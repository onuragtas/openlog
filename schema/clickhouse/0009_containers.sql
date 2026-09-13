-- openlog:phase expand
-- Containers (semantic-conventions.md §2 "Container metrics", D-043): a container entity table and the
-- service <-> container link, both maintained by materialized views like apm_service_hosts (0006_apm),
-- plus skip indexes for filtering container logs.
--
-- containers_mv reads the infra agent's per-container data points on metrics_local, which is sharded
-- by cityHash64(tenant_id, host_id): all rows of one (tenant, host, container) land on one shard. Every
-- column is mergeable (min/max/argMax) and the API re-aggregates with GROUP BY, so re-delivered
-- blocks (deduplicated or not) never change the result. State, health and start time come only from
-- openlog.container.status points: other points use the epoch as argMax version and never win over them.

CREATE TABLE IF NOT EXISTS openlog.containers_local ON CLUSTER '{cluster}'
(
    tenant_id          LowCardinality(String),
    host_id            String,
    container_id       String,
    first_seen         SimpleAggregateFunction(min, DateTime64(9, 'UTC')),
    last_seen          SimpleAggregateFunction(max, DateTime64(9, 'UTC')),
    host_name          AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    name               AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    image_name         AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    image_tags         AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    runtime            AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    compose_project    AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    compose_service    AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    k8s_pod_name       AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    k8s_namespace_name AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    k8s_container_name AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    state              AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    health             AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    started_at         AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    restarts           AggregateFunction(argMax, Float64, DateTime64(9, 'UTC')),
    -- data point attributes of the latest openlog.container.status point
    attributes         AggregateFunction(argMax, Map(String, String), DateTime64(9, 'UTC'))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/containers_local', '{replica}')
ORDER BY (tenant_id, host_id, container_id)
TTL toDateTime(last_seen) + INTERVAL 30 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.containers_mv ON CLUSTER '{cluster}'
TO openlog.containers_local
AS SELECT
    tenant_id,
    host_id,
    cid AS container_id,
    min(ts) AS first_seen,
    max(ts) AS last_seen,
    argMaxState(hname, ts) AS host_name,
    argMaxState(attrs['container.name'], ts) AS name,
    argMaxState(attrs['container.image.name'], ts) AS image_name,
    argMaxState(attrs['container.image.tags'], ts) AS image_tags,
    argMaxState(attrs['container.runtime'], ts) AS runtime,
    argMaxState(attrs['docker.compose.project'], ts) AS compose_project,
    argMaxState(attrs['docker.compose.service'], ts) AS compose_service,
    argMaxState(attrs['k8s.pod.name'], ts) AS k8s_pod_name,
    argMaxState(attrs['k8s.namespace.name'], ts) AS k8s_namespace_name,
    argMaxState(attrs['k8s.container.name'], ts) AS k8s_container_name,
    argMaxState(attrs['openlog.container.state'], status_ts) AS state,
    argMaxState(attrs['openlog.container.health'], status_ts) AS health,
    argMaxState(attrs['openlog.container.started_at'], status_ts) AS started_at,
    argMaxState(value, restarts_ts) AS restarts,
    argMaxState(attrs, status_ts) AS attributes
FROM
(
    SELECT tenant_id, toString(host_id) AS host_id, lower(attributes['container.id']) AS cid, toString(host_name) AS hname,
           value, timestamp AS ts, CAST(attributes, 'Map(String, String)') AS attrs,
           if(metric_name = 'openlog.container.status', timestamp, toDateTime64(0, 9, 'UTC')) AS status_ts,
           if(metric_name = 'container.restarts', timestamp, toDateTime64(0, 9, 'UTC')) AS restarts_ts
    FROM openlog.metrics_local
    -- container.cpu.time: containers of agents that do not send openlog.container.status yet
    WHERE metric_name IN ('openlog.container.status', 'container.restarts', 'container.cpu.time') AND attributes['container.id'] != ''
)
GROUP BY tenant_id, host_id, container_id;

CREATE TABLE IF NOT EXISTS openlog.containers ON CLUSTER '{cluster}'
AS openlog.containers_local
ENGINE = Distributed('{cluster}', openlog, containers_local, cityHash64(tenant_id, host_id));

-- ---- service <-> container links (apm.md §1) ----
-- Spans are sharded by trace: rows of one link exist on several shards; read with GROUP BY.

CREATE TABLE IF NOT EXISTS openlog.apm_service_containers_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    container_id           String,
    service_name           LowCardinality(String),
    service_namespace      LowCardinality(String),
    deployment_environment LowCardinality(String),
    first_seen             SimpleAggregateFunction(min, DateTime64(9, 'UTC')),
    last_seen              SimpleAggregateFunction(max, DateTime64(9, 'UTC')),
    host_id                AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    container_name         AggregateFunction(argMax, String, DateTime64(9, 'UTC'))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/apm_service_containers_local', '{replica}')
ORDER BY (tenant_id, container_id, service_name, service_namespace, deployment_environment)
TTL toDateTime(last_seen) + INTERVAL 30 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.apm_service_containers_mv ON CLUSTER '{cluster}'
TO openlog.apm_service_containers_local
AS SELECT
    tenant_id,
    cid AS container_id,
    service_name,
    service_namespace,
    deployment_environment,
    min(ts) AS first_seen,
    max(ts) AS last_seen,
    argMaxState(hid, ts) AS host_id,
    argMaxState(cname, ts) AS container_name
FROM
(
    SELECT tenant_id, lower(resource_attributes['container.id']) AS cid, service_name, service_namespace, deployment_environment,
           timestamp AS ts, toString(host_id) AS hid, resource_attributes['container.name'] AS cname
    FROM openlog.spans_local
    WHERE resource_attributes['container.id'] != '' AND service_name != ''
)
GROUP BY tenant_id, container_id, service_name, service_namespace, deployment_environment;

CREATE TABLE IF NOT EXISTS openlog.apm_service_containers ON CLUSTER '{cluster}'
AS openlog.apm_service_containers_local
ENGINE = Distributed('{cluster}', openlog, apm_service_containers_local, cityHash64(tenant_id, container_id));

-- ---- container logs ----
-- Skip indexes for GET /api/v1/logs?container_id=… and the compose service filter. Parts written before
-- this migration are not indexed (no MATERIALIZE INDEX: logs have a 14-day TTL).

ALTER TABLE openlog.logs_local ON CLUSTER '{cluster}'
    ADD INDEX IF NOT EXISTS idx_container_id resource_attributes['container.id'] TYPE bloom_filter(0.01) GRANULARITY 1,
    ADD INDEX IF NOT EXISTS idx_compose_service resource_attributes['docker.compose.service'] TYPE bloom_filter(0.01) GRANULARITY 1;
