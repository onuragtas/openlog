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

**Container ↔ service linkage:** a span resource with both `service.name` and `container.id` links the service to that
container (`apm_service_containers`, lower-cased id; also records the resource's `host.id` and `container.name`). The infra
agent reports the same `container.id` for the container's metrics, status and logs (semantic-conventions §2, §4.1), so the
service page lists its containers with state, CPU and memory, and the container page lists its services with RED metrics.
OTel SDKs set `container.id` with their container resource detectors (Go agent: `agents/go`, from `/proc/self/cgroup`, else
`/proc/self/mountinfo` under a private cgroup namespace) or `OTEL_RESOURCE_ATTRIBUTES`. Services without `container.id` are
not linked: matching by `host.id` + process is not done (a host runs many containers; PIDs differ between namespaces).
Only spans link; logs and metrics of applications do not.

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
numbers delimited by non-word characters → `<n>` (`shard4` stays); whitespace collapsed; at most 512 bytes.
**Fingerprint v2** (APM GA, D-068) adds, before the rules above: ISO 8601 date-times (`2026-09-14T10:11:12.345Z`,
`2026-09-14 10:11:12+03:00`) → `<ts>`; the id of a `word_` prefix (`req_8f3a9c2b1d` → `req_<id>`, `order_123456` →
`order_<id>`) when it has ≥ 4 digits only, or ≥ 6 characters mixing digits and letters (`shard_4`, `user_not_found`
stay). Messages without these patterns keep their v1 group id; groups whose message or top frame changes get a new
id once after the upgrade (the old group stops receiving occurrences and expires with retention). The group
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

Fingerprint v2 normalizes the frame key further (build- and deploy-specific parts): Go func literals
`pkg.fn.func1.2` → `pkg.fn.func` and generic instantiations `Fn[go.shape.int]` → `Fn[...]`; Java hidden lambda
classes `Foo$$Lambda$123/0x…` → `Foo$$Lambda`, `lambda$handle$0` → `lambda$handle`, `$Proxy12`,
`GeneratedMethodAccessor12`, `$$EnhancerBySpringCGLIB$$1a2b` without the number; PHP `class@anonymous…` →
`class@anonymous`; in the file: `file://` prefix, query string and fragment removed, directories of ≥ 8 digits, ≥ 12
hex characters or a UUID (release directories) → `<id>`, content hashes of bundled JS/TS files (`main.3f2a1b9c.js` →
`main.js`).

### 3.3 Error group dimensions

`apm_error_group_dims` (schema `0035_apm_ga`) counts every error span of a group per dimension, over retention:
`version` (resource `service.version`, empty values skipped), `host` (`host_id`), `container` (lower-cased
`container.id`) and `transaction` (`transaction_name`, else the span name): `first_seen`, `last_seen`, weighted
`count`, `samples`. The error group detail shows the top 20 values per dimension ("affected versions, hosts,
containers, transactions"); regression detection (§3.4) reads the newest occurrence per version.

### 3.4 Error inbox workflow

Every error group has a workflow state in PostgreSQL (`apm_error_group_states`, [postgres.md](postgres.md#apm-error-workflow-0025_apm_error_workflow)),
keyed by organization and `group_id`; a group without a row is **unresolved** and unassigned.

| Field | Meaning |
|---|---|
| `status` | `unresolved` · `resolved` · `ignored` |
| `assignee` | a member of the organization, or none |
| `resolved_at`, `resolved_in_version` | set while resolved; the version is optional |
| `regressed_at`, `regression_count` | last automatic reopening and how many there were |

Transitions (any, by a signed-in member, admin or owner; audit event `apm.error_group.update` per changed group with
`details.status`/`assignee_user_id` from/to): resolving sets `resolved_at = now` (kept when an already resolved group
is resolved again with the same version); unresolving or ignoring clears `resolved_at` and the version. A no-op
change writes nothing. Ignored groups stay ignored whatever happens (they are excluded from `apm_error` new-group
alerts).

**Regression (auto-reopen).** A resolved group is reopened — status `unresolved`, `regressed_at` = the newest
qualifying occurrence, `regression_count + 1`, audit event `apm.error_group.regressed` by `openlog-apm` with the
occurrence time and version — when it occurs after `resolved_at`:

- without `resolved_in_version`: any occurrence (`last_seen` of the group, or of any version);
- with `resolved_in_version` V: an occurrence in V, or in a version first seen (in `apm_service_versions_1m`, §12) at or
  after V's first span — newer deployments; while V has not reported yet, a version first seen after `resolved_at`.
  Occurrences of versions that were already running are old instances draining and do not reopen the group.
  Versions are compared by first appearance, not by name (no semver assumption); spans without `service.version`
  cannot regress a group resolved in a version.

Detection is not a background job: the API checks resolved groups whenever an inbox or group detail is read, and every
`apm_error` alert rule with `event: regressed` checks the resolved groups in its scope at each evaluation. The first
detector persists the transition with a conditional update (`status = 'resolved' AND resolved_at < occurrence`), so
concurrent detectors reopen a group once.

**Inbox reads.** Candidates are the groups with occurrences in the range (at most 2000, most frequent first); when the
filter selects resolved or ignored groups or an assignee, groups with a matching state row (at most 500) are added with
`count` 0. Filters: `status` (list), `assignee` (`any`, `none`, `me`, user id), `q` (substring of type, message, service,
span name), `sort` (`count`, `last_seen`, `first_seen`). `counts` are per status after the assignee and `q` filters.
Comments (1–4000 bytes, `apm_error_group_comments`) and the group's audit events ("activity") are shown in the detail.

## 4. Latency, throughput, Apdex

**Sampling correction.** Every span has `sample_weight = 1 / p` where `p` is the sampling probability, first found of:

1. tracestate `ot=th:<hex>` (OTel probability sampling threshold): `p = 1 − T / 2^56`, `T` = hex padded right with
   zeros to 14 digits; `th:0` → `p = 1`;
2. tracestate `ot=p:<n>` (legacy power-of-two): `p = 2^−n` (`n` ≤ 62; `p:63` → weight 0, not counted);
3. span attribute, then resource attribute `sampling.ratio` (float in (0, 1]);
4. otherwise `p = 1`.

`p` is clamped to [1e-6, 1]. All counts below are **weighted** (sums of `sample_weight`); `samples` counts stored spans.

### 4.2 Tail sampling (D-075)

When `OPENLOG_TAILSAMPLING_ENABLED=true`, `openlog-sampler` decides per trace before the processor sees the spans
(operations: docs/operations/tail-sampling.md; topics: kafka.md). Its output keeps rule (1) above valid:

- **Policy** (per organization, PostgreSQL `tail_sampling_policies`, API `GET/PUT /api/v1/apm/sampling`; else
  `OPENLOG_TAILSAMPLING_DEFAULT_POLICY`; else keep all): `enabled`, `baseline_ratio`, `max_spans_per_second`, ordered
  `rules` of type `error`, `latency`, `service`, `route`, `attribute`, each with a keep `ratio` (default 1). The
  **first** matching rule gives the tail probability `q`; no match → `baseline_ratio`; `enabled: false` → `q = 1`.
- **Rate limit:** when the tenant's expected kept span rate on the instance exceeds `max_spans_per_second`,
  `q ← q × limit / rate`.
- **Decision:** the trace's head probability `h` is the largest `ot=th`/`ot=p` probability of its spans. With one, the
  trace is kept iff its randomness `R` (tracestate `ot=rv`, else the last 7 bytes of the trace id) ≥ `T(h·q)`,
  i.e. with probability `q` given the head decision (OTel consistent probability sampling). Without one, it is kept
  iff `xxhash64("openlog-tail-sampling/v1:" ‖ trace id) >> 8 < q · 2^56`. `q` is raised so that `h_span · q ≥ 1e-6`
  for every span (the clamp above).
- **Weights:** with `q < 1`, every kept span gets tracestate `ot=th:<T(h_span · q)>` (`th`/`p` sub-keys replaced, other
  `ot` sub-keys and vendors kept, `ot` moved first), where `h_span` is the span's own probability by rules (1)–(4).
  So `sample_weight = 1 / (h_span · q)` and every weighted count stays an unbiased (Horvitz–Thompson) estimate of the
  unsampled traffic. With `q = 1` spans are unchanged.
- **Late spans** of a decided trace (decision cache, `OPENLOG_TAILSAMPLING_DECISION_CACHE_TTL`) follow the decision
  with the same `q`.
- **Preview** (`POST /api/v1/apm/sampling/preview`): the policy is applied to at most 20 000 stored traces of the
  window; each trace counts with `max(sample_weight)` of its spans; kept ratios are `Σ w·q / Σ w` (traces) and
  `Σ w·spans·q / Σ w·spans` (spans).

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

### 5.1 Map filters, counts and transaction paths (GA)

- Without `service`, `namespace` and `environment` filter the whole map: transactions, services, attribute edges and
  trace-linked edges (source and target) of that namespace/environment.
- Service nodes carry `host_count` and `container_count`: distinct `host_id` / `container.id` linked to the service
  (§1) with `last_seen` since `from`. Dependency nodes have 0.
- **Path of a transaction** (`GET /apm/map/path`): up to 50 traces with an entry span of the transaction in the range;
  their spans (±10 minutes) give the node ids of every service and dependency they touch and the edge ids of
  client/producer attribute edges (`source->type:name`, `peer.service` resolved to a service of the same trace) and of
  client/producer → server/consumer parent links between services. Ids are those of `GET /apm/map`; the UI highlights
  the intersection (an external edge hidden by the merge rules has no counterpart).
- The UI keeps dragged node positions per user in the browser (`localStorage`), not on the server.

## 6. Edge-linking job

- Runs on the **api leader** (`postgres.Leader`; in `OPENLOG_AUTH_MODE=static`, on every api process) every
  `OPENLOG_APM_LINK_INTERVAL` (1m), and runs **on each shard**: it connects to one replica per shard (from
  `system.clusters`) and executes `INSERT INTO apm_service_links_1m_local SELECT … FROM spans_local c JOIN spans_local s`
  there. Spans are sharded by `(tenant_id, trace_id)`, so a client span and its child are always on the same shard:
  the join is complete and local.
- Window: whole minutes in `[now − OPENLOG_APM_LINK_LOOKBACK (10m, 1m–24h), now − OPENLOG_APM_LINK_DELAY (1m))`. Every
  run recomputes every minute of the window (late spans up to the lookback are picked up).
- **Daily catch-up** (`OPENLOG_APM_LINK_CATCHUP_ENABLED`, default on): once per UTC day, starting at
  `OPENLOG_APM_LINK_CATCHUP_AT` (default `03:00` UTC, low traffic), the leader re-links the whole previous UTC day in
  windows of `OPENLOG_APM_LINK_CATCHUP_BATCH` (1h), one window at a time over all shards with `max_threads=2` and a 10 s
  pause between windows, next to the regular runs (which never cover the same minutes). Spans that arrived later than
  the lookback but before the pass are linked. The pass is started up to 6 h after its time (a new leader or a restart
  in that window runs it again — harmless, the result is identical); a failed pass is retried at most every 30 min.
  Late calls = weighted linked calls per window after the pass minus before it (`FINAL` on the replica used; approximate
  while replicas lag): `openlog_apm_link_late_calls_total`. Later spans are handled by the late-span re-link.
- **Late-span re-link** (`OPENLOG_APM_RELINK_ENABLED`, default on; schema `0020_apm_relink_queue`). *Processor:* after
  converting a chunk at time `now`, every span that takes part in linking — `client`/`producer` of a service with
  `sample_weight > 0` (its client minute `m`), or `server`/`consumer` of a service with a parent (every client minute
  `m` whose child range `[m, m + 6m)` contains the span, i.e. up to 6 minutes) — contributes the minutes
  `m < now − OPENLOG_APM_RELINK_AFTER` (default `OPENLOG_APM_LINK_LOOKBACK`; newer minutes are covered by the regular
  runs). Minutes are deduplicated per (tenant, minute) within the chunk and written, after the chunk's spans and with
  the chunk's deduplication token, as rows (`tenant_id`, `minute`, `trace_id` = first late trace, `spans`,
  `enqueued_at = now`) into the `apm_relink_queue` Distributed table (sharding key `cityHash64(tenant_id, trace_id)`
  like spans, MergeTree, TTL 8 days on `enqueued_at`). Spans older than `OPENLOG_APM_RELINK_MAX_AGE` (`168h`) are only
  counted. *Leader:* every `OPENLOG_APM_RELINK_INTERVAL` (5m) a background pass reads, through the Distributed table,
  the distinct minutes with rows enqueued in `(watermark − 10m, now − 2m]` (the 2 min settle covers processor clock
  skew and replication; the 10 min overlap re-reads rows that became visible late), older than the regular window's
  start and not older than `OPENLOG_APM_RELINK_MAX_AGE`. A minute is due when it has a row newer than the read bound of
  the pass that last re-linked it. At most `OPENLOG_APM_RELINK_MAX_MINUTES` (120) due minutes per pass, oldest first,
  are re-linked on **every** shard (the queue is not per shard; one late batch usually spans many traces) in windows
  of adjacent minutes (at most `OPENLOG_APM_LINK_CATCHUP_BATCH`) with the link statement and `max_threads=2`; the
  rest, and minutes of failed windows, stay due and hold the watermark. The watermark and the per-minute bounds live
  in memory: a new leader (or restart) re-reads the last hour of the queue (re-linking is idempotent). The catch-up and
  the re-link pass never run at the same time (the later one waits for the next loop iteration). Set
  `OPENLOG_APM_RELINK_AFTER` ≤ the api's `OPENLOG_APM_LINK_LOOKBACK` (same environment on processor and api); a larger
  value leaves a gap between the regular window and the queued minutes. Like the regular runs, a child span that
  starts before its client span's minute (clock skew) is linked only when both are in the same window. The processor
  needs the table: apply migration 0020 before rolling out the processor (expand first, as always).
- Idempotent: `apm_service_links_1m_local` is a `ReplicatedReplacingMergeTree(computed_at)`; a re-run replaces the
  previous result for the same (tenant, source, target, minute) on that shard. Reads use `FINAL` (applied per shard by
  the Distributed table, then summed across shards).
- Metrics: `openlog_apm_link_runs_total{result}`, `openlog_apm_link_rows_total`, `openlog_apm_link_duration_seconds`,
  `openlog_apm_link_lag_seconds` (now − end of the last successfully linked window),
  `openlog_apm_link_catchup_runs_total{result}`, `openlog_apm_link_catchup_duration_seconds`,
  `openlog_apm_link_late_calls_total`. Late-span re-link: processor `openlog_apm_relink_enqueued_total` ((tenant, minute)
  rows built), `openlog_apm_relink_too_old_total` (late spans older than the max age); api
  `openlog_apm_relink_runs_total{result=ok|error|skipped}`, `openlog_apm_relink_minutes_total`,
  `openlog_apm_relink_backlog_minutes` (due minutes left after the last pass), `openlog_apm_relink_late_calls_total`,
  `openlog_apm_relink_duration_seconds`; queue inserts also appear in
  `openlog_processor_rows_inserted_total{table="apm_relink_queue"}`.

## 7. Database queries

Every span of kind `client` with `db.system` (`db.system.name`) is a DB call (instrumentations that emit DB calls as
`internal` spans are not counted: an ORM's internal span with a client child would count twice), **except
connection-management spans**: no statement (`db.query.text`, `db.statement` absent or empty) and an operation
(`db.operation.name` | `db.operation` | span name, lower-cased) that contains `connector` or whose last word (after
`.`, ` `, `/`, `:`) is `connect`, `connection`, `reconnect`, `disconnect`, `close`, `ping`, `reset_session`,
`resetsession`, `reset`, `acquire`, `release` or `handshake` — e.g. otelsql `sql.connector.connect`,
`sql.conn.reset_session`, `sql.conn.ping`, `db.connect`, `pg.connect`. They get no DB columns (`db_system` … `db_operation`
stay empty, so they are not in `apm_db_queries_1m`); their attribute edge to the database (§5) is kept. Rows aggregated
before this rule (processor versions without it) stay until their TTL.
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
| `apm_service_containers` | AggregatingMergeTree | tenant, container_id, service triple | first_seen, last_seen, host_id, container_name | 30 d after last_seen (fixed, not `OPENLOG_APM_RETENTION_DAYS`; schema 0009) |
| `apm_relink_queue` | MergeTree (written by the processor, sharding key `cityHash64(tenant_id, trace_id)`) | enqueued_at, tenant, minute | client minutes of late spans (§6 "Late-span re-link"): trace_id, spans | 8 d after enqueued_at (fixed; schema 0020) |
| `apm_error_group_dims` | AggregatingMergeTree | tenant, service triple, error_group_id, dim, value | first_seen, last_seen, count (weighted), samples per version/host/container/transaction (§3.3) | 30 d after last_seen (fixed; schema 0035) |
| `apm_service_versions_1m` | AggregatingMergeTree | tenant, service triple, minute, service_version | span_count, first_seen, last_seen (§12) | 30 d (fixed; schema 0035) |

**Retention.** The TTLs above (30 d) are the default of `OPENLOG_APM_RETENTION_DAYS` (1–3650). `openlog-migrate` (and
`openlog-allinone` with `OPENLOG_MIGRATE_ON_START`) compares it, after applying the migration files, with the value
recorded in `openlog.table_settings` (`apm_retention_days`; created by the expand migration `0008_table_settings`; no row
= 30) and, when it differs, runs for each of the eight `_local` tables
`ALTER TABLE openlog.<table> ON CLUSTER '<cluster>' MODIFY TTL <timestamp | toDateTime(last_seen)> + INTERVAL <days> DAY`,
then records the new value (a failure part way is repaired by the next run; every ALTER is idempotent). `-plan`
prints the pending change. **ON CLUSTER:** the statement goes through the distributed DDL queue to every host of the
cluster and migrate waits for them (`distributed_ddl_task_timeout`, default 180 s; a host that is down applies it when
it comes back). The tables are `Replicated*`: the metadata change is coordinated through Keeper per shard, so every
replica ends with the same TTL. ClickHouse then materializes the TTL in an asynchronous mutation per replica
(`system.mutations`): with `ttl_only_drop_parts` and daily partitions, shortening the retention drops whole expired
parts (disk space is freed within minutes to hours), lengthening it keeps parts that have not been deleted yet
(data already removed is not restored). Set the same value on every process that runs migrations.

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
| `GET /apm/services/{s}/errors` · `GET /apm/errors` | error inbox with workflow state and filters (§3.4) |
| `GET /apm/services/{s}/errors/{group_id}` | group detail: stack, series, sample traces, affected dimensions (§3.3), state, comments, activity |
| `PATCH /apm/errors/groups` | bulk status / assignee / resolved-in-version (signed-in member+) |
| `GET/POST /apm/errors/groups/{group_id}/comments`, `DELETE …/{comment_id}` | comments |
| `GET /apm/services/{s}/deployments`, `…/deployments/compare` | deployments and before/after comparison (§12) |
| `GET /apm/map/path` | nodes and edges used by a transaction (§5.1) |
| `GET /apm/services/{s}/databases` | normalized DB queries |
| `GET /apm/services/{s}/hosts` | hosts of the service |
| `GET/PUT /apm/services/{s}/settings` | Apdex T |
| `GET /apm/hosts/{host_id}/services` | services on a host |
| `GET /apm/map` | service map (nodes + edges with RED) |
| `GET /apm/traces` | trace search over entry spans → `GET /traces/{trace_id}`; logs via `GET /logs?trace_id=` (`&span_id=` for one span, `transaction` + `transaction_service` for a transaction's traces; cursor paging) |

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
Service level objectives read the same table: their SLI, error budget and burn rates (rule type `slo_burn`)
are computed from `requests`, `errors` and `duration_hist` with the weights of §4 ([slo.md](slo.md)).

## 11. Log ↔ trace navigation

Log records carry `trace_id`/`span_id` from the OTel log record (or the SDK's log bridge). The UI links both ways:
log rows (log explorer, host, container and agent logs) link to the trace and to the span (`/traces/{id}?span=`);
the trace page lists the trace's logs (`GET /logs?trace_id=` within the trace's start − 1 min … end + 1 min, optionally
`span_id=` of the selected span); a transaction links to the logs of its traces (`GET /logs?transaction=&transaction_service=`,
the entry spans in the range, at most 10000 traces). Logs are paged with `next_cursor` ([api.md](api.md#logs)).

**Metric → trace (exemplars, D-130).** OTLP data points carry exemplars — the trace a measurement came from. The
processor stores them in `metric_exemplars` (schema 0092) and `POST /api/v1/metrics/exemplars` returns them for a
metric, range and the Metrics Explorer's filters, so a spike in a chart opens as a trace (`/traces/{id}?span=`). The
table's TTL follows the **traces** retention, not the metrics one: an exemplar is a pointer into a trace, and on the
30-day metrics retention it would outlive its trace by 23 days. There is no trace → metric direction: a trace does not
record which metric instruments observed it.

## 12. Deployments

A **deployment** is a `service.version` that starts reporting spans for a service (per namespace/environment) after
another version did. The materialized view `apm_service_versions_1m` counts spans per version per minute (resource
`service.version`, `''` when missing); detection runs at query time (`apm.DetectDeployments`, `GET /apm/services/{s}/deployments`):

- rows from the range start − 24 h (lookback for the previous version) to the end, in minute order;
- version V at minute m is a deployment when V has not reported in the previous `gap` (default 30 min; 5 min–24 h), and
  the most recently seen other version (the *previous version*) was seen after V's last span — a pause and restart of
  the same version is not a deployment; rolling updates (both versions within the gap) give one deployment at the
  first minute of V;
- no other version seen before: *initial* when V's first span within retention is that minute, else nothing (older than
  the lookback); *rollback* when V had already reported more than `gap` before m;
- spans without `service.version` never form a deployment.

The UI marks deployments on the APM charts. **Compare** (`…/deployments/compare?at=&window=`) returns the RED metrics
and Apdex (§4) of `[at − window, at)` and `[at, at + window)` (capped at now; `at` truncated to the minute; window
5 min–24 h, default 30 min) and the error groups first seen in the second period.

## 13. openlog APM agents

The rules above apply to spans from any OpenTelemetry SDK. The openlog agents are OpenTelemetry distributions that
produce what §1–§7 and §11 need without configuration; all follow the same agent-side contract:

| Rule | Go agent (`agents/go`, D-033) | Node.js agent (`agents/node`, `openlog-node`, D-060) |
|---|---|---|
| Service identity (§1) | `service.name/version/namespace`, `deployment.environment.name` from `OPENLOG_*` | same variables |
| Host and container linkage (§1) | `host.id`: `/run/openlog-infra-agent/host-id` → machine-id files → infra agent state → platform id → generated (semantic-conventions §1); `container.id` from `/proc/self/cgroup`, else `/proc/self/mountinfo` | same chain and sources |
| Entry spans and transaction names (§2) | `openloghttp` sets `http.route` from ServeMux patterns; chi/gin/echo modules | express, koa and `@fastify/otel` set `http.route` on the HTTP server span; otherwise (NestJS, custom routers) the agent copies the longest `http.route` of a local descendant onto the entry server span and renames it `<METHOD> <route>`. NestJS 4–11: OpenTelemetry Nest instrumentation (request context and handler spans); NestJS 12 (ESM-only, not covered upstream): the express/fastify instrumentation names the entry span when the app starts with `--import openlog-node/register`, and the optional `OpenLogNestInterceptor` (`openlog-node/nest`) sets `http.route` from the matched route itself and adds `nestjs.controller`/`nestjs.callback` (no Nest handler spans on 12) |
| Errors (§3) | `exception` events from instrumentations | `exception` events from instrumentations (Node stack format) |
| Sampling weight (§4) | parent-based consistent probability sampling: sampled roots write tracestate `ot=th:<T>` and `sampling.ratio`; local entry spans of sampled remote parents get `sampling.ratio` from `ot=th`/`ot=p`; W3C random flag; optional `ot=rv` (`OPENLOG_SAMPLING_RV`) | identical; verified against fixtures generated from the Go sampler (D-061) |
| DB queries (§7) | `db.query.text` sanitized (`openlogsql.Sanitize`; `OPENLOG_DB_QUERY_TEXT` = `sanitized`, `raw` or `off`) | same algorithm and variable for `db.query.text`/`db.statement`; Redis/Memcached `CMD ? ?`; capped at 4096 characters |
| Log ↔ trace (§11) | slog bridge: OTLP logs with trace/span ids | pino and winston (console opt-in): OTLP logs with trace/span ids, `trace_id`/`span_id` also in the application's own output |
| Deployments (§12) | `service.version` from `OPENLOG_SERVICE_VERSION` | same |

The **Java agent** (`agents/java`, `openlog-javaagent.jar`, D-072) is the upstream OpenTelemetry Java agent plus an openlog
extension and follows the same contract. It uses the same `OPENLOG_*` variables (also as `openlog.*` system
properties) and the same `host.id` chain and `container.id` sources. The upstream instrumentations (Spring MVC/WebFlux,
JAX-RS on a servlet container, Quarkus REST, Vert.x Web, servlet, …) set `http.route` and name the server span
`<METHOD> <route>`; Micronaut and Jersey on Grizzly/Jersey's Jetty handler get a server span named `<METHOD>` without
`http.route`, which the backend groups by normalized `url.path` (§2.1). Exceptions are recorded as `exception`
events in the Java stack format (§3.2). The sampler matches the Go agent's and is verified against the Go fixtures;
the W3C random flag is set by the OTel Java SDK. `db.query.text`/`db.statement` are sanitized with the Go algorithm on
the export path; the upstream sanitizer is disabled by default, and MongoDB/Elasticsearch/OpenSearch keep upstream
sanitization. Redis/Memcached statements become `CMD ? ?`, capped at 4096 characters. Logback/Log4j2 records get
`trace_id`/`span_id` in the MDC and are exported as OTLP logs. `process.command_line`/`process.command_args` are not
sent.

The **.NET agent** (`agents/dotnet`, NuGet `OpenLog.Agent`, D-074) is an OpenTelemetry .NET distribution with the same
contract: the same `OPENLOG_*` variables, `host.id` chain and `container.id` sources. ASP.NET Core sets `http.route`
from the endpoint's route template (minimal APIs, MVC, gRPC) and names the server span `<METHOD> <route>`; the
Node.js agent's longest-descendant-route fallback applies otherwise. Exceptions are `exception` events in the .NET
stack format. The sampler is the Go sampler's port, verified against the Go fixtures; the W3C random flag is added to
every new `Activity` and carried by the agent's own W3C propagator, because OpenTelemetry .NET only models the sampled
bit. OpenTelemetry .NET creates no `Activity` below an unsampled local parent, so downstream services receive the
parent's span id with the same decision and `tracestate`. `db.query.text`/`db.statement` are sanitized with the Go
algorithm (Npgsql, SqlClient, MySqlConnector; EF Core through its provider), Redis statements become `CMD ? ?`, capped
at 4096 characters. `ILogger` records are exported as OTLP logs with trace/span ids, which are also added to the
application's log scopes. MassTransit and Confluent.Kafka (through its OpenTelemetry instrumentation package) continue
the request trace into PRODUCER and consumer-side spans with `messaging.*` attributes. Applications that cannot be
recompiled use the OpenTelemetry .NET automatic instrumentation with the agent's plugin, which applies the same resource,
sampler, propagator, processors and license header (tested against a pinned automatic instrumentation release).
On .NET Framework 4.8 (ASP.NET 4.x) the `netstandard2.0` build runs with OpenTelemetry's `TelemetryHttpModule`, which
creates the SERVER span of every managed request; CI job `dotnet-agent-netfx` (Windows, IIS Express or IIS) runs the
sample and checks the exported span (trace continuation, `url.path`, status, resource, license header).

The **Python agent** (`agents/python`, PyPI `openlog-agent`, D-073) is a distribution of the OpenTelemetry Python SDK
and contrib instrumentations (`openlog-instrument <command>` or `openlog_agent.start()`) with the same `OPENLOG_*`
variables, `host.id` chain and `container.id` sources; stable HTTP and database semantic conventions are enabled by
default. Django, Flask, FastAPI/Starlette, Tornado, Falcon and Pyramid server spans carry `http.route` and are named
`<METHOD> <route>` (the agent sets the route for Flask from the URL rule and for Tornado from the span name, prefixes
Django resolver routes with `/`, adds the method to Pyramid span names and records Falcon responder exceptions); gRPC
server spans carry `rpc.system`/`rpc.service`/`rpc.method`. Celery tasks and aio-pika (RabbitMQ), kafka-python and
aiokafka consumers are `consumer` spans that continue the producer's trace; confluent-kafka consumer spans link to it. Exceptions are `exception` events with
Python tracebacks. The sampler is the Go sampler's port, verified against the Go fixtures (the W3C random flag is set by
the SDK from 1.42, by the agent on the Python 3.9 line). `db.query.text`/`db.statement` are sanitized with the Go
algorithm on the export path, Redis statements become `CMD ? ?`, capped at 4096 UTF-8 bytes. stdlib `logging` records
are exported as OTLP logs and get `trace_id`/`span_id` for the application's own output (structlog processor
available). Workers of pre-fork servers (gunicorn, uWSGI, Celery prefork) report their own `process.pid`; under uWSGI,
which forks without Python's fork hooks, the providers start in each worker after fork. gevent/eventlet greenlets keep
their own active span. Python 3.9 – 3.13.

Runtime metrics are regular OTLP metrics (not APM tables): Java `jvm.*` (memory, GC, threads, classes, CPU); Python
`cpython.gc.*`, `openlog.cpython.gc.time`/`openlog.cpython.gc.pause.max` plus `process.*` (CPU, memory, threads, file
descriptors, context switches); .NET
`dotnet.*` on .NET 9+ (`process.runtime.dotnet.*` on .NET 8) plus `process.cpu.time`, `process.memory.usage`; Go `go.*` and `openlog.go.gc.*`; Node.js
`nodejs.eventloop.*`, `v8js.*` (heap spaces, `v8js.gc.duration`, `v8js.resource.active`), `process.cpu.time`,
`process.memory.usage`. All openlog agents authenticate with the `openlog-license-key` header and export OTLP/HTTP protobuf
(gzip) by default.
