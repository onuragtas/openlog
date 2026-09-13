# Contract: APM (v1, M2)

Application performance monitoring derived from OTLP traces (D-032, [10-m2.md](../plan/10-m2.md) §3). This file is
binding for the processor, ClickHouse schema (`schema/clickhouse/0006_apm.sql`), the edge-linking job
(`internal/apm`), the API (`/api/v1/apm/*`, [api.md](api.md#apm)) and **alert rule conditions** (§10). Identifiers and
metric definitions in §1–§8 are stable: changes are additive only.

## 1. Service identity

A service is the triple taken from the span's **resource** attributes; missing values are `''`.

| Field | Attribute | Column |
|---|---|---|
| `service_name` | `service.name` | `service_name` |
| `service_namespace` | `service.namespace` | `service_namespace` |
| `environment` | `deployment.environment.name`, else `deployment.environment` | `deployment_environment` |

Spans without `service.name` are stored but are not part of APM (no transactions, edges, errors or service rows).
API paths name the service by `service_name` only (`/apm/services/{service_name}`); the optional query parameters
`namespace` and `environment` restrict to one namespace/environment. **Omitted means "all"** (aggregated), present
(even empty) means exact match.

Other resource attributes shown by the UI: `service.version`, `telemetry.sdk.language`, `telemetry.sdk.name`,
`host.id`, `host.name`, `container.id`, `k8s.pod.name`.

**Host ↔ service linkage:** a resource with both `service.name` and `host.id` links the service to that host
(`apm_service_hosts`). The infra agent sends the same `host.id` (semantic-conventions §1), so the host page lists the
services running on it. The processor also upserts the `hosts` table from trace resources carrying `host.id`
(unchanged M1 behavior).

## 2. Transactions

A **transaction** is a service's entry span. The processor sets `is_entry = true` for a span when

1. `kind` is `server` or `consumer`, and
2. the parent is absent or remote:
   - `parent_span_id` is empty, or
   - the span flags carry `SPAN_FLAGS_CONTEXT_HAS_IS_REMOTE` and `SPAN_FLAGS_CONTEXT_IS_REMOTE` (parent is remote), or
   - the flags do not say the parent is local, and the parent span is not in the same OTLP export request with the
     same `service.name`.

The parent lookup is limited to the export request so conversion stays a function of the Kafka record (deduplication
tokens, [kafka.md](kafka.md)). A server span nested under a local span of the same service in a *different* request
without `is_remote` flags is counted as an entry span (SDKs emit a service's local spans in one batch in practice).

### 2.1 Transaction type and name

| `transaction_type` | Condition (first match) | `transaction_name` |
|---|---|---|
| `web` | `http.request.method` or `http.method` present | `<METHOD> <route>`; `route` = `http.route`, else the normalized path (§2.2) of `url.path`, `http.target` or the path of `url.full`/`http.url`; else the span name |
| `rpc` | `rpc.system` present | `<rpc.service>/<rpc.method>` (missing parts from the span name) |
| `messaging` | `messaging.system` present | `<messaging.operation.name \| messaging.operation \| "process"> <destination>`; `destination` = `messaging.destination.name` (normalized like a path segment) |
| `other` | otherwise | span name |

`METHOD` is upper-cased; unknown methods become `_OTHER` (OTel). Names are truncated to 256 bytes (UTF-8 safe).

### 2.2 Path normalization

Query string and fragment are removed; empty segments are collapsed; each segment is replaced when it is

| Segment | Replacement |
|---|---|
| UUID (8-4-4-4-12 hex, any case) | `{uuid}` |
| decimal number (optionally signed) | `{id}` |
| hex string of ≥ 16 characters, or ≥ 8 characters containing a digit and a letter `a-f` only | `{hex}` |
| ≥ 20 characters of `[A-Za-z0-9_-]` containing a digit (tokens, base64 ids) | `{token}` |
| an e-mail address | `{email}` |

More than 8 segments: the rest is replaced by `/…`. Example: `/orders/42/items/5f0e…?x=1` → `/orders/{id}/items/{hex}`.

## 3. Errors

`is_error = true` for a span when `status.code = ERROR`, or `kind = server` and `http.response.status_code`
(`http.status_code`) ≥ 500. Client spans are errors only by status code (OTel SDKs set ERROR for client 4xx/5xx).

**Exceptions** are span events named `exception` with `exception.type`, `exception.message`,
`exception.stacktrace`. The **last** exception event of a span is the span's error.

### 3.1 Error group fingerprint

For every error span of a service (not only entry spans):

```
error_type    = exception.type                     | "HTTP <status>" (HTTP status ≥ 400) | "error"
message       = normalize(exception.message         | status.message | "")
frame         = top in-app frame of exception.stacktrace (§3.2), "" if none
error_group_id = xxhash64(service_name \0 service_namespace \0 environment \0 error_type \0 message \0 frame)
```

`normalize`: quoted strings (`'…'`, `"…"`, `` `…` ``, not starting inside a word, so `can't` stays) → `'?'`; UUIDs →
`<uuid>`; e-mail addresses → `<email>`; IPv4 (with port) → `<ip>`; hex runs ≥ 8 containing a digit → `<hex>`;
numbers delimited by non-word characters → `<n>` (`shard4` stays); whitespace collapsed; at most 512 bytes. The group
id is a `UInt64`, exposed as 16 lower-case hex digits (`group_id`). `error_message` (stored) is the normalized
message; the raw message and stack of samples come from the spans.

### 3.2 Top in-app frame

The first stack frame (top of stack) that is **not** runtime or library code. A frame is library code when its file
path contains `/vendor/`, `node_modules`, `/usr/local/go/`, `/usr/lib/go`, `/go/pkg/mod/`, `GOROOT`, `internal/`
(Node) prefixed by `node:`, `<anonymous>`, `/usr/share/php`, `/usr/lib/python`, `site-packages`, or the function starts
with `runtime.`, `net/http.`, `panic`. Supported formats: Go (`pkg.Func(...)` + `\t/path/file.go:12 +0x1f`), Node/JS
(`    at fn (/app/x.js:10:5)`), PHP (`#0 /app/src/X.php(12): Cls->fn()`), Java (`\tat a.b.C.fn(C.java:12)`), Python
(`  File "/app/x.py", line 12, in fn`). The frame key is `function@file` **without line numbers** (stable across
deploys). No frame found → the first frame of any kind; no stack → `""`.

## 4. Latency, throughput, Apdex

**Sampling correction.** Every span has `sample_weight = 1 / p` where `p` is the sampling probability, first found of:

1. tracestate `ot=th:<hex>` (OTel probability sampling threshold): `p = 1 − T / 2^56`, `T` = hex padded right with
   zeros to 14 digits; `th:0` → `p = 1`;
2. tracestate `ot=p:<n>` (legacy power-of-two): `p = 2^−n` (`n` ≤ 62; `p:63` → weight 0, not counted);
3. span attribute, then resource attribute `sampling.ratio` (float in (0, 1]);
4. otherwise `p = 1`.

`p` is clamped to [1e-6, 1]. All counts below are **weighted** (sums of `sample_weight`); `samples` counts stored spans.

| Metric | Definition (over a time range, per service or transaction) |
|---|---|
| `requests` | Σ weight of entry spans |
| `throughput` (rpm) | `requests / minutes in range` |
| `errors` | Σ weight of entry spans with `is_error` |
| `error_rate` | `errors / requests` (0 when no requests), 0..1 |
| `avg_ms` | Σ(weight × duration) / requests |
| `p50_ms` / `p95_ms` / `p99_ms` | weighted quantile of entry span duration from the latency histogram (§4.1) |
| `time_consumed_ms` | Σ(weight × duration) (sort key "most time consuming") |
| `apdex` | `(satisfied + tolerating / 2) / requests`; satisfied: duration ≤ T **and not error**, tolerating: T < duration ≤ 4T and not error, frustrated: the rest (errors are frustrated, as in New Relic); `null` without requests |

**Apdex T** is set per service (§7, PostgreSQL `apm_service_settings`), default **500 ms**
(`OPENLOG_APM_DEFAULT_APDEX_T`). Lookup: exact (`service_name`, `service_namespace`, `environment`), else
(`service_name`, `''`, `''`), else the default. T is applied **at query time**, so changing it recomputes history.

### 4.1 Latency histogram

Durations are counted in log-linear buckets with 8 buckets per power of two (ratio 2^(1/8) ≈ 1.0905):

```
bucket(d) = clamp(ceil(8 · log2(d_ms)), −80, 200)        d_ms = duration_ns / 1e6;  d_ns ≤ 1000 → −80
bucket b covers (2^((b−1)/8), 2^(b/8)] ms                (b = −80: ≤ ~1 µs, b = 200: ≥ 2^24.875 ms ≈ 8.3 h)
```

stored as `duration_hist Tuple(Array(Int16), Array(Float64))` = (bucket, Σ weight) merged with `sumMap`.

- **Quantile q:** walk buckets in order; in the bucket where the cumulative weight reaches `q · total`, interpolate
  geometrically between its bounds. The result lies in the same bucket as `quantileExactWeighted(q)` on the raw spans,
  so the relative difference is **< 9.1 %** (one bucket width) and typically < 1 % with hundreds of samples (measured:
  0.1–0.5 %, `internal/apm/integration_test.go`).
- **Apdex:** satisfied/tolerating need "not error", so the MV keeps a second histogram of non-error spans
  (`ok_hist`); counts ≤ T use the buckets fully below T plus a linear share of the bucket containing T (same ±4.4 %
  bound on the threshold).
- Reference implementations: Go `apm.HistQuantile`, `apm.HistApdex` (`internal/apm/hist.go`); ClickHouse SQL recipe in
  §10.

## 5. Service map

Nodes: services (`type=service`) and dependencies: `db` (`<db.system>` or `<db.system>/<db.namespace|db.name>`),
`messaging` (`<messaging.system>/<messaging.destination.name>`), `external` (`<server.address>[:<server.port>]`).

**Attribute edges** (processor + MV, every `client` or `producer` span of a service), first match:

| Target type | Condition | Target name |
|---|---|---|
| `service` | `peer.service` | its value |
| `db` | `db.system` (`db.system.name`) | as above |
| `messaging` | `messaging.system` | as above |
| `external` | `server.address`, `net.peer.name`, or host of `url.full`/`http.url` | `host[:port]` (port omitted for 80/443) |

**Trace-linked edges** (edge-linking job, §6): a `client`/`producer` span of service A whose child `server`/`consumer`
span belongs to service B ≠ A. The job records the client span's attribute target as `via` (e.g. `orders:8080`).

API merge rules per (source, target): trace-linked `service` edges win over attribute `service` edges (both count the
same client spans); an attribute `external` edge from A is hidden when a trace-linked edge from A has `via` equal to
its target name (the external address is that service).

Edge RED: `calls` = Σ weight of client spans, `errors` (`is_error`), `avg_ms`, `p95_ms` from the client-side duration.

## 6. Edge-linking job

- Runs on the **api leader** (`postgres.Leader`; in `OPENLOG_AUTH_MODE=static`, on every api process) every
  `OPENLOG_APM_LINK_INTERVAL` (1m), and runs **on each shard**: it connects to one replica per shard (from
  `system.clusters`) and executes `INSERT INTO apm_service_links_1m_local SELECT … FROM spans_local c JOIN spans_local s`
  there. Spans are sharded by `(tenant_id, trace_id)`, so a client span and its child are always on the same shard:
  the join is complete and local.
- Window: whole minutes in `[now − OPENLOG_APM_LINK_LOOKBACK (10m), now − OPENLOG_APM_LINK_DELAY (1m))`. Every run
  recomputes every minute of the window (late spans up to the lookback are picked up).
- Idempotent: `apm_service_links_1m_local` is a `ReplicatedReplacingMergeTree(computed_at)`; a re-run replaces the
  previous result for the same (tenant, source, target, minute) on that shard. Reads use `FINAL` (applied per shard by
  the Distributed table, then summed across shards).
- Metrics: `openlog_apm_link_runs_total{result}`, `openlog_apm_link_rows_total`, `openlog_apm_link_duration_seconds`,
  `openlog_apm_link_lag_seconds` (now − end of the last successfully linked window).

## 7. Database queries

Every span of kind `client` with `db.system` (`db.system.name`) is a DB call (instrumentations that emit DB calls as
`internal` spans are not counted: an ORM's internal span with a client child would count twice).
`db_statement_normalized` = normalize(`db.query.text` | `db.statement` | span name):

- comments removed; string literals (`'…'` with `''` escapes, `E'…'`, `$$…$$`) → `?`; numbers, hex (`0x…`), booleans
  in comparisons → `?`; placeholders `$1`, `:name`, `@p1`, `?` → `?`;
- `IN (?, ?, …)` → `IN (?)`; repeated `VALUES (…), (…)` → `VALUES (…)`;
- key/value stores (`redis`, `memcached`): the command word upper-cased, every argument → `?` (`GET products:42` →
  `GET ?`);
- whitespace collapsed to one space, at most 2048 bytes (then `…`).

`db_operation` = `db.operation.name` | `db.operation` | first keyword of the statement (upper-cased).

## 8. Storage (ClickHouse)

All tables are `Replicated*` `_local` tables with a `Distributed` table of the same name without `_local`, fed by
materialized views on `spans_local` (or the link job). They work with direct shard inserts (D-018) because the MV
fires on the shard that receives the spans block.

| Table | Engine | Key (ORDER BY) | Content | TTL |
|---|---|---|---|---|
| `apm_transactions_1m` | AggregatingMergeTree | tenant, service triple, minute, type, name | requests, samples, errors, duration_sum_ms, duration_max_ms, duration_hist, ok_hist | 30 d |
| `apm_service_edges_1m` | AggregatingMergeTree | tenant, source triple, minute, target_type, target | calls, errors, duration_sum_ms, duration_hist | 30 d |
| `apm_service_links_1m` | ReplacingMergeTree(computed_at) | tenant, source triple, minute, target triple, via | calls, errors, duration_sum_ms, duration_hist | 30 d |
| `apm_db_queries_1m` | AggregatingMergeTree | tenant, service triple, minute, db_system, statement | calls, errors, duration_sum_ms, duration_max_ms, duration_hist, operation, db_name | 30 d |
| `apm_errors_1m` | AggregatingMergeTree | tenant, service triple, minute, error_group_id | count (weighted), samples | 30 d |
| `apm_error_groups` | AggregatingMergeTree | tenant, service triple, error_group_id | first_seen, last_seen, count, error_type, message, stacktrace, span_name, sample trace ids (≤ 10) | 30 d after last_seen |
| `apm_services` | AggregatingMergeTree | tenant, service triple | first_seen, last_seen, version, language, sdk, resource attributes | 30 d after last_seen |
| `apm_service_hosts` | AggregatingMergeTree | tenant, host_id, service triple | first_seen, last_seen, host_name | 30 d after last_seen |

**Sharding correctness.** Spans are sharded by `cityHash64(tenant_id, trace_id)`, so rows of one aggregate key
(e.g. a service's transaction in one minute) are spread over **all** shards, and on each shard over several parts.
Every column is a mergeable aggregate (`sum`, `min`, `max`, `sumMap`, `groupUniqArray`, `anyLast`), and **every read
re-aggregates with `GROUP BY`** through the Distributed table (never relying on background merges or reading `_local`
tables). Merging partial aggregates of disjoint span sets gives the same result as aggregating all spans, so the
answer does not depend on placement. `apm_service_links_1m` is the exception to "mergeable": each shard's job replaces
only its own rows (`FINAL` per shard), and the per-shard results are over disjoint traces, so their sum is exact.
Retried blocks are not counted twice: inserts use `deduplicate_blocks_in_dependent_materialized_views=1`.
The Distributed tables' sharding key `cityHash64(tenant_id, service_name)` is unused (nothing inserts through them).

Derived span columns (expand migration on `spans`): `service_namespace`, `deployment_environment`, `is_entry`,
`transaction_type`, `transaction_name`, `is_error`, `http_status_code`, `sample_weight`, `peer_type`, `peer_name`,
`db_system`, `db_name`, `db_operation`, `db_statement_normalized`, `error_group_id`, `error_type`, `error_message`.
Rows written by an older processor have defaults and are ignored by the APM views.

## 9. API

Base `/api/v1/apm`, tenant-scoped like every telemetry endpoint (viewer role, API keys allowed); the Apdex settings
`PUT` needs a signed-in admin/owner. `from`/`to` as elsewhere (default last hour); `step` (Go duration, ≥ 60s, default
≈ 60 points, rounded up to whole minutes). Service scope parameters on every `services/{service_name}` endpoint:
`namespace`, `environment` (§1). Durations are milliseconds (float), rates 0..1, `throughput` requests per minute.
Endpoints and shapes: [api.md § APM](api.md#apm), [openapi.yaml](openapi.yaml) (tag `apm`).

| Endpoint | Content |
|---|---|
| `GET /apm/services` | services with RED, Apdex, sparkline |
| `GET /apm/services/{s}` | identity, attributes, hosts, Apdex T |
| `GET /apm/services/{s}/overview` | timeseries (throughput, error rate, p50/p95/p99, Apdex) + totals + top transactions |
| `GET /apm/services/{s}/transactions` | transactions list (`sort=time\|throughput\|slowest\|errors`) |
| `GET /apm/services/{s}/transaction?name=&type=` | one transaction: totals, histogram, timeseries, slowest traces |
| `GET /apm/services/{s}/errors` | error inbox (groups) |
| `GET /apm/services/{s}/errors/{group_id}` | group detail: stack, series, sample traces |
| `GET /apm/services/{s}/databases` | normalized DB queries |
| `GET /apm/services/{s}/hosts` | hosts of the service |
| `GET/PUT /apm/services/{s}/settings` | Apdex T |
| `GET /apm/hosts/{host_id}/services` | services on a host |
| `GET /apm/map` | service map (nodes + edges with RED) |
| `GET /apm/traces` | trace search over entry spans → `GET /traces/{trace_id}`; logs via `GET /logs?trace_id=` |

## 10. Alert rule conditions (for `openlog-alert`)

A condition names `service_name` (+ optional `service_namespace`, `environment`, `transaction_type`,
`transaction_name`), a metric from §4 (`throughput`, `error_rate`, `errors`, `avg_ms`, `p50_ms`, `p95_ms`, `p99_ms`,
`apdex`) and a window. Evaluate with the tenant-scoped query layer on `apm_transactions_1m` (table
`query.ApmTransactions1m`), e.g. error rate and p95 of a service over the last 5 minutes:

```sql
SELECT sum(requests) AS req, sum(errors) AS err,
       sumMap(duration_hist) AS h,          -- Go: apm.HistQuantile(h, 0.95); apm.HistApdex(sumMap(ok_hist), total, T)
       sumMap(ok_hist) AS ok
FROM openlog.apm_transactions_1m
WHERE tenant_id = {tenant_id:String} AND service_name = {service:String}
  AND timestamp >= toStartOfMinute(now() - INTERVAL 5 MINUTE) AND timestamp < toStartOfMinute(now())
```

Pure-SQL equivalents: `err / req`; satisfied at T ms with `ok = (keys, vals)`:
`arraySum(arrayMap((k, v) -> if(pow(2, k / 8) <= T, v, if(pow(2, (k - 1) / 8) < T, v * (T - pow(2, (k - 1) / 8)) / (pow(2, k / 8) - pow(2, (k - 1) / 8)), 0)), ok.1, ok.2))`.
The current (incomplete) minute and the last `OPENLOG_PROCESSOR_FLUSH_INTERVAL × 1.5` of data are not final; evaluate
complete minutes. "No data" for a service: no row in `apm_services` with `last_seen` in the window.
