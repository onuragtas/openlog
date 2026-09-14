#!/bin/sh
# Kubernetes live test of the openlog-agent chart (docs/operations/kubernetes.md): creates a kind cluster (1 control
# plane + 1 worker), deploys an OTLP capture server and demo workloads (a 2-replica deployment writing logs, a crash-
# looping deployment, a CronJob), installs deploy/helm/openlog-agent rendered with helm (in Docker), waits, and checks
# the captured metrics, logs and events (verify.py). No openlog backend is needed.
#
#   agents/infra/test/k8s/run.sh            # from the repository root; KEEP=1 keeps the cluster
#
# Needs: docker, kind, kubectl, python3. Images built: openlog-m4-k8s/agent:dev, openlog-m4-k8s/capture:dev.
set -eu

ROOT=$(cd "$(dirname "$0")/../../../.." && pwd)
CLUSTER=${CLUSTER:-openlog-m4-k8s}
CTX="kind-$CLUSTER"
WAIT=${WAIT:-150}
HELM_IMAGE=${HELM_IMAGE:-alpine/helm:3}
WORK=$(mktemp -d)
ARCH=$(docker version -f '{{.Server.Arch}}')

cleanup() {
	rm -rf "$WORK"
	if [ "${KEEP:-0}" != 1 ]; then
		kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
		docker rmi openlog-m4-k8s/agent:dev openlog-m4-k8s/capture:dev >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT

echo "== build images"
docker build -q -f "$ROOT/agents/infra/Dockerfile" --build-arg VERSION=0.0.0-k8stest -t openlog-m4-k8s/agent:dev "$ROOT" >/dev/null
(cd "$ROOT/agents/infra" && CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -o "$WORK/capture" ./test/k8s/capture)
printf 'FROM scratch\nCOPY capture /capture\nENTRYPOINT ["/capture"]\n' >"$WORK/Dockerfile"
docker build -q -t openlog-m4-k8s/capture:dev "$WORK" >/dev/null
docker image inspect alpine:3 >/dev/null 2>&1 || docker pull -q alpine:3 >/dev/null

echo "== kind cluster $CLUSTER"
if ! kind get clusters | grep -qx "$CLUSTER"; then
	printf 'kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nnodes:\n- role: control-plane\n- role: worker\n' >"$WORK/kind.yaml"
	kind create cluster --name "$CLUSTER" --config "$WORK/kind.yaml" --wait 3m
fi
kind load docker-image openlog-m4-k8s/agent:dev openlog-m4-k8s/capture:dev alpine:3 --name "$CLUSTER" >/dev/null

echo "== fixtures and chart"
kubectl --context "$CTX" apply -f "$ROOT/agents/infra/test/k8s/manifests.yaml" >/dev/null
# A fresh capture server (it keeps data in memory) before the agents start, so nothing they send is lost.
kubectl --context "$CTX" -n openlog rollout restart deploy/capture >/dev/null
kubectl --context "$CTX" -n openlog rollout status deploy/capture --timeout=120s
docker run --rm -v "$ROOT/deploy/helm":/charts -w /charts "$HELM_IMAGE" lint openlog-agent \
	--set clusterName=kind-m4,endpoint=http://capture.openlog.svc:4318,licenseKey=test-license
docker run --rm -v "$ROOT/deploy/helm":/charts -w /charts "$HELM_IMAGE" template oa openlog-agent -n openlog \
	--set clusterName=kind-m4,endpoint=http://capture.openlog.svc:4318,licenseKey=test-license \
	--set image.repository=openlog-m4-k8s/agent,image.tag=dev,image.pullPolicy=Never,cluster.replicas=2,cluster.interval=15s \
	>"$WORK/agent.yaml"
kubectl --context "$CTX" apply -f "$WORK/agent.yaml" >/dev/null
kubectl --context "$CTX" -n openlog rollout restart ds/oa-openlog-agent-node deploy/oa-openlog-agent-cluster >/dev/null
kubectl --context "$CTX" -n openlog rollout status ds/oa-openlog-agent-node --timeout=180s
kubectl --context "$CTX" -n openlog rollout status deploy/oa-openlog-agent-cluster --timeout=180s
# Reset the crash loop back-off so the crashing container logs (and restarts) within the wait.
kubectl --context "$CTX" -n demo delete pod -l app=crasher --wait=false >/dev/null

echo "== waiting ${WAIT}s for data"
sleep "$WAIT"
kubectl --context "$CTX" -n openlog get lease oa-openlog-agent-cluster -o jsonpath='leader: {.spec.holderIdentity}{"\n"}'
kubectl --context "$CTX" get --raw /api/v1/namespaces/openlog/services/capture:4318/proxy/summary >"$WORK/summary.json"
python3 "$ROOT/agents/infra/test/k8s/verify.py" <"$WORK/summary.json"
