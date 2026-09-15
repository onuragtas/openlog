# Contract: OQL query language (M3)

OQL is openlog's NRQL-like query language for charts, custom dashboards ([api.md](api.md#query-language-oql),
[api.md](api.md#dashboards)) and `oql` alert rules ([alerting.md](alerting.md) §2.10). Code: `internal/oql`
(lexer → parser → AST → validated plan → parameterized ClickHouse SQL through `internal/api/query`). Decision: D-063.

```sql
SELECT count(*), percentile(duration.ms, 95) FROM Transaction
WHERE service.name = 'checkout' AND http.status_code >= 500
FACET transaction.name SINCE 3 hours ago TIMESERIES 5 minutes LIMIT 5 COMPARE WITH 1 day ago
```

## 1. Grammar

```
query      := SELECT item (',' item)* FROM EventType clause*
clause     := WHERE cond | FACET attr (',' attr)* | SINCE time | UNTIL time
            | TIMESERIES [duration | AUTO] | LIMIT integer | COMPARE WITH duration AGO
item       := agg [AS (identifier | string)]
agg        := count '(' ('*' | attr) ')' | (sum | average | avg | min | max | uniqueCount | median
            | latest | earliest) '(' attr ')' | percentile '(' attr (',' number)+ ')'
            | rate '(' agg ',' duration ')' | filter '(' agg ',' WHERE cond ')'
            | histogram '(' attr ',' number [',' integer] ')'
cond       := and (OR and)*      and := not (AND not)*      not := NOT not | '(' cond ')' | predicate
predicate  := attr ('=' | '!=' | '<>' | '<' | '<=' | '>' | '>=') value
            | attr [NOT] IN '(' value (',' value)* ')' | attr [NOT] LIKE value | attr [NOT] CONTAINS value
            | attr IS [NOT] NULL
value      := string | number | true | false | variable
attr       := identifier | `backtick quoted` | attributes '[' string ']' | resource '[' string ']'
duration   := number unit            unit := second(s) | sec | minute(s) | min | hour(s) | day(s) | week(s)
time       := duration AGO | string (RFC3339 or 'YYYY-MM-DD HH:MM:SS', UTC) | integer (unix ms) | NOW
variable   := '{{' name '}}'
```

- Keywords and function names are case-insensitive; attribute names are case-sensitive. Clauses after `FROM` may come in
  any order, each at most once. Strings use `'…'` or `"…"` with `\\`, `\'`, `\"`, `\n`, `\t` escapes (`''` inside a
  single-quoted string is one quote). `--` and `//` start a comment until the end of the line.
- Identifiers: `[A-Za-z_][A-Za-z0-9_.]*` or any backtick-quoted text without backticks.
- Defaults: `SINCE 1 hour ago`, `UNTIL now`, `LIMIT 10` (facets), `TIMESERIES` = `TIMESERIES AUTO`.
- Every result column is an aggregate (no raw event listing); `percentile(x, 50, 95)` yields one column per level.

## 2. Event types and attributes

| Event type | Table | Time column | Notes |
|---|---|---|---|
| `Log` | `logs` | `timestamp` | 14-day retention |
| `Span` | `spans` | `timestamp` | 7-day retention |
| `Transaction` | `spans` | `timestamp` | entry spans only (`is_entry` is always added, cannot be removed) |
| `Metric` | `metrics` or `metrics_1m` | `timestamp` | 1-minute rollup chosen automatically (§4) |
| `Host` | `hosts FINAL` | `last_seen` | hosts seen in the range |
| `Container` | `containers` (merged per container) | `last_seen` | containers seen in the range |

Attributes (`GET /api/v1/query/schema` lists them with types):

| Event type | Attributes (type string unless noted) |
|---|---|
| `Log` | `service.name`, `host.id`, `host.name`, `severity` (= `severity.text`), `severity.number` (number), `message` (= `body`), `trace.id`, `span.id`, `event.name`, `scope.name` |
| `Span`, `Transaction` | `name` (Transaction: transaction name; `span.name` = span name), `kind`, `status.code`, `status.message`, `trace.id`, `span.id`, `parent.id`, `service.name` (= `appName`), `service.namespace`, `deployment.environment`, `host.id`, `duration` (number, seconds), `duration.ms` (number), `transaction.name`, `transaction.type`, `entry` (bool), `error` (bool), `http.status_code` (number), `db.system`, `db.name`, `db.operation`, `db.statement`, `peer.type`, `peer.name`, `error.type`, `error.message`, `sample.weight` (number), `scope.name` |
| `Metric` | `metricName` (= `metric.name`), `metric.type`, `unit`, `service.name`, `host.id`, `host.name`\*, `value` (number), `count`\* (number), `sum`\* (number), `scope.name`\* |
| `Host` | `host.id`, `host.name`, `os.type`, `os.description`, `arch`, `agent.name`, `agent.version` |
| `Container` | `container.id`, `container.name`, `host.id`, `host.name`, `image.name`, `image.tags`, `runtime`, `compose.project`, `compose.service`, `k8s.pod.name`, `k8s.namespace.name`, `k8s.container.name`, `state`, `health`, `restarts` (number) |

\* raw data points only (prevents the rollup).

Maps: `attributes['k']` (Log, Span, Transaction, Metric, Container) and `resource['k']` or `resource.k` (Log, Span,
Transaction, Metric\*, Host). Any other identifier on an event type with `attributes` is read as `attributes['<identifier>']`
(e.g. `http.route`); validation reports a warning for it. `tenant_id` and names starting with `_` are rejected.
Map values are strings: compared with a number, or used in `sum`/`average`/`min`/`max`/`percentile`/`histogram`, they are
converted with `toFloat64OrNull` (non-numeric values are ignored).

## 3. Semantics

**Predicates.** String attributes take string values (numbers are compared as their text); number attributes take numbers
(a numeric string is accepted); bool attributes take `true`/`false`. `LIKE` uses SQL wildcards (`%`, `_`, case-sensitive)
on strings. `CONTAINS` is a case-insensitive substring match on strings in which `%`, `_` and `\` are ordinary
characters — the same condition as `contains` in the Logs/Metrics/Traces explorers (D-122; `CONTAINS` is not reserved,
so an attribute named `contains` still works). `IS NULL`: map key absent, string attribute empty; never true for number/bool attributes.

**Functions.**

| Function | Result |
|---|---|
| `count(*)`, `count(attr)` | events; with a map attribute: events that have the key |
| `sum`, `average`/`avg`, `min`, `max` | over number attributes (map values converted) |
| `uniqueCount(attr)` | exact distinct values |
| `percentile(attr, p…)`, `median(attr)` | quantile per level (0 < p < 100); approximate above ~8k values per group |
| `latest(attr)`, `earliest(attr)` | value of the newest/oldest event (strings allowed) |
| `rate(agg, duration)` | `agg` scaled to per-`duration` (per bucket width with `TIMESERIES`, else per query range) |
| `filter(agg, WHERE cond)` | `agg` over events matching `cond` (any aggregate except `rate`, `filter`, `histogram`) |
| `histogram(attr, ceiling, buckets=40)` | event counts in `buckets` equal buckets over `[0, ceiling)` (1–200 buckets); must be the only item; no `FACET`, `TIMESERIES` or `COMPARE WITH` |

**FACET** groups by up to 5 attributes (values rendered as strings). Groups are ordered by the first column, descending;
`LIMIT` keeps the first *n* (default 10, at most 2000; with `TIMESERIES` at most 50). With `TIMESERIES`, the top groups
are chosen over the whole range, then bucketed.

**TIMESERIES** buckets are aligned to the unix epoch. `AUTO` chooses the smallest of 10s, 30s, 1m, 5m, 10m, 15m, 30m,
1h, 3h, 6h, 12h, 1d, 7d giving at most 300 buckets (at least 1m on the rollup). An explicit bucket must give at most 1000
buckets and be ≥ 10s (rollup: whole minutes ≥ 1m). Missing buckets are `0` for `count`, `sum`, `uniqueCount` and `rate`,
otherwise `null`.

**COMPARE WITH** runs the query again over the range shifted back by the duration (1 minute – 400 days); the result carries
it in `compare`, with timeseries points shifted forward so both overlay.

**Variables.** `{{name}}` stands for a value; the request supplies `variables: {"name": "v" | ["v1", "v2"]}`. Values are
bound like literals (never spliced into SQL). With several values `=` becomes `IN` and `!=` becomes `NOT IN`. A
predicate whose variable is missing, empty or `*` is `true` (no filter). Names: `[A-Za-z_][A-Za-z0-9_]{0,63}`.

## 4. Metric rollup

`FROM Metric` reads `metrics_1m` when **all** hold: the range is longer than 6 hours; every attribute is one of
`metricName`, `metric.type`, `unit`, `service.name`, `host.id`, `attributes[…]`; every aggregate is `count(*)` (sum of
`value_count`), `sum(value)`, `average(value)` (`sum(value_sum)/sum(value_count)`), `min(value)`, `max(value)`,
`latest(value)` (`argMaxMerge(value_last)`), `uniqueCount` of an allowed attribute, and `rate`/`filter` of those
(`latest` without `filter`); the bucket is ≥ 1 minute. Rollup rows are per minute, so range edges are minute-aligned.
Otherwise raw `metrics` are read; raw ranges are limited to 31 days (`metadata.rollup` tells which table was used).
`metrics_1m` only contains gauges and sums (no histograms/summaries). Rollup rows keep one attribute set per series and
minute; filters and facets on `attributes[…]` are exact because a series (`series_id`) is defined by its attributes.

## 5. Limits and security

- Query text ≤ 8 KiB; ≤ 20 select items (≤ 30 columns after percentile expansion); ≤ 5 facets; ≤ 500 values per `IN`;
  ≤ 100 predicates; nesting depth ≤ 20; strings ≤ 4096 bytes; map keys 1–256 bytes without control characters.
- Range: `SINCE` < `UNTIL`, at most 31 days (Metric on the rollup: 400 days), not older than 400 days; series ≤ 500
  (facets × columns), points ≤ 100 000.
- Parsing never produces SQL text from user input: identifiers come from the per-event-type whitelist above, every
  literal, map key, variable value and time is a bound query parameter (`{pN:Type}`), and the SQL is built with
  `internal/api/query`, which always adds `tenant_id = {tenant_id:String}` for the caller's organization (the query cannot
  name tables, `tenant_id` or sub-queries) and rejects forbidden tokens in every fragment.
- Queries run as the read-only ClickHouse user with the organization's limits (`max_memory_usage`, `max_rows_to_read`,
  `max_execution_time`, `quota_key`, `log_comment`; D-047). Limit errors map to `422`/`429`, timeouts to `504`.

## 6. Errors

Syntax and validation errors carry the byte offset, length, 1-based line and column of the offending token
(`POST /api/v1/query/validate`). `POST /api/v1/query` answers `400 invalid_argument` with the message prefixed by
`line L, column C: `.

## 7. Dashboard filters

`POST /api/v1/query` and `/query/validate` accept `"filters": [{"attribute", "value", "event_type"?}]` (≤ 10), the
cross-widget filters of dashboards (api.md "Dashboards" › "Cross-widget filters"). `attribute` is parsed like an attribute
in a query (`host.name`, `` `quoted` ``, `attributes['k']`, `resource['k']`, `resource.k`; unparsable → `400`); `value` is
≤ 4096 bytes without control characters. Each applicable filter becomes the predicate `attribute = value`, ANDed with the
query's `WHERE` before planning: the value is a bound parameter like every literal and variable value, and rollup
eligibility, facet limits and time range limits apply as if the predicate were written in the query. A filter applies when
- the attribute is a known attribute of the query's event type (number attributes need a numeric value, booleans
  `true`/`false`/`1`/`0`), or
- it is an explicit `attributes[...]` / `resource[...]` lookup and the event type has that map, or
- it is an unknown name (an implicit `attributes['name']` lookup) and `event_type` is the query's event type.

Otherwise the filter is skipped and its attribute is listed in `metadata.ignored_filters` (omitted when empty). `tenant_id`
and names starting with `_` never apply.
