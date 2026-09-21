-- openlog:phase expand
-- 0100_host_vulns: the vulnerable packages found on each host (D-142, migrations/postgres/0097_vulnerabilities).
-- Written by the api leader's matcher, which reads the latest inventory snapshot of every host and compares
-- each installed package with the catalog's affected ranges (internal/vuln). Read only through the
-- Distributed table and the tenant-scoped query layer: a match is about one host, so it is tenant data even
-- though the advisory it names is public.
--
-- ReplacingMergeTree on (tenant, host, vulnerability, package): a host keeps one row per finding, and the
-- next match run replaces it — a package that was upgraded stops being reported when its row is replaced by
-- a run that no longer finds it, which is why `resolved_at` exists rather than a delete.
--
-- TTL 90 days on last_seen, the class of the alert evaluations: a finding nothing has confirmed for three
-- months is about a host that stopped reporting, and the inventory it came from is long gone.

CREATE TABLE IF NOT EXISTS openlog.host_vulnerabilities_local ON CLUSTER '{cluster}'
(
    tenant_id   LowCardinality(String),
    host_id     String,
    host_name   String CODEC(ZSTD(1)),
    -- The advisory and, when it has one, the CVE it is known by.
    vuln_id     String CODEC(ZSTD(1)),
    cve         String CODEC(ZSTD(1)),
    severity    LowCardinality(String),
    score       Float32 CODEC(Gorilla, ZSTD(1)),
    ecosystem   LowCardinality(String),
    package     String CODEC(ZSTD(1)),
    -- The version the host has installed and the one that fixes it ('' when the feed knows none).
    version     String CODEC(ZSTD(1)),
    fixed_in    String CODEC(ZSTD(1)),
    summary     String CODEC(ZSTD(1)),
    -- When the finding was first seen and when it was last confirmed by a match run.
    first_seen  DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    last_seen   DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    -- Set by the match run that no longer finds the package vulnerable (upgraded, or the advisory was
    -- withdrawn); epoch 0 while the finding is open.
    resolved_at DateTime('UTC') DEFAULT toDateTime(0) CODEC(DoubleDelta, ZSTD(1))
)
ENGINE = ReplicatedReplacingMergeTree('/clickhouse/tables/{shard}/openlog/host_vulnerabilities_local', '{replica}', last_seen)
PARTITION BY toYYYYMM(last_seen)
ORDER BY (tenant_id, host_id, vuln_id, package)
TTL toDateTime(last_seen) + INTERVAL 90 DAY
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS openlog.host_vulnerabilities ON CLUSTER '{cluster}'
AS openlog.host_vulnerabilities_local
ENGINE = Distributed('{cluster}', openlog, host_vulnerabilities_local, cityHash64(tenant_id, host_id));
