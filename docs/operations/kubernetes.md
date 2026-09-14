# Runbook: monitoring Kubernetes with openlog

The `deploy/helm/openlog-agent` chart (Apache-2.0) runs the openlog infra agent in a Kubernetes cluster and sends OTLP
to an openlog ingest endpoint (inside or outside the cluster). Data model: [semantic-conventions.md §7](../contracts/semantic-conventions.md);
decisions D-070, D-071; API: [api.md](../contracts/api.md) "Kubernetes".

| Workload | What it does | Host access |
|---|---|---|
| `DaemonSet <release>-openlog-agent-node` (every node) | host metrics, processes, inventory and discovery of the node; cgroup metrics of every container with pod, workload and label attributes; kubelet `/stats/summary` pod/container/node metrics; container stdout/stderr from `/var/log/pods` | host root read-only, `hostPID`, `hostNetwork`, root with `DAC_READ_SEARCH` + `SYS_PTRACE` only |
| `Deployment <release>-openlog-agent-cluster` (1–2 replicas, one active) | kube-state metrics (nodes, pods, workloads, jobs, HPAs, namespaces, quotas), Kubernetes events as logs | none (non-root, read-only root filesystem, all capabilities dropped) |

## 1. Install

Prerequisites: Kubernetes >= 1.27, Helm >= 3.14, an openlog ingest license key, and the agent image
(`ghcr.io/onuragtas/openlog-infra-agent:<version>`; build it with `make -C agents/infra docker` and push it to your
registry until a published image exists).

```bash
kubectl create namespace openlog-agent
# The node agent needs host access: the namespace must allow the "privileged" Pod Security Standard.
kubectl label namespace openlog-agent pod-security.kubernetes.io/enforce=privileged
kubectl -n openlog-agent create secret generic openlog-license --from-literal=license-key='<LICENSE KEY>'

helm install openlog-agent deploy/helm/openlog-agent -n openlog-agent \
  --set clusterName=prod-eu-1 \
  --set endpoint=https://ingest.openlog.example:4318 \
  --set existingSecret.name=openlog-license
```

Verify:

```bash
kubectl -n openlog-agent rollout status ds/openlog-agent-openlog-agent-node
kubectl -n openlog-agent logs ds/openlog-agent-openlog-agent-node | grep 'kubernetes node mode'
kubectl -n openlog-agent get lease openlog-agent-openlog-agent-cluster -o jsonpath='{.spec.holderIdentity}{"\n"}'
```

Within about a minute the cluster, its nodes (linked to their host pages), workloads and pods appear under **Kubernetes** in
the UI; container pages show pod and workload names; pod pages show container logs and events.

Important values (`values.yaml` has all of them):

| Value | Default | Notes |
|---|---|---|
| `clusterName` | — (required) | `k8s.cluster.name`; the cluster identity is the `kube-system` namespace uid (`k8s.cluster.uid`), so renaming keeps history |
| `endpoint`, `existingSecret.name` / `licenseKey` | — (required) | a `licenseKey` value is stored in a chart-managed Secret |
| `node.kubelet.insecureSkipVerify` | `true` | kubelet serving certificates are self-signed on kubeadm/kind; set `false` when they are signed by the cluster CA (`serverTLSBootstrap: true`) |
| `node.containerLogs.enabled` | `true` | tails `/var/log/pods/<ns>_<pod>_<uid>/<container>/<n>.log` (CRI format); exclude with `node.config.logs.containers.exclude` or the pod annotation-free label `openlog.logs=false` on the container runtime |
| `node.tolerations` | `[{operator: Exists}]` | runs on tainted (control-plane) nodes too |
| `node.stateHostPath` | `/var/lib/openlog-infra-agent` | disk buffer and log positions survive pod restarts |
| `node.config`, `cluster.config` | `{}` | any agent configuration key ([config.example.yaml](../../agents/infra/packaging/config.example.yaml)), merged over the generated config |
| `cluster.replicas` | `1` | `2` gives a ~15 s failover (Lease); only the holder lists and sends |
| `cluster.interval` | `30s` | cluster metric interval |
| `cluster.events` | `true` | Kubernetes events as logs (`event_name = k8s.event`) |

Upgrade: `helm upgrade` with the same values. Containers never update themselves (`update.enabled: false`); change the image
tag instead. Uninstall: `helm uninstall openlog-agent -n openlog-agent` (the host path state directory stays on the nodes).

## 2. RBAC

Everything is read-only apart from the leader election Lease. Two ServiceAccounts:

