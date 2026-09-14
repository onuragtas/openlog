-- openlog:phase expand
-- Test-only (build tags openlog_testmigrations + openlog_testmigrations_next, test/autoupdate): an expand
-- migration shipped by the broken release "N+2". The updater applies it before the health check fails and rolls
-- the containers back, so release N+1 must keep working with it (it never reads the new column).
ALTER TABLE mixedversion_probe ADD COLUMN IF NOT EXISTS next_release_note text;
