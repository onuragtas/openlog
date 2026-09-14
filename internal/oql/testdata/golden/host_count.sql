-- SELECT uniqueCount(host.id) FROM Host FACET os.type, resource.cloud.provider
-- kind=facets table=hosts rollup=false bucket=0s limit=10 from=2026-09-14T11:00:00Z to=2026-09-14T12:00:00Z
SELECT toString(os_type) AS f0, toString(resource_attributes[{p0:String}]) AS f1, CAST(uniqExact(host_id) AS Nullable(Float64)) AS a0 FROM `openlog`.hosts FINAL
WHERE (tenant_id = {tenant_id:String}) AND (last_seen >= fromUnixTimestamp64Nano({t_from:Int64}) AND last_seen < fromUnixTimestamp64Nano({t_to:Int64})) GROUP BY f0, f1 ORDER BY a0 DESC, f0, f1 LIMIT 11
-- p0 = cloud.provider
-- t_from = 1789383600000000000
-- t_to = 1789387200000000000
-- tenant_id = tenant-a
-- column "uniqueCount(host.id)" uniqueCount number zero=true
