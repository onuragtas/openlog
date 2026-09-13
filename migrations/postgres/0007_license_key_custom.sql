-- openlog:phase expand
-- 0007_license_key_custom: ingest license keys whose value was chosen by an operator (imported under
-- Settings -> License keys, or OPENLOG_BOOTSTRAP_LICENSE_KEY) instead of generated (docs/contracts/postgres.md).
--
-- Mixed versions (releases-updates.md §6): older binaries neither read nor write the column; keys they create
-- get the default (false), which is correct because they only generate keys. license_keys.key_hash is already
-- UNIQUE (0001_init), which keeps an imported value from being used by two organizations or reused after revocation.
ALTER TABLE license_keys ADD COLUMN IF NOT EXISTS custom boolean NOT NULL DEFAULT false;

-- Existing bootstrap keys are operator-chosen; generated keys always start with olk_.
UPDATE license_keys SET custom = true WHERE custom = false AND key_prefix NOT LIKE 'olk\_%';
