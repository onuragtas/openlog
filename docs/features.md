# What openlog does

A tour of the product, screen by screen — every menu and every tab, in the order the sidebar lists them.
Everything here is built and shipped in the current release; the things deliberately left out are named at the
end, because a feature list that only lists wins is not one you can plan against.

openlog speaks **OTLP** and nothing else: the agents are OpenTelemetry distributions, so the data you send is
portable and the platform has no private wire format to lock you in.

> The screenshots are captured from the demo dataset, so the hosts, services and incidents in them are
> fictional. The screens, columns and controls are the real ones.

---

## Add data

![Add data](images/add-data.png)

The starting point: pick what to monitor and copy a command that is already filled in with this server's
endpoint and a license key. Infrastructure (Linux, macOS, Windows, Docker, Kubernetes), APM (Go, Node.js,
Python, Java, .NET, PHP) and logs (files and journald, containers, browsers). Cards say what a step depends
on rather than letting you discover it later — the PHP agent is marked *Set up first: Linux host*.

---

## Hosts

![Hosts](images/hosts.png)

CPU, memory, disk, load and agent version per machine, with the attributes the agent reports (`env=prod`,
`region=eu-central`, `team=payments`) as filters. Linux, macOS and Windows, amd64 and arm64. A host that
stopped reporting keeps its row and its last-seen time instead of disappearing.

A host opens into six tabs.

### Overview

![Host overview](images/host-overview.png)

CPU by mode, load average, memory by state and filesystem by mountpoint, next to what the machine *is*: OS,
architecture, agent version, host id, tags, the services discovered on it, and its estimated cost with the
idle share.

### Services

![Host services](images/host-services.png)

What the agent found running, and what openlog can do about it: NGINX with its integration enabled, ports and
PIDs; PHP-FPM with no integration and a button that installs the PHP agent. Discovery is matched by process
and listening port, and the card says which.

### Containers

![Host containers](images/host-containers.png)

The containers on this machine with state, image, compose service and live CPU and memory — the same table as
the global screen, minus the host column.

### Inventory

![Host inventory](images/host-inventory.png)

The full snapshot: packages, processes, listening ports, systemd units, kernel modules, users, mounts and
discovered services, counted per category and filterable by key or value. Secrets are masked before they
leave the machine; environment variables and file contents are never collected.

### Vulnerabilities

![Host vulnerabilities](images/host-vulnerabilities.png)

The packages this host reports, matched against published advisories, with severity, CVSS and the version
that fixes each one.

### Logs

![Host logs](images/host-logs.png)

The logs explorer scoped to this machine by a locked `host.id` filter, with the volume histogram grouped by
severity and a link out to the full explorer.

---

## Containers

![Containers](images/containers.png)

cgroup v2 metrics, status and restarts, with Docker Compose and Kubernetes names. containerd and CRI-O
through the CRI API, not just Docker. Unhealthy and exited containers keep their row.

![Container detail](images/container-detail.png)

One container: CPU against the host, memory against its limit, network and block I/O, plus image, runtime,
compose project, health and restart count — with its APM services, logs and attributes a tab away.

---

## Costs

![Costs](images/costs.png)

What the infrastructure costs, estimated from your own telemetry: total, per hour, the idle share and the
part attributable to services, split by service and by host. Prices come from a dated public list-price
table, and a host openlog cannot price is excluded and *said* to be excluded rather than silently averaged.

---

## Kubernetes

![Kubernetes](images/kubernetes.png)

Cluster health from the same agent binary in two modes (DaemonSet and a leader-elected cluster collector) —
no client-go, no sidecar per pod. Nodes ready, pods not ready, unhealthy workloads, allocatable CPU and
memory, workload health by kind, and the recent warning events with their counts.

### Workloads

![Kubernetes workloads](images/kubernetes-workloads.png)

Deployments, StatefulSets, DaemonSets, Jobs and CronJobs with replica health, restarts and resource usage.
A workload whose cluster stopped reporting is `Unknown`, which is not the same as unhealthy.

### Pods

![Kubernetes pods](images/kubernetes-pods.png)

Phase, readiness, restarts, node and IP per pod, namespace under the name.

![Kubernetes pod](images/kubernetes-pod.png)

