-- openlog:phase expand
-- 0099_browser_key_value: a browser key can be read back, because it was never a secret (rum.md §3).
--
-- Every other credential openlog issues is stored as sha256 and shown once, and that stays true: a license
-- key, an API key or an SSO client secret read back from the database would be a real escalation. A browser
-- key is the one exception, and the reason is not convenience — it is that **the value is already public by
-- construction**. It ships inside a web page; everyone who can open the page has it, and the threat model
-- (rum.md §3.5) assumes exactly that. Storing it adds no exposure that loading the site does not.
--
-- What it removes is a genuine operational absurdity: today an operator who loses the value of a key that
-- is printed in their own HTML has to rotate it and redeploy the page, because openlog refuses to repeat
-- something the internet already knows.
--
-- `key_hash` stays and stays unique: it is what the ingest path resolves, and a dump must still not let
-- someone else's key be registered here. The plaintext is additional, never a replacement.
--
-- Existing rows get '' and cannot be filled in: their plaintext was never stored and is not recoverable.
-- Those keys can only be rotated, which is what an operator had to do before this migration anyway.

ALTER TABLE browser_keys
    ADD COLUMN IF NOT EXISTS key_value text NOT NULL DEFAULT '';

COMMENT ON COLUMN browser_keys.key_value IS
    'The key in plaintext, readable by anyone who may list browser keys. Public by construction: it ships '
    'in the page. Empty for keys created before 0099 — their value was never stored. Never true of '
    'license_keys, api_keys or SSO secrets, which remain hash-only.';
