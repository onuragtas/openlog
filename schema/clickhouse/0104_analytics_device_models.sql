-- openlog:phase expand
-- 0104_analytics_device_models: sessions and people by device model, per day, for 90 days (docs/contracts/analytics.md §5.4).
--
-- analytics_events holds the country, device type, OS and browser, but not the model, and its view cannot be altered
-- (rum.md §3.7). This stands beside it. The model is a resource attribute that browsers do not send, so only mobile
-- applications have rows here.
--
-- There is no personal data: a session id is random, an actor is a hash, and only sketches of them are stored. So this is
-- not touched by erasure, which has nothing here to find (analytics.md §7).

CREATE TABLE IF NOT EXISTS openlog.analytics_device_models_1d_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    app                    LowCardinality(String),
    deployment_environment LowCardinality(String),
    day                    Date,
    model                  LowCardinality(String),
    sessions               AggregateFunction(uniqCombined64, String),
    actors                 AggregateFunction(uniqCombined64, UInt64),
    weight                 SimpleAggregateFunction(max, Float64)
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/analytics_device_models_1d_local', '{replica}')
PARTITION BY toYYYYMM(day)
ORDER BY (tenant_id, app, deployment_environment, day, model)
TTL toDateTime(day) + INTERVAL 90 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.analytics_device_models_1d_mv ON CLUSTER '{cluster}'
TO openlog.analytics_device_models_1d_local
AS SELECT
    tenant_id,
    app,
    deployment_environment,
    day,
    model,
    uniqCombined64State(session) AS sessions,
    uniqCombined64IfState(actor, actor != 0) AS actors,
    max(w) AS weight
FROM
(
    SELECT tenant_id, service_name AS app, deployment_environment,
           toDate(timestamp) AS day,
           resource_attributes['device.model.identifier'] AS model,
           attributes['session.id'] AS session,
           toFloat64(sample_weight) AS w,
           if(attributes['openlog.analytics.mode'] = 'visitors'
                  AND (attributes['openlog.rum.visitor.id'] != '' OR attributes['user.id'] != ''),
              sipHash64(tenant_id, if(attributes['openlog.rum.visitor.id'] != '', attributes['openlog.rum.visitor.id'], attributes['user.id'])),
              toUInt64(0)) AS actor
    FROM openlog.spans_local
    WHERE attributes['openlog.rum.event'] IN ('page_view', 'custom')
      AND attributes['session.id'] != '' AND service_name != ''
      AND resource_attributes['device.model.identifier'] != ''
)
GROUP BY tenant_id, app, deployment_environment, day, model;

CREATE TABLE IF NOT EXISTS openlog.analytics_device_models_1d ON CLUSTER '{cluster}'
AS openlog.analytics_device_models_1d_local
ENGINE = Distributed('{cluster}', openlog, analytics_device_models_1d_local, cityHash64(tenant_id, app));
