-- openlog:phase expand
-- 0080_saved_views: saved explorer views (Logs/Metrics Explorer; docs/contracts/api.md "Saved views", postgres.md,
-- D-118). A view is the explorer state (filters, groups, columns, time range, …) as an opaque JSON object validated by
-- the API; visibility private (creator only) or org (every member).
--
-- Mixed versions: older binaries do not use the table.

CREATE TABLE IF NOT EXISTS saved_views (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    signal      text NOT NULL CHECK (signal IN ('logs', 'metrics', 'traces')),
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    visibility  text NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'org')),
    state       jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(state) = 'object' AND octet_length(state::text) <= 65536),
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS saved_views_org_signal_name ON saved_views (org_id, signal, lower(name));
