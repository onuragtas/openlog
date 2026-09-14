-- openlog:phase expand
-- Skip indexes for Kubernetes logs and events (semantic-conventions.md §7.2, §7.5): GET /api/v1/logs?k8s_pod_uid=… reads
-- container logs by the resource attribute k8s.pod.uid; GET /api/v1/kubernetes/events filters event_name = 'k8s.event'
-- and the involved object's uid. Parts written before this migration are not indexed (no MATERIALIZE INDEX: logs have a
-- 14-day TTL).

ALTER TABLE openlog.logs_local ON CLUSTER '{cluster}'
    ADD INDEX IF NOT EXISTS idx_k8s_pod_uid resource_attributes['k8s.pod.uid'] TYPE bloom_filter(0.01) GRANULARITY 1,
    ADD INDEX IF NOT EXISTS idx_k8s_object_uid attributes['k8s.object.uid'] TYPE bloom_filter(0.01) GRANULARITY 1,
    ADD INDEX IF NOT EXISTS idx_event_name event_name TYPE set(64) GRANULARITY 1;
