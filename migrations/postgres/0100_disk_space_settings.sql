-- openlog:phase expand
-- 0100_disk_space_settings: the levels a ClickHouse disk is reported at, set in the UI
-- (docs/operations/disk-space.md).
--
-- One row for the whole installation, not one per organization: the disks belong to the operator, not to a
-- tenant. The primary key is a constant so the table can hold exactly one row and an upsert needs no id.
--
-- NULL = use the built-in default (80 / 90 / 5). There is deliberately no environment layer: a setting the UI
-- can save but an OPENLOG_* variable silently overrides is worse than no setting at all.
--
-- Mixed versions: older binaries ignore the table and keep the built-in defaults until upgraded.

CREATE TABLE IF NOT EXISTS disk_space_settings (
    id           boolean PRIMARY KEY DEFAULT true CHECK (id),
    warn_percent integer CHECK (warn_percent BETWEEN 1 AND 99),
    high_percent integer CHECK (high_percent BETWEEN 1 AND 99),
    hysteresis   integer CHECK (hysteresis BETWEEN 0 AND 50),
    updated_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT disk_space_settings_order CHECK (warn_percent IS NULL OR high_percent IS NULL OR warn_percent < high_percent)
);
