# Runbook: scaling the openlog cluster

Principle ([04-cluster.md](../plan/04-cluster.md)): services are stateless; state lives in Kafka and
ClickHouse. Scale a service by adding replicas; scale Kafka by partitions and brokers; scale ClickHouse
by shards (write capacity) and replicas (read capacity and durability).

Data path and where each limit shows up:

```
clients --> Envoy --> openlog-ingest xN --> Kafka (P partitions/topic) --> openlog-processor xM (M <= P) --> ClickHouse S shards x R replicas
             L7        CPU, produce latency     disk, network, ISR          CPU, lag                      parts, merges, insert latency
```

## 1. Key metrics

Names of openlog's own metrics other than `openlog_processor_records_rejected_total{reason}` are not yet
fixed by a contract; use the ones exported on `:9464/metrics` for request rate, latency and
status codes. The Kafka and ClickHouse metrics below are standard.

| Area | Signal | Healthy | Action when not |
|---|---|---|---|
| Ingest | CPU per pod (HPA target 70%) | < 70% | more ingest replicas |
| Ingest | Rate of 503/`UNAVAILABLE` (with `Retry-After`) | ~0 | Kafka produce is slow or unavailable: check brokers, not ingest |
| Ingest | Rate of 429 | expected only for tenants over their limits | per-tenant limits, not capacity (D-014) |
| Ingest | Request latency p99 | < produce timeout (10s) with margin | brokers / ISR |
| Ingest | Requests per pod (Envoy `envoy_cluster_upstream_rq_total` per host) | even | check L7 balancing |
| Kafka | `kafka_consumergroup_lag` (group `openlog-processor`), sum and max per partition | flat, low | more processors, or ClickHouse is slow |
| Kafka | Under-replicated partitions, offline partitions | 0 | broker health, disk |
| Kafka | Broker disk usage | < 70% at 24h retention | more brokers / disk, or rebalance |
| Kafka | Request handler idle ratio, produce p99 | > 30%, low | more brokers |
| Processor | CPU per pod | < 70% | more processors (until M = P) |
| Processor | `openlog_processor_records_rejected_total` | 0 | producer/schema bug |
| Processor | Consumer group rebalances per hour | low | slow down HPA scale-down |
| ClickHouse | `ClickHouseProfileEvents_InsertedRows`, insert query p99 | stable | more shards |
| ClickHouse | Active parts per partition (`system.parts`), `DelayedInserts`, `RejectedInserts` | < 300 parts, 0 | bigger batches, more shards |
| ClickHouse | `ReplicasMaxQueueSize`, `ReplicasMaxAbsoluteDelay` | low | replica / Keeper health |
| ClickHouse | Background merge pool utilization, disk | < 80% | more shards, disk |
| ClickHouse | Query p95 (API) | targets in 04-cluster.md | more replicas (reads) |
| Keeper | Latency (`mntr`: `zk_avg_latency`), outstanding requests | < 10 ms, ~0 | faster disk, dedicated nodes |

Useful ClickHouse queries (run on any node):

```sql
-- parts per table per shard host
SELECT hostName(), table, count() AS parts
FROM clusterAllReplicas('openlog', system.parts)
WHERE database = 'openlog' AND active GROUP BY 1, 2 ORDER BY parts DESC;

-- insert rate over the last 5 minutes
SELECT hostName(), sum(written_rows) / 300 AS rows_per_s
FROM clusterAllReplicas('openlog', system.query_log)
WHERE type = 'QueryFinish' AND query_kind = 'Insert' AND event_time > now() - 300
GROUP BY 1;

-- data balance across shards
SELECT hostName(), table, formatReadableSize(sum(bytes_on_disk))
FROM clusterAllReplicas('openlog', system.parts)
WHERE database = 'openlog' AND active GROUP BY 1, 2 ORDER BY 2, 1;
```

### Processor metrics and consumer lag alerts

