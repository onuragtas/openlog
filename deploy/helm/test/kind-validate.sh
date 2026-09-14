#!/usr/bin/env bash
# Validation of the openlog chart in `dependencies.mode: operators` on a small kind cluster
# (docs/operations/kind-dev-cluster.md, "Validation run"). Steps (STEPS, comma-separated, run in this order):
#
#   base      kind cluster (1 control-plane + 2 workers), Strimzi, Altinity, CloudNativePG, cert-manager, metrics-server,
#             openlog image built from the tree and loaded, tiny install, loadgen -> ClickHouse -> API query
#   failover  delete the CloudNativePG primary and poll the API during the switchover
#   tls       cert-manager CA + certificates, ClickHouse server TLS (step A), openlog clients on 9440 with a client
#             certificate in strict mode (step B), Keeper TLS (9281 + Raft) and interserver HTTPS (9010) checks
#   tiered    in-cluster MinIO + clickhouse.tieredStorage (two upgrades), old-timestamped rows, MATERIALIZE TTL, parts on
#             the S3 disk, Distributed reads, openlog-admin storage status
#   sampling  tailSampling.enabled: sampler Deployment + PDB + CPU HPA, traces through the sampler, decisions, weights
#   agent     deploy/helm/openlog-agent (DaemonSet + cluster collector) -> in-cluster ingest, /api/v1/kubernetes/*
#   insecure  clickhouse.tls.server.disableInsecure (needs tls): operator over HTTPS, no plaintext ClickHouse/Keeper ports
#
#   STEPS=base,failover,tls,tiered,sampling,agent deploy/helm/test/kind-validate.sh
#   KEEP=1 ...          keep the cluster and $WORK (kubeconfig, values) afterwards; default: delete cluster, images, WORK
#   WORK=<dir> STEPS=sampling ...   continue on a kept cluster (values of earlier steps are re-read from $WORK)
#   SKIP_BUILD=1 ...    use an existing $IMAGE / $AGENT_IMAGE
#   KIND=... HELM=... KUBECTL=...   tool binaries
#
# Needs docker, kind >= 0.33, kubectl, helm >= 3.14, ~20 GB free Docker disk, ~8 GiB free memory.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
CHART="$ROOT/deploy/helm/openlog"
AGENT_CHART="$ROOT/deploy/helm/openlog-agent"
CLUSTER=${CLUSTER:-openlog-kind-validate}
NS=openlog
IMAGE=${IMAGE:-openlog-kind/openlog:dev}
AGENT_IMAGE=${AGENT_IMAGE:-openlog-kind/agent:dev}
MINIO_IMAGE=minio/minio:RELEASE.2025-04-22T22-12-26Z
STEPS=${STEPS:-base,failover,tls}
KIND=${KIND:-kind}
HELM=${HELM:-helm}
KUBECTL=${KUBECTL:-kubectl}
if [[ -z "${WORK:-}" ]]; then WORK=$(mktemp -d); fi
mkdir -p "$WORK"
export KUBECONFIG="$WORK/kubeconfig"
# Keep helm repositories out of the user's configuration.
export HELM_CONFIG_HOME="$WORK/helm-config" HELM_CACHE_HOME="$WORK/helm-cache" HELM_DATA_HOME="$WORK/helm-data"
PF=""

log() { printf '\n== %s %s\n' "$(date +%T)" "$*"; }
has() { [[ ",$STEPS," == *",$1,"* ]]; }
kind() { command "$KIND" "$@"; }
helm() { command "$HELM" "$@"; }
kubectl() { command "$KUBECTL" "$@"; }
k() { kubectl -n "$NS" "$@"; }
# clickhouse-client inside a replica; switches to the secure port once the plaintext one is gone (step insecure).
ch() { k exec "${CH_POD:-chi-openlog-openlog-0-0-0}" -- clickhouse-client ${CH_SECURE:-} -q "$1"; }
cleanup() {
  [[ -n "$PF" ]] && kill "$PF" 2>/dev/null || true
  if [[ "${KEEP:-0}" != 1 ]]; then
    kind delete cluster --name "$CLUSTER" || true
    [[ "${SKIP_BUILD:-0}" == 1 ]] || docker rmi "$IMAGE" "$AGENT_IMAGE" >/dev/null 2>&1 || true
    rm -rf "$WORK"
  else
    echo "kept: cluster $CLUSTER, WORK=$WORK (KUBECONFIG=$KUBECONFIG)"
  fi
}
trap cleanup EXIT