One pod: CPU, memory, network and a restart counter that steps up over time, with its node, workload, QoS
class and the APM services it runs.

### Nodes

![Kubernetes nodes](images/kubernetes-nodes.png)

Readiness and conditions per node — `MemoryPressure` shows next to `Ready`, because a node can be both —
with capacity, usage, kubelet version and the openlog host behind it.

---

## Integrations

![Integrations](images/integrations.png)

Metrics integrations for the services discovery found, per host: enabled, needs configuration, error, or not
available. A failing integration carries the reason it failed — `NOAUTH Authentication required`, a refused
connection — instead of a red dot.

![Cloud connections](images/integrations-cloud.png)

Managed services from AWS, Azure and Google Cloud accounts, with scopes, polled services and the health of
each connection.

---

## APM

![APM](images/apm.png)

Throughput, error rate, p95 and Apdex per service, with the agent's state on the row: up to date, outdated,
unsupported. **Agents for Go, Node.js, Python, Java, .NET and PHP** — the PHP one is our own C extension
covering PHP 7.1–8.4, including Laravel, Symfony, WordPress and the worker runtimes (Octane, RoadRunner,
Swoole, FrankenPHP), with function-level detail rather than just request timing.

### Service map

![Service map](images/apm-map.png)

Drawn from real traffic, not configuration: services, databases and external calls as nodes, requests per
minute, error rate and p95 on the edges, with the hosts and containers behind each node one toggle away.

### Error inbox

![Error inbox](images/apm-errors.png)

Errors grouped by a fingerprint that survives a deploy — content hashes in bundle names, Go func literals,
Java lambda and proxy numbers, PHP anonymous classes are all normalized away — so "the same error" stays the
same row across releases. Each group carries state (`unresolved` / `resolved` / `ignored`), an assignee,
comments and an activity trail. A group that comes back after a resolve **reopens itself** and is marked
*Regressed*. Browser errors land in this same inbox; there is deliberately no second inbox for the frontend.

### Agent versions

![Agent versions](images/apm-agents.png)

Which agent reports each service and whether it is current, against the latest stable and the oldest
supported release. Third-party OpenTelemetry SDKs are listed as what they are. openlog does not update
language agents by itself — they are your dependencies — so the screen links to Renovate and Dependabot
instead of pretending otherwise.

### A service, end to end

![Service overview](images/apm-service-overview.png)

Throughput, latency percentiles, error rate and Apdex, with deployment markers on the charts and the hosts,
containers and pods the service runs on. The Apdex T threshold is shown and editable, because a score whose
threshold you cannot see is a number you cannot argue with.

![Transactions](images/apm-service-transactions.png)

Per transaction: throughput, average, p95, error rate, Apdex and the share of total time — sorted by time
consumed, which is the order that answers "what should I fix".

![Service errors](images/apm-service-errors.png)

The same error inbox, scoped to this service.

![Databases](images/apm-service-databases.png)

The statements this service runs, with throughput, latency and time share — here one `pg_sleep` query owns
96.6% of the database time — and a link to the database server's own view.

![Service map, focused](images/apm-service-map.png)

This service and its immediate dependencies, with a transaction path you can highlight through the graph.

![Traces](images/apm-service-traces.png)

Search this service's traces by transaction, duration bounds, span attributes or errors only.

---

## Browser

![Browser applications](images/rum.png)

Real user monitoring: page views, sessions and errors per application. The SDK is dependency-free and about
10 KB gzipped.

### Overview

![Browser overview](images/rum-app-overview.png)

**Core Web Vitals** (LCP, INP, CLS, FCP, TTFB) scored on the p75 against the published thresholds, which are
constants here, not settings — a number that means the same thing everywhere is the point of the programme.
A session is a **visit, not a person**: a random, per-tab id that expires, which is what makes it
collectable without a consent banner.

### Pages

![Browser pages](images/rum-app-pages.png)

Per route: views, average, p75, p95, TTFB, LCP p75 and errors. Because a page view is the root of the trace,
one trace runs from the click through your backend to the database.

### Sessions

![Browser sessions](images/rum-app-sessions.png)

Visits with their duration, page views, errors, entry route and browser.

