# Runbook: openlog cluster profile on kind

Same topology as the [k3d runbook](k3d-dev-cluster.md) (Strimzi Kafka KRaft x3, Altinity ClickHouse
2 shards x 1 replica + Keeper x3, one replica of each openlog service, Envoy in front of ingest), on
[kind](https://kind.sigs.k8s.io/) with 1 control-plane + 3 workers. Sections 5-9 of the k3d runbook
(port-forward, loadgen, scaling, chaos, teardown) apply unchanged apart from the notes below.
Results of the first run: [benchmarks/2026-09-13-kind.md](benchmarks/2026-09-13-kind.md).

Tested versions (2026-09-13): kind v0.33.0 (node image `kindest/node:v1.37.0`), kubectl v1.37.0,
Helm v4.3.0, Strimzi operator chart 1.2.0 (Kafka 4.3.1), Altinity clickhouse-operator chart 0.27.3,
metrics-server chart 3.14.0, ClickHouse/Keeper 25.8, Envoy v1.35.3, on OrbStack (10 CPU, 16 GiB).

## 0. Prerequisites

- Docker with >= 8 CPUs / 12 GiB RAM (the stack idles at ~5 GiB; ~7 GiB under load).
- `kind` >= 0.33, `kubectl` within one minor version of the node image (v1.37 here), `helm` >= 3.14, `jq`.

## 1. Create the cluster

```bash
kind create cluster --config deploy/kind/cluster.yaml --wait 5m
kubectl config use-context kind-openlog-dev
kubectl get nodes -L topology.kubernetes.io/zone
```

[`deploy/kind/cluster.yaml`](../../deploy/kind/cluster.yaml) labels each worker with a zone. Differences
from k3d:

| | k3d | kind |
|---|---|---|
| Default StorageClass | `local-path` | `standard` (same rancher local-path provisioner) |
| metrics-server | built in | **not installed** - needed for `kubectl top` and HPAs |
| Image import | `k3d image import` | `kind load docker-image` |
| Control-plane node | tainted only if you taint it | tainted `NoSchedule` for regular pods |

`values-dev.yaml` uses the cluster's default StorageClass (`storageClass: ""`), so it works on both.

metrics-server (kubelets in kind use self-signed certificates):

```bash
helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/
helm install metrics-server metrics-server/metrics-server --version 3.14.0 -n kube-system \
  --set 'args={--kubelet-insecure-tls}' --wait
```

## 2. Install the operators

```bash
# Strimzi >= 1.0 serves only kafka.strimzi.io/v1 (the chart default). For 0.46-0.48 set
# kafka.strimzi.apiVersion=kafka.strimzi.io/v1beta2.
helm install strimzi oci://quay.io/strimzi-helm/strimzi-kafka-operator --version 1.2.0 \
  -n strimzi --create-namespace --set watchAnyNamespace=true --wait

helm repo add altinity https://docs.altinity.com/clickhouse-operator/
# Chart >= 0.27 watches ONLY its own namespace unless watchNamespaces is set.
helm install clickhouse-operator altinity/altinity-clickhouse-operator --version 0.27.3 \
  -n clickhouse-operator --create-namespace --set 'watchNamespaces={openlog}' --wait
```

Symptom of a missing `watchNamespaces`: the `ClickHouseInstallation` / `ClickHouseKeeperInstallation`
stay without a `STATUS`, no `chi-*`/`chk-*` pods appear, and the migrate Job retries forever.

## 3. Build and load the image

```bash
docker build -t ghcr.io/onuragtas/openlog:dev .
kind load docker-image ghcr.io/onuragtas/openlog:dev --name openlog-dev
# optional, avoids pulls inside the nodes:
kind load docker-image clickhouse/clickhouse-server:25.8 envoyproxy/envoy:v1.35.3 --name openlog-dev
```

After a rebuild: load again, then `kubectl -n openlog rollout restart deploy`.

## 4. Install openlog

```bash
helm install openlog deploy/helm/openlog -n openlog --create-namespace \
  -f deploy/helm/openlog/values-dev.yaml --timeout 30m
```

No `--wait` (post-install migrate hook, see the chart README). On this laptop the whole stack
(Keeper, ClickHouse, Kafka, topics, migrations) was Ready ~3 minutes after `helm install`.

Verify (in addition to the k3d runbook's checks):

```bash
# schema on every shard (20 objects each) and ON CLUSTER DDL finished everywhere
kubectl -n openlog exec chi-openlog-openlog-0-0-0 -- clickhouse-client -q \
  "SELECT hostName(), count() FROM clusterAllReplicas('openlog', system.tables) WHERE database='openlog' GROUP BY 1"
kubectl -n openlog exec chi-openlog-openlog-0-0-0 -- clickhouse-client -q \
  "SELECT hostName(), count(), countIf(status='Finished') FROM clusterAllReplicas('openlog', system.distributed_ddl_queue) GROUP BY 1"
# readiness of each service
for c in ingest processor api; do
  kubectl -n openlog exec deploy/openlog-$c -- wget -qO- http://127.0.0.1:9464/readyz; echo
done
```

## 5-9. Load, scaling, chaos, teardown

Follow the [k3d runbook](k3d-dev-cluster.md#5-port-forward). kind-specific notes:

- Consumer lag: Strimzi 1.x creates no Service for the Kafka exporter; it listens on 9404
  (`kubectl -n openlog port-forward deploy/openlog-kafka-exporter 9308:9404`, then
  `curl -s localhost:9308/metrics | grep kafka_consumergroup_lag`; -1 = no committed offset yet).
  Do **not** poll `kafka-consumer-groups.sh` inside a broker pod in a loop: each run starts a JVM in
  the broker's memory cgroup and can get the broker itself OOM-killed. A one-off run is fine.
- Run loadgen as Jobs on the control-plane node (toleration for its taint +
  `nodeSelector: {kubernetes.io/hostname: openlog-dev-control-plane}`) so it does not compete with
  the workers' pods for scheduling. All kind nodes still share the same Docker VM CPUs.
- Pods run unlimited in `values-dev.yaml`; on a 10-CPU laptop a single unlimited ingest or
  processor pod cannot be saturated. For scaling measurements give them a fixed CPU size, e.g.
  `kubectl -n openlog set resources deploy/openlog-ingest deploy/openlog-processor --requests=cpu=300m --limits=cpu=300m`.
- `/var` of all kind nodes is the same Docker VM disk. With high load, lower topic retention for
  the test (`kubectl -n openlog patch kafkatopic openlog-otlp-logs-v1 --type merge -p '{"spec":{"config":{"retention.ms":1800000,"segment.bytes":104857600}}}'`, same for metrics/traces).

### Pitfalls seen on this setup

- **Helm 4 + `kubectl scale`:** Helm 4 applies manifests server-side. After `kubectl scale`, the
  next `helm upgrade` fails with `conflict with "kubectl" with subresource "scale" ... .spec.replicas`
  (and leaves a `failed` revision with some objects already updated). Either set replicas through
  values, or re-run the upgrade with `--force-conflicts`.
- **ClickHouse memory:** ClickHouse caps itself at 90% of the container limit and enforces the cap
  against RSS, which includes the mark cache (default 5 GiB). With a 2Gi limit and a few tens of
  millions of rows stored, RSS sat at the cap and every insert failed with `MEMORY_LIMIT_EXCEEDED`.
  `values-dev.yaml` therefore uses 3Gi and `mark_cache_size: 268435456`.
- **Processor memory on a backlog:** a processor that starts on a large consumer lag decodes
  big fetches at once; 50k-row log/span batches peak near 1 GiB. `values-dev.yaml` gives it a 2Gi limit.
- **Retention vs. disk:** do not shorten topic retention below the longest backlog you may create.
  If processors stall (e.g. ClickHouse down) for longer than `retention.ms`, unconsumed records are
  deleted and the consumer silently resets to the earliest offset. Check with
  `kafka-consumer-groups.sh --describe` (current offset) vs. `kafka-get-offsets.sh --time -2` (earliest).
- **Envoy and pod churn:** Envoy discovers ingest pods through the headless Service (`STRICT_DNS`), and
  kind's CoreDNS caches answers for 30 s, so Envoy can keep connecting to a deleted ingest pod for
  up to ~30 s. The chart handles this with an ingest `preStop` sleep of 15 s, route retries on
  connection failures (`connect-failure`, `refused-stream`, `reset-before-request`) with a
  `retry_budget`, and local-origin outlier ejection after 2 connection failures. Before those
  fixes, deleting one ingest pod under load cost ~250 client 503s and an ingest rollout ~380.
- **Reinstall after `helm uninstall`:** wait until `kubectl -n openlog get kafkatopic` is empty before
  installing again; otherwise the install can end with no topics (every OTLP request 503) - run
  `helm upgrade` with the same values to recreate them.
- **Keeper placement:** the chart now spreads Keeper pods over nodes (soft anti-affinity), but
  local-path volumes pin a pod to the node its PV was first created on. Existing installations keep
  their placement; recreate the Keeper PVCs to move them.

Teardown:

```bash
helm uninstall openlog -n openlog
kubectl -n openlog delete pvc --all
kubectl -n openlog delete secret openlog-credentials
kind delete cluster --name openlog-dev
```
