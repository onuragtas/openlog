-- openlog:phase expand
-- 0075_license_key_tombstones: remembers the license keys revoked by an organization deletion so ingest can answer
-- 403 org_deleted instead of 401 for them, during the grace period and after the purge (docs/contracts/postgres.md
-- "Data subject requests", D-115).
--
-- Mixed versions: older api pods do not write tombstones when they schedule a deletion (those keys keep answering 401)
-- and older ingest pods never read the table (they answer 401 for every revoked key, as before).

CREATE TABLE IF NOT EXISTS license_key_tombstones (
    -- license_keys.key_hash at scheduling time (hash of the secret, never the secret; no personal data)
    key_hash        bytea PRIMARY KEY,
    reason          text NOT NULL CHECK (reason IN ('org_deleted')),
    -- no foreign key: the tombstone outlives the organization; org_deletions rows are kept
    org_deletion_id uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    -- NULL while the deletion can still be cancelled; set by the purge (completed_at + 365 days) and removed after it
    expires_at      timestamptz
);
CREATE INDEX IF NOT EXISTS license_key_tombstones_deletion_idx ON license_key_tombstones (org_deletion_id);
CREATE INDEX IF NOT EXISTS license_key_tombstones_expires_idx ON license_key_tombstones (expires_at) WHERE expires_at IS NOT NULL;
