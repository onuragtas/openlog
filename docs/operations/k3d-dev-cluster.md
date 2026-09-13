# Runbook: openlog cluster profile on k3d

Local 3-agent Kubernetes cluster running the `cluster` profile with Strimzi Kafka (KRaft, 3 brokers),
Altinity ClickHouse (2 shards x 1 replica, Keeper x3), one replica of each openlog service and Envoy in
front of ingest. Use it for load tests (ingest/processor 1 -> 2 -> 4) and chaos tests
(see [04-cluster.md](../plan/04-cluster.md)).

Chart: [`deploy/helm/openlog`](../../deploy/helm/openlog) with `values-dev.yaml`.
Using kind instead of k3d: [kind-dev-cluster.md](kind-dev-cluster.md) (sections 1-4 differ, the rest is shared);
measured results: [benchmarks/2026-09-13-kind.md](benchmarks/2026-09-13-kind.md).

## 0. Prerequisites

- Docker with at least **8 CPUs / 12 GiB RAM** allocated to it (Kafka x3 + ClickHouse x2 + Keeper x3 + services).
- `k3d` >= 5.6, `kubectl`, `helm` >= 3.14, `jq`.
- A checkout of the repo; commands below run from the repo root.

## 1. Create the cluster

```bash
k3d cluster create openlog \
  --agents 3 \
  --k3s-arg "--disable=traefik@server:0" \
  --k3s-node-label "topology.kubernetes.io/zone=zone-a@agent:0" \
  --k3s-node-label "topology.kubernetes.io/zone=zone-b@agent:1" \
  --k3s-node-label "topology.kubernetes.io/zone=zone-c@agent:2" \
  --wait

kubectl get nodes -o wide
```

Traefik is disabled: the dev setup reaches services with port-forward and uses Envoy for OTLP
balancing. k3s ships `local-path` storage (the default StorageClass, which `values-dev.yaml` uses) and metrics-server (needed by HPAs).

Optional: keep workloads off the server node so the three agents behave like three worker nodes:

```bash
kubectl taint node k3d-openlog-server-0 node-role.kubernetes.io/control-plane=:NoSchedule
```

## 2. Install the operators

Neither operator is vendored in the chart.

```bash
# Strimzi cluster operator (KRaft; tested 1.2.0 - Strimzi >= 1.0 needs the chart default kafka.strimzi.apiVersion=kafka.strimzi.io/v1)
helm install strimzi oci://quay.io/strimzi-helm/strimzi-kafka-operator --version 1.2.0 \
  -n strimzi --create-namespace \
  --set watchAnyNamespace=true \
  --wait

# Altinity clickhouse-operator (tested 0.27.3; chart >= 0.27 watches only its own namespace unless watchNamespaces is set)
helm repo add altinity https://docs.altinity.com/clickhouse-operator/
helm repo update
helm install clickhouse-operator altinity/altinity-clickhouse-operator --version 0.27.3 \
  -n clickhouse-operator --create-namespace --set "watchNamespaces={openlog}" --wait

kubectl get crd | grep -E 'strimzi|altinity'
```

Expected CRDs include `kafkas.kafka.strimzi.io`, `kafkanodepools.kafka.strimzi.io`,
`kafkatopics.kafka.strimzi.io`, `clickhouseinstallations.clickhouse.altinity.com` and
`clickhousekeeperinstallations.clickhouse-keeper.altinity.com`.

If you prefer plain manifests instead of Helm:
`kubectl create -f 'https://strimzi.io/install/latest?namespace=strimzi' -n strimzi` (then allow
it to watch the `openlog` namespace, see Strimzi docs) and
`kubectl apply -f https://raw.githubusercontent.com/Altinity/clickhouse-operator/master/deploy/operator/clickhouse-operator-install-bundle.yaml`.

## 3. Build and import the image

```bash
docker build -t ghcr.io/onuragtas/openlog:dev .
k3d image import ghcr.io/onuragtas/openlog:dev -c openlog
```

