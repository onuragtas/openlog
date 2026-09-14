-- openlog:phase expand
-- 0011_email_verification: e-mail address verification of self-service sign-ups (docs/contracts/postgres.md, D-045).
--
-- users.email_verified_at is NULL only for sign-ups that have not confirmed their address yet. Existing users are
-- treated as verified. The column default is now() so users created by older binaries during a rollout (invitation
-- acceptance, bootstrap, sign-up without verification) are verified, as they were before; newer binaries write
-- NULL explicitly for unverified sign-ups.
ALTER TABLE users ADD COLUMN IF NOT EXISTS email_verified_at timestamptz;
UPDATE users SET email_verified_at = created_at WHERE email_verified_at IS NULL;
ALTER TABLE users ALTER COLUMN email_verified_at SET DEFAULT now();

-- One-time verification tokens. token_hash = sha256(token); used_at is set when the link is opened.
CREATE TABLE IF NOT EXISTS email_verifications (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    email       text NOT NULL,
    token_hash  bytea NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz
);
CREATE INDEX IF NOT EXISTS email_verifications_user_idx ON email_verifications (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS email_verifications_expires_idx ON email_verifications (expires_at);
