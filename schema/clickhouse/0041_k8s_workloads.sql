-- openlog:phase expand
-- Kubernetes workloads (semantic-conventions.md §7.4, §7.6): one row per (tenant, cluster, namespace, kind, name) from
-- openlog.k8s.workload.status points of the cluster agent (host_id '', one shard per tenant). Counts are sent as
-- strings and stored as Int64 (non-numeric → 0). Mergeable columns only; the API re-aggregates with GROUP BY.

CREATE TABLE IF NOT EXISTS openlog.k8s_workloads_local ON CLUSTER '{cluster}'
(
    tenant_id    LowCardinality(String),
    cluster_uid  String,
    namespace    String,
    kind         LowCardinality(String),
    name         String,
    first_seen   SimpleAggregateFunction(min, DateTime64(9, 'UTC')),
    last_seen    SimpleAggregateFunction(max, DateTime64(9, 'UTC')),
    cluster_name AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    uid          AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    desired      AggregateFunction(argMax, Int64, DateTime64(9, 'UTC')),
    ready        AggregateFunction(argMax, Int64, DateTime64(9, 'UTC')),
    available    AggregateFunction(argMax, Int64, DateTime64(9, 'UTC')),
    updated      AggregateFunction(argMax, Int64, DateTime64(9, 'UTC')),
    created_at   AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    -- data point attributes of the latest status point (e.g. openlog.k8s.workload.suspended)
    attributes   AggregateFunction(argMax, Map(String, String), DateTime64(9, 'UTC'))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/k8s_workloads_local', '{replica}')
ORDER BY (tenant_id, cluster_uid, namespace, kind, name)
TTL toDateTime(last_seen) + INTERVAL 30 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.k8s_workloads_mv ON CLUSTER '{cluster}'
TO openlog.k8s_workloads_local
AS SELECT
    tenant_id,
    cuid AS cluster_uid,
    ns AS namespace,
    wkind AS kind,
    wname AS name,
    min(ts) AS first_seen,
    max(ts) AS last_seen,
    argMaxState(cname, ts) AS cluster_name,
    argMaxState(attrs['openlog.k8s.workload.uid'], ts) AS uid,
    argMaxState(toInt64OrZero(attrs['openlog.k8s.workload.desired']), ts) AS desired,
    argMaxState(toInt64OrZero(attrs['openlog.k8s.workload.ready']), ts) AS ready,
    argMaxState(toInt64OrZero(attrs['openlog.k8s.workload.available']), ts) AS available,
    argMaxState(toInt64OrZero(attrs['openlog.k8s.workload.updated']), ts) AS updated,
    argMaxState(attrs['openlog.k8s.workload.created_at'], ts) AS created_at,
    argMaxState(attrs, ts) AS attributes
FROM
(
    SELECT tenant_id, resource_attributes['k8s.cluster.uid'] AS cuid, resource_attributes['k8s.cluster.name'] AS cname,
           attributes['k8s.namespace.name'] AS ns, attributes['openlog.k8s.workload.kind'] AS wkind,
           attributes['openlog.k8s.workload.name'] AS wname, timestamp AS ts, CAST(attributes, 'Map(String, String)') AS attrs
    FROM openlog.metrics_local
    WHERE metric_name = 'openlog.k8s.workload.status' AND resource_attributes['k8s.cluster.uid'] != ''
      AND attributes['openlog.k8s.workload.kind'] != '' AND attributes['openlog.k8s.workload.name'] != ''
)
GROUP BY tenant_id, cuid, ns, wkind, wname;

CREATE TABLE IF NOT EXISTS openlog.k8s_workloads ON CLUSTER '{cluster}'
AS openlog.k8s_workloads_local
ENGINE = Distributed('{cluster}', openlog, k8s_workloads_local, cityHash64(tenant_id, cluster_uid));
