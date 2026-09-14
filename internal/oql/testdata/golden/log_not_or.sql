-- SELECT count(*) FROM Log WHERE NOT (severity = 'DEBUG' OR severity = 'TRACE') AND service.name NOT IN ('a', 'b') AND message NOT LIKE 'health%'
-- kind=single table=logs rollup=false bucket=0s limit=10 from=2026-09-14T11:00:00Z to=2026-09-14T12:00:00Z
SELECT CAST(count() AS Nullable(Float64)) AS a0 FROM `openlog`.logs
WHERE (tenant_id = {tenant_id:String}) AND (timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})) AND (((NOT ((severity_text = {p0:String}) OR (severity_text = {p1:String}))) AND (NOT (has({p2:Array(String)}, service_name)))) AND (NOT (body LIKE {p3:String})))
-- p0 = DEBUG
-- p1 = TRACE
-- p2 = ['a','b']
-- p3 = health%
-- t_from = 1789383600000000000
-- t_to = 1789387200000000000
-- tenant_id = tenant-a
-- column "count(*)" count number zero=true