Mobile SDKs for iOS (Swift), Android (Kotlin/JVM) and Flutter (Dart) report screens, errors, custom events
and identity through the same pipeline; a mobile key is scoped by an application allowlist rather than an
origin, since a mobile app has none.

---

## Profiling

![Profiling](images/profiles.png)

CPU and allocation profiles per service, with sample counts and totals. A type is never guessed: nanoseconds
and bytes do not add up, so they are never summed into one number.

![Flame graph](images/profiles-flame.png)

Where the time went, frame by frame, zoomable into any subtree.

![Functions](images/profiles-functions.png)

Self time per function — the value attributed to the innermost frame, which is the first question asked of a
profile — with each function's share and sample count.

The **Go** agent profiles on by default: the cost is a few percent of one core, and a service nobody profiled
is a service whose slow span has no explanation. The **Node.js** agent samples V8 and is off by default
(`OPENLOG_PROFILING=true`), because switching a new signal on for everyone who upgrades would multiply what
they store without anyone asking.

Everything that is *not* instrumented — a database, a PHP-FPM pool, a cron job, a kernel thread — can be
profiled too, by a separate **eBPF whole-host profiler** that `install.sh` installs alongside the agent
(`--no-ebpf-profiler` leaves it out). It samples every process on the machine and names the frames from each
binary's symbols. It is its own package because it runs with `CAP_BPF` and `CAP_PERFMON`, which the infra
agent deliberately does not have; an operator who never installs it keeps that posture exactly. That is
profiling, not auto-instrumentation: it produces flame graphs, not spans.

---

## Databases

![Databases](images/databases.png)

Query performance of the database servers the infra agents monitor: calls per second, average latency,
average active sessions and the wait that dominates.

### Activity

![Database activity](images/database-activity.png)

Active sessions by wait type over time — CPU means no wait, the session was executing — with the wait events
ranked by share of time and the statements that carry the load.

### Queries

![Database queries](images/database-queries.png)

Normalized statements by share of total time, with calls, average latency, rows per call and errors. The
slowest statement is rarely the most expensive one, and this table shows both.

### Sessions

![Database sessions](images/database-sessions.png)

Blocking chains as chains: the session that blocks, what it waits on, how long it has been idle in
transaction, and the sessions stuck behind it.

---

## SLOs

![SLOs](images/slos.png)

Availability or latency objectives over 7-, 28- or 30-day rolling windows, with the SLI, the error budget
left and the current burn rate.

![SLO detail](images/slo-detail.png)

The error budget over the window and the **multi-window multi-burn-rate** windows behind the alert: a fast
window (1 h / 5 m) and a slow one (6 h / 30 m), each with the rate it fires at and whether it is burning
right now. Budgets and burn rates are computed at query time, not precomputed into a table you have to trust.

---

## Synthetics

![Synthetics](images/synthetics.png)

Scheduled checks that call your endpoints and record uptime, latency and what came back — as results *and*
as ordinary metrics, so your existing alert rules and dashboards work on them unchanged. Check types are
HTTP, TCP, DNS and TLS certificate expiry.

![Synthetic detail](images/synthetic-detail.png)

One check: uptime, run and failure counts, latency percentiles, the locations it runs from and when it runs
next.

---

## Job monitoring

![Job monitoring](images/jobs.png)

Cron jobs and heartbeats: the job reports that it ran, and openlog notices when it does not. A job that is
overdue is `Late` before anyone has to look.

![Job detail](images/job-detail.png)

The one line you paste at the end of a crontab entry — `$?` reports both that the job ran and whether it
worked — with the run history, durations, lateness and last output. The ping URL is public by construction,
because it lives in a crontab; the screen says so and offers to replace it.

---

## Vulnerabilities

![Vulnerabilities](images/vulnerabilities.png)

The packages your hosts already report, matched against published advisories, counted by severity, with the
catalog's sync time on the page.

![Vulnerability detail](images/vulnerability-detail.png)

One advisory: which hosts have it, which package, and the installed version next to the fixed one.

---

## Logs

![Logs explorer](images/logs.png)

One filter model over top-level fields, attributes, resource attributes and JSON bodies, with sixteen
operators and OR-groups — the same model in the logs, traces and metrics explorers, with key and value
autocomplete from an index built as data arrives. **Log patterns** cluster millions of lines into the handful
of templates they came from, so a spike shows you *what* is spiking. Drag across the histogram to zoom.

