-- openlog:phase expand
-- 0094_rum: real user monitoring rollups (docs/contracts/rum.md §5, D-136).
--
-- RUM stores no raw table of its own. The browser SDK sends **OTLP spans**, so a page view, a web vital, a
-- JS error and a fetch call are ordinary rows of `spans` with the ordinary 7-day trace retention, and they
-- get the whole existing pipeline for free:
--
--   * a browser page view is the **root of the trace** its fetch calls continue into the backend, so
--     trace ↔ trace correlation is the trace model itself, not a second join (rum.md §6);
--   * a JS error is a span with status ERROR and an `exception` event, so it lands in `apm_errors_1m` and
--     `apm_error_groups` through the views of 0006_apm.sql — the RUM error inbox **is** the APM error
--     inbox, with the same fingerprint, the same grouping and the same workflow state (apm.md §3);
--   * `apm_services` records the browser app like any other service.
--
-- What the spans cannot answer cheaply is the RUM-shaped question: the p75 of LCP per route over 30 days,
-- the slowest pages, how many sessions there were. Those are the three views below. They are aggregates on
-- `spans_local` exactly like the APM ones (apm.md §8): every column is mergeable (sum/min/max/sumMap/
-- argMax/anyLast), spans are sharded by cityHash64(tenant_id, trace_id) so one key's rows are spread over
-- every shard, and every read re-aggregates with GROUP BY through the Distributed table.
--
-- Page views are not entry spans (the SDK emits them as `internal`), so none of this reaches
-- apm_transactions_1m: RUM never invents throughput or Apdex for a browser app.
--
-- Vital histogram bucket (rum.md §5.1, Go: rum.Bucket) — the APM log-linear scheme (apm.md §4.1) widened
-- downwards, because CLS is a unitless number well below 1 while LCP is thousands of milliseconds:
--   toInt16(if(v <= 0, -160, greatest(-160, least(200, ceil(8 * log2(v))))))
-- The page-load histogram keeps the APM bucket exactly (it is a duration in ms like every other span).

-- ---- page views ----
--
-- One row per (app, environment, route, kind, device, browser) per minute. The route — not the URL — is the
-- key: `url.path` is unbounded (every order id is a new value), and a rollup keyed on it would grow without
-- limit. The SDK normalises to a route and the ingest re-normalises and bounds it (internal/rum), so this
-- table's cardinality is a property of the application's routing table, not of its traffic.

CREATE TABLE IF NOT EXISTS openlog.rum_page_views_1m_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    app                    LowCardinality(String),
    deployment_environment LowCardinality(String),
    timestamp              DateTime('UTC'),
    route                  String CODEC(ZSTD(1)),
    -- 'load' (a document navigation) or 'route_change' (an SPA history change). They are kept apart
    -- because their durations mean different things and averaging them together is meaningless.
    page_view_kind         LowCardinality(String),
    device_type            LowCardinality(String),
    browser_name           LowCardinality(String),
    views                  SimpleAggregateFunction(sum, Float64),
    samples                SimpleAggregateFunction(sum, UInt64),
    -- Page load duration: total, maximum and the histogram the p50/p75/p95 come from.
    duration_sum_ms        SimpleAggregateFunction(sum, Float64),
    duration_max_ms        SimpleAggregateFunction(max, Float64),
    duration_hist          SimpleAggregateFunction(sumMap, Tuple(Array(Int16), Array(Float64))),
    -- Navigation Timing phases, as weighted sums: the averages behind the waterfall on the page detail.
    -- A phase the browser did not perform (a cached DNS answer, a plain-http origin) contributes 0.
    ttfb_sum_ms            SimpleAggregateFunction(sum, Float64),
    dns_sum_ms             SimpleAggregateFunction(sum, Float64),
    connect_sum_ms         SimpleAggregateFunction(sum, Float64),
    tls_sum_ms             SimpleAggregateFunction(sum, Float64),
    response_sum_ms        SimpleAggregateFunction(sum, Float64),
    dom_interactive_sum_ms SimpleAggregateFunction(sum, Float64),
    dom_content_loaded_sum_ms SimpleAggregateFunction(sum, Float64),
    load_event_sum_ms      SimpleAggregateFunction(sum, Float64)
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/rum_page_views_1m_local', '{replica}')
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (tenant_id, app, deployment_environment, timestamp, route, page_view_kind, device_type, browser_name)
TTL timestamp + INTERVAL 30 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.rum_page_views_1m_mv ON CLUSTER '{cluster}'
TO openlog.rum_page_views_1m_local
AS SELECT
    tenant_id,
    app,
    deployment_environment,
    toStartOfMinute(ts) AS timestamp,
    route,
    page_view_kind,
    device_type,
    browser_name,
    sum(w) AS views,
    toUInt64(count()) AS samples,
    sum(w * dur_ms) AS duration_sum_ms,
    max(dur_ms) AS duration_max_ms,
    sumMap([bucket], [w]) AS duration_hist,
    sum(w * ttfb) AS ttfb_sum_ms,
    sum(w * dns) AS dns_sum_ms,
    sum(w * connect) AS connect_sum_ms,
    sum(w * tls) AS tls_sum_ms,
    sum(w * response) AS response_sum_ms,
    sum(w * dom_interactive) AS dom_interactive_sum_ms,
    sum(w * dom_content_loaded) AS dom_content_loaded_sum_ms,
    sum(w * load_event) AS load_event_sum_ms