`values-dev.yaml` uses `tag: dev` with `pullPolicy: IfNotPresent`, so the imported image is used.
After rebuilding, re-import and `kubectl -n openlog rollout restart deploy`.

## 4. Install openlog

```bash
helm install openlog deploy/helm/openlog \
  -n openlog --create-namespace \
  -f deploy/helm/openlog/values-dev.yaml \
  --timeout 30m
```

Do **not** add `--wait` (the migrate hook runs post-install in operators mode; see the chart README).
In another terminal:

```bash
kubectl -n openlog get kafka,kafkanodepool,kafkatopic,chk,chi
kubectl -n openlog get pods -o wide -w
```

Bring-up order: Keeper (3 pods `chk-openlog-keeper-0-*`), ClickHouse (`chi-openlog-openlog-0-0-0`,
`chi-openlog-openlog-1-0-0`), Kafka (`openlog-dual-0..2`) and entity operator, KafkaTopics `Ready`,
then the `openlog-migrate` Job completes and `helm install` returns. It typically takes 5-10 minutes.

Verify:

```bash
kubectl -n openlog get kafkatopic -o custom-columns=NAME:.spec.topicName,PARTS:.spec.partitions,RF:.spec.replicas,READY:.status.conditions[0].type
kubectl -n openlog exec chi-openlog-openlog-0-0-0 -- clickhouse-client -q "SHOW TABLES FROM openlog"
kubectl -n openlog exec chi-openlog-openlog-0-0-0 -- clickhouse-client -q \
  "SELECT cluster, shard_num, replica_num, host_name FROM system.clusters WHERE cluster='openlog'"
kubectl -n openlog get deploy
```

If the migrate Job fails: `kubectl -n openlog logs job/openlog-migrate` (the Job stays around after a
failure), fix, then `helm upgrade openlog deploy/helm/openlog -n openlog -f deploy/helm/openlog/values-dev.yaml --timeout 30m`.

## 5. Port-forward

```bash
kubectl -n openlog port-forward svc/openlog-envoy 4317:4317 4318:4318 &   # OTLP through Envoy
kubectl -n openlog port-forward svc/openlog-api 8080:8080 &
kubectl -n openlog port-forward deploy/openlog-kafka-exporter 9308:9404 &   # consumer lag (Strimzi exporter)

# values-dev.yaml bootstraps tenant `dev` with owner admin@openlog.local / openlog-dev-password and the
# ingest key dev-license-key. The query API takes a session or a read-only API key (Settings → API keys),
# not the ingest key:
curl -s -c /tmp/openlog.jar -H 'Content-Type: application/json' \
  -d '{"email":"admin@openlog.local","password":"openlog-dev-password"}' http://localhost:8080/api/v1/auth/login >/dev/null
curl -s -b /tmp/openlog.jar http://localhost:8080/api/v1/hosts | jq .
```
Open <http://localhost:8080> to use the UI with the same account.

Strimzi 1.x creates **no Service** for the Kafka exporter and serves metrics on port **9404**
(`deploy/<kafka>-kafka-exporter`), hence the port-forward to the Deployment. The exporter reports
`kafka_consumergroup_lag` = -1 for partitions without a committed offset yet; ignore negative values
when summing.

## 6. Generate load with openlog-loadgen

Run the load generator **inside the cluster** so port-forward does not become the bottleneck.
`openlog-loadgen` ships in the same image (`/usr/local/bin/openlog-loadgen`). It sends OTLP/HTTP
(protobuf) for host metrics, inventory snapshots, logs and spans.

| Flag | Default | Meaning |
|---|---|---|
| `-endpoint` | `http://localhost:4318` | OTLP/HTTP base URL of openlog-ingest (or Envoy) |
| `-license-key` | `$OPENLOG_LICENSE_KEY` | required |
| `-hosts` | `10` | simulated hosts |
| `-host-prefix` | `loadgen` | host name prefix |
| `-interval` | `10s` | host metrics interval |
| `-logs-per-sec` / `-spans-per-sec` | `100` / `100` | rates across all hosts |
| `-inventory-interval` | `10m` | inventory snapshot interval per host |
| `-duration` | `0` | stop after this long (0 = until interrupted) |
| `-concurrency` | `8` | concurrent HTTP requests |
| `-gzip` | `true` | gzip request bodies |

