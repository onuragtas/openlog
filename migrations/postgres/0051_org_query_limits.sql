-- openlog:phase expand
-- 0051_org_query_limits: per-organization ClickHouse query limits set in the UI (docs/contracts/usage.md §4.5,
-- docs/contracts/postgres.md "org_query_limits").
--
-- Precedence for a tenant's api/alert queries, lowest to highest: OPENLOG_QUERY_* defaults < plan limits.query
-- (with org_plans overrides) < this row < OPENLOG_QUERY_TENANT_LIMITS. NULL = inherit the lower layer; 0 = openlog does
-- not set the ClickHouse setting (the read user's profile applies).
--
-- Mixed versions: older binaries ignore the table (their pods keep plan/environment limits until upgraded).

CREATE TABLE IF NOT EXISTS org_query_limits (
    org_id            uuid PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    max_memory_usage  bigint CHECK (max_memory_usage >= 0),
    max_rows_to_read  bigint CHECK (max_rows_to_read >= 0),
    max_bytes_to_read bigint CHECK (max_bytes_to_read >= 0),
    updated_by        uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at        timestamptz NOT NULL DEFAULT now()
);
