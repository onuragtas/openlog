-- SELECT percentile(value, 95) FROM Metric WHERE metricName = 'x' SINCE 2 days ago
-- kind=single table=metrics rollup=false bucket=0s limit=10 from=2026-09-12T12:00:00Z to=2026-09-14T12:00:00Z
SELECT CAST(quantile(0.95)(value) AS Nullable(Float64)) AS a0 FROM `openlog`.metrics
WHERE (tenant_id = {tenant_id:String}) AND (timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})) AND (metric_name = {p0:String})
-- p0 = x
-- t_from = 1789214400000000000
-- t_to = 1789387200000000000
-- tenant_id = tenant-a
-- column "percentile(value, 95)" percentile number zero=false
