#!/usr/bin/env python3
"""Assertions of the Kubernetes live test (run.sh) on the capture server summary (GET /summary, stdin)."""
import json
import sys

d = json.load(sys.stdin)
s = d["summary"]
names = set(d["metric_names"])
failures = []


def check(cond, msg):
    print(("ok   " if cond else "FAIL ") + msg)
    if not cond:
        failures.append(msg)


def points(metric):
    return list((s.get("points") or {}).get(metric, {}).values())


def find(metric, **attrs):
    return [p for p in points(metric) if all(p.get(k.replace("__", ".")) == v for k, v in attrs.items())]


# ---- resources ----
res = s["resources"]
node, cluster = res.get("node", {}), res.get("cluster", {})
check(node.get("k8s.cluster.name") == "kind-m4" and node.get("k8s.node.name") and node.get("k8s.cluster.uid") and node.get("host.id"),
      "node agent resource: k8s.cluster.name/uid, k8s.node.name, host.id")
check(cluster.get("openlog.entity.type") == "k8s_cluster" and cluster.get("k8s.cluster.uid") == node.get("k8s.cluster.uid") and "host.id" not in cluster,
      "cluster agent resource: entity k8s_cluster, same cluster uid, no host.id")

# ---- metric names (semantic-conventions §7.3, §7.4) ----
for m in ["k8s.node.cpu.usage", "k8s.pod.cpu.usage", "k8s.pod.memory.working_set", "k8s.pod.network.io", "k8s.container.cpu.usage",
          "k8s.container.memory.working_set", "openlog.k8s.cluster.status", "openlog.k8s.node.status", "k8s.node.condition",
          "k8s.node.allocatable_cpu", "openlog.k8s.pod.status", "k8s.pod.phase", "k8s.container.restarts", "k8s.container.ready",
          "k8s.container.cpu_request", "k8s.container.memory_limit", "openlog.k8s.workload.status", "openlog.k8s.workload.unavailable",
          "k8s.deployment.desired", "k8s.deployment.available", "k8s.daemonset.ready_nodes", "k8s.cronjob.active_jobs",
          "k8s.job.successful_pods", "k8s.namespace.phase", "container.cpu.utilization", "openlog.container.status"]:
    check(m in names, "metric " + m)

# ---- node / kubelet ----
ready = find("k8s.node.condition", condition="Ready")
check(len(ready) == 2 and all(p["_value"] == "1" for p in ready), "2 nodes with Ready=1 (%d)" % len(ready))
check(len(points("openlog.k8s.node.status")) == 2 and all(p.get("openlog.k8s.node.ready") == "true" for p in points("openlog.k8s.node.status")),
      "openlog.k8s.node.status for 2 ready nodes")
web_cpu = find("k8s.pod.cpu.usage", k8s__namespace__name="demo", k8s__deployment__name="web")
check(len(web_cpu) >= 2 and all(p.get("k8s.pod.label.app") == "web" and p.get("openlog.k8s.workload.kind") == "Deployment" for p in web_cpu),
      "kubelet pod cpu of the 2 web pods with owner and label attributes")
check(all("k8s.pod.label.secret-label" not in p for p in web_cpu), "labels outside the allowlist are not sent")
wc = find("k8s.container.memory.working_set", k8s__namespace__name="demo", k8s__container__name="app")
check(len(wc) >= 2 and all(len(p.get("container.id", "")) == 64 for p in wc), "kubelet container metrics carry container.id")

# ---- container (cgroup) metrics enriched ----
cg = [p for p in points("container.cpu.utilization") if p.get("k8s.namespace.name") == "demo" and p.get("k8s.container.name") == "app"]
check(len(cg) >= 2 and all(p.get("k8s.deployment.name") == "web" and p.get("k8s.pod.uid") and p.get("k8s.node.name") and p.get("k8s.cluster.name") == "kind-m4"
                           for p in cg), "cgroup container metrics enriched with pod uid, deployment, node, cluster")
static = [p for p in points("container.cpu.utilization") if p.get("k8s.container.name") == "etcd"]
check(all(p.get("openlog.k8s.workload.kind") == "Pod" for p in static), "static pods reported as bare pods")

# ---- cluster state ----
crash = find("openlog.k8s.pod.status", k8s__namespace__name="demo", k8s__deployment__name="crasher")
check(any(p.get("openlog.k8s.pod.reason") in ("CrashLoopBackOff", "Error") and int(p.get("openlog.k8s.pod.restarts", "0")) >= 1 for p in crash),
      "crasher pod status reason CrashLoopBackOff/Error with restarts")
web_wl = find("openlog.k8s.workload.status", openlog__k8s__workload__kind="Deployment", openlog__k8s__workload__name="web")
check(len(web_wl) == 1 and web_wl[0].get("openlog.k8s.workload.desired") == "2" and web_wl[0].get("openlog.k8s.workload.available") == "2",
      "web deployment desired=2 available=2")
crash_un = find("openlog.k8s.workload.unavailable", openlog__k8s__workload__name="crasher")
check(len(crash_un) == 1 and float(crash_un[0]["_value"]) >= 0, "crasher unavailable replicas reported")
check(len(find("openlog.k8s.workload.status", openlog__k8s__workload__kind="CronJob", openlog__k8s__workload__name="tick")) == 1, "cronjob tick workload")
check(not find("openlog.k8s.workload.status", openlog__k8s__workload__kind="ReplicaSet"), "deployment-owned ReplicaSets are not workloads")
tick_pods = find("openlog.k8s.pod.status", k8s__cronjob__name="tick")
check(len(tick_pods) >= 1 and all(p.get("openlog.k8s.workload.kind") == "CronJob" for p in tick_pods), "cronjob pods resolve to the CronJob")
ds = find("openlog.k8s.pod.status", openlog__k8s__workload__kind="DaemonSet", openlog__k8s__workload__name="oa-openlog-agent-node")
check(len(ds) >= 2, "agent DaemonSet pods (%d, including pods replaced by a rollout)" % len(ds))

# ---- logs ----
clogs = s["logs"].get("container", [])
web_logs = [l for l in clogs if l.get("body", "").startswith("web-log line")]
check(len(web_logs) >= 1 and all(l.get("resource.k8s.deployment.name") == "web" and l.get("resource.k8s.pod.uid") and l.get("resource.k8s.pod.label.app") == "web"
                                 and l.get("resource.k8s.cluster.name") == "kind-m4" for l in web_logs),
      "container logs of web pods with k8s resource attributes (%d samples)" % len(web_logs))
check(any(l.get("body", "").startswith("crasher-log") for l in clogs), "logs of the crashing container")
events = s["logs"].get("event", [])
backoff = [e for e in events if e.get("k8s.event.reason") == "BackOff" and e.get("k8s.pod.name", "").startswith("crasher-")]
check(len(backoff) >= 1 and all(e.get("k8s.event.type") == "Warning" and e.get("k8s.object.kind") == "Pod" and e.get("k8s.pod.uid") for e in backoff),
      "Warning BackOff events of the crasher pod")
check(any(e.get("k8s.object.kind") == "CronJob" or e.get("k8s.object.kind") == "Job" for e in events), "events of the cronjob")

auth = [k for k in s["requests"]]
print("requests:", s["requests"])
if failures:
    print("\n%d check(s) failed" % len(failures))
    sys.exit(1)
print("\nall checks passed")
