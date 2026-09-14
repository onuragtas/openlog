-- SELECT count(attributes['http.route']) FROM Log WHERE attributes['http.status'] >= 500 AND resource.k8s.namespace.name IN ('prod', 'staging') AND http.method = 'GET' FACET resource['cloud.region']
-- kind=facets table=logs rollup=false bucket=0s limit=10 from=2026-09-14T11:00:00Z to=2026-09-14T12:00:00Z
SELECT toString(resource_attributes[{p6:String}]) AS f0, CAST(countIf(mapContains(attributes, {p7:String})) AS Nullable(Float64)) AS a0 FROM `openlog`.logs
WHERE (tenant_id = {tenant_id:String}) AND (timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})) AND (((toFloat64OrNull(attributes[{p0:String}]) >= {p1:Float64}) AND ((has({p2:Array(String)}, resource_attributes[{p3:String}])))) AND (attributes[{p4:String}] = {p5:String})) GROUP BY f0 ORDER BY a0 DESC, f0 LIMIT 11
-- p0 = http.status
-- p1 = 500
-- p2 = ['prod','staging']
-- p3 = k8s.namespace.name
-- p4 = http.method
-- p5 = GET
-- p6 = cloud.region
-- p7 = http.route
-- t_from = 1789383600000000000
-- t_to = 1789387200000000000
-- tenant_id = tenant-a
-- column "count(attributes['http.route'])" count number zero=true
