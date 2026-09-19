-- openlog:phase expand
-- 0094_source_maps: source maps for browser (RUM) errors, so a stack stops reading like
-- "at n (main.3f2a1b9c.js:1:842)" (docs/contracts/rum.md §8 listed this as the first thing missing).
--
-- Why the bundle file name is the key, and not a release identifier: a RUM span carries no build id at all.
-- internal/rum.Sanitize rebuilds the resource from an allowlist that has no service.version (rum.md §3.3),
-- and the browser key has no such field either. What a stack *does* carry is the content hash inside the
-- bundle name, which is exactly what identifies one build — apm's fingerprint deliberately strips it from
-- the group key (errors.go bundleHashRe) but it survives verbatim in the stored stack text. So (app, script)
-- is the natural key, and a new deploy simply uploads maps under its new hashed names.
--
-- The document itself is not stored here. Maps are megabytes of JSON; they go to internal/objstore (the
-- same local/S3 store the data export uses), and this table is the index that makes listing, replacing and
-- deleting possible without listing a bucket. The object key is "sourcemaps/<org_id>/<id>", built from ids
-- only, so nothing a user types has to be sanitised into a storage path.

CREATE TABLE IF NOT EXISTS source_maps (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    -- The browser application the map belongs to: the browser key's service_name, i.e. the APM service the
    -- errors land under.
    app         text NOT NULL CHECK (length(app) BETWEEN 1 AND 512),
    -- The generated file as a stack frame names it, without directory, query string or fragment:
    -- "main.3f2a1b9c.js". 255 is the longest name any bundler produces by a wide margin.
    script      text NOT NULL CHECK (length(script) BETWEEN 1 AND 255),
    size_bytes  bigint NOT NULL CHECK (size_bytes > 0),
    -- Of the stored document, so a re-upload of the same bytes is visible as such.
    sha256      bytea NOT NULL CHECK (length(sha256) = 32),
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- One map per file per application: uploading the same script again replaces it, keeping the id (and with
-- it the object key), so storage never accumulates orphans behind a changed row.
CREATE UNIQUE INDEX IF NOT EXISTS source_maps_org_app_script ON source_maps (org_id, app, script);
CREATE INDEX IF NOT EXISTS source_maps_org_idx ON source_maps (org_id, created_at DESC);
