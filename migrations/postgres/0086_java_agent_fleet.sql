-- openlog:phase expand
-- 0086_java_agent_fleet: keeping openlog-javaagent.jar updated through the infra agent (docs/contracts/java-agent.md
-- §2, releases-updates.md §3 "Java agent", postgres.md "Fleet", D-123): the policy's java_agent section, per-host Java
-- agent modes and the JVM inventory agents report in sync.
--
-- Mixed versions: an older api writes policies without java_agent (the column keeps its value, '{}' = defaults), an
-- older ingest updates agent_hosts without touching java_agent.

ALTER TABLE agent_update_policies ADD COLUMN java_agent jsonb NOT NULL DEFAULT '{}'::jsonb;

-- The last java_agent section of the agent's sync request (JVMs, managed jar version, link state, last operation);
-- NULL when the agent does not report one.
ALTER TABLE agent_hosts ADD COLUMN java_agent jsonb;

CREATE TABLE agent_java_host_overrides (
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    host_id     text NOT NULL CHECK (length(host_id) BETWEEN 1 AND 256),
    mode        text NOT NULL CHECK (mode IN ('off', 'manual', 'auto')),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (org_id, host_id)
);
