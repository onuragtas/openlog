-- SELECT count(*) FROM Span FACET status.code COMPARE WITH 1 week ago
-- kind=facets table=spans rollup=false bucket=0s limit=10 from=2026-09-14T11:00:00Z to=2026-09-14T12:00:00Z
SELECT toString(toString(status_code)) AS f0, CAST(count() AS Nullable(Float64)) AS a0 FROM `openlog`.spans
WHERE (tenant_id = {tenant_id:String}) AND (timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})) GROUP BY f0 ORDER BY a0 DESC, f0 LIMIT 11
-- t_from = 1789383600000000000
-- t_to = 1789387200000000000
-- tenant_id = tenant-a
-- column "count(*)" count number zero=true
