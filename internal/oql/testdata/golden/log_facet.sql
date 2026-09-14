-- SELECT count(*) FROM Log WHERE severity = 'ERROR' FACET service.name SINCE 3 hours ago
-- kind=facets table=logs rollup=false bucket=0s limit=10 from=2026-09-14T09:00:00Z to=2026-09-14T12:00:00Z
SELECT toString(service_name) AS f0, CAST(count() AS Nullable(Float64)) AS a0 FROM `openlog`.logs
WHERE (tenant_id = {tenant_id:String}) AND (timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})) AND (severity_text = {p0:String}) GROUP BY f0 ORDER BY a0 DESC, f0 LIMIT 11
-- p0 = ERROR
-- t_from = 1789376400000000000
-- t_to = 1789387200000000000
-- tenant_id = tenant-a
-- column "count(*)" count number zero=true
