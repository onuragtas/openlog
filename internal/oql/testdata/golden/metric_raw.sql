-- SELECT average(value), max(value) FROM Metric WHERE metricName = 'system.cpu.utilization' FACET host.name TIMESERIES
-- kind=timeseries table=metrics rollup=false bucket=30s limit=10 from=2026-09-14T11:00:00Z to=2026-09-14T12:00:00Z
SELECT toString(host_name) AS f0, toInt64(intDiv(toUnixTimestamp64Milli(timestamp), {b_ms:Int64}) * {b_ms:Int64}) AS bk, CAST(avg(value) AS Nullable(Float64)) AS a0, CAST(max(value) AS Nullable(Float64)) AS a1 FROM `openlog`.metrics
WHERE (tenant_id = {tenant_id:String}) AND (timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})) AND (metric_name = {p0:String}) AND (toString(host_name) GLOBAL IN (SELECT toString(host_name) FROM `openlog`.metrics
WHERE (tenant_id = {tenant_id:String}) AND (timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})) AND (metric_name = {p0:String}) GROUP BY toString(host_name) ORDER BY avg(value) DESC, toString(host_name) LIMIT 10)) GROUP BY f0, bk ORDER BY bk LIMIT 100001
-- b_ms = 30000
-- p0 = system.cpu.utilization
-- t_from = 1789383600000000000
-- t_to = 1789387200000000000
-- tenant_id = tenant-a
-- column "average(value)" average number zero=false
-- column "max(value)" max number zero=false
