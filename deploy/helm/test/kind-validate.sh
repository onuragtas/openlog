#!/usr/bin/env bash
# Validation of the openlog chart in `dependencies.mode: operators` on a small kind cluster
# (docs/operations/kind-dev-cluster.md, "Validation run"). Steps (STEPS, comma-separated):
#
#   base      kind cluster (1 control-plane + 2 workers), Strimzi, Altinity, CloudNativePG, cert-manager,
#             openlog image built from the tree and loaded, tiny install, loadgen -> ClickHouse -> API query
#   failover  delete the CloudNativePG primary and poll the API during the switchover
#   tls       cert-manager CA + certificates, ClickHouse server TLS (step A), openlog clients on 9440 with a client
#             certificate in strict mode (step B), Keeper TLS (9281 + Raft) and interserver HTTPS (9010) checks
#   tiered    in-cluster MinIO + clickhouse.tieredStorage (two upgrades), MATERIALIZE TTL, storage status
#   sampling  tailSampling.enabled: sampler Deployment + PDB, traces through the sampler
#
# base, failover and tls were run step by step on 2026-09-14 (results in the runbook); tiered and sampling were not
# completed there (Docker VM disk exhausted) and are unvalidated.
#
#   STEPS=base,failover,tls deploy/helm/test/kind-validate.sh
#   KEEP=1 ...    keep the cluster afterwards (default: delete cluster and loaded image)
#
# Needs docker, kind >= 0.33, kubectl, helm >= 3.14, ~15 GB free Docker disk, ~8 GiB free memory.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
CHART="$ROOT/deploy/helm/openlog"
CLUSTER=${CLUSTER:-openlog-r-kind}
NS=openlog
IMAGE=${IMAGE:-openlog-kind/openlog:dev}
STEPS=${STEPS:-base,failover,tls}
WORK=$(mktemp -d)
export KUBECONFIG="$WORK/kubeconfig"

log() { printf '\n== %s %s\n' "$(date +%T)" "$*"; }
has() { [[ ",$STEPS," == *",$1,"* ]]; }
k() { kubectl -n "$NS" "$@"; }
ch() { k exec chi-openlog-openlog-0-0-0 -- clickhouse-client -q "$1"; }
cleanup() {
  if [[ "${KEEP:-0}" != 1 ]]; then
    kind delete cluster --name "$CLUSTER" || true
    docker rmi "$IMAGE" >/dev/null 2>&1 || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

wait_for() { # wait_for <seconds> <description> <command...>
  local deadline=$((SECONDS + $1)) what=$2; shift 2
  until "$@" >/dev/null 2>&1; do
    ((SECONDS < deadline)) || { echo "timeout: $what" >&2; return 1; }
    sleep 10
  done
}
chi_completed() { [[ $(k get chi openlog -o jsonpath='{.status.status}') == Completed ]]; }
chk_completed() { [[ $(k get chk openlog -o jsonpath='{.status.status}') == Completed ]]; }
pods_ready() { k get pods --no-headers | grep -v Completed | awk '{split($2,a,"/"); if (a[1]!=a[2]) bad=1} END {exit bad}'; }

cat >"$WORK/values.yaml" <<EOF
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
    storage: {size: 2Gi, reclaimPolicy: Delete}
    keeper: {replicas: 3, resources: {requests: {cpu: 50m, memory: 128Mi}, limits: {memory: 384Mi}}, storage: {size: 512Mi}}
ingest: {replicaCount: 1, autoscaling: {enabled: false}, pdb: {enabled: false}, resources: {requests: {cpu: 50m, memory: 64Mi}, limits: {memory: 256Mi}}}
processor: {replicaCount: 1, autoscaling: {enabled: false}, pdb: {enabled: false}, resources: {requests: {cpu: 50m, memory: 128Mi}, limits: {memory: 512Mi}}}
api: {replicaCount: 1, autoscaling: {enabled: false}, pdb: {enabled: false}, resources: {requests: {cpu: 50m, memory: 64Mi}, limits: {memory: 256Mi}}}
alert: {replicaCount: 1, autoscaling: {enabled: false}, pdb: {enabled: false}, resources: {requests: {cpu: 20m, memory: 64Mi}, limits: {memory: 256Mi}}}
migrate: {backoffLimit: 20}
EOF
VALUES=(-f "$WORK/values.yaml")

upgrade() { helm upgrade openlog "$CHART" -n "$NS" "${VALUES[@]}" --timeout 20m; }

loadgen() {
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
                    -hosts, "3", -logs-per-sec, "50", -spans-per-sec, "50", -duration, 2m, -concurrency, "2"]
EOF
  k wait --for=condition=complete job/loadgen --timeout=300s
  k logs job/loadgen | tail -1
}

