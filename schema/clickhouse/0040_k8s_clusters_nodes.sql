-- openlog:phase expand
-- Kubernetes clusters and nodes (semantic-conventions.md §7.4, §7.6, D-070): entity tables maintained by materialized
-- views on metrics_local, like containers_mv (0009_containers).
--
-- The cluster agent sends no host.id: its points land on the shard of cityHash64(tenant_id, ''), so every row of one
-- cluster is on one shard. Every column is mergeable (min/max/argMax keyed by the point timestamp) and the API
-- re-aggregates with GROUP BY, so re-delivered blocks never change the result. The cluster is identified by the
-- resource attribute k8s.cluster.uid (uid of the kube-system namespace); points without it are ignored.

CREATE TABLE IF NOT EXISTS openlog.k8s_clusters_local ON CLUSTER '{cluster}'
(
    tenant_id    LowCardinality(String),
    cluster_uid  String,
    first_seen   SimpleAggregateFunction(min, DateTime64(9, 'UTC')),
    last_seen    SimpleAggregateFunction(max, DateTime64(9, 'UTC')),
    cluster_name AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    version      AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    nodes        AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    namespaces   AggregateFunction(argMax, String, DateTime64(9, 'UTC'))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/k8s_clusters_local', '{replica}')
ORDER BY (tenant_id, cluster_uid)
TTL toDateTime(last_seen) + INTERVAL 30 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.k8s_clusters_mv ON CLUSTER '{cluster}'
TO openlog.k8s_clusters_local
AS SELECT
    tenant_id,
    cuid AS cluster_uid,
    min(ts) AS first_seen,
    max(ts) AS last_seen,
    argMaxState(cname, ts) AS cluster_name,
    argMaxState(attrs['openlog.k8s.cluster.version'], ts) AS version,
    argMaxState(attrs['openlog.k8s.cluster.nodes'], ts) AS nodes,
    argMaxState(attrs['openlog.k8s.cluster.namespaces'], ts) AS namespaces
FROM
(
    SELECT tenant_id, resource_attributes['k8s.cluster.uid'] AS cuid, resource_attributes['k8s.cluster.name'] AS cname,
           timestamp AS ts, CAST(attributes, 'Map(String, String)') AS attrs
    FROM openlog.metrics_local
    WHERE metric_name = 'openlog.k8s.cluster.status' AND resource_attributes['k8s.cluster.uid'] != ''
)
GROUP BY tenant_id, cuid;

CREATE TABLE IF NOT EXISTS openlog.k8s_clusters ON CLUSTER '{cluster}'
AS openlog.k8s_clusters_local
ENGINE = Distributed('{cluster}', openlog, k8s_clusters_local, cityHash64(tenant_id, cluster_uid));

-- ---- nodes (openlog.k8s.node.status) ----

CREATE TABLE IF NOT EXISTS openlog.k8s_nodes_local ON CLUSTER '{cluster}'
(
    tenant_id         LowCardinality(String),
    cluster_uid       String,
    node_name         String,
    first_seen        SimpleAggregateFunction(min, DateTime64(9, 'UTC')),
    last_seen         SimpleAggregateFunction(max, DateTime64(9, 'UTC')),
    cluster_name      AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    node_uid          AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    ready             AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    unschedulable     AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    roles             AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    kubelet_version   AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    os_image          AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    container_runtime AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    internal_ip       AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    created_at        AggregateFunction(argMax, String, DateTime64(9, 'UTC'))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/k8s_nodes_local', '{replica}')
ORDER BY (tenant_id, cluster_uid, node_name)
TTL toDateTime(last_seen) + INTERVAL 30 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.k8s_nodes_mv ON CLUSTER '{cluster}'
TO openlog.k8s_nodes_local
AS SELECT
    tenant_id,
    cuid AS cluster_uid,
    node AS node_name,
    min(ts) AS first_seen,
    max(ts) AS last_seen,
    argMaxState(cname, ts) AS cluster_name,
    argMaxState(attrs['k8s.node.uid'], ts) AS node_uid,
    argMaxState(attrs['openlog.k8s.node.ready'], ts) AS ready,
    argMaxState(attrs['openlog.k8s.node.unschedulable'], ts) AS unschedulable,
    argMaxState(attrs['openlog.k8s.node.roles'], ts) AS roles,
    argMaxState(attrs['openlog.k8s.node.kubelet_version'], ts) AS kubelet_version,
    argMaxState(attrs['openlog.k8s.node.os_image'], ts) AS os_image,
    argMaxState(attrs['openlog.k8s.node.container_runtime'], ts) AS container_runtime,
    argMaxState(attrs['openlog.k8s.node.internal_ip'], ts) AS internal_ip,
    argMaxState(attrs['openlog.k8s.node.created_at'], ts) AS created_at
FROM
(
    SELECT tenant_id, resource_attributes['k8s.cluster.uid'] AS cuid, resource_attributes['k8s.cluster.name'] AS cname,
           attributes['k8s.node.name'] AS node, timestamp AS ts, CAST(attributes, 'Map(String, String)') AS attrs
    FROM openlog.metrics_local
    WHERE metric_name = 'openlog.k8s.node.status' AND resource_attributes['k8s.cluster.uid'] != '' AND attributes['k8s.node.name'] != ''
)
GROUP BY tenant_id, cuid, node;

CREATE TABLE IF NOT EXISTS openlog.k8s_nodes ON CLUSTER '{cluster}'
AS openlog.k8s_nodes_local
ENGINE = Distributed('{cluster}', openlog, k8s_nodes_local, cityHash64(tenant_id, cluster_uid));
