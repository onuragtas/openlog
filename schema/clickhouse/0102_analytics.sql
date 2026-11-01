-- openlog:phase expand
-- 0102_analytics: product analytics that outlive the 7-day trace retention (docs/contracts/analytics.md §5).
--
-- Like RUM, this has no ingest path of its own. Every table below is written by a materialized view on `spans_local`,
-- and each view filters on `openlog.analytics.mode`, which internal/rum.Sanitize forces from the browser key. A page
-- cannot opt itself in, and a key in 'off' mode costs nothing here.
--
-- None of this touches `rum_sessions`: a view cannot be altered (rum.md §3.7), so these are new views on new tables.
--
-- The raw identifiers stay on the spans and expire with them. Only `actor_hash` reaches these tables, which is
-- pseudonymisation and not anonymisation: they remain personal data, which is why they have an erasure path
-- (analytics.md §7) and a fixed retention in a storage class of their own (§5.4).
--
-- The actor is the visitor id when there is one, otherwise user.id (§1.3). In 'sessions' mode it is always 0, because
-- that mode exists for applications that decided not to recognise anybody across visits.

-- ---- events ----
--
-- One row per page view or custom event, so that event volume, attribution and funnels can reach past 7 days.
-- Vitals, errors and requests are not copied: they have rollups of their own and are not what this table answers.

CREATE TABLE IF NOT EXISTS openlog.analytics_events_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    app                    LowCardinality(String),
    deployment_environment LowCardinality(String),
    timestamp              DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    -- 'page_view' or 'custom'.
    kind                   LowCardinality(String),
    -- The event name, or the normalized route for a page view. Bounded per key by internal/rum (analytics.md §4).
    name                   String CODEC(ZSTD(1)),
    route                  String CODEC(ZSTD(1)),
    session_id             String CODEC(ZSTD(1)),
    actor_hash             UInt64 CODEC(ZSTD(1)),
    weight                 Float64 CODEC(ZSTD(1)),
    version                LowCardinality(String),
    country                LowCardinality(String),
    device_type            LowCardinality(String),
    os_name                LowCardinality(String),
    browser_name           LowCardinality(String),
    referrer_host          LowCardinality(String),
    utm_source             LowCardinality(String),
    utm_medium             LowCardinality(String),
    utm_campaign           String CODEC(ZSTD(1)),
    params                 Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    INDEX idx_actor actor_hash TYPE bloom_filter(0.01) GRANULARITY 4
)
ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/openlog/analytics_events_local', '{replica}')
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (tenant_id, app, deployment_environment, timestamp, name)
TTL toDateTime(timestamp) + INTERVAL 90 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.analytics_events_mv ON CLUSTER '{cluster}'
TO openlog.analytics_events_local
AS SELECT
    tenant_id,
    service_name AS app,
    deployment_environment,
    toDateTime64(timestamp, 3) AS timestamp,
    attributes['openlog.rum.event'] AS kind,
    if(attributes['openlog.rum.event'] = 'custom', attributes['openlog.rum.custom.name'], attributes['openlog.rum.route']) AS name,
    attributes['openlog.rum.route'] AS route,
    attributes['session.id'] AS session_id,
    if(attributes['openlog.analytics.mode'] = 'visitors'
           AND (attributes['openlog.rum.visitor.id'] != '' OR attributes['user.id'] != ''),
       sipHash64(tenant_id, if(attributes['openlog.rum.visitor.id'] != '', attributes['openlog.rum.visitor.id'], attributes['user.id'])),
       toUInt64(0)) AS actor_hash,
    sample_weight AS weight,
    resource_attributes['service.version'] AS version,
    attributes['geo.country.iso_code'] AS country,
    attributes['device.type'] AS device_type,
    if(attributes['os.name'] != '', attributes['os.name'], resource_attributes['os.name']) AS os_name,
    attributes['browser.name'] AS browser_name,
    attributes['openlog.rum.referrer.host'] AS referrer_host,
    attributes['openlog.rum.utm.source'] AS utm_source,
    attributes['openlog.rum.utm.medium'] AS utm_medium,
    attributes['openlog.rum.utm.campaign'] AS utm_campaign,
    mapApply((k, v) -> (substring(k, 26), v), mapFilter((k, v) -> startsWith(k, 'openlog.rum.custom.param.'), attributes)) AS params
FROM openlog.spans_local
WHERE attributes['openlog.analytics.mode'] IN ('sessions', 'visitors')
  AND attributes['openlog.rum.event'] IN ('page_view', 'custom')
  AND service_name != '' AND sample_weight > 0;

CREATE TABLE IF NOT EXISTS openlog.analytics_events ON CLUSTER '{cluster}'
AS openlog.analytics_events_local
ENGINE = Distributed('{cluster}', openlog, analytics_events_local, cityHash64(tenant_id, app));

-- ---- actors ----
--
-- One row per actor, with a lifetime rather than a period, like rum_sessions. This is what makes "new" mean new:
-- first_seen has to survive the 90 days of the events, or anyone returning after three months would count as a new
-- user. The first_* columns are what a cohort can be broken down by.

