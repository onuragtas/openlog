-- openlog:phase expand
-- 0050_email_locale: language of transactional e-mails (internal/mail/templates, docs/contracts/postgres.md).
--
-- There is no organization language setting; the language is the supported language the client preferred
-- (Accept-Language) when the row was created: the inviter's for invitations (used again on resend), the new user's for
-- verification links and users (used for usage notifications to owners). '' = unknown = English.
--
-- Mixed versions: older binaries neither read nor write the columns; their rows get '' (English, as before).
ALTER TABLE invitations ADD COLUMN IF NOT EXISTS locale text NOT NULL DEFAULT '' CHECK (length(locale) <= 16);
ALTER TABLE email_verifications ADD COLUMN IF NOT EXISTS locale text NOT NULL DEFAULT '' CHECK (length(locale) <= 16);
ALTER TABLE users ADD COLUMN IF NOT EXISTS locale text NOT NULL DEFAULT '' CHECK (length(locale) <= 16);