| ServiceAccount | ClusterRole rules | Why |
|---|---|---|
| `<fullname>-node` | `pods` get/list/watch | pod metadata of its own node (`fieldSelector=spec.nodeName`) |
| | `nodes/stats` get | kubelet `/stats/summary` (the kubelet authorizes with a SubjectAccessReview) |
| | `namespaces` get, only `kube-system` | `k8s.cluster.uid` |
| `<fullname>-cluster` | `nodes`, `pods`, `namespaces`, `resourcequotas`, `events` get/list/watch | kube-state metrics, events |
| | `apps` deployments/statefulsets/daemonsets/replicasets, `batch` jobs/cronjobs, `autoscaling` HPAs get/list/watch | workloads and exact owners |
| | Role in the release namespace: `leases` create; get/update only the chart's Lease | leader election |

No rule grants Secrets, ConfigMaps, `nodes/proxy` (kubelet exec/logs) or any write outside the Lease. Kinds the cluster
collector may not list (403) are skipped with a warning, so a narrowed ClusterRole works (nodes and pods are required).

## 3. Security posture

- **Node agent privileges.** The DaemonSet mounts the host root read-only (`/host`, `mountPropagation: HostToContainer`)
  and runs with `hostPID` and `hostNetwork` as uid 0 with every capability dropped except `DAC_READ_SEARCH` (read root-only files such as
  container logs and `/proc/<pid>/*` of other users) and `SYS_PTRACE` (per-process I/O counters and executable links),
  `allowPrivilegeEscalation: false`, a read-only root filesystem and the `RuntimeDefault` seccomp profile. It is **not**
  `privileged`. Read access to the host root is equivalent to reading every Secret mounted into pods on that node
  (`/var/lib/kubelet/pods/*/volumes`), so treat the agent namespace like `kube-system`: restrict who can exec into or
  modify the DaemonSet. `hostNetwork` and `hostPID` can be switched off (`node.hostNetwork`, `node.hostPID`) at the cost
  of wrong host network counters, listening ports and process metrics.
- **CRI socket.** The container runtime socket is reachable through the host root mount (CRI list/status calls only; the
  agent never creates, execs into or stops containers, but the socket itself would allow it — this is why the image is
  scratch-based and runs no shell).
- **Kubelet TLS.** With `node.kubelet.insecureSkipVerify: true` (default) the ServiceAccount token is sent to the node's own
  `status.hostIP:10250` without verifying the kubelet certificate. The token only grants the read-only rules above.
  Enable kubelet serving certificate bootstrap and set it to `false` where possible.
- **Tokens.** The projected ServiceAccount token is re-read every minute (rotation). The license key comes from a Secret
  as an environment variable.
- **Data.** Pod labels are only sent when listed in `kubernetes.label_allowlist` (default: `app` and the
  `app.kubernetes.io/*` recommended labels). Container logs go through the agent's secret masking (`logs.mask_secrets`).
  Event messages are sent as-is (truncated at 16 KiB).
- **API server load.** The node agents watch only their node's pods (one watch each, relist every 5 minutes). The cluster
  collector lists with `resourceVersion=0` (served from the API server watch cache, no etcd reads) every 30 s and holds one
  events watch.

## 4. Troubleshooting

| Symptom | Check |
|---|---|
| Pods pending with `violates PodSecurity "baseline"` | label the namespace `pod-security.kubernetes.io/enforce=privileged` (node agent only) |
| `kubelet metrics disabled` or HTTP 401/403 in node agent logs | `nodes/stats` RBAC; kubelet `--authorization-mode=Webhook`; on some managed clusters the kubelet port is firewalled: set `node.config.kubernetes.kubelet.endpoint` |
| `x509: certificate signed by unknown authority` from the kubelet | set `node.kubelet.insecureSkipVerify=true` or bootstrap kubelet serving certificates |
| No container names/pod attributes on container metrics | node agent log `pod list failed` (RBAC), or the CRI socket path is not in `containers.cri_sockets` (defaults: containerd, k3s containerd, CRI-O) |
| No cluster metrics | `kubectl get lease`: a holder must exist; cluster pod logs `k8s.cluster.uid` (needs `namespaces/kube-system` get) |
| Duplicate events after a failover | expected: a new leader re-sends events updated within the last 5 minutes |

Local test on kind (no backend: an OTLP capture server receives the data): [`agents/infra/test/k8s/run.sh`](../../agents/infra/test/k8s/run.sh).