wait_for() { # wait_for <seconds> <description> <command...>
  local deadline=$((SECONDS + $1)) what=$2; shift 2
  until "$@" >/dev/null; do
    ((SECONDS < deadline)) || { echo "timeout: $what" >&2; return 1; }
    sleep 10
  done
}
chi_completed() {
  local st
  st=$(k get chi openlog -o jsonpath='{.status.status}')
  [[ $st == Aborted ]] && { echo "CHI Aborted: $(k get chi openlog -o jsonpath='{.status.errors}')" >&2; exit 1; }
  [[ $st == Completed ]]
}
chk_completed() { [[ $(k get chk openlog -o jsonpath='{.status.status}') == Completed ]]; }
pods_ready() { k get pods --no-headers | grep -v Completed | awk '{split($2,a,"/"); if (a[1]!=a[2]) bad=1} END {exit bad}'; }
# An upgrade that changes the CHI/CHK: wait until the operator has picked it up and finished.
wait_operator_rollout() {
  sleep 30
  wait_for 1500 "CHK Completed" chk_completed
  wait_for 1500 "CHI Completed" chi_completed
}

# Values: base file plus one file per step, applied in step order, so a later invocation (WORK=...) reproduces them.
cat >"$WORK/values-00-base.yaml" <<EOF
image: {repository: ${IMAGE%:*}, tag: ${IMAGE##*:}, pullPolicy: Never}
auth: {mode: postgres, session: {cookieSecure: false}}
postgres:
  mode: operator
  operator: {instances: 2, storage: {size: 1Gi}, resources: {requests: {cpu: 50m, memory: 128Mi}, limits: {memory: 384Mi}}}
bootstrap: {enabled: true, tenantId: dev, orgName: Dev, ownerEmail: admin@openlog.local, ownerPassword: openlog-dev-password, licenseKey: dev-license-key}
dependencies: {mode: operators}
kafka:
  topics: {partitions: 3, replicationFactor: 1, minInsyncReplicas: 1, retentionMs: 3600000}
  strimzi:
    config: {auto.create.topics.enable: false, default.replication.factor: 1, min.insync.replicas: 1, offsets.topic.replication.factor: 1, transaction.state.log.replication.factor: 1, transaction.state.log.min.isr: 1, message.max.bytes: 12582912, replica.fetch.max.bytes: 12582912}
    nodePools:
      - {name: dual, replicas: 1, roles: [controller, broker], storage: {size: 2Gi, deleteClaim: true}, resources: {requests: {cpu: 100m, memory: 768Mi}, limits: {memory: 768Mi}}, jvmOptions: {"-Xms": 384m, "-Xmx": 384m}}
clickhouse:
  altinity:
    shards: 1
    replicas: 2
    resources: {requests: {cpu: 100m, memory: 512Mi}, limits: {memory: 1536Mi}}
    settings: {mark_cache_size: 134217728}
    storage: {size: 3Gi, reclaimPolicy: Delete}
    keeper: {replicas: 3, resources: {requests: {cpu: 50m, memory: 128Mi}, limits: {memory: 384Mi}}, storage: {size: 512Mi}}
ingest: {replicaCount: 1, autoscaling: {enabled: false}, pdb: {enabled: false}, resources: {requests: {cpu: 50m, memory: 64Mi}, limits: {memory: 256Mi}}}
processor: {replicaCount: 1, autoscaling: {enabled: false}, pdb: {enabled: false}, resources: {requests: {cpu: 50m, memory: 128Mi}, limits: {memory: 512Mi}}}
api: {replicaCount: 1, autoscaling: {enabled: false}, pdb: {enabled: false}, resources: {requests: {cpu: 50m, memory: 64Mi}, limits: {memory: 256Mi}}}
alert: {replicaCount: 1, autoscaling: {enabled: false}, pdb: {enabled: false}, resources: {requests: {cpu: 20m, memory: 64Mi}, limits: {memory: 256Mi}}}
migrate: {backoffLimit: 20}
EOF
upgrade() {
  local args=() f
  for f in "$WORK"/values-*.yaml; do args+=(-f "$f"); done
  helm upgrade openlog "$CHART" -n "$NS" "${args[@]}" --timeout 20m
}

loadgen() { # loadgen [duration] [spans/s]
  k delete job loadgen --ignore-not-found --wait
  k apply -f - <<EOF
apiVersion: batch/v1
kind: Job
metadata: {name: loadgen}
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: loadgen
          image: $IMAGE
          imagePullPolicy: Never
          command: [/usr/local/bin/openlog-loadgen, -endpoint, "http://openlog-ingest.$NS.svc:4318", -license-key, dev-license-key,
                    -hosts, "3", -logs-per-sec, "50", -spans-per-sec, "${2:-50}", -duration, "${1:-2m}", -concurrency, "2"]
EOF
  k wait --for=condition=complete job/loadgen --timeout=600s
  k logs job/loadgen | tail -1
}

api_start() {
  [[ -n "$PF" ]] && return 0
  k port-forward svc/openlog-api 18080:8080 >"$WORK/pf.log" 2>&1 &
  PF=$!
  wait_for 60 "api port-forward" curl -s -o /dev/null http://127.0.0.1:18080/
  curl -sf -c "$WORK/cookies" -H 'Content-Type: application/json' \
    -d '{"email":"admin@openlog.local","password":"openlog-dev-password"}' http://127.0.0.1:18080/api/v1/auth/login >/dev/null
}
api() { curl -s -m 10 -b "$WORK/cookies" -o /dev/null -w '%{http_code}' "http://127.0.0.1:18080$1"; }
api_body() { curl -s -m 20 -b "$WORK/cookies" "http://127.0.0.1:18080$1"; }

if has base; then
  log "kind cluster $CLUSTER"
  cat >"$WORK/kind.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
  - {role: worker, labels: {topology.kubernetes.io/zone: zone-a}}
  - {role: worker, labels: {topology.kubernetes.io/zone: zone-b}}
EOF
  kind create cluster --name "$CLUSTER" --config "$WORK/kind.yaml" --kubeconfig "$KUBECONFIG" --wait 5m

  log "operators (pinned)"
  helm repo add altinity https://docs.altinity.com/clickhouse-operator/ >/dev/null
  helm repo add cnpg https://cloudnative-pg.github.io/charts >/dev/null
  helm repo add jetstack https://charts.jetstack.io >/dev/null
  helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/ >/dev/null
  helm repo update >/dev/null
  pids=()
  helm install strimzi oci://quay.io/strimzi-helm/strimzi-kafka-operator --version 1.2.0 -n strimzi --create-namespace \
    --set watchAnyNamespace=true --wait --timeout 10m & pids+=($!)
  helm install clickhouse-operator altinity/altinity-clickhouse-operator --version 0.27.3 -n clickhouse-operator \
    --create-namespace --set "watchNamespaces={$NS}" --wait --timeout 10m & pids+=($!)
  helm install cnpg cnpg/cloudnative-pg --version 0.26.1 -n cnpg-system --create-namespace --wait --timeout 10m & pids+=($!)
  helm install cert-manager jetstack/cert-manager --version v1.21.2 -n cert-manager --create-namespace \
    --set crds.enabled=true --wait --timeout 10m & pids+=($!)
  # CPU HPAs need the resource metrics API; kind kubelets serve self-signed certificates.
  helm install metrics-server metrics-server/metrics-server --version 3.13.1 -n kube-system \
    --set 'args={--kubelet-insecure-tls}' --wait --timeout 10m & pids+=($!)
  # `wait` without arguments ignores failures.
  for p in "${pids[@]}"; do wait "$p"; done

  if [[ "${SKIP_BUILD:-0}" != 1 ]]; then
    log "image $IMAGE"
    docker build -t "$IMAGE" "$ROOT"
  fi
  kind load docker-image "$IMAGE" --name "$CLUSTER"

  log "install openlog (no --wait: post-install migrate hook)"
  helm install openlog "$CHART" -n "$NS" --create-namespace -f "$WORK/values-00-base.yaml" --timeout 30m
  wait_for 900 "openlog pods ready" pods_ready
  # Pods are ready before the operator has added the second replica to the cluster and created its tables.
  wait_for 900 "CHK Completed" chk_completed
  wait_for 900 "CHI Completed" chi_completed
  ch "SELECT hostName(), count() FROM clusterAllReplicas('openlog', system.tables) WHERE database='openlog' GROUP BY 1"

  log "ingest -> Kafka -> processor -> ClickHouse -> API"
  loadgen
  sleep 15
  ch "SELECT 'logs', count() FROM openlog.logs UNION ALL SELECT 'spans', count() FROM openlog.spans"
  api_start
  [[ $(api /api/v1/hosts) == 200 ]] && echo "API /api/v1/hosts 200"
fi

if has failover; then
  log "CloudNativePG failover (delete primary)"
  api_start
  old=$(k get cluster.postgresql.cnpg.io openlog-pg -o jsonpath='{.status.currentPrimary}')
  k delete pod "$old" --wait=false
  for _ in $(seq 1 100); do
    st=$(k get cluster.postgresql.cnpg.io openlog-pg -o jsonpath='{.status.currentPrimary}|{.status.phase}')
    echo "$(date +%T) me=$(api /api/v1/auth/me) hosts=$(api /api/v1/hosts) $st"
    [[ $st == *"|Cluster in healthy state" && $st != "$old|"* ]] && break
    sleep 3
  done
  PF_OLD=$PF; PF=""; kill "$PF_OLD" 2>/dev/null || true # session may be on the old connection pool; log in again later
fi

if has tls; then
  log "cert-manager CA and certificates"
  k apply -f - <<'EOF'
apiVersion: cert-manager.io/v1
kind: Issuer
metadata: {name: selfsigned}
spec: {selfSigned: {}}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: openlog-ca}
spec: {isCA: true, commonName: openlog-dev-ca, secretName: openlog-ca, privateKey: {algorithm: ECDSA, size: 256}, issuerRef: {name: selfsigned, kind: Issuer}}
---
apiVersion: cert-manager.io/v1
kind: Issuer
metadata: {name: openlog-ca}
spec: {ca: {secretName: openlog-ca}}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: openlog-clickhouse-tls}
spec:
  secretName: openlog-clickhouse-tls
  privateKey: {algorithm: ECDSA, size: 256}
  usages: [server auth, client auth, digital signature, key encipherment]
  dnsNames: [clickhouse-openlog.openlog.svc, "*.openlog.svc", "*.openlog.svc.cluster.local", chi-openlog-openlog-0-0,
             chi-openlog-openlog-0-1, keeper-openlog.openlog.svc, chk-openlog-keeper-0-0,
             chk-openlog-keeper-0-1, chk-openlog-keeper-0-2, localhost]
  issuerRef: {name: openlog-ca, kind: Issuer}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: openlog-clickhouse-client}
