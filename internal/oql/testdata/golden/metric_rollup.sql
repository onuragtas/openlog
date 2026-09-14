-- SELECT average(value), min(value), max(value), sum(value), count(*), latest(value), uniqueCount(host.id) FROM Metric WHERE metricName = 'system.cpu.utilization' AND attributes['state'] != 'idle' FACET host.id TIMESERIES SINCE 7 days ago
-- kind=timeseries table=metrics_1m rollup=true bucket=1h0m0s limit=10 from=2026-09-07T12:00:00Z to=2026-09-14T12:00:00Z
SELECT toString(host_id) AS f0, toInt64(intDiv(toInt64(toUnixTimestamp(timestamp)) * 1000, {b_ms:Int64}) * {b_ms:Int64}) AS bk, CAST(sum(value_sum) / sum(value_count) AS Nullable(Float64)) AS a0, CAST(min(value_min) AS Nullable(Float64)) AS a1, CAST(max(value_max) AS Nullable(Float64)) AS a2, CAST(sum(value_sum) AS Nullable(Float64)) AS a3, CAST(sum(value_count) AS Nullable(Float64)) AS a4, CAST(argMaxMerge(value_last) AS Nullable(Float64)) AS a5, CAST(uniqExact(host_id) AS Nullable(Float64)) AS a6 FROM `openlog`.metrics_1m
WHERE (tenant_id = {tenant_id:String}) AND (timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})) AND ((metric_name = {p0:String}) AND (attributes[{p1:String}] != {p2:String})) AND (toString(host_id) GLOBAL IN (SELECT toString(host_id) FROM `openlog`.metrics_1m
WHERE (tenant_id = {tenant_id:String}) AND (timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})) AND ((metric_name = {p0:String}) AND (attributes[{p1:String}] != {p2:String})) GROUP BY toString(host_id) ORDER BY sum(value_sum) / sum(value_count) DESC, toString(host_id) LIMIT 10)) GROUP BY f0, bk ORDER BY bk LIMIT 100001
-- b_ms = 3600000
-- p0 = system.cpu.utilization
-- p1 = state
-- p2 = idle
-- t_from = 1788782400000000000
-- t_to = 1789387200000000000
-- tenant_id = tenant-a
-- column "average(value)" average number zero=false
-- column "min(value)" min number zero=false
-- column "max(value)" max number zero=false
-- column "sum(value)" sum number zero=true
-- column "count(*)" count number zero=true
-- column "latest(value)" latest number zero=false
-- column "uniqueCount(host.id)" uniqueCount number zero=true
