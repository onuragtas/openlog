-- openlog:phase expand
-- 0060_language_preferences: user language preference and organization default language (docs/contracts/api.md
-- "E-mail language", postgres.md, D-095).
--
-- users.locale (0050) keeps its meaning for rows with locale_explicit = false: the supported language the client preferred
-- when the account was created. locale_explicit = true means the user chose users.locale in Settings → Profile; the web
-- UI starts in that language and e-mails to the user use it. organizations.locale is the default e-mail language of the
-- organization ('' = none) used when the recipient has no explicit preference.
--
-- E-mail language: user preference → organization default → stored request language → English.
--
-- Mixed versions: older binaries neither read nor write the columns (their e-mails keep the request language).
ALTER TABLE users ADD COLUMN IF NOT EXISTS locale_explicit boolean NOT NULL DEFAULT false;
ALTER TABLE organizations ADD COLUMN IF NOT EXISTS locale text NOT NULL DEFAULT '' CHECK (locale IN ('', 'en', 'tr'));
