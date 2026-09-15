# Logs Explorer and Metrics Explorer

The **Logs** (`/logs`) and **Metrics** (`/metrics`) pages search any log record and any OTLP metric of the organization
with the same query builder (D-118, D-119). API: [api.md "Fields"](../contracts/api.md#fields), "Logs", "Metrics",
[“Saved views”](../contracts/api.md#saved-views).

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

Operators: `=`, `!=`, `IN`, `NOT IN`, `contains` / `not contains` (case-insensitive), `like` / `not like` (`%` and `_`
wildcards), `regex` / `not regex` (RE2), `exists` / `not exists`, and `>`, `>=`, `<`, `<=` for numbers. Text such as
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
- **Saved views**: save the current filters, columns and time range as a private or organization-wide view (members and
  higher; admins can change organization-wide views of others). Saved views need the PostgreSQL auth mode.
- Links from APM ("logs of this transaction"), traces and hosts keep working; the host, container and pod log tabs are
  unchanged.

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

## Metric types after the upgrade

Earlier versions stored exponential histograms without their scale, offset, negative and zero buckets, and dropped the
quantiles of summaries. From this version the processor stores exponential histogram buckets as explicit bounds and
summary quantiles in `metrics.quantiles` / `metrics.quantile_values` (ClickHouse `0081_metrics_summary_quantiles`), and
drops data points flagged "no recorded value" (counted in `openlog_processor_items_dropped_total` with reason
`no_recorded_value`). Percentiles of exponential histograms and summaries are therefore only available for data
received after the upgrade; count, sum and average work for older data too.
