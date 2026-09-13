-- openlog:phase contract
-- openlog:requires-all-at-least 0.9.1
-- Test-only (build tag openlog_testmigrations, test/mixedversion): contract migration of release
-- "N+1"; runs only when no instance older than 0.9.1 is live.
CREATE TABLE IF NOT EXISTS openlog.mixedversion_contract_local ON CLUSTER '{cluster}'
(
    id  UInt64
)
ENGINE = ReplicatedMergeTree
ORDER BY id;
