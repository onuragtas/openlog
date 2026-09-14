-- openlog:phase expand
-- 0056_sso_connections_slo: several SSO connections per organization with per-connection domains and role
-- mappings, single logout (SAML SLO, OIDC RP-initiated logout) session data, the claimed-domain invitation policy
-- and the IdP metadata refresh health (docs/contracts/postgres.md "Single sign-on and SCIM", D-088, D-089).
--
-- Mixed versions: an older api still reads one connection per organization (the first row it gets) and treats
-- connection-specific role mappings as organization-wide until the rollout finishes; it does not create
-- sso_sessions rows, so sessions it creates cannot be ended by an IdP LogoutRequest.

-- Several connections per organization. The oldest one (created_at, id) is the organization's default connection:
-- domains without connection_id route to it.
ALTER TABLE sso_connections DROP CONSTRAINT IF EXISTS sso_connections_org_id_key;
CREATE INDEX IF NOT EXISTS sso_connections_org_idx ON sso_connections (org_id, created_at, id);
ALTER TABLE sso_connections
    -- false: members of the domains routed to this connection must use SSO for invitations of other organizations.
    ADD COLUMN IF NOT EXISTS allow_external_invitations boolean NOT NULL DEFAULT true,
    -- Background refresh of OIDC discovery/JWKS and SAML metadata (leader job).
    ADD COLUMN IF NOT EXISTS idp_refreshed_at     timestamptz,
    ADD COLUMN IF NOT EXISTS idp_refresh_ok       boolean,
    ADD COLUMN IF NOT EXISTS idp_refresh_error    text NOT NULL DEFAULT '' CHECK (length(idp_refresh_error) <= 1000),
    ADD COLUMN IF NOT EXISTS idp_refresh_failures integer NOT NULL DEFAULT 0 CHECK (idp_refresh_failures >= 0),
    ADD COLUMN IF NOT EXISTS idp_next_refresh_at  timestamptz,
    -- {"fetched_at", "issuer", "oidc_discovery", "oidc_jwks", "saml_valid_until"}: the last successful fetch.
    ADD COLUMN IF NOT EXISTS idp_cache            jsonb NOT NULL DEFAULT '{}'::jsonb;
CREATE INDEX IF NOT EXISTS sso_connections_refresh_idx ON sso_connections (idp_next_refresh_at) WHERE enabled;

-- Per-connection domain routing (NULL = the organization's default connection).
ALTER TABLE sso_domains ADD COLUMN IF NOT EXISTS connection_id uuid REFERENCES sso_connections (id) ON DELETE SET NULL;

-- Per-connection role mappings (NULL = organization-wide, used by SCIM and by connections without own mappings).
ALTER TABLE sso_role_mappings ADD COLUMN IF NOT EXISTS connection_id uuid REFERENCES sso_connections (id) ON DELETE CASCADE;
ALTER TABLE sso_role_mappings DROP CONSTRAINT IF EXISTS sso_role_mappings_pkey;
CREATE UNIQUE INDEX IF NOT EXISTS sso_role_mappings_uniq
    ON sso_role_mappings (org_id, coalesce(connection_id, '00000000-0000-0000-0000-000000000000'::uuid), group_name);

-- Logout states (SAML LogoutRequest id / OIDC state until the IdP returns).
ALTER TABLE sso_login_states DROP CONSTRAINT IF EXISTS sso_login_states_purpose_check;
ALTER TABLE sso_login_states ADD CONSTRAINT sso_login_states_purpose_check CHECK (purpose IN ('login', 'test', 'logout'));

-- The IdP session of an SSO session: SAML NameID and SessionIndex (IdP LogoutRequest matching, SP LogoutRequest),
-- OIDC subject, sid and the sealed ID token (id_token_hint). Deleted with the session.
CREATE TABLE IF NOT EXISTS sso_sessions (
    session_id         uuid PRIMARY KEY REFERENCES sessions (id) ON DELETE CASCADE,
    connection_id      uuid NOT NULL REFERENCES sso_connections (id) ON DELETE CASCADE,
    org_id             uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    user_id            uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    subject            text NOT NULL DEFAULT '' CHECK (length(subject) <= 1024),
    name_id_format     text NOT NULL DEFAULT '' CHECK (length(name_id_format) <= 256),
    name_qualifier     text NOT NULL DEFAULT '' CHECK (length(name_qualifier) <= 1024),
    sp_name_qualifier  text NOT NULL DEFAULT '' CHECK (length(sp_name_qualifier) <= 1024),
    session_index      text NOT NULL DEFAULT '' CHECK (length(session_index) <= 1024),
    id_token_enc       bytea,
    created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS sso_sessions_subject_idx ON sso_sessions (connection_id, subject);
CREATE INDEX IF NOT EXISTS sso_sessions_user_idx ON sso_sessions (user_id);
