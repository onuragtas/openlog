-- openlog:phase expand
-- 0095_synthetic_check_types: TCP, DNS and TLS certificate checks next to the HTTP check (D-140,
-- 0090_synthetics, docs/contracts/api.md "Synthetic monitoring"). The type column was the extension point:
-- a check keeps one row and one schedule per location, and what changes per type is the target and the few
-- fields that describe what a successful answer is.
--
-- http keeps `url` and the request columns; tcp, dns and tls use `target` (host:port, or the name to
-- resolve) and leave the HTTP columns at their defaults. `url` therefore loses its NOT-EMPTY bound, which
-- is now enforced per type by internal/synthetics and by the constraint below.
--
-- Mixed versions (releases-updates.md §6): an older api binary does not know the new types. It would refuse
-- to run such a check with a validation error on its own definition, which is why the new types only become
-- selectable in a UI served by a binary that has this migration.

ALTER TABLE synthetic_checks
    DROP CONSTRAINT IF EXISTS synthetic_checks_type_check;

ALTER TABLE synthetic_checks
    ADD CONSTRAINT synthetic_checks_type_check CHECK (type IN ('http', 'tcp', 'dns', 'tls'));

ALTER TABLE synthetic_checks
    DROP CONSTRAINT IF EXISTS synthetic_checks_url_check;

ALTER TABLE synthetic_checks
    ADD CONSTRAINT synthetic_checks_url_check CHECK (length(url) <= 2048);

ALTER TABLE synthetic_checks
    -- tcp and tls: host:port; dns: the name to resolve.
    ADD COLUMN IF NOT EXISTS target text NOT NULL DEFAULT '' CHECK (length(target) <= 512),
    -- dns only: which record is asked for, and the answers that make the run succeed (empty = any answer).
    ADD COLUMN IF NOT EXISTS dns_record_type text NOT NULL DEFAULT ''
        CHECK (dns_record_type IN ('', 'A', 'AAAA', 'CNAME', 'MX', 'NS', 'TXT')),
    ADD COLUMN IF NOT EXISTS dns_expected text[] NOT NULL DEFAULT '{}'
        CHECK (coalesce(array_length(dns_expected, 1), 0) <= 10),
    -- tls only: a certificate expiring within this many days fails the run, so the alert arrives before the
    -- outage does. Also reported as a metric for https checks, which see the certificate anyway.
    ADD COLUMN IF NOT EXISTS tls_warning_days integer NOT NULL DEFAULT 14
        CHECK (tls_warning_days BETWEEN 0 AND 365);

-- A check addresses exactly one thing: an http check has a URL, every other type has a target.
ALTER TABLE synthetic_checks
    DROP CONSTRAINT IF EXISTS synthetic_checks_target_per_type;

ALTER TABLE synthetic_checks
    ADD CONSTRAINT synthetic_checks_target_per_type CHECK (
        (type = 'http' AND length(url) > 0 AND target = '')
        OR (type <> 'http' AND length(target) > 0 AND url = '')
    );