spec:
  secretName: openlog-clickhouse-client
  commonName: openlog
  privateKey: {algorithm: ECDSA, size: 256}
  usages: [client auth, digital signature, key encipherment]
  issuerRef: {name: openlog-ca, kind: Issuer}
EOF
  k wait --for=condition=Ready certificate --all --timeout=120s

  # Two upgrades: in one, the pre-upgrade migrate hook dials 9440 before the CHI serves it and never succeeds.
  log "step A: ClickHouse server TLS + Keeper TLS + interserver HTTPS (openlog still plaintext)"
  cat >"$WORK/values-10-tls.yaml" <<'EOF'
clickhouse:
  tls:
    server: {secretName: openlog-clickhouse-tls, verificationMode: strict}
EOF
  upgrade
  wait_operator_rollout

  log "step B: openlog on 9440 with a client certificate"
  cat >"$WORK/values-11-tls-clients.yaml" <<'EOF'
clickhouse:
  tls:
    enabled: true
    clientSecret: {name: openlog-clickhouse-client}
EOF
  upgrade
  k rollout status deploy --timeout=300s
  loadgen
  sleep 15
  for p in chi-openlog-openlog-0-0-0 chi-openlog-openlog-0-1-0; do
    k exec "$p" -- clickhouse-client -q "SELECT hostName(), (SELECT count() FROM openlog.logs_local), \
      (SELECT concat(host, ':', toString(port)) FROM system.zookeeper_connection), \
      (SELECT count() FROM system.replication_queue WHERE last_exception != '')"
  done
  k exec chk-openlog-keeper-0-0-0 -- bash -c 'exec 3<>/dev/tcp/127.0.0.1/2181; printf mntr >&3; cat <&3' | grep zk_server_state
  k logs chk-openlog-keeper-0-1-0 | grep -m1 "SSL enabled" || true
