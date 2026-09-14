-- SELECT rate(sum(value), 1 minute), filter(average(value), WHERE host.id = 'h1') FROM Metric WHERE metricName IN ('a', 'b') SINCE 2 days ago
-- kind=single table=metrics_1m rollup=true bucket=0s limit=10 from=2026-09-12T12:00:00Z to=2026-09-14T12:00:00Z
SELECT CAST((sum(value_sum)) * {p1:Float64} AS Nullable(Float64)) AS a0, CAST(sumIf(value_sum, host_id = {p2:String}) / sumIf(value_count, host_id = {p2:String}) AS Nullable(Float64)) AS a1 FROM `openlog`.metrics_1m
WHERE (tenant_id = {tenant_id:String}) AND (timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})) AND ((has({p0:Array(String)}, metric_name)))
-- p0 = ['a','b']
-- p1 = 0.00034722222222222224
-- p2 = h1
-- t_from = 1789214400000000000
-- t_to = 1789387200000000000
-- tenant_id = tenant-a
-- column "rate(sum(value), 1 minute)" rate number zero=true
-- column "filter(average(value), WHERE host.id = 'h1')" filter number zero=false
