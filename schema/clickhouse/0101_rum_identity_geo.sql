-- openlog:phase expand
-- 0101_rum_identity_geo: find a person's sessions without reading every span of the tenant (rum.md §3.7).
--
-- `user.id` and `geo.country.iso_code` are span attributes, not columns of the session rollup, and that is a
-- deliberate limit rather than an oversight. `rum_sessions_local` is written by a materialized view, and a
-- view cannot be altered: adding a column to the rollup would mean dropping and recreating the view, which
-- no migration here is allowed to do (the shape test permits only CREATE and idempotent ADD COLUMN/ADD
-- INDEX). Pointing a second view at the same target would count every row twice. So identity and country
-- are answered from `spans`, and the consequence is stated plainly: **these reads reach back only as far as
-- the 7-day trace retention, not the 30 days of the session rollup.** Carrying identity for thirty days
-- needs its own target table, which is a decision about retaining personal data and deserves to be made on
-- purpose rather than arrived at through a migration.
--
-- `spans` is ordered by (tenant_id, …, timestamp), so filtering on a map attribute reads every span of the
-- tenant in the range. This bloom filter makes a user id a cheap skip, exactly as 0094_rum did for
-- `session.id` and 0043_k8s_logs_indexes for `k8s.pod.uid`.

ALTER TABLE openlog.spans_local ON CLUSTER '{cluster}'
    ADD INDEX IF NOT EXISTS idx_rum_user attributes['user.id'] TYPE bloom_filter(0.01) GRANULARITY 4;

-- No index for the country, on purpose. It holds about two hundred distinct values, so nearly every granule
-- contains nearly every country and the filter would skip almost nothing — an index with the write cost of
-- a useful one and none of the benefit. A country read is bounded by the application and the time range it
-- is always combined with.
