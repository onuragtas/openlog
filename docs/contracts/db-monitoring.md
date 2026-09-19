# Contract: Database query performance monitoring (D-138)

What the infra agent's database integrations send about statements, sessions and plans, how the backend stores
them and the API that reads them. It complements the server metrics of the integrations
(semantic-conventions §6.4–6.7) and the client view of APM (apm.md §7): APM answers "which of my service's
database calls are slow", this answers "what costs the most on this database server, what are its sessions waiting
for, how does the planner run it — and which services send it".

## 1. Overview

```
PostgreSQL ── pg_stat_statements, pg_stat_activity, EXPLAIN ─┐
MySQL      ── performance_schema digests/threads, EXPLAIN ────┼─ infra agent ─ OTLP logs ─ processor ─ db_query_stats
SQL Server ── dm_exec_query_stats/requests, dm_exec_query_plan┘   (redacts)                (normalizes)  db_session_samples
                                                                                                        db_query_plans
```

Opt-in per integration: `integrations.<postgresql|mysql|mssql>.query_stats.enabled: true` (config.yaml; also per
instance). With it enabled the agent, per instance:

| Kind | When | Event (`event.name`) | Table |
|---|---|---|---|
| Statement statistics | every collection (`integrations.interval`, 30 s) | `openlog.db.query_stats` | `db_query_stats` |
| Session sample | every `query_stats.sample_interval` (10 s) | `openlog.db.session_sample` | `db_session_samples` |
| Execution plan | a top statement at most every `explain_interval` (1 h); sent when its shape changed or once a day | `openlog.db.query_plan` | `db_query_plans` |

| Key (`query_stats.…`) | Default | Meaning |
|---|---|---|
| `enabled` | `false` | switch |
| `top_n` | 20 (max 100) | statements sent per collection, by time spent in the interval |
| `min_calls` | 1 | skip statements executed fewer times in total |
| `sessions` | `true` | sample non-idle sessions |
| `sample_interval` | `10s` (min 1 s) | session sampling interval |
| `explain` | `true` | capture plans |
| `explain_interval` | `1h` (min 5 m) | how often one statement is explained again |

## 2. Records (OTLP log records)

Resource: the integration instance resource (semantic-conventions §6.1): host attributes, `service.instance.id`
(the key of a database instance everywhere below), `server.address`, `server.port`, `openlog.integration.id`.
Every record carries `event.name` and `db.system.name` (`postgresql`, `mysql`, `mssql`).

| Attribute | Stats | Session | Plan | Meaning |
|---|---|---|---|---|
| `db.namespace` | ✓ | ✓ | ✓ | database / schema |
| `db.query.text` | ✓ (required) | ✓ | ✓ (required) | statement text **with literals redacted** (§3.4) |
| `openlog.db.query.id` | ✓ | ✓ | – | server id: `queryid`, `DIGEST`, `query_hash` |
| `openlog.db.user` | ✓ (PG) | ✓ | – | role / login |
| `openlog.db.interval_seconds` | ✓ (required) | – | – | length of the interval the deltas cover |
| `openlog.db.calls`, `.total_time_ms`, `.rows` | ✓ | – | – | executions, time (ms), rows returned/affected **in the interval** |
| `openlog.db.rows_examined`, `.errors`, `.no_index_used` | MySQL | – | – | |
| `openlog.db.blocks_hit`, `.blocks_read` | PG, SQL Server | – | – | shared blocks / logical − physical and physical reads |
| `openlog.db.session.id`, `.session.state` | – | ✓ | – | pid / processlist id / session_id; `active`, `idle in transaction`, `query`, `suspended`, … |
| `openlog.db.wait.type`, `.wait.event` | – | ✓ | – | PG `wait_event_type`/`wait_event`; MySQL `wait/<type>/<event>`; SQL Server Query Store category / `wait_type`; absent = on CPU |
| `openlog.db.duration_ms` | – | ✓ | – | running statement (or, idle in transaction, time since its last statement) |
| `openlog.db.blocking_session_ids` | – | ✓ | – | string array: sessions holding the locks this one waits for |
| `openlog.db.application`, `client.address` | – | ✓ | – | |
| `openlog.db.plan.format`, `.plan.hash`, `.plan.cost` | – | – | ✓ | `json` or `xml`; hash of the plan without estimates; root cost |
| body | – | – | ✓ | the plan document (≤ 512 KiB) |

Statistics are **deltas**: the agent keeps the previous cumulative counters of every fetched statement
(`5 × top_n`, at least 200, at most 1000 by cumulative time), drops the first collection after a (re)start and every
statement whose calls went down (reset, eviction, plan cache flush), and sends the `top_n` heaviest of the interval.

## 3. Agent

### 3.1 PostgreSQL