fi

if has tiered; then
  log "tiered storage with in-cluster MinIO"
  docker image inspect "$MINIO_IMAGE" >/dev/null 2>&1 || docker pull -q "$MINIO_IMAGE"
  kind load docker-image "$MINIO_IMAGE" --name "$CLUSTER"
  k create secret generic openlog-s3 --from-literal=access-key-id=openlog --from-literal=secret-access-key=openlog-minio-secret \
    --dry-run=client -o yaml | k apply -f -
  k apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata: {name: minio}
spec:
  selector: {matchLabels: {app: minio}}
  template:
    metadata: {labels: {app: minio}}
    spec:
      containers:
        - name: minio
          image: $MINIO_IMAGE
          imagePullPolicy: Never
          args: [server, /data]
          env: [{name: MINIO_ROOT_USER, value: openlog}, {name: MINIO_ROOT_PASSWORD, value: openlog-minio-secret}]
          readinessProbe: {httpGet: {path: /minio/health/ready, port: 9000}}
          resources: {requests: {cpu: 20m, memory: 128Mi}, limits: {memory: 512Mi}}
---
apiVersion: v1
kind: Service
metadata: {name: minio}
spec: {selector: {app: minio}, ports: [{port: 9000}]}
EOF
  k rollout status deploy/minio --timeout=180s
  k exec deploy/minio -- sh -c 'mc alias set local http://127.0.0.1:9000 openlog openlog-minio-secret >/dev/null && mc mb -p local/openlog-cold'

  cat >"$WORK/values-20-tiered.yaml" <<EOF
