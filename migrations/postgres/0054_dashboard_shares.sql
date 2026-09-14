-- openlog:phase expand
-- 0054_dashboard_shares: organization dashboard sharing settings and read-only share links (docs/contracts/api.md
-- "Dashboards" › "Share links", postgres.md, D-087). Share links are opt-in per organization, always expire, can be
-- revoked, and are stored only as SHA-256 hashes of their tokens.

CREATE TABLE IF NOT EXISTS dashboard_org_settings (
    org_id         uuid PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    -- public read-only share links (off by default)
    shares_enabled boolean NOT NULL DEFAULT false,
    -- e-mail domains scheduled reports may be sent to besides organization members (lower case)
    report_domains text[] NOT NULL DEFAULT '{}' CHECK (cardinality(report_domains) <= 20),
    updated_by     uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS dashboard_shares (
    id           uuid PRIMARY KEY,
    org_id       uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    dashboard_id uuid NOT NULL REFERENCES dashboards (id) ON DELETE CASCADE,
    token_hash   bytea NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    label        text NOT NULL DEFAULT '' CHECK (length(label) <= 100),
    -- relative range ("24h") or a fixed range (range_from, range_to)
    time_range   text NOT NULL DEFAULT '' CHECK (length(time_range) <= 8),
    range_from   timestamptz,
    range_to     timestamptz,
    -- locked variable values {"name": ["v1", …]}
    variables    jsonb NOT NULL DEFAULT '{}',
    created_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz,
    revoked_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    last_used_at timestamptz,
    use_count    bigint NOT NULL DEFAULT 0,
    CHECK ((time_range <> '' AND range_from IS NULL AND range_to IS NULL)
        OR (time_range = '' AND range_from IS NOT NULL AND range_to IS NOT NULL AND range_from < range_to))
);

CREATE INDEX IF NOT EXISTS dashboard_shares_dashboard ON dashboard_shares (dashboard_id, created_at DESC);
