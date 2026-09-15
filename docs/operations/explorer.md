# Logs, Traces and Metrics Explorer

The **Logs** (`/logs`), **Traces** (`/traces`) and **Metrics** (`/metrics`) pages search any log record, span and OTLP
metric of the organization with the same query builder (D-118, D-119, D-122). API:
[api.md "Fields"](../contracts/api.md#fields), "Logs", "Traces", "Metrics", [“Saved views”](../contracts/api.md#saved-views).

## Query builder

Type in the filter bar to pick a **key**, then an **operator**, then a **value**; suggestions come from the data of the
selected time range.

| Key | Example | Reads |
|---|---|---|
| top-level field | `service.name`, `severity_text`, `severity_number`, `host.name`, `trace_id`, `body` | the record's own columns |
| `attributes.<key>` | `attributes.http.route` | a log record / data point attribute |
| `resource.<key>` | `resource.k8s.pod.name` | a resource attribute (SDK, collector, infra agent) |
| `body.<path>` | `body.user.id` | a value inside a JSON log body |
| bare key | `http.route` | the record attribute, else the resource attribute |

Operators: `=`, `!=`, `IN`, `NOT IN`, `contains` / `not contains` (case-insensitive substring; `%` and `_` are ordinary
characters), `like` / `not like` (case-sensitive, `%` and `_` wildcards), `regex` / `not regex` (RE2), `exists` / `not exists`, and `>`, `>=`, `<`, `<=` for numbers. Text such as
`service.name = checkout`, `severity_text IN (ERROR, WARN)` or `http.status_code >= 500` can be pasted or typed directly;
anything else searches the log body. **+ OR** starts a new group: conditions inside a group must all match, any group
may match. Every filter, column and the time range are in the URL, so a copied link opens the same view.

Limits: 50 conditions, 10 OR groups, 100 values per `IN`, 1024 bytes per value. Organization query limits
(Settings → Usage) apply to every explorer query.

## Logs Explorer

- **Histogram**: log volume per time bucket, stacked by severity or any chosen key (top 10 values plus "other"). Drag
  across it to zoom the time range.
- **Columns**: add any key as a column (column picker, or "Add as column" in the record details), drag to reorder,
  drag the header edge to resize, remove; the timestamp stays first. The column set is kept in the URL and remembered in
  the browser. Toggle body wrapping and row density in the table toolbar.
- **Record details**: every field as a table or JSON with per-field actions: filter for the value (`=`), exclude it
  (`!=`), add/remove as column, copy. Records with a trace id link to the trace.
- **Cell menu**: every table cell (except the time) has a menu with the same actions for that value: filter in, filter
  out, add or remove the column, copy.
- **Top values**: the side panel lists the 10 most frequent values of a chosen key for the current query (filters,
  OR groups, body search and context) with counts and their share of the records that have the key; click a value to
  filter for it or exclude it. Conditions on the chosen key itself are not applied, so its other values stay visible.
- **Saved views**: save the current filters, columns and time range as a private or organization-wide view (members and
  higher; admins can change organization-wide views of others). Saved views need the PostgreSQL auth mode.
- **Log tabs** of hosts, containers and Kubernetes pods embed the explorer with a locked context condition
  (`host.id`, `resource.container.id`, `resource.k8s.pod.uid`) that every query, histogram and suggestion includes;
  "Open in Logs Explorer" continues on `/logs` with the condition as an ordinary, removable chip. Older links with
  `lq`, `severity`, `lsrc`, `lfile`, `ldisc`, `lunit` or `stream` open with the equivalent chips.
- **APM links**: "logs of this transaction" shows a removable *transaction* chip (the logs of the traces whose entry span
  is that transaction, as before); "related logs" of a trace or span become `trace_id` / `span_id` chips.

## Traces Explorer

Searches every span (not only APM entry spans). Top-level keys: `name`, `kind`, `status_code`, `service.name`,
`duration_ms`, `http.status_code`, `error`, `is_entry`, `transaction.name`, `trace_id`, … plus `attributes.<key>` and
`resource.<key>` (for spans, `http.status_code`, `db.system` and `peer.name` read the APM columns derived from the
attributes; use `attributes.http.status_code` for the raw attribute).

- **Root spans only** keeps spans without a parent (one per trace for complete traces).
- **Charts**: span count per bucket (optionally split by a key) and p50 / p95 / p99 duration of the matching spans; drag
  to zoom the time range.
- **Table**: the same columns, reordering, resizing, cell menu and "Slowest first" order (the slowest spans of the range,
  one page). Click the time or a row for the span details; the trace id opens the waterfall.
- **Saved views** and URL state work as in the Logs Explorer (`signal: traces`).

## Metrics Explorer

The metric list shows every metric received in the range — from the infra agent, APM agents, OpenTelemetry SDKs or a
collector — with type, unit, temporality, description, number of series and reporting services. Select a metric, add
filters, choose an aggregation and group-by keys:

| Metric type | Aggregations |
|---|---|
| gauge | avg, min, max, sum, last, count |
| monotonic sum (counter) | rate (per second), increase, sum, last |
| non-monotonic sum (up-down counter) | last, avg, min, max, sum, count |
| histogram, exponential histogram | p50, p75, p90, p95, p99 (interpolated inside buckets), avg, count, sum, rate |
| summary | p50…p99 (the quantiles the SDK reported), avg, count, sum |

Several queries (A, B, …) can be charted together, with a formula such as `A / B * 100`. **Add to dashboard** creates
an OQL widget with the closest equivalent query; aggregations OQL cannot express (for example histogram percentiles)
disable the button.

## How it works and what it costs

- **Key suggestions** read the hourly key index `attribute_keys` (ClickHouse `0080_attribute_keys`): materialized views
  on the logs, spans and metrics tables count, per tenant, hour and key, the records that have the key, how many values
  are numbers or booleans (the key type) and an approximate distinct value count. No attribute values are stored. The
  views add a small amount of work to every insert (one row per attribute of each inserted block, grouped in the
  block). Data written before the upgrade is not indexed; for such ranges the API samples at most 20000 records of the
  last hour. JSON body keys are sampled from at most 2000 records of the last 15 minutes.
- **Value suggestions** and "top values" read at most 100000 matching records of the range.
- **Log search** uses the logs table's sort key (tenant, service, host, time) and the body token index for `contains`
  on `body`; filters on attributes scan the time range, so narrow ranges and a `service.name` filter are fastest.
- **Metric queries** longer than 6 hours read the 1-minute rollup for gauges and sums when all filters and group-by keys
  are data point attributes, `host.id` or `service.name`; resource attribute filters read raw data points (30-day
  retention). Histograms and summaries always read raw data points (at most 200000 series × step rows); at most 200
  series are returned.

## Release notes (this version)

- New **Traces Explorer** (`/traces`, `POST /api/v1/traces/query`, `POST /api/v1/traces/aggregate`).
- Host, container and pod log tabs and APM/trace log links use the Logs Explorer. `GET /api/v1/logs` is unchanged for
  API clients.
- **`contains` semantics (D-122).** The explorers' `contains` was already case-insensitive. OQL now has
  `attr [NOT] CONTAINS 'text'` with exactly the same meaning, and "Add to dashboard" generates it. Dashboards and alert
  rules created earlier with `LIKE '%text%'` (including queries generated by "Add to dashboard" before this version)
  are **not changed**: they stay case-sensitive and treat `%` / `_` inside the text as wildcards. Replace
  `x LIKE '%text%'` with `x CONTAINS 'text'` to get the explorer behaviour.
- "Add to dashboard" writes a bare key filter (e.g. `pool = 'heap'`) as "attribute, else resource attribute" — the
  explorer's meaning — instead of the attribute only. Group-by keys without a prefix still facet on the attribute.

## Metric types after the upgrade

Earlier versions stored exponential histograms without their scale, offset, negative and zero buckets, and dropped the
quantiles of summaries. From this version the processor stores exponential histogram buckets as explicit bounds and
summary quantiles in `metrics.quantiles` / `metrics.quantile_values` (ClickHouse `0081_metrics_summary_quantiles`), and
drops data points flagged "no recorded value" (counted in `openlog_processor_items_dropped_total` with reason
`no_recorded_value`). Percentiles of exponential histograms and summaries are therefore only available for data
received after the upgrade; count, sum and average work for older data too.