clickhouse:
  tieredStorage:
    enabled: true
    s3:
      endpoint: "http://minio.$NS.svc:9000/openlog-cold/{shard}/{replica}/"
      region: us-east-1
      credentialsSecret: {name: openlog-s3}
      cacheMaxSize: 256Mi
EOF
  upgrade # CHI gets the policy; migrate skips the moves (policy not live yet)
  wait_operator_rollout
  wait_for 600 "openlog pods ready" pods_ready
  upgrade # migrate applies policy + TTL moves
  ch "SELECT policy_name, volume_name, disks FROM system.storage_policies WHERE policy_name='openlog_tiered'"
  ch "SELECT name, engine_full LIKE '%openlog_tiered%', engine_full LIKE '%TO VOLUME%' FROM system.tables WHERE database='openlog' AND name IN ('logs_local','spans_local')"

  log "old-timestamped rows (5 days: past logs/traces coldAfterDays 3) and MATERIALIZE TTL"
  ch "INSERT INTO openlog.logs_local SELECT * REPLACE (timestamp - INTERVAL 5 DAY AS timestamp) FROM openlog.logs_local WHERE timestamp > now() - INTERVAL 1 DAY"
  ch "INSERT INTO openlog.spans_local SELECT * REPLACE (timestamp - INTERVAL 5 DAY AS timestamp) FROM openlog.spans_local WHERE timestamp > now() - INTERVAL 1 DAY"
  ch "ALTER TABLE openlog.logs_local ON CLUSTER openlog MATERIALIZE TTL SETTINGS mutations_sync = 2"
  ch "ALTER TABLE openlog.spans_local ON CLUSTER openlog MATERIALIZE TTL SETTINGS mutations_sync = 2"
  cold_parts() {
    [[ $(ch "SELECT count() FROM clusterAllReplicas('openlog', system.parts) WHERE database='openlog' AND table IN ('logs_local','spans_local') AND active AND disk_name='openlog_s3' AND max_time < now() - INTERVAL 4 DAY") -ge 4 ]]
  }
  wait_for 600 "old parts on the s3 disk of both replicas" cold_parts
  ch "SELECT hostName(), table, disk_name, count(), sum(rows) FROM clusterAllReplicas('openlog', system.parts) WHERE database='openlog' AND table IN ('logs_local','spans_local') AND active GROUP BY ALL ORDER BY ALL"
  k exec deploy/minio -- sh -c 'mc du --depth 3 local/openlog-cold'
  log "Distributed reads over cold parts"
  ch "SELECT 'logs', countIf(timestamp < now() - INTERVAL 4 DAY), count() FROM openlog.logs UNION ALL SELECT 'spans', countIf(timestamp < now() - INTERVAL 4 DAY), count() FROM openlog.spans"
  ch "SELECT body FROM openlog.logs WHERE timestamp < now() - INTERVAL 4 DAY ORDER BY timestamp LIMIT 1 SETTINGS enable_filesystem_cache = 0"
  api_start
  from=$(date -u -d '@'$(($(date +%s) - 6 * 86400)) +%FT%TZ 2>/dev/null || date -u -r $(($(date +%s) - 6 * 86400)) +%FT%TZ)
  to=$(date -u -d '@'$(($(date +%s) - 4 * 86400)) +%FT%TZ 2>/dev/null || date -u -r $(($(date +%s) - 4 * 86400)) +%FT%TZ)
  echo "API /api/v1/logs over cold range: $(api "/api/v1/logs?from=$from&to=$to&limit=5")"
  k exec deploy/openlog-api -- openlog-admin storage status
