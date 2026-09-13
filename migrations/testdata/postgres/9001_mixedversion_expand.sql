-- openlog:phase expand
-- Test-only (build tag openlog_testmigrations, test/mixedversion): an expand migration shipped by
-- release "N+1". Release N must keep working after it ran.
CREATE TABLE IF NOT EXISTS mixedversion_probe (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    legacy_note text NOT NULL DEFAULT '',
    note        text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);