Needs `pg_stat_statements` (`shared_preload_libraries`, `CREATE EXTENSION` in the integration's database) and a
role with `pg_monitor`. Sessions: `pg_stat_activity` client backends not `idle`, `pg_blocking_pids()`, `query_id`
from PostgreSQL 14. Plans: `EXPLAIN (GENERIC_PLAN, FORMAT JSON)` on PostgreSQL 16+; older servers only explain
statements without `$n` parameters. Only `SELECT`/`WITH`/`VALUES`/`TABLE`/`INSERT`/`UPDATE`/`DELETE`/`MERGE`
statements without `;` are explained, each in `BEGIN TRANSACTION READ ONLY` … `ROLLBACK` with a
`statement_timeout` of at most 5 s. EXPLAIN without ANALYZE never executes the statement. Note: the planner does
evaluate immutable functions on constants; a database user who can create such functions can have them run as the
monitoring role — turn `explain` off where that matters.

### 3.2 MySQL / MariaDB

`performance_schema.events_statements_summary_by_digest` (statement digests on, the MySQL 8 default). The text is
`QUERY_SAMPLE_TEXT` (MySQL 8.0.3+, a real execution: it normalizes to what APM stores) unless truncated, else
`DIGEST_TEXT` (MariaDB). Sessions: `performance_schema.threads` (foreground, not `Sleep`) with
`events_waits_current`; blocking from `data_lock_waits` (MySQL 8). Plans: `EXPLAIN FORMAT=JSON` of the sample in
its schema; every `*condition` string of the plan is redacted (MySQL repeats literal values there).

### 3.3 SQL Server

`sys.dm_exec_query_stats` aggregated per `query_hash` and database; `sys.dm_exec_requests` (+ sessions,
connections) and sleeping sessions that block others (open transactions); the cached showplan of the most expensive
plan handle from `sys.dm_exec_query_plan`, with `ParameterCompiledValue`/`ParameterRuntimeValue` removed and
`ScalarString`/`StatementText`/`ConstValue` redacted. Permission: `VIEW SERVER STATE` (2022:
`VIEW SERVER PERFORMANCE STATE`).

### 3.4 Redaction

Statement text read from activity views carries the values applications sent. Before anything leaves the host the
agent (`internal/integrations/sqlredact`) replaces string, number, hex/bit and dollar-quoted literals with `?`,
drops comments and bounds the text to 4096 bytes; identifiers, keywords and bind placeholders stay. The agent does
not produce the canonical form (§4.1).

## 4. Backend

### 4.1 Normalization and correlation

The processor normalizes `db.query.text` with `apm.NormalizeStatement` — the function applied to `db.statement` of
APM client spans — and stores the result as `query_text`, with `fingerprint` = FNV-1a 64 of it. So the services that
run a statement are `apm_db_queries_1m` rows with `db_statement_normalized = query_text` and the same `db_system`:
no second normalizer can drift. Limits: statements longer than 2048 bytes after normalization are compared on their
truncated prefix; MariaDB (no sample text) matches only statements written with backticks like its digests.

### 4.2 Tables (schema `0096_db_monitoring`)

Sharded by `cityHash64(tenant_id, instance)`, partitioned by day. `db_query_stats` and `db_query_plans` follow the
metrics storage class (30 days), `db_session_samples` the traces class (7 days); all three are trimmed by the
per-tenant retention (D-081). Records without instance, system or (stats, plans) text are dropped
(`invalid_db_*`), plans over 512 KiB too (`db_query_plan_too_large`).

## 5. API

All `GET`, tenant-scoped, `from`/`to` like every read (default: the last hour). `instance` is the
`service.instance.id`.

| Endpoint | Answer |
|---|---|
| `/api/v1/db/instances` | instances with statement statistics or samples in the range: system, host, calls, throughput, total and average time, statements, errors, average active sessions, top wait type |
| `/api/v1/db/queries?instance=&sort=time\|calls\|avg\|rows\|errors\|reads&db=&q=&limit=` | statements (≤ 200): fingerprint, text, calls, throughput, total/avg time, share of the instance's time, rows (per call), rows examined, errors, no-index executions, blocks, cache hit ratio |
| `/api/v1/db/queries/{fingerprint}?instance=&step=` | one statement: totals, series (calls, throughput, avg and total time, rows per step), plans (newest first; `is_current`, `plan_change`), wait events of its samples, callers (APM services) |
| `/api/v1/db/activity?instance=&step=` | average active sessions per wait type per step (samples ÷ sampling instants), top wait events, statements with the most samples |
| `/api/v1/db/sessions?instance=&at=` | the latest sampling instant at or before `at` (ms, default now, within 5 minutes): sessions with state, wait, statement, `blocking_session_ids` and `blocks` (sessions waiting on it, transitively) |
| `/api/v1/db/lookup?db_system=&statement=` | instances whose statistics hold a normalized statement: the link from an APM database call to this view |
