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

## Validation run: operators mode + CloudNativePG + TLS (2026-09-14)

Script: [`deploy/helm/test/kind-validate.sh`](../../deploy/helm/test/kind-validate.sh) (`STEPS=base,failover,tls`,
`KEEP=1` keeps the cluster). It encodes the steps below, which were run by hand on kind v0.33.0 (`kindest/node:v1.37.0`,
1 control-plane + 2 workers, cluster `openlog-r-kind`), Helm v4.3.0, Strimzi chart 1.2.0, Altinity operator chart
0.27.3, CloudNativePG chart 0.26.1 (operator 1.27.1), cert-manager v1.21.2, openlog image built from the tree and
loaded with `kind load` (`pullPolicy: Never`). Tiny sizing: Kafka 1 dual node (RF 1, 3 partitions), ClickHouse
1 shard x 2 replicas, Keeper x3, CNPG 2 instances, one replica per service, no Envoy.

| Step | Result |
|---|---|
| Install (no `--wait`) | Keeper, ClickHouse, Kafka, CNPG, migrations and bootstrap done ~3 min after `helm install`; 87 schema objects on both replicas; org `Dev` + owner created |
| loadgen (2 min, 3 hosts, 50 logs/s + 50 spans/s) | 0 request errors; 5950 logs / 5950 spans / 1520 datapoints in ClickHouse; `/api/v1/hosts`, `/api/v1/logs`, `POST /api/v1/query` (session login) answer with the data |
| CNPG failover (`kubectl delete pod` of the primary) | API kept answering 200. The old primary stayed `Terminating` for ~3 min 12 s (smart shutdown waits for the openlog pods' pooled connections, CNPG default `smartShutdownTimeout` 180 s), then promotion took ~1 s with a single 503 on `/auth/me`. The chart now sets `postgres.operator.smartShutdownTimeout: 15` (not re-measured) |
| ClickHouse server TLS, step A (`clickhouse.tls.server.secretName`, `verificationMode: strict`) | 9440/8443 listening, `remote_servers` on 9440 with `<secure>1</secure>`, rolling restart ~2.5 min; `clickhouse-client --secure` with a client certificate works, without one the handshake is reset |
| Step B (`clickhouse.tls.enabled`, `clientSecret`) | openlog on `clickhouse-openlog:9440` with client certificate; loadgen again 0 errors, rows doubled; no TLS warnings |
| Keeper + replication TLS (D-093, on by default with server TLS) | ClickHouse connects to `secure://keeper-openlog.openlog.svc:9281`; Raft "SSL enabled" (leader + 2 synced followers); 9009 gone, 9010 listening; replicas register `scheme: https` / port 9010 in Keeper and download parts (`DownloadPart` without error), both replicas 17700 logs, empty replication queue errors |
| Tiered storage with in-cluster MinIO | **chart bug found** (below); end-to-end run not completed: the Docker VM disk (shared with other workloads) dropped to 2.8 GB free, so the cluster was deleted |
| Tail sampling, agent chart | not run (same reason); chart render with `tailSampling.enabled` checked only |

Resources of this stack: node containers ~6 GiB RSS (worker 3.3 GiB, worker2 1.7 GiB, control-plane 0.9 GiB) and
~12 GB of disk (containerd images + local-path volumes). With the rest of the Docker VM busy the control plane's
controller-manager and scheduler lost their leases and restarted a few times.

Pitfalls found on this run:

- **Enabling ClickHouse TLS on an existing release needs two upgrades.** The migrate Job is a pre-upgrade hook and runs
  before the CHI change is applied; with `tls.enabled` in the same upgrade it would dial 9440 before ClickHouse serves it.
  Between step A and step B the old plaintext processor/api pods read port 9440 from `system.clusters` and their direct
  shard connections fail the handshake (`apm edge linking failed ... handshake`); ingest keeps writing to Kafka, so run
  step B right after the CHI shows `Completed`.
- **Keeper TLS on a running ensemble** leaves Raft mixed (TLS and plaintext peers): keeper-0 failed its liveness probe
  3 times and the operator's StatefulSet wait timed out (5 min) before it rolled the other two; ~6 min without quorum
  (Replicated inserts and `ON CLUSTER` DDL stop). Enable it in a maintenance window or at install time.
- **Tiered storage enabled on an existing release** deadlocked: the pre-upgrade migrate hook failed with
  `storage policy "openlog_tiered" is not usable` until its deadline, so the CHI never received the policy. Fixed in the
  chart: migrate keeps tiering off while the live CHI lacks the policy file; run `helm upgrade` a second time after the
  CHI rollout (docs/operations/tiered-storage.md).
- **`disableInsecure` with `verificationMode: strict`** was not tried: operator 0.27.3 can reach ClickHouse over HTTPS
  (`configs.files.config.yaml.clickhouse.access.scheme/port/rootCASecretRef`) but cannot present a client certificate,
  which strict mode requires on 8443. The plaintext Keeper port 2181 also stays (operator liveness probe `ruok`).
- **Upgrading the Altinity operator chart** runs a CRD pre-upgrade Job with `bitnami/kubectl:latest`; its first pull took
  ~2 min and the default `--timeout 5m` expired. Use `--timeout 10m`.
- The operator-generated per-host ClickHouse/Keeper Services are headless, so the extra TLS ports (9281, 9010) need no
  Service change; only the CHI's ClusterIP Service lists 9440/8443.