---

## Traces

![Traces explorer](images/traces.png)

Spans with the same filter model, grouped and charted by any key, with duration percentiles over the window
and root-spans-only and slowest-first as one click each.

![Trace detail](images/trace-detail.png)

The waterfall, the selected span's attributes, and the logs of that trace in the same screen. **Log ↔ trace**
navigation works in both directions, and a metric chart's **exemplars** jump straight to a trace that
produced the data point.

---

## Metrics

![Metrics explorer](images/metrics.png)

Every OTLP metric with its type, unit, temporality, series count and the services reporting it. Filter,
aggregate and group by any attribute, add several queries and combine them with a formula. The dots on the
chart are **exemplars**: traces recorded while the metric was measured, each one a link.

---

## Query

![OQL](images/query.png)

**OQL** is the query language: `SELECT count(*) FROM Log FACET severity TIMESERIES`. It compiles to
parameterized ClickHouse SQL through a whitelist, runs as a read-only user with per-organization limits, and
reports syntax errors with a byte offset. Every result says what it cost — rows read, milliseconds, bucket
size, table — so an expensive query is expensive in public. A guided builder writes the query for you when
you do not know the schema yet, and the query text lives in the URL, so a link is a shared answer.

---

## Dashboards

![Dashboards](images/dashboards.png)

Dashboards built from OQL, with visibility, content summary and export per row.

![Dashboard](images/dashboard.png)

Lines, areas, bars, tables, billboards, pies, heatmaps and markdown on a 12-column grid, with variables,
cross-widget filters, several pages, version history (diff and restore), import/export, expiring public share
links and scheduled PDF-quality PNG reports by e-mail. A billboard can compare against the previous period
and say so.

---

## Inventory

![Inventory search](images/inventory.png)

One question across the whole fleet — *which hosts have openssl?* — answered from the snapshots the agents
already send, with the installed version per host.

---

## Fleet

![Fleet](images/fleet.png)

Agent versions, update capability and staged rollouts. **One version across everything** — backend, UI,
chart, images and agents — published as a signed release manifest, and the catalog's signature is verified
before anything is offered. Rollouts go in waves (10% → 50% → 100%), honour maintenance windows, halt
automatically on a failure rate, and can be rolled back to a named version. A single host can be held or
pinned. Agents that cannot update themselves (containers) are counted separately rather than reported as
failures.

---

## Alerts

![Incidents](images/alerts-incidents.png)

Incidents with state, severity, the condition that opened them and how long they have been open. The
incident title *is* the evaluation — `system.cpu.utilization avg over 5m is 0.94 (> 0.9) on web-1` — so the
list reads without opening a row.

![Incident](images/alerts-incident.png)

One incident: the value at open against the threshold, the current value, labels, a timeline you can annotate,
and the delivery attempts per channel including the ones that failed and were retried.

![Rules](images/alerts-rules.png)

Rules with type, severity, state and last evaluation. A disabled rule says it has not been evaluated rather
than showing a stale `OK`.

![New rule](images/alerts-rules-new.png)

Ten rule types — metric thresholds, log matches, no-data, discovery events, APM (error rate, latency,
Apdex), APM silence, APM error groups, OQL, **SLO burn rate** and **baseline anomaly** — each with
hysteresis, `for`/recovery windows, flapping protection, renotification and multi-dimensional grouping.

![Templates](images/alerts-templates.png)

Prebuilt rules for hosts, containers, APM services, Kubernetes and individual integrations: set the
parameters, check the preview, create the rule — or open it in the editor and keep going.

![Channels](images/alerts-channels.png)

Slack, e-mail, signed webhooks, Microsoft Teams, **PagerDuty** (Events API v2) and **Opsgenie**, each
testable from the row, with an outbox so a notification is not lost when a channel is down.

![Routing](images/alerts-routing.png)

The first matching rule decides which channels an incident reaches — by severity, service, rule type, labels
or local time of day. Without a match, the alert rule's own channels are notified.

![Mute windows](images/alerts-mutes.png)