```bash
kubectl -n openlog run loadgen --restart=Never --image=ghcr.io/onuragtas/openlog:dev \
  --image-pull-policy=IfNotPresent \
  --command -- /usr/local/bin/openlog-loadgen \
    -endpoint=http://openlog-envoy.openlog.svc:4318 \
    -license-key=dev-license-key \
    -hosts=200 -logs-per-sec=20000 -spans-per-sec=20000 \
    -concurrency=32 -duration=10m

kubectl -n openlog logs -f loadgen
```

Loadgen prints a status line periodically:
`requests=… errors=… | datapoints=… logs=… spans=… inventory_records=…`. The datapoints, logs
and spans counters only count requests ingest answered with HTTP 200, so the final line gives the
**accepted** numbers for the comparisons below. `errors` counts rejected or failed requests (for
example 503 during a Kafka fault). Loadgen does not retry them, so they are not part of "accepted".
If the pod finishes before you read the logs, `kubectl -n openlog logs loadgen | tail -1` still
shows the last line. Delete it with `kubectl -n openlog delete pod loadgen` before starting another run.

## 7. Scale and observe throughput

Watch these while scaling (Prometheus or plain `curl` on the admin ports):

```bash
# consumer lag per topic (total)
curl -s localhost:9308/metrics | grep '^kafka_consumergroup_lag{' | grep openlog-processor \
  | awk -F'topic="' '{split($2,a,"\""); s[a[1]]+=$NF} END {for (t in s) print t, s[t]}'

# rows stored per second (run twice, 10s apart)
kubectl -n openlog exec chi-openlog-openlog-0-0-0 -- clickhouse-client -q \
  "SELECT 'logs', count() FROM openlog.logs UNION ALL SELECT 'spans', count() FROM openlog.spans UNION ALL SELECT 'metrics', count() FROM openlog.metrics"

# CPU per pod
kubectl -n openlog top pods
```

Steps (hold each for ~3 minutes, raise the loadgen rate until lag grows with 1 processor):

```bash
kubectl -n openlog scale deploy/openlog-processor --replicas=2
kubectl -n openlog scale deploy/openlog-processor --replicas=4
kubectl -n openlog scale deploy/openlog-ingest --replicas=2
kubectl -n openlog scale deploy/openlog-ingest --replicas=4

# consumer group assignment (partitions per member)
kubectl -n openlog exec openlog-dual-0 -c kafka -- /opt/kafka/bin/kafka-consumer-groups.sh \
  --bootstrap-server localhost:9092 --describe --group openlog-processor
```

Expected:

- Processor: lag stops growing and ClickHouse rows/s rises roughly linearly until ClickHouse or
  partitions (12 in dev) become the limit. More than 12 processor replicas would sit idle.
- Ingest: with Envoy, new ingest pods receive requests within ~5 s (DNS refresh) even though
  loadgen reuses its keep-alive connections. Compare by pointing loadgen at `http://openlog-ingest.openlog.svc:4318`
  instead: kube-proxy balances connections, so new pods get little or nothing from loadgen's
  existing connections. Long-lived OTLP/gRPC clients (SDKs, infra agent) are affected even more -
  that is why Envoy is there.
- The chart HPAs are disabled in dev; `kubectl scale` is enough. To test HPA:
  `helm upgrade ... --set processor.autoscaling.enabled=true --set processor.autoscaling.maxReplicas=12`.

## 8. Chaos tests

Run each test under constant load. Before each one, note `produced` (loadgen counters) and
`stored` (ClickHouse counts). After loadgen finishes, wait until consumer lag reaches 0, then compare.

