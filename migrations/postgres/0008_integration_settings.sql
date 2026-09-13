-- openlog:phase expand
-- 0008_integration_settings: per-organization infra agent integration settings (endpoint, credentials) edited in
-- the UI and delivered to agents through POST /v1/openlog/agent/sync, and the revision each agent last applied
-- (docs/contracts/postgres.md "Integration settings", releases-updates.md §3, api.md "Integration settings").
--
-- Mixed versions (releases-updates.md §6): older binaries neither read nor write the table or the column. An old
-- ingest answers sync without integrations_config (agents keep their last applied config) and does not update
-- agent_hosts.integrations_config_revision (the stored value goes stale until a new ingest records the next sync).

CREATE TABLE integration_settings (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    -- NULL = every host of the organization.
    host_id         text CHECK (host_id IS NULL OR length(host_id) BETWEEN 1 AND 256),
    integration     text NOT NULL CHECK (integration IN ('nginx', 'redis', 'mysql', 'postgresql', 'docker')),
    -- Selects the discovered service instance(s) the setting applies to (all empty/NULL = every instance).
    match_port      integer CHECK (match_port IS NULL OR match_port BETWEEN 1 AND 65535),
    match_container text NOT NULL DEFAULT '',
    match_endpoint  text NOT NULL DEFAULT '',
    match_instance  text NOT NULL DEFAULT '',
    enabled         boolean NOT NULL DEFAULT true,
    endpoint        text NOT NULL DEFAULT '',
    username        text NOT NULL DEFAULT '',
    -- AES-256-GCM (OPENLOG_SECRETS_KEY): "ol1:<key id>:<base64(nonce|ciphertext|tag)>", AAD org_id/id; '' = no password.
    password_enc    text NOT NULL DEFAULT '',
    database        text NOT NULL DEFAULT '',
    databases       text[] NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    updated_by      uuid REFERENCES users (id) ON DELETE SET NULL
);
-- One setting per scope, integration and match.
CREATE UNIQUE INDEX integration_settings_scope_uniq ON integration_settings
    (org_id, coalesce(host_id, ''), integration, coalesce(match_port, 0), match_container, match_endpoint, match_instance);

-- Revision of the remote integration config the agent reported as applied ('' = none, 'disabled' = the agent
-- does not accept remote config).
ALTER TABLE agent_hosts ADD COLUMN IF NOT EXISTS integrations_config_revision text NOT NULL DEFAULT '';
