-- openlog:phase expand
-- 0098_synthetic_check_types: the run row describes which kind of check ran and what the answer was
-- (D-140, 0093_synthetic_runs, migrations/postgres/0095_synthetic_check_types).
--
-- `check_type` keeps the history readable after a check was deleted, `target` is what tcp, dns and tls
-- checks address (http keeps `url`), `answer` is the run's evidence — the resolved records of a dns check,
-- the address a tcp check reached, the certificate subject a tls check saw — and `cert_expires_at` is the
-- certificate's notAfter, recorded for tls checks and for https checks, which see the certificate anyway
-- (0 when the run saw none). Old rows read as 'http' with empty strings and a zero date, which is what they
-- were.

ALTER TABLE openlog.synthetic_runs_local ON CLUSTER '{cluster}'
    ADD COLUMN IF NOT EXISTS check_type      LowCardinality(String) DEFAULT 'http' AFTER check_name,
    ADD COLUMN IF NOT EXISTS target          String CODEC(ZSTD(1)) AFTER url,
    ADD COLUMN IF NOT EXISTS answer          String CODEC(ZSTD(1)) AFTER target,
    ADD COLUMN IF NOT EXISTS cert_expires_at DateTime('UTC') DEFAULT toDateTime(0) CODEC(DoubleDelta, ZSTD(1)) AFTER answer;

ALTER TABLE openlog.synthetic_runs ON CLUSTER '{cluster}'
    ADD COLUMN IF NOT EXISTS check_type      LowCardinality(String) DEFAULT 'http' AFTER check_name,
    ADD COLUMN IF NOT EXISTS target          String AFTER url,
    ADD COLUMN IF NOT EXISTS answer          String AFTER target,
    ADD COLUMN IF NOT EXISTS cert_expires_at DateTime('UTC') DEFAULT toDateTime(0) AFTER answer;
