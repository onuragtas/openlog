-- openlog:phase expand
-- 0106_crash_symbols: what turns the stack of a mobile crash back into source (docs/contracts/mobile-crash.md §3).
--
-- The files are not stored here: they go to internal/objstore, like source maps, and this is the index that makes listing,
-- replacing and deleting possible without listing a bucket. The object key is built from ids only.

CREATE TABLE IF NOT EXISTS crash_symbols (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    app         text NOT NULL CHECK (length(app) BETWEEN 1 AND 512),
    -- 'r8' is keyed by version, because an obfuscated Java stack names nothing more exact. 'native' is keyed by image id, a
    -- Mach-O UUID or an ELF build id, which identifies one binary exactly whatever version it shipped in.
    kind        text NOT NULL CHECK (kind IN ('r8', 'native')),
    key         text NOT NULL CHECK (length(key) BETWEEN 1 AND 128),
    size_bytes  bigint NOT NULL CHECK (size_bytes > 0),
    sha256      bytea NOT NULL CHECK (length(sha256) = 32),
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- One file per key: uploading again replaces it and keeps the id, and with it the object key.
CREATE UNIQUE INDEX IF NOT EXISTS crash_symbols_key ON crash_symbols (org_id, app, kind, key);
CREATE INDEX IF NOT EXISTS crash_symbols_org_idx ON crash_symbols (org_id, created_at DESC);
