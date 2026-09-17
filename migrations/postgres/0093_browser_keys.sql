-- openlog:phase expand
-- 0093_browser_keys: the public keys of the browser SDK (docs/contracts/rum.md §3, api.md "Browser keys",
-- D-136). A browser key is a **separate key type**, not an ingest license key, because it is public by
-- construction: it ships inside a web page, so anyone who can open the page can read it.
--
-- What makes it a different table rather than a column on license_keys:
--
--  1. Capability. An ingest license key authenticates /v1/traces, /v1/metrics, /v1/logs and the agent fleet
--     sync — everything. A browser key authenticates exactly one endpoint, POST /v1/rum, and every payload
--     it carries is validated as RUM data (internal/rum). Keeping the two in one table would mean one
--     revocation mistake or one forgotten scope check gives a public value the agent's powers.
--  2. Origins. A browser key carries an origin allowlist, which an ingest key has no concept of.
--  3. Rate limit. A browser key carries its own per-minute budget, because the traffic that reaches it is
--     the open internet rather than a fleet of machines an operator installed.
--
-- The key value is stored hashed like every other credential (HMAC-SHA256 with OPENLOG_KEY_HASH_SECRET,
-- else SHA-256; postgres.md "Secrets"). That is not about secrecy — the plaintext is on every page — but
-- about not handing a database dump the ability to write telemetry, and about the uniqueness index below.

CREATE TABLE IF NOT EXISTS browser_keys (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    -- 'olb_' + first 8 characters, like license_keys.key_prefix. The whole value is public anyway; the
    -- prefix is what the UI and the audit log show so a key can be named without pasting it.
    key_prefix    text NOT NULL,
    key_hash      bytea NOT NULL UNIQUE CHECK (length(key_hash) = 32),
    -- The application the key reports as (resource service.name). It is **forced server-side** on every
    -- payload: a copied key cannot be used to write telemetry under another service's name, so a browser
    -- key can never poison an APM service that a backend agent owns.
    service_name  text NOT NULL CHECK (length(service_name) BETWEEN 1 AND 512),
    -- Optional deployment.environment.name, forced the same way ('' = none).
    environment   text NOT NULL DEFAULT '' CHECK (length(environment) <= 256),
    -- Origins the key may be used from, as sent in the browser's Origin header: exact
    -- ('https://app.example.com') or a single-label subdomain wildcard ('https://*.example.com').
    -- Empty is rejected by the API: a key without an allowlist is a key anyone can point at this
    -- organization from any page, and making that the default would make the safe case the opt-in.
    origins       text[] NOT NULL DEFAULT '{}'
                  CHECK (array_length(origins, 1) BETWEEN 1 AND 50),
    -- Events per minute accepted for this key across the installation (soft, per ingest pod; rum.md §3.4).
    rate_limit_per_minute integer NOT NULL DEFAULT 6000
                  CHECK (rate_limit_per_minute BETWEEN 60 AND 10000000),
    -- Share of sessions the SDK keeps (0 < x <= 1). Served to the SDK by GET /v1/rum/config so the sampling
    -- rate can be lowered without redeploying the web application.
    sample_rate   double precision NOT NULL DEFAULT 1 CHECK (sample_rate > 0 AND sample_rate <= 1),
    created_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    updated_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    last_used_at  timestamptz,
    revoked_at    timestamptz,
    revoked_by    uuid REFERENCES users (id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS browser_keys_org_idx ON browser_keys (org_id, created_at DESC);

-- Revocation is a soft delete, like license_keys: the row stays for the audit trail and keeps the value
-- unusable forever (the UNIQUE key_hash). That matters more here than for an ingest key, because the
-- plaintext of a revoked browser key is still sitting in every browser cache that loaded the old page.