api_start() {
  k port-forward svc/openlog-api 18080:8080 >"$WORK/pf.log" 2>&1 &
  PF=$!
  wait_for 30 "api port-forward" curl -s -o /dev/null http://127.0.0.1:18080/
  curl -sf -c "$WORK/cookies" -H 'Content-Type: application/json' \
    -d '{"email":"admin@openlog.local","password":"openlog-dev-password"}' http://127.0.0.1:18080/api/v1/auth/login >/dev/null
}
api() { curl -s -m 10 -b "$WORK/cookies" -o /dev/null -w '%{http_code}' "http://127.0.0.1:18080$1"; }

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
  helm repo update >/dev/null
  helm install strimzi oci://quay.io/strimzi-helm/strimzi-kafka-operator --version 1.2.0 -n strimzi --create-namespace \
    --set watchAnyNamespace=true --wait --timeout 10m &
  helm install clickhouse-operator altinity/altinity-clickhouse-operator --version 0.27.3 -n clickhouse-operator \
    --create-namespace --set "watchNamespaces={$NS}" --wait --timeout 10m &
  helm install cnpg cnpg/cloudnative-pg --version 0.26.1 -n cnpg-system --create-namespace --wait --timeout 10m &
  helm install cert-manager jetstack/cert-manager --version v1.21.2 -n cert-manager --create-namespace \
    --set crds.enabled=true --wait --timeout 10m &
  wait

  log "image $IMAGE"
  docker build -t "$IMAGE" "$ROOT"
  kind load docker-image "$IMAGE" --name "$CLUSTER"

  log "install openlog (no --wait: post-install migrate hook)"
  helm install openlog "$CHART" -n "$NS" --create-namespace "${VALUES[@]}" --timeout 30m
  wait_for 900 "openlog pods ready" pods_ready
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
  old=$(k get cluster.postgresql.cnpg.io openlog-pg -o jsonpath='{.status.currentPrimary}')
  k delete pod "$old" --wait=false
  for _ in $(seq 1 60); do
    st=$(k get cluster.postgresql.cnpg.io openlog-pg -o jsonpath='{.status.currentPrimary}|{.status.phase}')
    echo "$(date +%T) me=$(api /api/v1/auth/me) hosts=$(api /api/v1/hosts) $st"
    [[ $st == *"|Cluster in healthy state" && $st != "$old|"* ]] && break
    sleep 3
  done
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
             chi-openlog-openlog-0-1, keeper-openlog.openlog.svc, chk-openlog-keeper-0-0, chk-openlog-keeper-0-1,
             chk-openlog-keeper-0-2, localhost]
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
  VALUES+=(--set clickhouse.tls.server.secretName=openlog-clickhouse-tls --set clickhouse.tls.server.verificationMode=strict)
  upgrade
  sleep 30
  wait_for 1200 "CHK Completed" chk_completed
  wait_for 1200 "CHI Completed" chi_completed

  log "step B: openlog on 9440 with a client certificate"
  VALUES+=(--set clickhouse.tls.enabled=true --set clickhouse.tls.clientSecret.name=openlog-clickhouse-client)
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
  k logs chk-openlog-keeper-0-1-0 | grep -m1 "SSL enabled"
fi

if has tiered; then
  log "tiered storage with in-cluster MinIO (UNVALIDATED)"
  k create secret generic openlog-s3 --from-literal=access-key-id=openlog --from-literal=secret-access-key=openlog-minio-secret
  k create deployment minio --image=minio/minio:RELEASE.2025-04-22T22-12-26Z -- sh -c \
    'mkdir -p /data/openlog-cold && MINIO_ROOT_USER=openlog MINIO_ROOT_PASSWORD=openlog-minio-secret exec minio server /data'
  k expose deployment minio --port 9000
  k rollout status deploy/minio --timeout=180s
  VALUES+=(--set clickhouse.tieredStorage.enabled=true
    --set "clickhouse.tieredStorage.s3.endpoint=http://minio.$NS.svc:9000/openlog-cold/{shard}/{replica}/"
    --set clickhouse.tieredStorage.s3.region=us-east-1 --set clickhouse.tieredStorage.s3.credentialsSecret.name=openlog-s3
    --set clickhouse.tieredStorage.s3.cacheMaxSize=256Mi)
  upgrade # CHI gets the policy; migrate skips the moves (policy not live yet)
  sleep 30
  wait_for 1200 "CHI Completed" chi_completed
  upgrade # migrate applies policy + TTL moves
  ch "SELECT policy_name, volume_name, disks FROM system.storage_policies WHERE policy_name='openlog_tiered'"
  k exec deploy/openlog-api -- openlog-admin storage status
fi

if has sampling; then
  log "tail sampling (UNVALIDATED)"
  VALUES+=(--set tailSampling.enabled=true --set tailSampling.decisionWait=10s --set sampler.replicaCount=2
    --set sampler.resources.requests.cpu=50m --set sampler.resources.requests.memory=128Mi
    --set sampler.resources.limits.memory=384Mi --set tailSampling.maxBufferedBytes=134217728)
  upgrade
  k rollout status deploy/openlog-sampler --timeout=300s
  k get deploy,pdb,hpa -l app.kubernetes.io/component=sampler
  loadgen
  sleep 30
  k exec deploy/openlog-sampler -- wget -qO- http://127.0.0.1:9464/metrics | grep -E '^openlog_tailsampling_(traces|decisions)' | head
fi

log "done"
