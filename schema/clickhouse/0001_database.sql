-- openlog:phase expand
-- openlog ClickHouse schema.
-- Every table is created ON CLUSTER with Replicated* engines, also in the `single` profile
-- (a 1 shard x 1 replica cluster named by the {cluster} macro). Local tables end in `_local`;
-- the unsuffixed name is the Distributed table services read from and write to.
-- Migrations must be forward compatible and idempotent (IF NOT EXISTS).

CREATE DATABASE IF NOT EXISTS openlog ON CLUSTER '{cluster}';

CREATE TABLE IF NOT EXISTS openlog.schema_migrations_local ON CLUSTER '{cluster}'
(
    version    UInt32,
    name       String,
    applied_at DateTime DEFAULT now()
)
ENGINE = ReplicatedReplacingMergeTree('/clickhouse/tables/{shard}/openlog/schema_migrations_local', '{replica}', applied_at)
ORDER BY version;

CREATE TABLE IF NOT EXISTS openlog.schema_migrations ON CLUSTER '{cluster}'
AS openlog.schema_migrations_local
ENGINE = Distributed('{cluster}', openlog, schema_migrations_local, version);
