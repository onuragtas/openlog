-- openlog:phase expand
-- 0030_sso: single sign-on (OIDC, SAML 2.0) per organization, claimed e-mail domains, IdP group → role mappings,
-- SCIM 2.0 provisioning and SSO-bound sessions (docs/contracts/postgres.md "Single sign-on and SCIM", D-077, D-078).
--
-- Mixed versions: an older api ignores these tables and the new session columns (SSO sessions then act like
-- password sessions of the same user until the rollout finishes; enforcement is not applied by old pods).

-- One SSO connection per organization. Secrets (OIDC client secret, SAML SP private key) are AES-256-GCM encrypted
-- with OPENLOG_SSO_SECRET_KEY (or a key derived from OPENLOG_KEY_HASH_SECRET), see postgres.md "Secrets".
CREATE TABLE sso_connections (
    id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                   uuid NOT NULL UNIQUE REFERENCES organizations (id) ON DELETE CASCADE,
    protocol                 text NOT NULL CHECK (protocol IN ('oidc', 'saml')),
    name                     text NOT NULL DEFAULT '' CHECK (length(name) <= 200),
    enabled                  boolean NOT NULL DEFAULT false,
    -- Protocol settings (OIDC issuer/client id/scopes, SAML IdP metadata/SP certificate …), postgres.md.
    config                   jsonb NOT NULL DEFAULT '{}'::jsonb,
    secret_enc               bytea,
    sp_key_enc               bytea,
    email_attribute          text NOT NULL DEFAULT '' CHECK (length(email_attribute) <= 256),
    name_attribute           text NOT NULL DEFAULT '' CHECK (length(name_attribute) <= 256),
    groups_attribute         text NOT NULL DEFAULT '' CHECK (length(groups_attribute) <= 256),
    jit_enabled              boolean NOT NULL DEFAULT true,
    default_role             text NOT NULL DEFAULT 'viewer' CHECK (default_role IN ('admin', 'member', 'viewer')),
    session_max_age_seconds  integer NOT NULL DEFAULT 0 CHECK (session_max_age_seconds >= 0),
    enforce                  boolean NOT NULL DEFAULT false,
    break_glass_user_ids     uuid[] NOT NULL DEFAULT '{}',
    -- config_version increases with every settings change; enforcement needs a successful test of the current one.
    config_version           integer NOT NULL DEFAULT 1,
    tested_version           integer NOT NULL DEFAULT 0,
    last_test_at             timestamptz,
    last_test_ok             boolean NOT NULL DEFAULT false,
    last_test_error          text NOT NULL DEFAULT '',
    last_test_details        jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by               uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by               uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now(),
    CHECK (NOT enforce OR enabled)
);

-- E-mail domains claimed by an organization. A verified domain belongs to exactly one organization; only users
-- whose address is in a verified domain of the organization can sign in through its connection.
CREATE TABLE sso_domains (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    domain              text NOT NULL CHECK (domain = lower(domain) AND length(domain) BETWEEN 3 AND 253),
    -- DNS TXT value "openlog-domain-verification=<token>" (public, not a credential).
    dns_token           text NOT NULL,
    -- E-mail verification: sha256 of the link token sent to email_address.
    email_token_hash    bytea CHECK (email_token_hash IS NULL OR length(email_token_hash) = 32),
    email_address       text NOT NULL DEFAULT '',
    email_expires_at    timestamptz,
    verified_at         timestamptz,
    verification_method text NOT NULL DEFAULT '' CHECK (verification_method IN ('', 'dns_txt', 'email')),
    last_checked_at     timestamptz,
    created_by          uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, domain)
);
CREATE UNIQUE INDEX sso_domains_verified_uniq ON sso_domains (domain) WHERE verified_at IS NOT NULL;
CREATE UNIQUE INDEX sso_domains_email_token_uniq ON sso_domains (email_token_hash) WHERE email_token_hash IS NOT NULL;

-- IdP group (OIDC claim, SAML attribute, SCIM group displayName) → role. The highest matching role wins.
CREATE TABLE sso_role_mappings (
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    group_name  text NOT NULL CHECK (length(group_name) BETWEEN 1 AND 512),
    role        text NOT NULL CHECK (role IN ('admin', 'member', 'viewer')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, group_name)
);

