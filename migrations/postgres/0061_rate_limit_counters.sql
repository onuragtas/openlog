-- openlog:phase expand
-- 0061_rate_limit_counters: cluster-wide request counters of the public dashboard share link endpoints (internal/ratelimit,
-- docs/contracts/api.md "Public share link endpoints", postgres.md, D-096).
--
-- One row per (key, minute bucket). key = first 16 bytes of sha256(purpose, subject) (client IPs and share tokens are
-- never stored). api pods add their counts in batches with INSERT … ON CONFLICT DO UPDATE SET n = n + excluded.n and
-- estimate a sliding one-minute window from the current and previous bucket. The api leader deletes buckets older
-- than two minutes.
--
-- UNLOGGED: counters are short-lived; after a PostgreSQL crash or failover they start from zero (limits reset once).
--
-- Mixed versions: older binaries do not use the table (their pods keep per-pod limits).
CREATE UNLOGGED TABLE IF NOT EXISTS rate_limit_counters (
    key    bytea NOT NULL CHECK (length(key) = 16),
    bucket timestamptz NOT NULL,
    n      integer NOT NULL CHECK (n >= 0),
    PRIMARY KEY (key, bucket)
);

CREATE INDEX IF NOT EXISTS rate_limit_counters_bucket ON rate_limit_counters (bucket);
