-- openlog:phase expand
-- Settings that openlog-migrate applies to existing tables outside of migration files, with the value it applied
-- last (docs/contracts/apm.md §8 "Retention"). Currently: apm_retention_days (OPENLOG_APM_RETENTION_DAYS); when the
-- configured value differs from the recorded one (default 30, the TTL of 0006_apm), migrate runs
-- ALTER TABLE … ON CLUSTER … MODIFY TTL on the APM tables and records the new value here.

CREATE TABLE IF NOT EXISTS openlog.table_settings_local ON CLUSTER '{cluster}'
(
    name       String,
    value      String,
    updated_at DateTime64(3, 'UTC') DEFAULT now64(3)
)
ENGINE = ReplicatedReplacingMergeTree('/clickhouse/tables/{shard}/openlog/table_settings_local', '{replica}', updated_at)
ORDER BY name;

CREATE TABLE IF NOT EXISTS openlog.table_settings ON CLUSTER '{cluster}'
AS openlog.table_settings_local
ENGINE = Distributed('{cluster}', openlog, table_settings_local, cityHash64(name));
