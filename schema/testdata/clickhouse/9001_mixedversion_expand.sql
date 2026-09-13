-- openlog:phase expand
-- Test-only (build tag openlog_testmigrations, test/mixedversion): expand migration of release "N+1".
CREATE TABLE IF NOT EXISTS openlog.mixedversion_probe_local ON CLUSTER '{cluster}'
(
    id    UInt64,
    note  String
)
ENGINE = ReplicatedMergeTree
ORDER BY id;
