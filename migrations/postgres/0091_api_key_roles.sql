-- openlog:phase expand
-- 0091_api_key_roles: API keys carry a membership role instead of being read-only viewers by definition
-- (docs/contracts/api.md "Authentication", postgres.md "api_keys"/"audit_log", D-133). A key's role is one of
-- viewer (today's behaviour and the default), member or admin; owner is deliberately not a key role, because
-- the owner-only operations are account and organization lifecycle, which stay with a human.
--
-- Every existing row gets 'viewer', so no key that exists today gains a permission from this migration.
--
-- audit_log learns who a key-authenticated change was made by: actor_user_id stays NULL (there is no user) and
-- the key is named by id and by the name it had at the time (the name is copied rather than joined, so the row
-- keeps its meaning after the key is renamed or deleted).
--
-- Mixed versions (releases-updates.md §6): older binaries never read api_keys.role and build a viewer principal
-- for every key, so a write-capable key is simply read-only until the last old api pod is gone. They also never
-- write the new audit_log columns, which are nullable/defaulted.

ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS role text NOT NULL DEFAULT 'viewer'
        CHECK (role IN ('viewer', 'member', 'admin'));

-- scope is derived from the role when the key is created and is kept for older binaries, which read it
-- and never read role. 0001_init constrained it to 'read', because every key was read-only then.
ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_scope_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_scope_check CHECK (scope IN ('read', 'write'));

ALTER TABLE audit_log
    ADD COLUMN IF NOT EXISTS actor_api_key_id uuid REFERENCES api_keys (id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS actor_api_key_name text NOT NULL DEFAULT '';
