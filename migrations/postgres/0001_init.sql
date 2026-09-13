-- openlog:phase expand
-- 0001_init: organizations (= tenants), users, memberships, ingest license keys,
-- API keys, sessions, invitations, audit log and login rate limiting.
-- See docs/contracts/postgres.md. Secrets (keys, session tokens, invitation
-- tokens) are stored only as SHA-256 hashes; passwords as argon2id PHC strings.

CREATE TABLE organizations (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- ClickHouse tenant key (first ORDER BY column of every table). Never changes.
    tenant_id   text NOT NULL UNIQUE CHECK (tenant_id ~ '^[a-z0-9][a-z0-9_-]{0,62}$'),
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email          text NOT NULL UNIQUE CHECK (email = lower(email) AND length(email) BETWEEN 3 AND 320),
    name           text NOT NULL DEFAULT '' CHECK (length(name) <= 200),
    -- argon2id PHC string; NULL for users that can only sign in through SSO (M4).
    password_hash  text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    last_login_at  timestamptz,
    disabled_at    timestamptz
);

CREATE TABLE memberships (
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role        text NOT NULL CHECK (role IN ('owner', 'admin', 'member', 'viewer')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, user_id)
);
CREATE INDEX memberships_user_idx ON memberships (user_id, created_at);

-- Ingest license keys (OTLP). key_hash = sha256(key).
CREATE TABLE license_keys (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    key_prefix    text NOT NULL,
    key_hash      bytea NOT NULL UNIQUE CHECK (length(key_hash) = 32),
    created_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz,
    revoked_at    timestamptz,
    revoked_by    uuid REFERENCES users (id) ON DELETE SET NULL
);
CREATE INDEX license_keys_org_idx ON license_keys (org_id, created_at);

-- API keys for programmatic, read-only Query API access. key_hash = sha256(key).
CREATE TABLE api_keys (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    key_prefix    text NOT NULL,
    key_hash      bytea NOT NULL UNIQUE CHECK (length(key_hash) = 32),
    scope         text NOT NULL DEFAULT 'read' CHECK (scope IN ('read')),
    created_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz,
    expires_at    timestamptz,
    revoked_at    timestamptz,
    revoked_by    uuid REFERENCES users (id) ON DELETE SET NULL
);
CREATE INDEX api_keys_org_idx ON api_keys (org_id, created_at);

-- Browser sessions. token_hash = sha256(cookie value).
CREATE TABLE sessions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash    bytea NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    csrf_token    text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    revoked_at    timestamptz,
    ip            text NOT NULL DEFAULT '',
    user_agent    text NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user_idx ON sessions (user_id, created_at DESC);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

-- Invitations. token_hash = sha256(token); the token is shown once to the inviter.
CREATE TABLE invitations (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    email        text NOT NULL CHECK (email = lower(email)),
    role         text NOT NULL CHECK (role IN ('owner', 'admin', 'member', 'viewer')),
    token_hash   bytea NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    invited_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    accepted_at  timestamptz,
    accepted_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    revoked_at   timestamptz
);
CREATE UNIQUE INDEX invitations_pending_uniq ON invitations (org_id, email)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;

CREATE TABLE audit_log (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id         uuid REFERENCES organizations (id) ON DELETE CASCADE,
    actor_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    actor_email    text NOT NULL DEFAULT '',
    action         text NOT NULL,
    target_type    text NOT NULL DEFAULT '',
    target_id      text NOT NULL DEFAULT '',
    details        jsonb NOT NULL DEFAULT '{}'::jsonb,
    ip             text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_org_idx ON audit_log (org_id, created_at DESC);

-- Failed login attempts per sha256(lower(email) | client IP), shared by all API pods.
CREATE TABLE login_failures (
    key_hash      bytea NOT NULL,
    attempted_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX login_failures_key_idx ON login_failures (key_hash, attempted_at);
CREATE INDEX login_failures_time_idx ON login_failures (attempted_at);
