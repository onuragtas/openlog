-- openlog:phase contract
-- openlog:requires-all-at-least 0.9.1
-- Test-only (build tag openlog_testmigrations, test/mixedversion): a contract migration that
-- must wait until no instance older than 0.9.1 is running.
ALTER TABLE mixedversion_probe DROP COLUMN IF EXISTS legacy_note;
