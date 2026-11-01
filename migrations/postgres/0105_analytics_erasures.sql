-- openlog:phase expand
-- 0105_analytics_erasures: requests to delete one person's analytics data (docs/contracts/analytics.md §7).
--
-- This is a queue rather than a direct delete because the API reads ClickHouse as a read-only user, and that boundary is
-- worth more than a synchronous answer. The leader's job runs the mutations with the writer's connection.
--
-- `actor` is the identifier being erased, so a row is itself personal data: the job clears it as soon as the mutations
-- are submitted, and what remains is only the record that a request was made and completed.

CREATE TABLE IF NOT EXISTS analytics_erasures (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    tenant_id    text NOT NULL,
    app          text NOT NULL CHECK (length(app) BETWEEN 1 AND 512),
    actor        text CHECK (length(actor) BETWEEN 1 AND 128),
    requested_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    -- A pending row always knows who it is for, and a completed one never does.
    CHECK ((completed_at IS NULL) = (actor IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS analytics_erasures_pending ON analytics_erasures (requested_at) WHERE completed_at IS NULL;
