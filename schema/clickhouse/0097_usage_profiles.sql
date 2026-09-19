-- openlog:phase expand
-- Usage metering for continuous profiling (0095_profiles, docs/contracts/usage.md, D-079).
--
-- Its own migration rather than an edit to 0050_usage: an applied migration is never rewritten, and the
-- profiles table did not exist when 0050 ran, so its materialized view could not have either.
--
-- Billing counts stored rows like every other signal, so a profile that ingest accepted but the processor
-- dropped is not billed. The fixed-size columns of profiles_local are timestamp (8), value (8) and
-- duration_ns (8) = 24 bytes; the rest is byteSize of the variable-size ones.

CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.usage_signals_profiles_mv ON CLUSTER '{cluster}'
TO openlog.usage_signals_1h_local
AS SELECT
    tenant_id,
    toStartOfHour(ts) AS hour,
    'profiles' AS signal,
    svc AS service_name,
    hid AS host_id,
    toUInt64(count()) AS items,
    toUInt64(sum(sz)) AS bytes
FROM
(
    SELECT tenant_id, timestamp AS ts, service_name AS svc, toString(host_id) AS hid,
           byteSize(service_name, service_namespace, deployment_environment, host_id, profile_type, unit,
                    stack, leaf, resource_attributes, attributes) + 24 AS sz
    FROM openlog.profiles_local
)
GROUP BY tenant_id, hour, service_name, host_id;

-- Active entities. A service that only profiles (a batch job profiled without tracing) would otherwise be
-- invisible to the entity counts; ReplacingMergeTree on (tenant, day, kind, entity) makes counting it here
-- and in the span view the same row, not two.
CREATE MATERIALIZED VIEW IF NOT EXISTS openlog.usage_entities_profiles_mv ON CLUSTER '{cluster}'
TO openlog.usage_entities_1d_local
AS SELECT tenant_id, d AS day, e.1 AS kind, e.2 AS entity
FROM
(
    SELECT tenant_id, toDate(timestamp) AS d,
           arrayJoin(arrayFilter(x -> x.2 != '', [('host', toString(host_id)), ('service', toString(service_name)),
                                                  ('container', lower(resource_attributes['container.id']))])) AS e
    FROM openlog.profiles_local
)
GROUP BY tenant_id, day, kind, entity;
