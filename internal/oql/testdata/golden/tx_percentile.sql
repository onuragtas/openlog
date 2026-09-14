-- SELECT percentile(duration.ms, 50, 95, 99), average(duration.ms) AS 'avg ms' FROM Transaction WHERE service.name = 'checkout' AND error = false FACET transaction.name
-- kind=facets table=spans rollup=false bucket=0s limit=10 from=2026-09-14T11:00:00Z to=2026-09-14T12:00:00Z
SELECT toString(transaction_name) AS f0, CAST(quantile(0.5)(duration_ns / 1e6) AS Nullable(Float64)) AS a0, CAST(quantile(0.95)(duration_ns / 1e6) AS Nullable(Float64)) AS a1, CAST(quantile(0.99)(duration_ns / 1e6) AS Nullable(Float64)) AS a2, CAST(avg(duration_ns / 1e6) AS Nullable(Float64)) AS a3 FROM `openlog`.spans
WHERE (tenant_id = {tenant_id:String}) AND (timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})) AND (is_entry) AND ((service_name = {p0:String}) AND (is_error = {p1:Bool})) GROUP BY f0 ORDER BY a0 DESC, f0 LIMIT 11
-- p0 = checkout
-- p1 = false
-- t_from = 1789383600000000000
-- t_to = 1789387200000000000
-- tenant_id = tenant-a
-- column "percentile(duration.ms, 50)" percentile number zero=false
-- column "percentile(duration.ms, 95)" percentile number zero=false
-- column "percentile(duration.ms, 99)" percentile number zero=false
-- column "avg ms" average number zero=false