FROM
(
    SELECT tenant_id, service_name AS app, deployment_environment,
           timestamp AS ts, sample_weight AS w, duration_ns / 1e6 AS dur_ms,
           attributes['openlog.rum.route'] AS route,
           attributes['openlog.rum.page_view.kind'] AS page_view_kind,
           attributes['device.type'] AS device_type,
           attributes['browser.name'] AS browser_name,
           toFloat64OrZero(attributes['openlog.rum.timing.ttfb_ms']) AS ttfb,
           toFloat64OrZero(attributes['openlog.rum.timing.dns_ms']) AS dns,
           toFloat64OrZero(attributes['openlog.rum.timing.connect_ms']) AS connect,
           toFloat64OrZero(attributes['openlog.rum.timing.tls_ms']) AS tls,
           toFloat64OrZero(attributes['openlog.rum.timing.response_ms']) AS response,
           toFloat64OrZero(attributes['openlog.rum.timing.dom_interactive_ms']) AS dom_interactive,
           toFloat64OrZero(attributes['openlog.rum.timing.dom_content_loaded_ms']) AS dom_content_loaded,
           toFloat64OrZero(attributes['openlog.rum.timing.load_event_ms']) AS load_event,
           toInt16(if(duration_ns <= 1000, -80, greatest(-80, least(200, ceil(8 * log2(duration_ns / 1e6)))))) AS bucket
    FROM openlog.spans_local
    WHERE attributes['openlog.rum.event'] = 'page_view' AND service_name != '' AND sample_weight > 0
)
GROUP BY tenant_id, app, deployment_environment, timestamp, route, page_view_kind, device_type, browser_name;

CREATE TABLE IF NOT EXISTS openlog.rum_page_views_1m ON CLUSTER '{cluster}'
AS openlog.rum_page_views_1m_local
ENGINE = Distributed('{cluster}', openlog, rum_page_views_1m_local, cityHash64(tenant_id, app));

-- ---- web vitals ----
--
-- One row per (app, environment, vital, route, device) per minute. `good`, `needs_improvement` and `poor`
-- are counted here rather than derived at read time, because the thresholds are per vital (rum.md §2) and a
-- histogram bucket does not line up with them: counting at write time gives the exact share, which is the
-- number the Core Web Vitals programme is actually defined in terms of. The histogram is what the p50/p75/
-- p95 come from — p75 being the percentile Google's "good" assessment uses, which is why the UI leads with
-- it and why storing only an average would have been useless here.

CREATE TABLE IF NOT EXISTS openlog.rum_vitals_1m_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    app                    LowCardinality(String),
    deployment_environment LowCardinality(String),
    timestamp              DateTime('UTC'),
    -- lcp, inp, cls, fcp, ttfb (rum.md §2).
    vital                  LowCardinality(String),
    route                  String CODEC(ZSTD(1)),
    device_type            LowCardinality(String),
    count                  SimpleAggregateFunction(sum, Float64),
    samples                SimpleAggregateFunction(sum, UInt64),
    value_sum              SimpleAggregateFunction(sum, Float64),
    value_max              SimpleAggregateFunction(max, Float64),
    value_hist             SimpleAggregateFunction(sumMap, Tuple(Array(Int16), Array(Float64))),
    good                   SimpleAggregateFunction(sum, Float64),
    needs_improvement      SimpleAggregateFunction(sum, Float64),
    poor                   SimpleAggregateFunction(sum, Float64)
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/rum_vitals_1m_local', '{replica}')
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (tenant_id, app, deployment_environment, timestamp, vital, route, device_type)
TTL timestamp + INTERVAL 30 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.rum_vitals_1m_mv ON CLUSTER '{cluster}'
TO openlog.rum_vitals_1m_local
AS SELECT
    tenant_id,
    app,
    deployment_environment,
    toStartOfMinute(ts) AS timestamp,
    vital,
    route,
    device_type,
    sum(w) AS count,
    toUInt64(count()) AS samples,
    sum(w * value) AS value_sum,
    max(value) AS value_max,
    sumMap([bucket], [w]) AS value_hist,
    sumIf(w, rating = 'good') AS good,
    sumIf(w, rating = 'needs_improvement') AS needs_improvement,
    sumIf(w, rating = 'poor') AS poor
