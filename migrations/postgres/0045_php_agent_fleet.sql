-- openlog:phase expand
-- 0045_php_agent_fleet: PHP agent installation through the infra agent (docs/contracts/php-agent.md §7.3,
-- releases-updates.md §3–§4 "PHP agent", postgres.md "Fleet", D-083): the policy's php_agent section, per-host PHP
-- agent modes and the PHP runtime inventory agents report in sync.
--
-- Mixed versions: an older api writes policies without php_agent (the column keeps its value, '{}' = defaults), an
-- older ingest updates agent_hosts without touching php_agent.

ALTER TABLE agent_update_policies ADD COLUMN php_agent jsonb NOT NULL DEFAULT '{}'::jsonb;

-- The last php_agent section of the agent's sync request (runtimes, installed version, last operation); NULL when the
-- agent does not report one.
ALTER TABLE agent_hosts ADD COLUMN php_agent jsonb;

CREATE TABLE agent_php_host_overrides (
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    host_id     text NOT NULL CHECK (length(host_id) BETWEEN 1 AND 256),
    mode        text NOT NULL CHECK (mode IN ('off', 'manual', 'auto')),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (org_id, host_id)
);
