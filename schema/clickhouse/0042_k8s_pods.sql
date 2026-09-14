-- openlog:phase expand
-- Kubernetes pods (semantic-conventions.md §7.4, §7.6): one row per (tenant, cluster, pod uid) from openlog.k8s.pod.status
-- points of the cluster agent (host_id '', one shard per tenant). containers_json keeps the latest
-- openlog.k8s.pod.containers JSON array (links pods to containers.container_id). Mergeable columns only; the API
-- re-aggregates with GROUP BY.

CREATE TABLE IF NOT EXISTS openlog.k8s_pods_local ON CLUSTER '{cluster}'
(
    tenant_id     LowCardinality(String),
    cluster_uid   String,
    pod_uid       String,
    first_seen    SimpleAggregateFunction(min, DateTime64(9, 'UTC')),
    last_seen     SimpleAggregateFunction(max, DateTime64(9, 'UTC')),
    cluster_name  AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    namespace     AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    pod_name      AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    node_name     AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    workload_kind AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    workload_name AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    phase         AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    ready         AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    reason        AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    restarts      AggregateFunction(argMax, Int64, DateTime64(9, 'UTC')),
    pod_ip        AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    qos_class     AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    created_at    AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    started_at    AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    containers_json AggregateFunction(argMax, String, DateTime64(9, 'UTC'))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/k8s_pods_local', '{replica}')
ORDER BY (tenant_id, cluster_uid, pod_uid)
TTL toDateTime(last_seen) + INTERVAL 30 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.k8s_pods_mv ON CLUSTER '{cluster}'
TO openlog.k8s_pods_local
AS SELECT
    tenant_id,
    cuid AS cluster_uid,
    puid AS pod_uid,
    min(ts) AS first_seen,
    max(ts) AS last_seen,
    argMaxState(cname, ts) AS cluster_name,
    argMaxState(attrs['k8s.namespace.name'], ts) AS namespace,
    argMaxState(attrs['k8s.pod.name'], ts) AS pod_name,
    argMaxState(attrs['k8s.node.name'], ts) AS node_name,
    argMaxState(attrs['openlog.k8s.workload.kind'], ts) AS workload_kind,
    argMaxState(attrs['openlog.k8s.workload.name'], ts) AS workload_name,
    argMaxState(attrs['openlog.k8s.pod.phase'], ts) AS phase,
    argMaxState(attrs['openlog.k8s.pod.ready'], ts) AS ready,
    argMaxState(attrs['openlog.k8s.pod.reason'], ts) AS reason,
    argMaxState(toInt64OrZero(attrs['openlog.k8s.pod.restarts']), ts) AS restarts,
    argMaxState(attrs['openlog.k8s.pod.ip'], ts) AS pod_ip,
    argMaxState(attrs['openlog.k8s.pod.qos_class'], ts) AS qos_class,
    argMaxState(attrs['openlog.k8s.pod.created_at'], ts) AS created_at,
    argMaxState(attrs['openlog.k8s.pod.started_at'], ts) AS started_at,
    argMaxState(attrs['openlog.k8s.pod.containers'], ts) AS containers_json
FROM
(
    SELECT tenant_id, resource_attributes['k8s.cluster.uid'] AS cuid, resource_attributes['k8s.cluster.name'] AS cname,
           attributes['k8s.pod.uid'] AS puid, timestamp AS ts, CAST(attributes, 'Map(String, String)') AS attrs
    FROM openlog.metrics_local
    WHERE metric_name = 'openlog.k8s.pod.status' AND resource_attributes['k8s.cluster.uid'] != '' AND attributes['k8s.pod.uid'] != ''
)
GROUP BY tenant_id, cuid, puid;

CREATE TABLE IF NOT EXISTS openlog.k8s_pods ON CLUSTER '{cluster}'
AS openlog.k8s_pods_local
ENGINE = Distributed('{cluster}', openlog, k8s_pods_local, cityHash64(tenant_id, cluster_uid));
