-- openlog:phase expand
-- Test-only (build tags openlog_testmigrations + openlog_testmigrations_next, test/autoupdate): expand migration of
-- the broken release "N+2"; it stays applied after the rollback and must not disturb release N+1.
ALTER TABLE openlog.mixedversion_probe_local ON CLUSTER '{cluster}' ADD COLUMN IF NOT EXISTS next_release_note String DEFAULT '';