```bash
cat > /tmp/openlog-counts.sql <<'EOF'
SELECT 'logs'  AS t, count() AS rows FROM openlog.logs
UNION ALL SELECT 'spans', count() FROM openlog.spans
UNION ALL SELECT 'spans_unique', uniqExact(trace_id, span_id) FROM openlog.spans
UNION ALL SELECT 'metrics', count() FROM openlog.metrics
EOF
kubectl -n openlog exec -i chi-openlog-openlog-0-0-0 -- clickhouse-client --multiquery < /tmp/openlog-counts.sql
```

For a clean run, start from empty tables:
`kubectl -n openlog exec chi-openlog-openlog-0-0-0 -- clickhouse-client -q "TRUNCATE TABLE openlog.logs_local ON CLUSTER openlog"`
(repeat for `spans_local`, `metrics_local`, and the rollup/index tables if you check them).

Acceptance (at-least-once): `stored >= accepted` for every signal, i.e. **no loss** of anything
ingest acknowledged. Small duplicates (`spans - spans_unique > 0`) are allowed after rebalances
([04-cluster.md](../plan/04-cluster.md), open item 2). Requests rejected with 503 (+ `Retry-After`)
during the fault are not "accepted" and must have been retried by the client.

### 8.1 Kill a Kafka broker

```bash
kubectl -n openlog delete pod openlog-dual-1
kubectl -n openlog get pods -w -l strimzi.io/cluster=openlog
```

With RF=3 and `min.insync.replicas=2`, producers (`acks=all`) keep succeeding with 2 of 3 replicas;
partition leadership moves within seconds. Expect a short latency spike in ingest and possibly a
consumer-group rebalance. Check under-replicated partitions return to 0 after the pod is back:

```bash
kubectl -n openlog exec openlog-dual-0 -c kafka -- /opt/kafka/bin/kafka-topics.sh \
  --bootstrap-server localhost:9092 --describe --under-replicated-partitions
```

Stronger variant: delete two brokers - produces must fail (`503`/`UNAVAILABLE` from ingest)
rather than be acknowledged with a single replica.

### 8.2 Kill a ClickHouse pod

```bash
kubectl -n openlog delete pod chi-openlog-openlog-1-0-0
```

The dev layout is 2 shards x **1** replica, so shard 1 is unavailable until the pod restarts.
Processor inserts into the Distributed tables (`distributed_foreground_insert=1`) fail, offsets are
not committed, lag grows, and after the pod returns the processor retries the same batches with the
same `insert_deduplication_token` - no loss, no duplicates from the retries. API queries touching
that shard fail meanwhile. To test the replica case (reads and writes continue), upgrade with
`--set clickhouse.altinity.replicas=2` (needs ~2 GiB more RAM) and delete one replica.

### 8.3 Kill a processor pod

```bash
kubectl -n openlog scale deploy/openlog-processor --replicas=3
POD=$(kubectl -n openlog get pod -l app.kubernetes.io/component=processor -o name | head -1)
kubectl -n openlog delete "$POD"                             # graceful: flush + commit
# harsher: no graceful flush
POD=$(kubectl -n openlog get pod -l app.kubernetes.io/component=processor -o name | head -1)
kubectl -n openlog delete "$POD" --grace-period=0 --force
```

Graceful delete: the pod flushes and commits within `terminationGracePeriodSeconds`. Forced delete:
uncommitted records are re-consumed by another member after the rebalance; rows may be duplicated
if batch boundaries differ (visible as `spans - spans_unique`), never lost.

### 8.4 Kill an ingest pod / Envoy

```bash
kubectl -n openlog delete pod -l app.kubernetes.io/component=ingest
```

In-flight requests on that pod fail and are retried by the client; acknowledged data is already in
Kafka. Envoy health checks (`/readyz`) and outlier detection remove the endpoint.

## 9. Teardown

```bash
helm uninstall openlog -n openlog
kubectl -n openlog delete secret openlog-credentials   # kept by resource-policy
kubectl delete ns openlog
k3d cluster delete openlog
```