| Metric | Meaning |
|---|---|
| `openlog_processor_consumer_lag_records{topic,partition}` | Log end offset minus committed offset for the partitions assigned to this processor pod (refreshed every 15 s via the group coordinator). Partitions without an owner have no series. |
| `openlog_processor_consumer_lag_seconds{topic}` | Age of the oldest record that this pod has fetched or is behind on and not yet inserted and committed (record timestamp = ingest produce time), max over its partitions. Computed at scrape time, so it keeps growing while inserts are stuck. Normal: about 1.5 × `OPENLOG_PROCESSOR_FLUSH_INTERVAL`. |
| `openlog_processor_insert_failures_total` | Failed insert attempts (retried with the same tokens). |
| `openlog_processor_chunk_cuts_total{reason}` | Chunk boundaries by rule; `window`/`bytes` are deterministic, a high share of `memory` means `OPENLOG_PROCESSOR_MAX_BUFFERED_BYTES` is too small (see kafka.md). |
| `openlog_processor_shard_insert_duration_seconds{shard}`, `openlog_processor_shard_insert_failures_total{shard,replica}`, `openlog_processor_clickhouse_shards` | `direct` insert mode: latency per shard, replica failovers, shards in the topology in use. |

**Retention is the loss boundary.** If records are older than the topic's `retention.ms` before they are
committed, Kafka deletes them and the group resumes at the earliest retained offset (benchmark
2026-09-13: a 17-minute ClickHouse outage came within ~10 minutes of that with a 30-minute retention).
Alert on lag in seconds against retention, not on records. Example Prometheus rules for the default
`retention.ms` = 86400000 (24 h); scale the thresholds with your retention:

```yaml
groups:
  - name: openlog-processor
    rules:
      - alert: OpenlogProcessorLagHigh
        # 25% of retention.ms (86400 s): hours of headroom left before data loss.
        expr: max by (topic) (openlog_processor_consumer_lag_seconds) > 0.25 * 86400
        for: 5m
        labels: { severity: warning }
        annotations:
          summary: "openlog processor is {{ $value | humanizeDuration }} behind on {{ $labels.topic }} (retention 24h)"
      - alert: OpenlogProcessorLagCritical
        # 50% of retention.ms: unconsumed telemetry will be deleted by Kafka if this continues.
        expr: max by (topic) (openlog_processor_consumer_lag_seconds) > 0.5 * 86400
        for: 1m
        labels: { severity: critical }
      - alert: OpenlogProcessorLagGrowing
        expr: sum by (topic) (openlog_processor_consumer_lag_records) > 100000
          and deriv(sum by (topic) (openlog_processor_consumer_lag_records)[15m:1m]) > 0
        for: 15m
        labels: { severity: warning }
      - alert: OpenlogProcessorInsertsFailing
        expr: sum(rate(openlog_processor_insert_failures_total[5m])) > 0
        for: 10m
        labels: { severity: warning }
      - alert: OpenlogProcessorAbsent
        # No processor exports lag: nobody consumes. Cross-check with kafka_consumergroup_lag
        # from a Kafka exporter, which does not depend on the processor being alive.
        expr: absent(openlog_processor_consumer_lag_seconds)
        for: 10m
        labels: { severity: critical }
```

## 2. Ingest

**Add replicas when** CPU is above target, or request latency rises while Kafka produce latency
is normal. Ingest does no batching of its own; throughput scales with replicas as long as Kafka
keeps up. Target from 04-cluster.md: >= 50k spans or logs per second per pod - measure your own.

- The HPA (CPU) handles normal growth: `ingest.autoscaling.{minReplicas,maxReplicas}`.
- Keep `minReplicas` >= 2 (3 in production) for PDB-safe node drains.
- **Do not add ingest replicas for 503 errors.** Ingest returns 503/`UNAVAILABLE` with `Retry-After`
  when Kafka does not acknowledge in time or is unavailable; more ingest pods add more load to Kafka.
  429 is reserved for per-tenant limits (D-014) and is not a capacity signal either.
- New replicas only take gRPC traffic if balancing is L7 (Envoy, gRPC-aware Ingress, GRPCRoute).
  Check requests per upstream host in Envoy stats after a scale-up. Behind an L4 LoadBalancer,
  also scale Envoy; `envoy.maxConnectionDuration` makes clients reconnect so load spreads across Envoy pods.
- Request body limit is 10 MiB decompressed; very large batches from agents increase memory per request.
  Raise `ingest.resources.limits.memory` before raising `OPENLOG_INGEST_MAX_BODY_BYTES`.

## 3. Processor vs. partitions

A consumer group assigns each partition to exactly one member, so **useful processor replicas <=
partitions per topic** (each processor consumes all three topics). Extra replicas sit idle. The chart
fails to render if `processor.autoscaling.maxReplicas` or `processor.replicaCount` exceeds
`kafka.topics.partitions`.

