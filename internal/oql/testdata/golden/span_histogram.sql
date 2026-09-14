-- SELECT histogram(duration.ms, 1000, 20) FROM Span WHERE kind = 'server'
-- kind=histogram table=spans rollup=false bucket=0s limit=10 from=2026-09-14T11:00:00Z to=2026-09-14T12:00:00Z
SELECT toInt64(assumeNotNull(floor(duration_ns / 1e6 / {p2:Float64}))) AS hb, CAST(count() AS Nullable(Float64)) AS a0 FROM `openlog`.spans
WHERE (tenant_id = {tenant_id:String}) AND (timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})) AND (toString(kind) = {p0:String}) AND (duration_ns / 1e6 >= 0 AND duration_ns / 1e6 < {p1:Float64}) GROUP BY hb ORDER BY hb
-- p0 = server
-- p1 = 1000
-- p2 = 50
-- t_from = 1789383600000000000
-- t_to = 1789387200000000000
-- tenant_id = tenant-a
-- column "histogram(duration.ms, 1000, 20)" histogram number zero=true
