-- openlog:phase expand
-- 0101_disk_space_shedding: dropping the oldest day when a ClickHouse disk fills (docs/operations/disk-space.md).
--
-- Separate columns from the alert levels of 0100 on purpose. warn_percent/high_percent only decide when a disk is
-- *reported*; these decide when data is *deleted*. Conflating the two is how an operator sets a threshold
-- expecting a warning and gets a deletion.
--
-- shed_enabled is NULL (= off) until an operator turns it on: irreversible deletion does not get an
-- on-by-default. NULL on the others = the built-in value (start 90, stop 85, keep at least 3 partitions per
-- table, drop at most 20 days per run).
--
-- Mixed versions: older binaries ignore these columns and never shed.

ALTER TABLE disk_space_settings
    ADD COLUMN IF NOT EXISTS shed_enabled boolean,
    ADD COLUMN IF NOT EXISTS shed_start_percent integer,
    ADD COLUMN IF NOT EXISTS shed_stop_percent integer,
    ADD COLUMN IF NOT EXISTS shed_min_partitions integer,
    ADD COLUMN IF NOT EXISTS shed_max_drops_per_run integer;

ALTER TABLE disk_space_settings
    ADD CONSTRAINT disk_space_settings_shed_start CHECK (shed_start_percent IS NULL OR shed_start_percent BETWEEN 1 AND 99),
    ADD CONSTRAINT disk_space_settings_shed_stop CHECK (shed_stop_percent IS NULL OR shed_stop_percent BETWEEN 1 AND 99),
    ADD CONSTRAINT disk_space_settings_shed_min CHECK (shed_min_partitions IS NULL OR shed_min_partitions >= 1),
    ADD CONSTRAINT disk_space_settings_shed_max CHECK (shed_max_drops_per_run IS NULL OR shed_max_drops_per_run BETWEEN 1 AND 1000),
    -- Stopping at or above where it starts would drop a day and immediately want another.
    ADD CONSTRAINT disk_space_settings_shed_order CHECK (
        shed_start_percent IS NULL OR shed_stop_percent IS NULL OR shed_stop_percent < shed_start_percent
    );