**Add processor replicas when** consumer lag grows steadily and processor CPU is high while
ClickHouse insert latency is normal.

**Do not add processors when** lag grows but processor CPU is low: the bottleneck is ClickHouse
(check insert latency, parts, `DelayedInserts`). More processors then just create more concurrent
inserts and more parts. Scale ClickHouse instead, or raise `OPENLOG_PROCESSOR_BATCH_ROWS` /
`OPENLOG_PROCESSOR_FLUSH_INTERVAL` to insert fewer, larger batches.

Every membership change triggers a rebalance: consumption pauses briefly. Buffered, uncommitted records
of revoked partitions are dropped and re-read by the new owner, which cuts the same chunks and dedup
tokens, so a rebalance normally writes nothing twice (remaining cases: kafka.md, "Consumption semantics").
Each processor inserts one block per active partition, table and shard per flush interval, so at low load
ClickHouse sees about `partitions / OPENLOG_PROCESSOR_FLUSH_INTERVAL` inserts per table per shard (48
partitions, 2 s: 24/s); raise the flush interval if parts pile up. Hence the slow scale-down policy in the chart
(`processor.autoscaling.behavior`). For lag-driven autoscaling, consider KEDA's Kafka scaler
(`lagThreshold`) instead of CPU; keep `maxReplicaCount` <= partitions.

Sizing rule of thumb: `partitions = 2-4 x expected steady-state processor replicas`, so there is headroom
to scale without repartitioning. The cluster profile default is 48.

### Increasing partitions safely

Partitions can only be increased, never decreased. Increasing them changes the key -> partition mapping
(`hash(key) % partitions`) for **new** records:

- Records for one key (`<tenant>/<host.id>` for metrics and logs, `<tenant>/<trace_id>` for traces)
  that were produced before the change stay in the old partition; new ones may go to a different one.
  During the drain window, ordering per host is not guaranteed. Kafka.md relies on this ordering so
  inventory items arrive before their "snapshot complete" record; a snapshot that straddles the
  change can be marked complete before all its items are stored. The next inventory snapshot corrects
  it. No data is lost.
- Ingest producers and processors pick up the new partitions from metadata refresh automatically
  (default `metadata.max.age.ms` 5 min); no restart is needed, but a rebalance will happen.

Procedure:

1. Pick a low-traffic window. Confirm consumer lag is near 0 (so little old-mapping data remains).
2. Increase partitions for all three topics to the same value (keep them equal; the chart uses one value).
   - operators mode: `helm upgrade ... --set kafka.topics.partitions=96` (the Topic Operator applies it).
   - external mode: `kafka-topics.sh --alter --topic openlog.otlp.metrics.v1 --partitions 96` for each topic.
     (`openlog-migrate` does not alter existing topics.)
3. Increase `processor.autoscaling.maxReplicas` in the same upgrade if needed.
4. If Kafka broker count changed or partition sizes are uneven, rebalance replicas:
   Strimzi Cruise Control (`kafka.strimzi.cruiseControl.enabled=true` + a `KafkaRebalance` resource),
   or `kafka-reassign-partitions.sh` in external mode.
5. Watch under-replicated partitions and consumer lag until stable.

Partition count also costs broker memory, file handles and leader-election time; stay within a few
thousand partition replicas per broker.

## 4. Kafka brokers

**Add brokers when** broker disk passes ~70% at the configured retention, network or request handler
threads saturate, or produce p99 rises with healthy ISR. New brokers receive no existing partitions
until you rebalance (Cruise Control `KafkaRebalance` with `mode: add-brokers`). Increase the broker
node pool `replicas` in values; never decrease it without first moving partitions off the removed
brokers (`mode: remove-brokers`).

Keep RF=3 / `min.insync.replicas=2`; this is what makes "kill one broker" safe. With RF=3 you can lose
one broker per partition without failed produces; losing two fails produces (ingest returns 503) instead
of losing acknowledged data.

## 5. ClickHouse: shards vs. replicas

| Need | Add | Why |
|---|---|---|
| More insert throughput, merges keeping up, more storage | **Shards** | Each shard holds a disjoint part of the data and does its own merges. |
| More concurrent queries, fault tolerance | **Replicas** | Each replica holds a full copy of its shard; Distributed reads pick one replica per shard. |

