-- openlog:phase expand
-- 0098_mobile_keys: mobile applications get their own kind of public key (rum.md §3.6).
--
-- A browser key is scoped by the `Origin` header, and a mobile application has none — it is not a page on a
-- site, it is a binary on a device. So the table gains a `kind` and, beside `origins`, an `app_ids` list of
-- the Android package names and iOS bundle identifiers the key ships in.
--
-- **The two scopes are not equally strong, and this is the place to say so.** A browser sets `Origin` itself
-- and page JavaScript cannot forge it. An application identifier is self-declared: the app sends its own
-- package name and curl sends whatever it likes. The app allowlist narrows casual reuse — a key lifted from
-- one APK does not work in another developer's app by accident — and nothing more. What bounds a determined
-- attacker is what already bounds a copied browser key: the key authorizes one endpoint, every payload is
-- rewritten server-side from the key's own row, the rate limit is per key, and revocation takes effect
-- within a minute. Real attestation (Play Integrity, App Attest) is the upgrade path, not what this is.
--
-- One table rather than two: both kinds are resolved by the same hash lookup on the ingest path and share
-- service/environment forcing, the rate limit, the sample rate and soft revocation. Splitting them would
-- duplicate all of that to express one difference.
--
-- Mixed versions (releases-updates.md §6): an older binary selects neither new column and sees every row as
-- the browser key it already was — `kind` defaults to 'browser', so existing rows need no backfill.

ALTER TABLE browser_keys
    ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'browser'
        CHECK (kind IN ('browser', 'mobile')),
    ADD COLUMN IF NOT EXISTS app_ids text[] NOT NULL DEFAULT '{}';

-- The original constraint required a non-empty origins list of every row, which a mobile key cannot satisfy.
-- It is replaced by one that requires exactly the list belonging to the row's kind, and forbids the other:
-- a row carrying both would be a key whose scope depends on which check happens to run.
ALTER TABLE browser_keys DROP CONSTRAINT IF EXISTS browser_keys_origins_check;

ALTER TABLE browser_keys DROP CONSTRAINT IF EXISTS browser_keys_scope_check;
ALTER TABLE browser_keys ADD CONSTRAINT browser_keys_scope_check CHECK (
    (kind = 'browser'
        AND array_length(origins, 1) BETWEEN 1 AND 50
        AND coalesce(array_length(app_ids, 1), 0) = 0)
    OR
    (kind = 'mobile'
        AND array_length(app_ids, 1) BETWEEN 1 AND 50
        AND coalesce(array_length(origins, 1), 0) = 0)
);

CREATE INDEX IF NOT EXISTS browser_keys_org_kind_idx ON browser_keys (org_id, kind, created_at DESC);