fi

if has sampling; then
  log "tail sampling"
  cat >"$WORK/values-30-sampling.yaml" <<'EOF'
tailSampling:
  enabled: true
  decisionWait: 10s
  maxBufferedBytes: 134217728
  defaultPolicy: '{"enabled":true,"baseline_ratio":0.1,"max_spans_per_second":0,"rules":[{"name":"errors","type":"error"}]}'
sampler:
  replicaCount: 2
  resources: {requests: {cpu: 50m, memory: 128Mi}, limits: {memory: 384Mi}}
  autoscaling: {enabled: true, minReplicas: 2, maxReplicas: 3, targetCPUUtilizationPercentage: 70}
EOF
  before=$(ch "SELECT count() FROM openlog.spans")
  upgrade
  k rollout status deploy/openlog-sampler --timeout=300s
  k rollout status deploy --timeout=300s
  k get deploy,pdb,hpa -l app.kubernetes.io/component=sampler
  loadgen 2m 100
  sleep 45
  for p in $(k get pods -l app.kubernetes.io/component=sampler -o name); do
    echo "-- $p"
    k exec "$p" -- wget -qO- http://127.0.0.1:9464/metrics | grep -E '^openlog_tailsampling_(decisions|spans)_total' || true
  done
  ch "SELECT status_code, count(), round(avg(sample_weight), 2), round(sum(sample_weight)) FROM openlog.spans WHERE timestamp > now() - INTERVAL 5 MINUTE GROUP BY 1"
  echo "spans before sampling step: $before, after: $(ch 'SELECT count() FROM openlog.spans')"
  k get hpa openlog-sampler
fi