Signals for **more shards**: insert latency rising, parts per partition growing, `DelayedInserts` /
"Too many parts", merges saturated, disk > 70%. Signals for **more replicas**: query CPU saturated
while inserts are fine, or a durability requirement (production should run >= 2 replicas).

### Adding replicas

`helm upgrade ... --set clickhouse.altinity.replicas=3`. The Altinity operator creates the pods,
creates the schema on them, and ReplicatedMergeTree fetches data from Keeper-coordinated peers.
Watch `system.replication_queue` until empty. Replicas add Keeper load and network during catch-up.

### Adding shards (and resharding caveats)

`helm upgrade ... --set clickhouse.altinity.shards=4`. The operator adds hosts to `remote_servers` and
creates the tables on the new shards; the `pre-upgrade` migrate hook re-applies the idempotent schema
as well (confirm with `SELECT hostName(), count() FROM clusterAllReplicas('openlog', system.tables)
WHERE database='openlog' GROUP BY 1`).

Caveats - adding shards does **not** reshard existing data:

1. **Existing data stays where it is.** The Distributed tables route new rows by
   `cityHash64(tenant_id, host_id)` (metrics, logs, hosts, inventory) or `cityHash64(tenant_id, trace_id)`
   (spans) modulo the total shard weight. New shards start empty and fill only with new data, so disk
   usage stays uneven until TTL expires old data (spans 7d, logs 14d, metrics 30d, rollups 395d).
   Queries stay correct because Distributed reads all shards.
2. **The same key moves to a different shard.** After the change, a host's rows (or a trace's spans)
   may be on two shards: old rows on the old shard, new rows on the new one.
   - `ReplacingMergeTree` tables (`hosts`, `inventory_snapshots`) deduplicate only within a shard. Reads
     with `FINAL` on the Distributed table can then return two rows for one host until the old row
     expires; reads using `argMax` across the Distributed table stay correct. Prefer argMax in the API.
   - `AggregatingMergeTree` rollups (`metrics_1m`, `trace_index`) need a final `GROUP BY` across shards
     (which Distributed queries do anyway) - fine.
   - Traces that are in flight when the layout changes can be split across shards; trace lookup
     through `trace_index` + Distributed still works.
3. **Do not change the shard key or the shard count back down** without a data migration.
   Removing a shard deletes its data from queries.
4. **To rebalance old data** (rarely worth it given TTLs): create a new cluster layout or new tables,
   `INSERT INTO openlog.<table> SELECT * FROM remote(...)` in partition-sized chunks, verify counts,
   then swap. Alternatively use shard `weight` in `remote_servers` to steer more new data to new
   shards temporarily (requires custom operator configuration).
5. A single very large tenant/host can still overload one shard; see 04-cluster.md open item 3.

### Keeper

Keep 3 nodes (5 for very large clusters). Keeper is latency-sensitive: fast disks, dedicated nodes
if possible. Keeper trouble shows up as read-only replicas and stuck `ON CLUSTER` DDL, which blocks
`openlog-migrate`.

## 6. API

Scale on CPU (HPA). Query cost is bounded by `OPENLOG_API_QUERY_TIMEOUT` (ClickHouse `max_execution_time`)
and `OPENLOG_API_MAX_ROWS`. If API pods are idle but queries are slow, the bottleneck is ClickHouse: add
replicas (concurrency) or shards (scan parallelism).

## 7. Quick decision table

| Symptom | Likely cause | Do |
|---|---|---|
| Ingest CPU high, no 503 | ingest capacity | + ingest replicas |
| Ingest 503 + Retry-After, ingest CPU low | Kafka slow / ISR shrunk | check brokers; + brokers |
| Ingest 429 for one tenant | tenant over its limit | tenant limits, not scaling |
| New ingest pods idle | L4 balancing of gRPC | enable Envoy / gRPC-aware ingress |
| Lag growing, processor CPU high, CH inserts fast | processor capacity | + processors (<= partitions) |
| Lag growing, processor CPU low, CH inserts slow | ClickHouse write capacity | + shards; larger batches |
| Processors == partitions and still lagging | partition ceiling | increase partitions (section 3) |
| Too many parts / DelayedInserts | small or too many concurrent inserts | larger batches; + shards |
| API slow, CH query CPU high | read capacity | + replicas |
| Replicas read-only, DDL stuck | Keeper | Keeper health, disk latency |