FROM
(
    SELECT tenant_id, service_name AS app, deployment_environment,
           timestamp AS ts, sample_weight AS w,
           attributes['openlog.rum.vital.name'] AS vital,
           attributes['openlog.rum.route'] AS route,
           attributes['device.type'] AS device_type,
           attributes['openlog.rum.vital.rating'] AS rating,
           toFloat64OrZero(attributes['openlog.rum.vital.value']) AS value,
           toInt16(if(value <= 0, -160, greatest(-160, least(200, ceil(8 * log2(value)))))) AS bucket
    FROM openlog.spans_local
    WHERE attributes['openlog.rum.event'] = 'vital' AND service_name != '' AND sample_weight > 0
      AND attributes['openlog.rum.vital.name'] != ''
)
GROUP BY tenant_id, app, deployment_environment, timestamp, vital, route, device_type;

CREATE TABLE IF NOT EXISTS openlog.rum_vitals_1m ON CLUSTER '{cluster}'
AS openlog.rum_vitals_1m_local
ENGINE = Distributed('{cluster}', openlog, rum_vitals_1m_local, cityHash64(tenant_id, app));

-- ---- sessions ----
--
-- One row per session, merged from every RUM span it produced. Not a per-minute rollup: a session is an
-- entity with a lifetime, so the key is the session id and the TTL hangs off last_seen, exactly like
-- apm_services and apm_error_groups. Counting page views and errors here (rather than joining the two
-- rollups at read time) is what makes "sessions with errors" answerable in one query.

CREATE TABLE IF NOT EXISTS openlog.rum_sessions_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    app                    LowCardinality(String),
    deployment_environment LowCardinality(String),
    session_id             String CODEC(ZSTD(1)),
    first_seen             SimpleAggregateFunction(min, DateTime64(9, 'UTC')),
    last_seen              SimpleAggregateFunction(max, DateTime64(9, 'UTC')),
    page_views             SimpleAggregateFunction(sum, Float64),
    errors                 SimpleAggregateFunction(sum, Float64),
    spans                  SimpleAggregateFunction(sum, UInt64),
    -- The first and last route of the session: where people came in and where they left.
    entry_route            AggregateFunction(argMin, String, DateTime64(9, 'UTC')),
    exit_route             AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    -- A trace of the session, so the detail always has something to open even when the page view rows
    -- have already expired with the 7-day trace retention.
    last_trace_id          AggregateFunction(argMax, String, DateTime64(9, 'UTC')),
    device_type            SimpleAggregateFunction(anyLast, LowCardinality(String)),
    browser_name           SimpleAggregateFunction(anyLast, LowCardinality(String)),
    browser_version        SimpleAggregateFunction(anyLast, String),
    os_name                SimpleAggregateFunction(anyLast, LowCardinality(String))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/rum_sessions_local', '{replica}')
ORDER BY (tenant_id, app, deployment_environment, session_id)
TTL toDateTime(last_seen) + INTERVAL 30 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.rum_sessions_mv ON CLUSTER '{cluster}'
TO openlog.rum_sessions_local
AS SELECT
    tenant_id,
    app,
    deployment_environment,
    session_id,
    min(ts) AS first_seen,
    max(ts) AS last_seen,
    sumIf(w, event = 'page_view') AS page_views,
    sumIf(w, event = 'error') AS errors,
    toUInt64(count()) AS spans,
    argMinState(route, ts) AS entry_route,
    argMaxState(route, ts) AS exit_route,
    argMaxState(tid, ts) AS last_trace_id,
    anyLast(device_type) AS device_type,
    anyLast(browser_name) AS browser_name,
    anyLast(browser_version) AS browser_version,
    anyLast(os_name) AS os_name
FROM
(
    SELECT tenant_id, service_name AS app, deployment_environment,
           timestamp AS ts, sample_weight AS w, trace_id AS tid,
           attributes['session.id'] AS session_id,
           attributes['openlog.rum.event'] AS event,
           attributes['openlog.rum.route'] AS route,
           attributes['device.type'] AS device_type,
           attributes['browser.name'] AS browser_name,
           attributes['browser.version'] AS browser_version,
           attributes['os.name'] AS os_name
    FROM openlog.spans_local
    WHERE attributes['openlog.rum.event'] != '' AND service_name != ''
      AND attributes['session.id'] != ''
)
GROUP BY tenant_id, app, deployment_environment, session_id;

CREATE TABLE IF NOT EXISTS openlog.rum_sessions ON CLUSTER '{cluster}'
AS openlog.rum_sessions_local
ENGINE = Distributed('{cluster}', openlog, rum_sessions_local, cityHash64(tenant_id, app));

-- ---- session lookup on the raw spans ----
--
-- The session detail reads the session's own spans out of `spans` (the page views with their trace ids and
-- the errors, within the trace retention). `spans` is ordered by (tenant_id, …, timestamp), so filtering on
-- a map attribute would read every span of the tenant in the range. This bloom filter makes a session id a
-- cheap skip, the same treatment k8s.pod.uid got on logs in 0043_k8s_logs_indexes.

ALTER TABLE openlog.spans_local ON CLUSTER '{cluster}'
    ADD INDEX IF NOT EXISTS idx_rum_session attributes['session.id'] TYPE bloom_filter(0.01) GRANULARITY 4;