Mute windows with recurrence, in your timezone, with DST handled, scoped to all rules or to one condition —
plus **holiday calendars**, named date lists that a recurring mute skips.

Delivery is exactly-once per transition: evaluation runs under a PostgreSQL lease with fencing, so two
replicas cannot both page you.

---

## Settings

![Organization](images/settings-organization.png)

Organizations are tenants, and the tenant comes **only** from the authenticated principal — no endpoint
accepts a tenant parameter, and the query layer injects the predicate itself. Support access is off until you
grant it, for 24 hours or 7 days, and data export is opt-in per signal.

![Profile](images/settings-profile.png)

Your account across every organization, a data request for yourself, and account deletion that asks for your
e-mail and password rather than a single button.

![Members](images/settings-members.png)

Four roles, invitations that expire after 7 days, and an honest note that invitations are not e-mailed yet —
you send the link.

![License keys](images/settings-license-keys.png)

The ingest keys agents and OTLP exporters authenticate with, with last use and revocation. Revoked keys stay
listed.

![API keys](images/settings-api-keys.png)

Keys for scripts and integrations, each carrying its own role and expiry. A key only reads data unless you
give it a role that may change configuration.

![Browser keys](images/settings-browser-keys.png)

The RUM SDK's public keys, bounded by origin allowlist, events per minute and sampling rather than by
secrecy — a browser key ships inside a web page and is public by construction, so openlog shows it rather
than forcing a rotation of something the internet already has.

![Source maps](images/settings-source-maps.png)

Upload the `.map` your build produced and browser stacks un-minify: `at greet (src/app.ts:5:3)` instead of
`at n (main.3f2a1b9c.js:1:842)`. Maps are matched by file name and are never served back — they are read only
to un-minify a stack.

![Security](images/settings-security.png)

Password change and every browser signed in to your account, each revocable.

![Single sign-on](images/settings-sso.png)

OIDC and SAML with SLO and back-channel logout, set up as a four-step wizard, over e-mail domains you verify
by DNS TXT record, with SCIM provisioning and group-to-role mapping.

![Audit log](images/settings-audit-log.png)

Who changed what: members, invitations, keys, fleet, alerting and settings, filterable by actor, action and
period, with the IP address on every row.

![APM sampling](images/settings-apm-sampling.png)

Tail sampling decided after a trace is complete: ordered rules (keep every error, keep slow requests of one
service, keep a named set of services, drop health checks to 1%) over a baseline ratio, with a span-rate
ceiling. The first matching rule wins, and **Preview on last hour** estimates what a policy would have kept
before you save it. Kept spans are weighted so APM counts stay accurate.

![Usage](images/settings-usage.png)

Data ingested, active hosts, users and query compute against the plan's limits for the billing period, with a
projection to period end, per-signal breakdown with retention, and CSV or JSON export.

![Status page](images/settings-status-page.png)

Publish incidents and maintenance windows to a public status page; component health and uptime come from
automatic checks.

---

## Run it yourself

- **Deploy** with Docker Compose on one machine or the Helm chart on a cluster (Strimzi, Altinity and CNPG
  operators), with tiered storage moving cold data to S3.
- The updater takes a PostgreSQL backup, applies expand/contract migrations, health-checks the new version
  and **rolls back** on failure.
- **Terraform provider** for alert rules, channels, routing, SLOs and dashboards; an **MCP server** so an AI
  assistant can query your telemetry read-only.
- Per-tenant retention, data export and deletion with a certificate at the end.

---

## What is not here

Named on purpose, so you can plan:

- **eBPF auto-instrumentation** — not built. Instrumenting a process without touching its code is a later
  milestone. The eBPF profiler above produces flame graphs, not spans.
- **Session replay** — out of scope.
- **Cloud billing** — the cost screens estimate from your own telemetry; openlog never reads a provider's
  bill, and only compute is priced.
- **Synthetic checks run from one location** — the openlog server itself.
- **No `rum` alert rule type**, and that is deliberate: the `oql` rule type runs any OQL query, and RUM page
  views, vitals and sessions are OQL event types, so alerting on them needs no rule type of its own.

More detail on any of these: the [contracts](contracts/) describe every signal, endpoint and guarantee, and
[the roadmap](plan/06-roadmap.md) lists what is validated and what is still open.
