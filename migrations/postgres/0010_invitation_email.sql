-- openlog:phase expand
-- 0010_invitation_email: invitation e-mails (docs/contracts/postgres.md "invitations", D-045).
--
-- Mixed versions (releases-updates.md §6): older binaries neither read nor write the columns; invitations they
-- create get the defaults (never e-mailed), which is what they did.
ALTER TABLE invitations ADD COLUMN IF NOT EXISTS last_sent_at timestamptz;
ALTER TABLE invitations ADD COLUMN IF NOT EXISTS send_count integer NOT NULL DEFAULT 0 CHECK (send_count >= 0);
