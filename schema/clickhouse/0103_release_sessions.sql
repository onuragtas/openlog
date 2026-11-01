-- openlog:phase expand
-- 0103_release_sessions: how each version of an application behaved, for 30 days (docs/contracts/mobile-crash.md §5).
--
-- rum_sessions has no version column and its view cannot be altered (rum.md §3.7), so release health used to count
-- sessions from the raw spans, which reach back 7 days while the release list reaches 30. Crash-free rate is the number a
-- slow mobile rollout is judged on, and a week is not enough for it.
--
-- Hourly, because the screens read arbitrary ranges. The columns are sketches, because a session spans hours and has to
-- count once. There is no personal data here: a session id is random, and only the sketch of it is stored.

CREATE TABLE IF NOT EXISTS openlog.rum_release_sessions_1h_local ON CLUSTER '{cluster}'
(
    tenant_id              LowCardinality(String),
    app                    LowCardinality(String),
    deployment_environment LowCardinality(String),
    hour                   DateTime('UTC'),
    version                LowCardinality(String),
    sessions               AggregateFunction(uniqCombined64, String),
    error_sessions         AggregateFunction(uniqCombined64, String),
    -- Sessions with a crash, and sessions of a build able to report one. Without the second, a version that cannot
    -- capture crashes would read as 100% crash-free, which is a false statement about the number a release is judged on.
    crashed_sessions       AggregateFunction(uniqCombined64, String),
    capturing_sessions     AggregateFunction(uniqCombined64, String),
    first_seen             SimpleAggregateFunction(min, DateTime64(3, 'UTC')),
    last_seen              SimpleAggregateFunction(max, DateTime64(3, 'UTC'))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/openlog/rum_release_sessions_1h_local', '{replica}')
PARTITION BY toYYYYMMDD(hour)
ORDER BY (tenant_id, app, deployment_environment, hour, version)
TTL hour + INTERVAL 30 DAY
SETTINGS ttl_only_drop_parts = 1;

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.rum_release_sessions_1h_mv ON CLUSTER '{cluster}'
TO openlog.rum_release_sessions_1h_local
AS SELECT
    tenant_id,
    app,
    deployment_environment,
    toStartOfHour(ts) AS hour,
    version,
    uniqCombined64State(session) AS sessions,
    uniqCombined64IfState(session, event = 'error') AS error_sessions,
    uniqCombined64IfState(session, crash_kind != '') AS crashed_sessions,
    uniqCombined64IfState(session, capture = '1') AS capturing_sessions,
    min(ts3) AS first_seen,
    max(ts3) AS last_seen
FROM
(
    SELECT tenant_id, service_name AS app, deployment_environment,
           timestamp AS ts, toDateTime64(timestamp, 3) AS ts3,
           resource_attributes['service.version'] AS version,
           attributes['session.id'] AS session,
           attributes['openlog.rum.event'] AS event,
           attributes['openlog.rum.crash.kind'] AS crash_kind,
           resource_attributes['openlog.rum.crash.capture'] AS capture
    FROM openlog.spans_local
    WHERE attributes['openlog.rum.event'] != '' AND attributes['session.id'] != '' AND service_name != ''
)
GROUP BY tenant_id, app, deployment_environment, hour, version;

CREATE TABLE IF NOT EXISTS openlog.rum_release_sessions_1h ON CLUSTER '{cluster}'
AS openlog.rum_release_sessions_1h_local
ENGINE = Distributed('{cluster}', openlog, rum_release_sessions_1h_local, cityHash64(tenant_id, app));