CREATE TABLE IF NOT EXISTS openlog.analytics_actors_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    app                    LowCardinality(String),
    deployment_environment LowCardinality(String),
    actor_hash             UInt64,
    first_seen             SimpleAggregateFunction(min, DateTime64(3, 'UTC')),
    last_seen              SimpleAggregateFunction(max, DateTime64(3, 'UTC')),
    weight                 SimpleAggregateFunction(max, Float64),
    first_version         AggregateFunction(argMin, String, DateTime64(3, 'UTC')),
    first_country          AggregateFunction(argMin, String, DateTime64(3, 'UTC')),
    first_referrer_host    AggregateFunction(argMin, String, DateTime64(3, 'UTC')),
    first_utm_source       AggregateFunction(argMin, String, DateTime64(3, 'UTC'))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/analytics_actors_local', '{replica}')
ORDER BY (tenant_id, app, deployment_environment, actor_hash)
TTL toDateTime(last_seen) + INTERVAL 395 DAY;

-- The attribution comes from the first page view and not from the first row: a custom event can be flushed ahead of
-- the page view it happened on, and a direct visit has to stay direct, so an empty referrer is a value here.
CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.analytics_actors_mv ON CLUSTER '{cluster}'
TO openlog.analytics_actors_local
AS SELECT
    tenant_id,
    app,
    deployment_environment,
    actor_hash,
    min(ts) AS first_seen,
    max(ts) AS last_seen,
    max(w) AS weight,
    argMinState(version, ts) AS first_version,
    argMinState(country, ts) AS first_country,
    argMinIfState(referrer_host, ts, event = 'page_view') AS first_referrer_host,
    argMinIfState(utm_source, ts, event = 'page_view') AS first_utm_source
FROM
(
    SELECT tenant_id, service_name AS app, deployment_environment,
           toDateTime64(timestamp, 3) AS ts,
           sample_weight AS w,
           attributes['openlog.rum.event'] AS event,
           sipHash64(tenant_id, if(attributes['openlog.rum.visitor.id'] != '', attributes['openlog.rum.visitor.id'], attributes['user.id'])) AS actor_hash,
           resource_attributes['service.version'] AS version,
           attributes['geo.country.iso_code'] AS country,
           attributes['openlog.rum.referrer.host'] AS referrer_host,
           attributes['openlog.rum.utm.source'] AS utm_source
    FROM openlog.spans_local
    WHERE attributes['openlog.analytics.mode'] = 'visitors'
      AND attributes['openlog.rum.event'] IN ('page_view', 'custom')
      AND (attributes['openlog.rum.visitor.id'] != '' OR attributes['user.id'] != '')
      AND service_name != '' AND sample_weight > 0
)
GROUP BY tenant_id, app, deployment_environment, actor_hash;

CREATE TABLE IF NOT EXISTS openlog.analytics_actors ON CLUSTER '{cluster}'
AS openlog.analytics_actors_local
ENGINE = Distributed('{cluster}', openlog, analytics_actors_local, cityHash64(tenant_id, app));

-- ---- active actors per day ----
--
-- DAU, WAU, MAU and retention read this and never the events, so a 12-month cohort chart does not scan 12 months of
-- events. The weight is per actor and not per event: sampling is once per visitor (analytics.md §2), and summing it
-- per day is what extrapolates a sampled count of people.

CREATE TABLE IF NOT EXISTS openlog.analytics_actor_days_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    app                    LowCardinality(String),
    deployment_environment LowCardinality(String),
    day                    Date,
    actor_hash             UInt64,
    weight                 SimpleAggregateFunction(max, Float64),
    events                 SimpleAggregateFunction(sum, Float64)
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/analytics_actor_days_local', '{replica}')
PARTITION BY toYYYYMM(day)
ORDER BY (tenant_id, app, deployment_environment, day, actor_hash)
TTL toDateTime(day) + INTERVAL 395 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.analytics_actor_days_mv ON CLUSTER '{cluster}'
TO openlog.analytics_actor_days_local
AS SELECT
    tenant_id,
    app,
    deployment_environment,
    day,
    actor_hash,
    max(w) AS weight,
    sum(w) AS events
FROM
(
    SELECT tenant_id, service_name AS app, deployment_environment,
           toDate(timestamp) AS day,
           sample_weight AS w,
           sipHash64(tenant_id, if(attributes['openlog.rum.visitor.id'] != '', attributes['openlog.rum.visitor.id'], attributes['user.id'])) AS actor_hash
    FROM openlog.spans_local
    WHERE attributes['openlog.analytics.mode'] = 'visitors'
      AND attributes['openlog.rum.event'] IN ('page_view', 'custom')
      AND (attributes['openlog.rum.visitor.id'] != '' OR attributes['user.id'] != '')
      AND service_name != '' AND sample_weight > 0
)
GROUP BY tenant_id, app, deployment_environment, day, actor_hash;

CREATE TABLE IF NOT EXISTS openlog.analytics_actor_days ON CLUSTER '{cluster}'
AS openlog.analytics_actor_days_local
ENGINE = Distributed('{cluster}', openlog, analytics_actor_days_local, cityHash64(tenant_id, app));