if has agent; then
  log "openlog-agent chart -> in-cluster ingest"
  if [[ "${SKIP_BUILD:-0}" != 1 ]]; then
    docker build -f "$ROOT/agents/infra/Dockerfile" --build-arg VERSION=0.0.0-kind -t "$AGENT_IMAGE" "$ROOT"
  fi
  kind load docker-image "$AGENT_IMAGE" --name "$CLUSTER"
  kubectl create namespace openlog-agent --dry-run=client -o yaml | kubectl apply -f -
  kubectl -n openlog-agent create secret generic openlog-license --from-literal=license-key=dev-license-key \
    --dry-run=client -o yaml | kubectl apply -f -
  helm upgrade --install openlog-agent "$AGENT_CHART" -n openlog-agent --wait --timeout 5m \
    --set clusterName=kind-validate --set endpoint="http://openlog-ingest.$NS.svc:4318" \
    --set existingSecret.name=openlog-license \
    --set image.repository="${AGENT_IMAGE%:*}",image.tag="${AGENT_IMAGE##*:}",image.pullPolicy=Never \
    --set cluster.interval=15s --set node.resources.requests.cpu=20m --set cluster.resources.requests.cpu=20m
  kubectl -n openlog-agent get ds,deploy,pods -o wide
  api_start
  k8s_data() { [[ $(api_body /api/v1/kubernetes/pods) == *'"pod_uid"'* ]]; }
  wait_for 300 "kubernetes pods in the API" k8s_data
  for path in clusters nodes workloads pods events; do
    body=$(api_body "/api/v1/kubernetes/$path")
    echo "/api/v1/kubernetes/$path: $(api "/api/v1/kubernetes/$path") ${#body} bytes: ${body:0:300}"
  done
  ch "SELECT event_name, count() FROM openlog.logs WHERE timestamp > now() - INTERVAL 15 MINUTE AND (event_name = 'k8s.event' OR has(mapKeys(resource_attributes), 'k8s.pod.uid') OR has(mapKeys(attributes), 'k8s.pod.uid')) GROUP BY 1"
fi

if has insecure; then
  log "disableInsecure: operator over HTTPS (CA from the TLS Secret), verificationMode relaxed, no plaintext ports"
  kubectl -n clickhouse-operator create secret generic openlog-ca --from-literal=ca.crt="$(k get secret openlog-ca -o jsonpath='{.data.ca\.crt}' | base64 -d)" \
    --dry-run=client -o yaml | kubectl apply -f -
  helm upgrade clickhouse-operator altinity/altinity-clickhouse-operator --version 0.27.3 -n clickhouse-operator --reuse-values \
    --set configs.files.config\\.yaml.clickhouse.access.scheme=https \
    --set configs.files.config\\.yaml.clickhouse.access.port=8443 \
    --set configs.files.config\\.yaml.clickhouse.access.rootCASecretRef.name=openlog-ca \
    --set configs.files.config\\.yaml.clickhouse.access.rootCASecretRef.key=ca.crt --wait --timeout 10m
  kubectl -n clickhouse-operator rollout status deploy --timeout=300s
  cat >"$WORK/values-40-insecure.yaml" <<'EOF'
clickhouse:
  tls:
    server: {verificationMode: relaxed, disableInsecure: true}
EOF
  upgrade
  wait_operator_rollout
  k rollout status deploy --timeout=300s
  CH_SECURE="--secure --port 9440 --config-file /tmp/ch-client.xml"
  for p in chi-openlog-openlog-0-0-0 chi-openlog-openlog-0-1-0; do
    k exec "$p" -- sh -c 'cat >/tmp/ch-client.xml <<X
<config><openSSL><client><caConfig>/etc/clickhouse-server/tls/ca.crt</caConfig><verificationMode>strict</verificationMode><loadDefaultCAFile>false</loadDefaultCAFile></client></openSSL></config>
X'
    k exec "$p" -- sh -c 'for port in 9000 8123 9440 8443 9009 9010; do (exec 3<>/dev/tcp/127.0.0.1/$port) 2>/dev/null && echo "$port open" || echo "$port closed"; done'
  done
  for p in chk-openlog-keeper-0-0-0 chk-openlog-keeper-0-1-0 chk-openlog-keeper-0-2-0; do
    k exec "$p" -- bash -c 'for port in 2181 9281; do (exec 3<>/dev/tcp/127.0.0.1/$port) 2>/dev/null && echo "$port open" || echo "$port closed"; done'
  done
  k get svc -o custom-columns=NAME:.metadata.name,PORTS:.spec.ports[*].port | grep -E 'clickhouse|keeper'
  loadgen 1m
  sleep 15
  ch "SELECT hostName(), (SELECT count() FROM openlog.logs), (SELECT concat(host, ':', toString(port)) FROM system.zookeeper_connection)"
  kubectl -n clickhouse-operator logs deploy/clickhouse-operator-altinity-clickhouse-operator -c altinity-clickhouse-operator --since=10m | grep -ciE 'error|fail' || true
  k get chi,chk
fi

log "done"