-- In-flight sign-ins (10 minutes). state_hash = sha256(state), binding_hash = sha256(browser binding cookie).
CREATE TABLE sso_login_states (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    state_hash       bytea NOT NULL UNIQUE CHECK (length(state_hash) = 32),
    binding_hash     bytea CHECK (binding_hash IS NULL OR length(binding_hash) = 32),
    org_id           uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    connection_id    uuid NOT NULL REFERENCES sso_connections (id) ON DELETE CASCADE,
    purpose          text NOT NULL CHECK (purpose IN ('login', 'test')),
    idp_initiated    boolean NOT NULL DEFAULT false,
    nonce            text NOT NULL DEFAULT '',
    pkce_verifier    text NOT NULL DEFAULT '',
    saml_request_id  text NOT NULL DEFAULT '',
    redirect_to      text NOT NULL DEFAULT '/',
    actor_user_id    uuid REFERENCES users (id) ON DELETE CASCADE,
    config_version   integer NOT NULL DEFAULT 0,
    result           jsonb,
    created_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    consumed_at      timestamptz
);
CREATE INDEX sso_login_states_expires_idx ON sso_login_states (expires_at);

-- SAML assertion replay cache: an assertion id is accepted once per connection until it expires.
CREATE TABLE sso_saml_assertions (
    connection_id  uuid NOT NULL REFERENCES sso_connections (id) ON DELETE CASCADE,
    assertion_id   text NOT NULL CHECK (length(assertion_id) BETWEEN 1 AND 512),
    expires_at     timestamptz NOT NULL,
    PRIMARY KEY (connection_id, assertion_id)
);
CREATE INDEX sso_saml_assertions_expires_idx ON sso_saml_assertions (expires_at);

-- SCIM bearer tokens, hashed like API keys (OPENLOG_KEY_HASH_SECRET, D-044).
CREATE TABLE scim_tokens (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    key_prefix    text NOT NULL,
    key_hash      bytea NOT NULL UNIQUE CHECK (length(key_hash) = 32),
    created_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz,
    expires_at    timestamptz,
    revoked_at    timestamptz
);
CREATE INDEX scim_tokens_org_idx ON scim_tokens (org_id, created_at);

-- SCIM users: one row per (organization, user). active=false keeps the row while the membership is removed.
CREATE TABLE scim_users (
    org_id        uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    user_id       uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    user_name     text NOT NULL CHECK (length(user_name) BETWEEN 1 AND 320),
    external_id   text NOT NULL DEFAULT '' CHECK (length(external_id) <= 512),
    active        boolean NOT NULL DEFAULT true,
    display_name  text NOT NULL DEFAULT '' CHECK (length(display_name) <= 200),
    given_name    text NOT NULL DEFAULT '' CHECK (length(given_name) <= 200),
    family_name   text NOT NULL DEFAULT '' CHECK (length(family_name) <= 200),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, user_id)
);
CREATE UNIQUE INDEX scim_users_user_name_uniq ON scim_users (org_id, lower(user_name));
CREATE UNIQUE INDEX scim_users_external_id_uniq ON scim_users (org_id, external_id) WHERE external_id <> '';

CREATE TABLE scim_groups (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    display_name  text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 512),
    external_id   text NOT NULL DEFAULT '' CHECK (length(external_id) <= 512),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX scim_groups_display_name_uniq ON scim_groups (org_id, lower(display_name));

CREATE TABLE scim_group_members (
    group_id  uuid NOT NULL REFERENCES scim_groups (id) ON DELETE CASCADE,
    user_id   uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, user_id)
);
CREATE INDEX scim_group_members_user_idx ON scim_group_members (user_id);

-- Sessions: how they were created and, for SSO, the only organization they may act in. Deleting the connection
-- deletes its sessions.
ALTER TABLE sessions
    ADD COLUMN auth_method text NOT NULL DEFAULT 'password' CHECK (auth_method IN ('password', 'oidc', 'saml')),
    ADD COLUMN org_id uuid REFERENCES organizations (id) ON DELETE CASCADE,
    ADD COLUMN sso_connection_id uuid REFERENCES sso_connections (id) ON DELETE CASCADE;
CREATE INDEX sessions_org_idx ON sessions (org_id) WHERE org_id IS NOT NULL;
