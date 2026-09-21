-- openlog:phase expand
-- 0097_vulnerabilities: the vulnerability catalog openlog matches installed packages against (D-142,
-- docs/contracts/api.md "Vulnerabilities"). Unlike almost everything else in this database the catalog is
-- **not** per organization: it is public data (OSV advisories), the same for every tenant, and every
-- organization's hosts are matched against the same rows. Keeping a copy per organization would multiply
-- tens of megabytes by the number of tenants for no difference in the answer.
--
-- The matches themselves are per host and therefore tenant data; they live in ClickHouse (0100_host_vulns).
--
-- Mixed versions (releases-updates.md §6): an older binary does not use these tables, so a synced catalog
-- simply sits unused until the api pod holding the leader lock is upgraded.

CREATE TABLE IF NOT EXISTS vulnerabilities (
    -- The advisory id as the feed publishes it (CVE-2024-1234, DSA-5600-1, GHSA-…).
    id          text PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 128),
    -- Where it came from, so a mirror or a second feed can be told apart and removed as a unit.
    source      text NOT NULL DEFAULT 'osv' CHECK (length(source) <= 64),
    -- The CVE id when the advisory is known by one ('' otherwise): a distribution advisory and the CVE it
    -- fixes are the same fact to the person reading it.
    cve         text NOT NULL DEFAULT '' CHECK (length(cve) <= 64),
    aliases     text[] NOT NULL DEFAULT '{}',
    summary     text NOT NULL DEFAULT '' CHECK (length(summary) <= 1024),
    details     text NOT NULL DEFAULT '' CHECK (length(details) <= 8192),
    severity    text NOT NULL DEFAULT 'none' CHECK (severity IN ('critical', 'high', 'medium', 'low', 'none')),
    -- The CVSS base score and the vector it was computed from, so the score can be explained.
    score       double precision NOT NULL DEFAULT 0 CHECK (score >= 0 AND score <= 10),
    vector      text NOT NULL DEFAULT '' CHECK (length(vector) <= 256),
    published   timestamptz,
    modified    timestamptz,
    references_ text[] NOT NULL DEFAULT '{}',
    -- A withdrawn advisory is kept (so a match that disappears can be explained) but never matched.
    withdrawn   boolean NOT NULL DEFAULT false,
    synced_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS vulnerabilities_severity_idx ON vulnerabilities (severity, published DESC);

-- One row per affected package range. This is the table the matcher reads in full: a version is vulnerable
-- when it is at or after `introduced` and before `fixed` (or at most `last_affected`).
CREATE TABLE IF NOT EXISTS vulnerability_affected (
    vuln_id       text NOT NULL REFERENCES vulnerabilities (id) ON DELETE CASCADE,
    -- The OSV ecosystem, including its release ("Debian:12", "Ubuntu:22.04", "Alpine:v3.19"): the same
    -- package is patched at different versions on different releases, which is why the release is part of
    -- the key rather than a detail.
    ecosystem     text NOT NULL CHECK (length(ecosystem) BETWEEN 1 AND 128),
    package       text NOT NULL CHECK (length(package) BETWEEN 1 AND 256),
    introduced    text NOT NULL DEFAULT '' CHECK (length(introduced) <= 128),
    fixed         text NOT NULL DEFAULT '' CHECK (length(fixed) <= 128),
    last_affected text NOT NULL DEFAULT '' CHECK (length(last_affected) <= 128),
    PRIMARY KEY (vuln_id, ecosystem, package, introduced, fixed, last_affected)
);

-- The matcher's read: every range of the ecosystems in use.
CREATE INDEX IF NOT EXISTS vulnerability_affected_ecosystem_idx ON vulnerability_affected (ecosystem, package);

-- The last sync per source and ecosystem, so the UI can say how fresh the catalog is and an operator can
-- see which ecosystem failed rather than only that "the sync failed".
CREATE TABLE IF NOT EXISTS vulnerability_sync (
    source     text NOT NULL CHECK (length(source) <= 64),
    ecosystem  text NOT NULL CHECK (length(ecosystem) <= 128),
    synced_at  timestamptz NOT NULL DEFAULT now(),
    count      integer NOT NULL DEFAULT 0,
    error      text NOT NULL DEFAULT '' CHECK (length(error) <= 1024),
    PRIMARY KEY (source, ecosystem)
);
