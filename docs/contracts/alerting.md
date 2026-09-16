# Contract: Alerting (M2, D-030)

Rules evaluate telemetry of one organization, open **incidents** and deliver **notifications** to Slack, e-mail
(SMTP), generic webhooks (HMAC-signed) and Microsoft Teams. Plan: [10-m2.md §1](../plan/10-m2.md), cluster rule:
[04-cluster.md](../plan/04-cluster.md) (Alert row). HTTP API: [api.md](api.md#alerting) /
[openapi.yaml](openapi.yaml) (tag `alerts`). Tables: [postgres.md](postgres.md#alerting-0004_alerting).
Configuration: [config.md](config.md#openlog-alert). Code: `internal/alert`, `cmd/openlog-alert`, `internal/api/alerts.go`.

## 1. Components

| Component | Runs in | Does |
|---|---|---|
| Rule/channel/mute/incident API | `openlog-api` (every pod) | CRUD, preview, test send, incident actions. Encrypts channel secrets |
| Evaluator | `openlog-alert` (N replicas) or `openlog-allinone` (`OPENLOG_ALERT_ENABLED=true`) | Leases rules, evaluates them on schedule, writes state transitions, incidents and outbox rows in **one transaction** |
| Dispatcher | same processes as the evaluator | Claims outbox rows (`FOR UPDATE SKIP LOCKED`), delivers them, retries, writes the delivery log |

All state is in PostgreSQL (D-006): an `openlog-alert` pod can be killed at any time. Telemetry in ClickHouse is only
read, and only through the tenant-scoped query layer (`internal/api/query`): the evaluator builds a `query.Scope` from
`organizations.tenant_id` of the rule's organization (never from rule content); every filter value and attribute key is
a bound query parameter. The only ClickHouse write is the evaluation history (§3.6), which is not needed for evaluation.

## 2. Rules

### 2.1 Common fields

| Field | Type | Default | Notes |
|---|---|---|---|
| `name` | string | required | 1–200 chars |
| `description` | string | `""` | ≤ 2000 chars |
| `type` | enum | required | `metric_threshold`, `log_match`, `no_data`, `discovery`, `apm` (§2.6), `apm_no_data` (§2.7), `apm_error` (§2.9), `oql` (§2.10), `slo_burn` (§2.11) |
| `severity` | enum | `warning` | `critical`, `warning`, `info` |
| `enabled` | bool | `true` | |
| `interval_seconds` | int | `60` | 10–3600, evaluation period |
| `for_seconds` | int | `0` | 0–86400, the condition must hold this long before firing (`pending`). Ignored for `discovery` |
| `recovery_for_seconds` | int | `0` | 0–86400, the recovery condition must hold this long before the incident resolves |
| `condition` | object | required | per type, §2.2–2.6 |
| `channel_ids` | uuid[] | `[]` | ≤ 20 channels of the same organization |
| `renotify_interval_seconds` | int | `0` | 0 = off, else 300–604800: repeat the notification while the incident is open and not acknowledged |
| `flapping` | object | `{"enabled": true, "transitions": 4, "window_seconds": 3600, "hold_seconds": 600}` | §3.4 |
| `runbook_url` | string | `""` | `http(s)` URL, ≤ 2000 chars |
| `labels` | map | `{}` | ≤ 20 pairs; keys `^[a-zA-Z0-9_.-]{1,64}$`, values ≤ 256 chars; added to incident labels |

`version` (read-only) increases on every change. Validation errors → `400 invalid_argument` with the field path
(`condition.threshold: required`).

**Filters** (`filters`, ≤ 20, AND-ed): `{"field": F, "op": OP, "values": ["…"]}`.
`F` ∈ `host.id`, `host.name`, `service.name`, `attr.<key>` (data point / log record attribute), `resource.<key>`
(resource attribute, e.g. `resource.env`). `OP` ∈ `eq`, `neq` (exactly one value), `in`, `not_in` (1–100 values),
`contains` (one value, case-insensitive substring). Values ≤ 1024 bytes, keys `^[A-Za-z0-9_.\-/]{1,128}$`.

**Group by** (`group_by`, ≤ 5): `host` (labels `host.id`, `host.name`), `service` (`service.name`), `attr.<key>`,
`resource.<key>`. Each distinct group is a separate **series** ("multi-dimensional"). Empty = one series for everything
that matches. At most `OPENLOG_ALERT_MAX_SERIES_PER_RULE` (1000) series per evaluation; more → evaluation `error`
(`too many series`), state unchanged.

**Target selection** is expressed with filters: one host (`host.id eq …`), a tag (`resource.env eq prod`) or all hosts
(no host filter) — combined with `group_by: ["host"]` for one series per host.

### 2.2 `metric_threshold`

```json
{"metric": "system.cpu.utilization", "aggregation": "avg", "series_aggregation": "sum",
 "window_seconds": 60, "filters": [{"field": "attr.cpu.mode", "op": "not_in", "values": ["idle"]}],
 "group_by": ["host"], "operator": "gt", "threshold": 0.9, "recovery_threshold": 0.8, "missing_data": "keep"}
```

| Field | Default | Notes |
|---|---|---|
| `metric` | required | metric name (gauges and sums; histograms use `count`/`sum` of their `value` column only) |
| `aggregation` | `avg` | per underlying time series over the window: `avg`, `min`, `max`, `sum`, `last`, `count`, `rate`, `p50`, `p95`, `p99` |
| `series_aggregation` | `avg` (`sum` for `rate`, `count`) | across the time series of one group: `avg`, `sum`, `min`, `max`. Ignored for percentiles |
| `window_seconds` | `300` | 10–21600 |
| `operator` | required | `gt`, `gte`, `lt`, `lte` |
| `threshold` | required | finite number |
| `recovery_threshold` | `null` (= `threshold`) | hysteresis; must not be on the breaching side (`gt/gte`: ≤ threshold, `lt/lte`: ≥ threshold) |
| `missing_data` | `keep` | §3.3 |

Aggregation math over the window `[end − window, end)` (raw `openlog.metrics`):
- `avg` = Σvalue / count, `min`, `max`, `sum`, `count`, `last` = value with the greatest timestamp, per time series
  (`series_id`), then `series_aggregation` across the series of the group.
- `rate` (per second): cumulative monotonic sums: sum of positive consecutive differences (a counter reset counts as 0)
  divided by the window; delta sums: Σvalue / window. Then `series_aggregation` (default `sum`).
- `p50`/`p95`/`p99`: `quantile` over all raw points of the group (not per series).

### 2.3 `log_match`

```json
{"query": "timeout", "severity_min": "ERROR", "filters": [], "group_by": ["host"],
 "window_seconds": 300, "operator": "gte", "threshold": 5, "recovery_threshold": null}
```
Value = number of log records in the window whose body contains `query` (case-insensitive; empty = any) with
`severity_number ≥ severity_min` (OTel names or a number; empty = any). Inventory events are excluded like in the logs
API. A group without matching records has value `0` (so `lt` conditions work and firing groups recover).

### 2.4 `no_data`

```json
{"signal": "host", "metric": "", "filters": [{"field": "resource.env", "op": "eq", "values": ["prod"]}],
 "group_by": ["host"], "window_seconds": 300, "lookback_seconds": 86400}
```
- `signal`: `host` (any telemetry: `openlog.hosts.last_seen`), `metric` (requires `metric`), `log`.
- `group_by` is required (`host` or `service`; `host` only for `signal=host`).
- Expected series = groups with data within `lookback_seconds` (600–604800, > window). Value = seconds since the last
  data point. Breaching when value ≥ `window_seconds` (60–86400). Recovers when data arrives again. A series older than
  the lookback disappears and its incident resolves with reason `expired`.

### 2.5 `discovery`

```json
{"event": "service_disappeared", "filters": [{"field": "host.name", "op": "eq", "values": ["web-1"]}],
 "match": "redis", "window_seconds": 900, "lookback_seconds": 86400}
```
Uses inventory items (semantic-conventions §3). `event`:
- `service_disappeared`: a `discovered_service` key (`<rule_id>:<instance>`) present on a host during
  `(end − lookback, end − window]` is absent from every snapshot of that host in `(end − window, end]`, and the host did
  report a snapshot in `(end − window, end]` (a silent host is a `no_data` case). Resolves when the service is seen again
  or when the key is older than the lookback (`expired`).
- `port_opened`: a `listening_port` key present in `(end − window, end]` was not present during
  `(end − lookback, end − window]` although the host reported snapshots then (new hosts do not fire). Resolves once the
  key is older than one window (the change is no longer new).

`match` (optional) is a case-insensitive substring of the item key. `window_seconds` 300–86400 must be ≥ the agent's
inventory interval; `lookback_seconds` > window, ≤ 604800. Series labels: `host.id`, `host.name`, `service` or `port`.
Value = 1 while the event condition holds. `for_seconds` is ignored; `interval_seconds` default for this type is 300.

### 2.6 `apm`

Conditions on the APM rollup of [apm.md](apm.md) §4/§10 (`apm_transactions_1m`, read through the query layer):
```json
{"service_name": "checkout", "service_namespace": null, "environment": "prod", "transaction_type": "",
 "transaction_name": "", "metric": "p95_ms", "group_by": ["transaction"], "window_seconds": 300,
 "min_requests": 10, "operator": "gt", "threshold": 800, "recovery_threshold": 600, "missing_data": "keep"}
```
| Field | Default | Notes |
|---|---|---|
| `service_name` | required | exact `service.name` |
| `service_namespace`, `environment` | `null` | `null` = all (aggregated), a string (also `""`) = exact match (apm.md §1) |
| `transaction_type`, `transaction_name` | `""` | `""` = all transactions |
| `metric` | required | `throughput` (requests/min), `error_rate` (0..1), `errors`, `avg_ms`, `p50_ms`, `p95_ms`, `p99_ms`, `apdex` (apm.md §4 definitions, weighted, histogram quantiles) |
| `group_by` | `[]` | `environment`, `transaction` (labels `environment`, `transaction.type`, `transaction.name`) |
| `window_seconds` | `300` | 60–21600, rounded up to whole minutes; windows end at the last complete minute |
| `min_requests` | `0` | a series with fewer (weighted) requests in the window has no value (missing data), avoiding alerts on a handful of requests |
| `operator`, `threshold`, `recovery_threshold`, `missing_data` | | as §2.2 |

Apdex uses the service's configured T (`apm_service_settings`, exact namespace/environment, then the service wildcard,
else `OPENLOG_APM_DEFAULT_APDEX_T`) resolved per series at evaluation time. Without requests `error_rate`, latency and
Apdex have no value; `throughput` and `errors` are 0. Series labels always include `service.name`.

### 2.7 `apm_no_data`

A service stopped reporting transactions (the no-data semantics of §2.4 on the APM rollup `apm_transactions_1m`):
```json
{"service_name": "checkout", "service_namespace": null, "environment": "prod", "group_by": [],
 "window_seconds": 600, "lookback_seconds": 86400}
```
| Field | Default | Notes |
|---|---|---|
| `service_name` | `""` | exact `service.name`; `""` = every service (one series per service) |
| `service_namespace`, `environment` | `null` | `null` = all, a string (also `""`) = exact match (apm.md §1) |
| `group_by` | `[]` | `namespace`, `environment`: additional series dimensions (labels `service.namespace`, `environment`) |
| `window_seconds` | `600` | 60–86400, rounded up to whole minutes |
| `lookback_seconds` | `86400` | 600–604800, > window, rounded up to whole minutes |

- Expected series = services (per group) with at least one transaction minute within the lookback. Value = seconds
  from the end of the last minute with transactions to the end of the last complete minute before the window end
  (a service that reported in the last complete minute has value 0). Breaching when value ≥ `window_seconds`;
  recovers when transactions arrive again; a series older than the lookback resolves as `expired`.
- Series labels: `service.name`, plus `service.namespace`/`environment` when grouped or fixed by the condition. Series
  key: `service.name=…[|service.namespace=…][|environment=…]`.
- `for_seconds` applies; `interval_seconds` default 60. Entry spans with `sample_weight = 0` do not count (apm.md §4).

### 2.9 `apm_error`

Event rule on the error inbox ([apm.md](apm.md) §3.4): a **new error group** appeared, or a resolved group **regressed**.
```json
{"event": "new_group", "service_name": "checkout", "service_namespace": null, "environment": "prod",
 "match": "timeout", "window_seconds": 300, "min_count": 5}
```
| Field | Default | Notes |
|---|---|---|
| `event` | required | `new_group` · `regressed` |
| `service_name` | `""` | exact `service.name`; `""` = every service |
| `service_namespace`, `environment` | `null` | `null` = all, a string (also `""`) = exact match |
| `match` | `""` | case-insensitive substring of the error type or the normalized message |
| `window_seconds` | `300` | 60–86400, rounded up to whole minutes |
| `min_count` | `0` | `new_group` only: weighted occurrences within the window at least (avoids alerting on a single occurrence); 400 with `regressed` |

- `new_group`: groups (`apm_error_groups`) whose first occurrence is within `[end − window, end)`, excluding ignored
  groups. `regressed`: first runs regression detection on the resolved groups in scope (reopening them, apm.md §3.4),
  then reports the groups with `regressed_at` within the window. Needs PostgreSQL (evaluation error otherwise).
- One series per group: key `error.group_id=<16 hex>`; labels `service.name`, `service.namespace`, `environment`,
  `error.group_id`, `error.type`, `error.message`. Value 1 while the event is in the window; the series then
  disappears and resolves as `expired` (like `discovery`). `for_seconds` is ignored; `interval_seconds` default 60.
- Preview (`Range`) is approximate: `new_group` uses first-seen times without `min_count`; `regressed` uses the stored
  `regressed_at` (no detection).
- Summary: `new error group in checkout (prod): TimeoutError: upstream timed out after <n>ms` /
  `error group regressed in …`.

### 2.10 `oql`

Threshold on an OQL query ([oql.md](oql.md), D-065; `0021_alert_oql`):
```json
{"query": "SELECT percentile(duration.ms, 95) FROM Transaction WHERE service.name = 'checkout' FACET transaction.name",
 "window_seconds": 300, "operator": "gt", "threshold": 800, "recovery_threshold": 600, "missing_data": "keep"}
```
| Field | Default | Notes |
|---|---|---|
| `query` | required | exactly one number column; no `TIMESERIES`, `SINCE`/`UNTIL`, `COMPARE WITH`, `histogram` or `{{variables}}` (`400` with the position) |
| `window_seconds` | `300` | 60–21600; the query range is `[end − window, end)` |
| `operator`, `threshold`, `recovery_threshold`, `missing_data` | | as §2.2 |

- Without `FACET` one series (key `*`); with `FACET` one series per group, labels = facet attribute names (as written) →
  values, key `name=value|…`. Without `LIMIT` at most `OPENLOG_ALERT_MAX_SERIES_PER_RULE` groups are read (more →
  evaluation error `too many series`); a `LIMIT` keeps the top groups by value.
- Raw data is always read (no Metric rollup). A group without events has no value (missing data), except `count`,
  `sum`, `uniqueCount` and `rate` without `FACET`, which are `0`.
- Preview (`Range`) is exact: one query assigns every event to each window that contains it (`arrayJoin`), so windows may
  overlap; `window_seconds / step` must be at most 720.
- Summary: `<column> over 5m is 912 (> 800) (transaction.name=GET /cart)`. `interval_seconds` default 60.

### 2.11 `slo_burn`

Multi-window multi-burn-rate alerting on the error budget of one SLO ([slo.md](slo.md) §3, D-125;
`0087_slo`):
```json
{"slo_id": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
 "windows": [{"name": "fast", "factor": 14.4, "long_seconds": 3600, "short_seconds": 300},
             {"name": "slow", "factor": 6, "long_seconds": 21600, "short_seconds": 1800}],
 "min_requests": 0}
```
| Field | Default | Notes |
|---|---|---|
| `slo_id` | required | an SLO of the organization (api.md [Service level objectives](api.md#service-level-objectives)) |
| `windows` | the two above | 1–4 pairs; `name` `^[a-z0-9_-]{1,32}$` (unique), `factor` > 0 and ≤ 1000, `long_seconds` 300–86400, `short_seconds` 60–`long_seconds` (default `long/12`), both rounded up to whole minutes |
| `min_requests` | `0` | a window whose long window has fewer (weighted) requests has no value |

- Value = `max over the windows of min(burn rate over long, burn rate over short) / factor`, so the comparison
  is fixed at `gte 1`: the factors carry the configuration and 1 means "burning at the configured rate". The
  burn rate is `(bad / requests) / (1 − objective)` over the window (slo.md §2), from `apm_transactions_1m`
  with the SLO's SLI (availability, or latency from the duration histogram).
- Requiring **both** windows means a burst that already stopped does not fire and a fixed incident recovers
  within the short window. Windows end at the last complete minute.
- One series per rule, key `slo.id=<uuid>`; labels `slo.id`, `slo.name`, `slo.window` (the window that
  produced the value), `service.name` and, when the SLO fixes them, `service.namespace`/`environment`.
- Without a value (no window has requests, or all are below `min_requests`) the missing-data behaviour is
  `keep`. `for_seconds` applies; `interval_seconds` default 60.
- Needs PostgreSQL for the definition: without it (or after the SLO was deleted) the evaluation is an error,
  the state is unchanged. Deleting an SLO does not delete its rules.
- Summary: `error budget of Checkout availability burns 21.6× too fast over 1h and 5m (≥ 14.4×)`.

### 2.8 Recommended templates

A catalog of prebuilt rules (code: `internal/alert/templates.go`) for hosts (`host_cpu_high`, `host_memory_high`,
`host_disk_full`, `host_not_reporting`, `service_disappeared`), containers (`container_restarts`, `container_cpu_high`,
`container_unhealthy`), APM services (`apm_error_rate`, `apm_apdex_low`, `apm_latency_p95`, `apm_service_silent`),
Kubernetes (`k8s_pod_crashloop`, `k8s_pod_not_ready`, `k8s_node_not_ready`, `k8s_workload_replicas_unavailable`; code
`internal/alert/templates_k8s.go`, metrics of semantic-conventions §7.4) and integrations (nginx, Redis, MySQL, PostgreSQL; metric names of semantic-conventions §6). `GET /alerts/templates` lists
them with `en`/`tr` names, descriptions and parameters (`kind` number/duration/host/instance/service/environment/text,
`unit`, `required`, `default`, `min`/`max`). `POST /alerts/templates/{id}/render` `{"params", "language", "name",
"channel_ids"}` returns a validated `RuleInput` (label `openlog.template=<id>`) that is previewed and created with the
normal rule endpoints; nothing is stored by rendering.

- Targets: host templates filter `host.id` when `host_id` is given (else all hosts, one series per host). Integration
  templates select one instance (`host_id` + `discovery_id` + `instance` → `resource.openlog.discovery.id/instance`
  filters, one series per host) or, without an instance, every instance of the integration
  (`resource.openlog.integration.id eq <id>`, grouped by `host` and `resource.openlog.discovery.instance`). APM
  templates take `service_name` and optional `environment`.
- Ratio thresholds (`redis_memory_high`: `ratio` × `redis.maxmemory`, `postgresql_connections_high`: `ratio` ×
  `postgresql.connection.max`) need one instance and resolve the reference's latest value in the last hour at render
  time (through the tenant-scoped query layer); missing or `0` → `409 failed_precondition`. The rule keeps the
  resolved number (re-render after changing the server setting).
- `container_restarts` alerts when the Docker restart count grew by at least `restarts` within the window
  (`rate` > (`restarts` − 0.5) / window).
- Kubernetes templates (category `kubernetes`) take optional `cluster_name` (`resource.k8s.cluster.name eq`) and
  `namespace` (`attr.k8s.namespace.name eq`; not on `k8s_node_not_ready`) and are always grouped by
  `resource.k8s.cluster.name` plus the object: `k8s_pod_crashloop` (`openlog.k8s.pod.status` with
  `attr.openlog.k8s.pod.reason eq CrashLoopBackOff`, `last`/`sum` > 0, per namespace and pod, missing data ok),
  `k8s_pod_not_ready` (`attr.openlog.k8s.pod.ready eq false`, phase not `Succeeded`/`Failed`, for 5 min),
  `k8s_node_not_ready` (`k8s.node.condition` with `attr.condition eq Ready`, `last`/`min` < 1 per `attr.k8s.node.name`,
  for 2 min: false and unknown alert), `k8s_workload_replicas_unavailable` (`openlog.k8s.workload.unavailable`, `min` over
  the window > 0 per namespace, kind and name; optional `kind` filter).
- Unknown or out-of-range parameters → `400` with `params.<key>`; unknown template → `404`.

## 3. Evaluation semantics

### 3.1 Schedule and alignment
- Each rule has a stable phase `φ = fnv32a(rule_id) mod interval` (seconds) so rules spread over the interval (jitter).
- An evaluation at wall time `now` covers the window ending at
  `end = floor((now − delay − φ) / interval) · interval + φ` with `delay = OPENLOG_ALERT_EVALUATION_DELAY` (15 s, lets
  the processor catch up). Windows are aligned: every pod computes the same `end`.
- `next_eval_at = end + interval + delay`. An evaluation whose `end` is not newer than the stored `last_eval_end` is
  skipped (idempotent across pods). Missed windows are not back-filled; if the previous evaluation is older than
  `2 × interval + delay`, a `pending` series restarts its `for` timer.

### 3.2 State machine (per series)

```
          breach               breach for ≥ for_seconds
   ok ───────────▶ pending ───────────────────────────▶ firing ──▶ (incident open)
   ▲                 │ no breach                          │ recovered for ≥ recovery hold
   └─────────────────┴───────────────────────────────────┘──▶ resolved (incident) → ok
```
- *breach*: `value OP threshold`. *recovered*: `NOT (value OP recovery_threshold)`. Between the two (hysteresis band)
  a firing series stays firing and a pending series returns to ok.
- `for_seconds = 0`: ok → firing directly. Entering firing opens **one incident** and enqueues one `opened`
  notification per enabled channel of the rule.
- Recovery hold = `recovery_for_seconds`, raised to `flapping.hold_seconds` while the series is flapping. A breach during
  the hold cancels it without a new incident or notification.
- Incident states: `open` → `acknowledged` (user) → `resolved` (auto or user). Resolve reasons: `recovered`, `manual`,
  `no_data` (missing_data=`ok`), `expired`, `rule_disabled`, `rule_deleted`, `rule_changed`.
- A manual resolve of a still-breaching series returns the series to `ok`: it goes through `pending` again and may open
  a new incident.
- Changing a rule's `type` or `condition`, disabling or deleting it resolves its open incidents (reason above) and clears
  its series state. Other edits (name, severity, channels, `for`) keep state.

### 3.3 Missing data (`metric_threshold`)
A series known to the state table (pending/firing/recovering) but absent from the current window:
`keep` (default) leaves the state unchanged; if absent for longer than `max(1h, 10 × window)` it resolves as `expired`.
`ok` treats it as recovered (incident resolves with reason `no_data`). `breach` treats it as breaching (value `null`).
`log_match` groups without records have value 0; `no_data`/`discovery` define absence themselves.

### 3.4 Flapping protection
The last 10 transitions into firing are kept per series. When `flapping.enabled` and at least `flapping.transitions`
(2–20) happened within `flapping.window_seconds` (300–86400), the series is **flapping**: its current incident is kept
open until the series has been recovered continuously for `flapping.hold_seconds` (60–86400), so a flapping signal
produces one incident and one pair of notifications. Timeline event `flapping` marks the start.

### 3.5 Budgets and limits
- Per pod: at most `OPENLOG_ALERT_MAX_CONCURRENT_EVALUATIONS` (16) evaluations at a time, per tenant at most
  `OPENLOG_ALERT_TENANT_MAX_CONCURRENT` (4) and `OPENLOG_ALERT_TENANT_EVALUATIONS_PER_MINUTE` (600, token bucket). An
  evaluation over budget is `throttled` and retried after one second (it keeps its window).
- Every ClickHouse query has `max_execution_time` = `OPENLOG_ALERT_QUERY_TIMEOUT` (20 s).
- `OPENLOG_ALERT_MAX_RULES_PER_ORG` (1000) rules per organization (API, `409 failed_precondition`).

### 3.6 Evaluation history (ClickHouse `alert_evaluations`)

With `OPENLOG_ALERT_EVALUATION_HISTORY=true` (default) every evaluation that **committed** (§4 fencing: at most one set of
rows per rule window) is summarized in `openlog.alert_evaluations` (`schema/clickhouse/0007_alert_evaluations.sql`):

| Column | Rule row (`series_key = ''`) | Series row |
|---|---|---|
| `tenant_id`, `rule_id`, `rule_type` | rule | rule |
| `evaluated_at` | end of the evaluated window (§3.1) | same |
| `labels` | `{}` | series labels |
| `value` | number of firing series | series value (`NULL` = no value) |
| `state` | `firing` if any series fires, else `pending` if any is pending, else `ok` | state after the evaluation (`ok`, `pending`, `firing`) |
| `result` | `ok` or `error` (an error evaluation has only the rule row) | `ok` |
| `duration_ms` | evaluation duration | same |

- **Write path:** independent of the processor. Each evaluator buffers rows and inserts them every 5 s (or per 10 000
  rows) directly into `alert_evaluations_local` of the shard `cityHash64(tenant_id, rule_id)` selects (D-018, the
  processor's direct-insert writer; all rows of a rule are on one shard). A failed batch is retried with the same
  insert deduplication token (never duplicated); while ClickHouse is unavailable at most 100 000 rows are buffered per
  pod and the oldest are dropped (`openlog_alert_evaluation_rows_total{result="dropped"}`). Rows buffered in a pod that
  dies are lost (a gap in the history; state and incidents are unaffected). SIGTERM flushes.
- TTL 30 days (`ttl_only_drop_parts`, daily partitions).
- **API:** `GET /api/v1/alerts/rules/{id}/evaluations?from&to` (viewer and API keys; default the last 24 h, at most
  30 days; `404` for a rule of another organization). Rows are bucketed to at most 500 points (`step_seconds` ≥ 10):
  per series the latest value in the bucket and the worst state (`firing` > `pending` > `ok`); per bucket of rule rows
  `firing_series` (maximum), `evaluations`, `errors`, `duration_ms` (latest) and `max_duration_ms`. At most 50 series
  (most non-ok buckets first); `truncated` when series or rows (50 000) were cut. Read through the tenant-scoped query
  layer (`query.AlertEvaluations`).

## 4. Rule ownership (leases)

Table `alert_rule_leases` (one row per rule, created with the rule) and `alert_evaluators` (heartbeats).
Every `OPENLOG_ALERT_LEASE_RENEW_INTERVAL` (10 s) each evaluator:
1. upserts its heartbeat; `live` = evaluators seen within `OPENLOG_ALERT_LEASE_TTL` (30 s);
2. renews its leases: `UPDATE … SET lease_until = now() + ttl WHERE owner = me` (disabled rules are released);
3. computes its fair share `ceil(enabled_rules / live)`;
4. above the share: releases the excess (latest `next_eval_at` first; `owner = ''`, `lease_until = -infinity`);
5. below the share: claims expired leases, oldest `next_eval_at` first:
   `SELECT … WHERE lease_until < now() ORDER BY next_eval_at LIMIT n FOR UPDATE SKIP LOCKED` → set owner, lease.

Timestamps use the database clock. A new pod gets its share within about two renew intervals; a dead pod's rules move
after the TTL; a stopping pod (SIGTERM) releases its leases and heartbeat at once.

**Fencing.** An evaluation commits only if, inside its transaction, the lease row (locked `FOR UPDATE`) still has
`owner = me AND lease_until > now() AND (last_eval_end IS NULL OR last_eval_end < end)` and the rule row still has the
evaluated `version` and `enabled = true`. Otherwise the whole transaction (series state, incidents, events, outbox rows)
is discarded (`openlog_alert_evaluations_total{result="lease_lost"|"stale"}`). The API locks the same lease row before
changing a rule or an incident, so both serialize. Two pods can therefore never both transition a series; the partial
unique index `alert_incidents_open_uniq (rule_id, series_key) WHERE state <> 'resolved'` is a second guard.

## 5. Notifications

### 5.1 Outbox and delivery
Rows in `alert_notifications` are inserted in the evaluation (or API) transaction that caused them:

| `kind` | When | Idempotency key |
|---|---|---|
| `opened` | series enters firing | `<incident_id>:opened:<channel_id>` |
| `resolved` | incident resolved (any reason) | `<incident_id>:resolved:<channel_id>` |
| `renotify` | open, unacknowledged incident after `renotify_interval_seconds` since the last notification | `<incident_id>:renotify:<n>:<channel_id>` |
| `test` | `POST /alerts/channels/{id}/test` (sent synchronously by the API) | `test:<notification_id>` |

The key is `UNIQUE` (`INSERT … ON CONFLICT DO NOTHING`) and sent to receivers (§5.3). Channels of `opened` = enabled
rule channels when the incident opens (stored on the incident); `resolved`/`renotify` go to the same channels.

Dispatchers (every `openlog-alert` pod, `OPENLOG_ALERT_DISPATCH_WORKERS` workers) claim due rows
(`status = pending AND next_attempt_at <= now()`, oldest first, `FOR UPDATE SKIP LOCKED`), set `status = sending`,
`claimed_until = now() + delivery timeout + 30 s`, deliver, then in one transaction write the attempt to
`alert_delivery_attempts`, the outcome to the row and an incident timeline event. Rules:
- **Ordering:** a row is not claimed while an older row of the same incident and channel is `pending`/`sending`.
  `resolved`/`renotify` are sent only if the `opened` row of that channel was `delivered`; otherwise they become
  `suppressed` (a receiver never gets a resolve without the opening message).
- **Retries:** network errors, timeouts, `408`, `429` (honouring `Retry-After`, ≤ 1 h), `5xx` and SMTP `4xx` are retried
  with exponential backoff and ±20 % jitter: 10 s, 30 s, 1 m, 2 m, 5 m, 10 m, 20 m, 30 m, then every 30 m. Other `4xx`
  and SMTP `5xx` fail at once. After `OPENLOG_ALERT_DELIVERY_MAX_ATTEMPTS` (10) attempts or 24 h the row is `failed`
  (give-up), with timeline event `notification_failed`.
- **Pod death:** a `sending` row whose `claimed_until` passed is returned to `pending` by any dispatcher. Delivery is
  therefore at-least-once: a duplicate is possible only if a process dies (SIGKILL/OOM/power) after the receiver accepted
  the message and before the outcome commit (milliseconds). SIGTERM finishes in-flight deliveries first. Webhook
  receivers deduplicate with `X-Openlog-Idempotency-Key`; e-mails carry a deterministic `Message-ID` that mail clients
  deduplicate. Evaluations are exactly-once by fencing (§4), so a pod death never creates a second incident or outbox row.
- Deleted or disabled channel → row `failed` (`channel deleted` / `channel disabled`) without attempts.
- Rows and attempts are kept 30 days after they finish (pruned by the evaluator loop).

### 5.2 Mute windows
`alert_mutes`: `starts_at`, `ends_at` (≤ 90 days apart), optional `rule_ids`, `matchers`
(`{"label", "op": "eq|neq|contains", "value"}` on incident labels; AND-ed). Checked by the dispatcher at delivery time:
- a matching `opened` row is postponed to the mute's end (timeline `notification_muted`); when it comes due and the
  incident is already resolved it is `suppressed` (and so is its `resolved` row);
- `resolved` rows are postponed like `opened` rows; `renotify` rows during a mute are `suppressed`.
Evaluation, incidents and the timeline are not affected by mutes. Incidents show `muted: true` while a mute matches.

**Recurring mutes.** A mute with `schedule` repeats; `starts_at`/`ends_at` of the input are then ignored:
```json
{"name": "nightly batch", "rule_ids": [], "matchers": [],
 "schedule": {"timezone": "Europe/Istanbul", "days": ["mon", "tue", "wed", "thu", "fri"], "start_time": "22:00",
              "end_time": "06:00", "from": null, "until": null}}
```
| Field | Notes |
|---|---|
| `timezone` | IANA name (default `UTC`) |
| `days` | `mon` … `sun` (stored deduplicated, Monday first) — or instead `rrule`: the RFC 5545 subset `FREQ=WEEKLY;BYDAY=MO,TU,…` (no ordinals) or `FREQ=DAILY`, optional `RRULE:` prefix and `INTERVAL=1`; stored normalized with the derived `days`. Other parts (`COUNT`, `UNTIL`, `BYHOUR`, …) → `400` |
| `start_time`, `end_time` | local `HH:MM`; `end_time` ≤ `start_time` means the next day (max. 23 h 59 min); equal → `400` |
| `from`, `until` | optional RFC3339 / unix ms bounds; `from` defaults to now; occurrences are clipped to `[from, until)` |
| `exdates` | optional, ≤ 366 local dates (`YYYY-MM-DD` or RFC 5545 `YYYYMMDD`) on which no occurrence **starts** (EXDATE subset); stored sorted and deduplicated |
| `holiday_calendar_ids` | optional, ≤ 10 holiday calendars of the organization whose dates are exceptions too (`400` for an unknown id) |

**Monthly rules.** `rrule` also accepts `FREQ=MONTHLY` with `BYMONTHDAY=1,15,-1` (±1..31; `-1` = last day; a day a
month does not have is skipped), `BYDAY=MO,TU` (every such weekday), ordinals `BYDAY=1MO,-1FR` (first Monday, last
Friday; ±1..5) and `BYSETPOS=±1..31` (positions in the month's set of days matching `BYMONTHDAY`/`BYDAY`, e.g.
`FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1` = last weekday). `BYMONTHDAY` and `BYDAY` together restrict each other
(`BYDAY=FR;BYMONTHDAY=13`). Duplicate parts, ordinals outside `MONTHLY`, `BYSETPOS` without `BYDAY`/`BYMONTHDAY` → `400`.
Monthly schedules store `days: []` and the normalized rule.

**Holiday calendars** (`alert_holiday_calendars`, `/alerts/holiday-calendars`): named date sets (≤ 1000 dates:
`YYYY-MM-DD` for one date, `MM-DD` or `--MM-DD` for every year) managed by admins and owners, read by every role. Dates
are local dates in each mute's time zone. Calendar edits apply to referencing mutes from their next check; a calendar
referenced by a mute cannot be deleted (`409 failed_precondition`).

- **Occurrences** start on every selected local date at `start_time` and end at `end_time` (same or next date). They
  keep local wall-clock times across DST changes (a 01:00–04:00 occurrence lasts 2 h on the night clocks go forward,
  4 h when they go back). A local time that does not exist (DST gap) moves forward by the gap (02:30 → 03:30); an
  ambiguous local time is the later instant (after the clocks went back). An occurrence whose converted end is not
  after its start is skipped.
- **Exceptions** skip occurrences that start on an exception date (exdate or holiday); an overnight occurrence that
  started the evening before still runs into the exception date. The next occurrence is searched up to 800 days ahead.
- Responses carry `upcoming` (up to 5 current/next occurrences with exceptions applied); `POST /alerts/mutes/preview`
  `{"schedule"}` returns the same for an unsaved schedule.
- A mute is active inside an occurrence (end exclusive); matching `opened`/`resolved` rows are postponed to the end of
  the **current occurrence**. Creating a mute whose schedule has no occurrence after now → `400`.
- Responses: `starts_at`/`ends_at` = the current or next occurrence (the last one after `until`), `schedule` as
  stored (`null` for one-off mutes), `active`. `GET /mutes` keeps recurring mutes until 7 days after `until`.
- Storage and mixed versions: `alert_mutes.schedule` (jsonb). `starts_at`/`ends_at` hold the current or next occurrence;
  dispatchers move them to the next occurrence once a minute after an occurrence ended, so dispatchers of an older
  version (which ignore `schedule`) mute during the same occurrences. Monthly rules and exceptions are ignored by
  dispatchers older than `0015_alert_holiday_calendars`: during a rolling upgrade they do not mute monthly schedules
  (empty `days`) and mute on exception dates.

### 5.3 Payloads

Common event fields (webhook body; the other formats render the same data):
```json
{"version": "1", "event": "incident.opened", "idempotency_key": "…:opened:…", "notification_id": "…",
 "sent_at": "2026-09-13T10:00:05.000000000Z",
 "organization": {"id": "…", "name": "Default"},
 "rule": {"id": "…", "name": "High CPU", "type": "metric_threshold", "severity": "critical",
          "runbook_url": "", "url": "https://openlog.example.com/alerts/rules/…"},
 "incident": {"id": "…", "state": "open", "url": "https://openlog.example.com/alerts/incidents/…",
              "summary": "system.cpu.utilization avg over 1m is 0.93 (> 0.9) on web-1", "value": 0.93,
              "threshold": 0.9, "labels": {"host.id": "…", "host.name": "web-1"},
              "opened_at": "…", "acknowledged_at": null, "resolved_at": null, "resolve_reason": null}}
```
`event` ∈ `incident.opened`, `incident.resolved`, `incident.renotify`, `test`. Links use `OPENLOG_PUBLIC_URL`
(omitted when unset). Values in summaries are rounded to 4 significant digits.

**Generic webhook** — `POST` to the channel URL, `Content-Type: application/json`, `User-Agent: openlog-alert/<version>`,
`X-Openlog-Event`, `X-Openlog-Delivery: <notification_id>`, `X-Openlog-Idempotency-Key`,
`X-Openlog-Timestamp: <unix seconds>` and, when the channel has an HMAC secret,
`X-Openlog-Signature: sha256=<hex(HMAC-SHA256(secret, timestamp + "." + body))>`. Receivers must recompute the HMAC over
the raw body, compare in constant time and reject timestamps older than 5 minutes. 2xx = delivered. Redirects are not
followed.

**Slack** (incoming webhook) — `{"text": "<fallback>", "blocks": [header ("🔴 FIRING: High CPU" / "✅ RESOLVED: …"),
section with summary, section fields (Severity, Rule, Labels, Opened/Resolved), context (value vs threshold),
actions button "Open incident" (when a public URL is set)]}`. Success = HTTP 200.

**Microsoft Teams** (Workflows "When a Teams webhook request is received" or legacy incoming webhook) —
`{"type": "message", "attachments": [{"contentType": "application/vnd.microsoft.card.adaptive", "content":
{"type": "AdaptiveCard", "version": "1.4", "body": [TextBlock title (attention/good colour), TextBlock summary,
FactSet (Severity, Rule, State, Value, Threshold, labels…)], "actions": [Action.OpenUrl "Open incident"]}}]}`.
Success = HTTP 200/202.

**E-mail** — `multipart/alternative` (plain text + HTML), `Subject: [openlog] FIRING critical: High CPU (web-1)` /
`RESOLVED …`, `Message-ID: <sha256(idempotency_key)[:32]@openlog>`, resolve and renotify mails carry `In-Reply-To` and
`References` of the opening mail (threading). Channel config: `to` (1–50 addresses) and optional SMTP override
(`smtp.host`, `port`, `username`, `from`, `tls`), password as a secret; without an override the global
`OPENLOG_SMTP_*` server is used. `tls`: `starttls` (default, required), `tls` (implicit TLS, port 465), `none`
(plaintext, authentication refused). Only `PLAIN` auth over TLS.

### 5.4 Channel secrets
Secret fields: `url` (slack, teams, webhook), `hmac_secret` (webhook), `smtp_password` (email). They are encrypted with
AES-256-GCM before they reach PostgreSQL:
- key: `OPENLOG_SECRETS_KEY` = base64 of 32 random bytes (`openssl rand -base64 32`); previous keys for reading in
  `OPENLOG_SECRETS_KEY_PREVIOUS` (comma-separated).
- stored value: `ol1:<key id>:<base64(12-byte nonce ‖ ciphertext ‖ tag)>`, key id = first 8 hex chars of
  `sha256(key)`, additional authenticated data = `org_id + "/" + channel_id` (a ciphertext copied to another row does not
  decrypt).
- The API never returns secrets after creation: responses carry `secret_hints` (`https://hooks.slack.com/…/•••f3a9`,
  `•••••• (set)`). `PUT` keeps omitted secret fields. A webhook created without `hmac_secret` gets a generated one,
  returned once as `generated_secrets.hmac_secret`.
- Without `OPENLOG_SECRETS_KEY` the API answers channel writes and test sends with `409 failed_precondition`; the
  dispatcher fails rows of channels it cannot decrypt (`cannot decrypt channel secrets`) after retries.

**Rotation:** (1) deploy with the new key in `OPENLOG_SECRETS_KEY` and the old one in `OPENLOG_SECRETS_KEY_PREVIOUS`
(API and alert pods); (2) run `openlog-alert rotate-secrets` once (re-encrypts every channel with the current key,
idempotent, prints counts); (3) remove the old key. `openlog-alert rotate-secrets -check` reports rows still using other
keys.

### 5.5 Egress
Webhook, Slack and Teams URLs must be `https://` (webhooks may use `http://`). With
`OPENLOG_ALERT_BLOCK_PRIVATE_DESTINATIONS=true` (recommended for SaaS) connections to loopback, private, link-local and
unspecified addresses are refused at dial time (DNS rebinding safe), also for SMTP overrides.

## 6. Incidents

`alert_incidents` rows keep `rule_name`, `rule_type`, `severity`, `labels`, `summary`, `threshold`, `value` (at open),
`last_value`, `opened_at`, `acknowledged_at/by`, `resolved_at/by`, `resolve_reason`, `channel_ids`. Timeline
(`alert_incident_events`, ordered): `opened`, `flapping`, `acknowledged`, `note`, `renotified`, `resolved`,
`notification_delivered`, `notification_failed`, `notification_suppressed`, `notification_muted`. Incidents are kept when
their rule is deleted (`rule_id` becomes null). Resolved incidents older than 400 days are pruned.

Incident labels = series labels + rule `labels` + `alert.severity`, `alert.rule_name`.

## 7. API roles

| Operation | viewer / API key | member | admin, owner |
|---|:-:|:-:|:-:|
| Read rules, rule types, incidents, channels (masked), mutes, delivery log; rule preview | ✓ | ✓ | ✓ |
| Create rules; update, enable/disable, delete **own** rules | | ✓ | ✓ |
| Acknowledge, resolve, add notes to incidents | | ✓ | ✓ |
| Create mutes; update/delete **own** mutes | | ✓ | ✓ |
| Render templates (`/alerts/templates/{id}/render`), mute schedule preview | ✓ | ✓ | ✓ |
| Update/delete any rule or mute; channels create/update/delete/test; holiday calendars create/update/delete | | | ✓ |

Writes need a signed-in user (API keys are read-only) and CSRF as usual. Every write is in the audit log
(`alert.rule.{create,update,delete,enable,disable}`, `alert.channel.{create,update,delete,test}`,
`alert.mute.{create,update,delete}`, `alert.holiday_calendar.{create,update,delete}`, `alert.incident.{acknowledge,resolve}`). Not available with
`OPENLOG_AUTH_MODE=static`.

## 8. Metrics (`openlog-alert` admin `/metrics`)

| Metric | Labels |
|---|---|
| `openlog_alert_evaluations_total` | `result` = `ok`, `error`, `throttled`, `lease_lost`, `stale`, `skipped` |
| `openlog_alert_evaluation_duration_seconds` (histogram) | `type` |
| `openlog_alert_evaluation_lag_seconds` (histogram) | — (start time − `next_eval_at`) |
| `openlog_alert_rules_owned` (gauge) | — |
| `openlog_alert_lease_changes_total` | `change` = `claimed`, `released`, `lost` |
| `openlog_alert_evaluators_live` (gauge) | — |
| `openlog_alert_transitions_total` | `to` = `pending`, `firing`, `ok`, `resolved` |
| `openlog_alert_notifications_total` | `channel_type`, `result` = `delivered`, `retry`, `failed`, `suppressed`, `muted` |
| `openlog_alert_delivery_duration_seconds` (histogram) | `channel_type` |
| `openlog_alert_outbox_pending` (gauge) | — |
| `openlog_alert_evaluation_rows_total` | `result` = `written`, `dropped` (§3.6) |
| `openlog_alert_evaluation_write_errors_total` | — (failed inserts, retried) |

## 9. Not in M2
PagerDuty/Opsgenie (M3); mute schedules beyond the §5.2 subset (yearly rules, `INTERVAL` > 1, RRULE `COUNT`/`UNTIL`, `RDATE`);
evaluation history for previews.
