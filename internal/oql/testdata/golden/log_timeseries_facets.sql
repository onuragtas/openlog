-- SELECT count(*), uniqueCount(host.name) FROM Log WHERE message LIKE '%timeout%' FACET service.name, host.name TIMESERIES 5 minutes SINCE 1 day ago LIMIT 5
-- kind=timeseries table=logs rollup=false bucket=5m0s limit=5 from=2026-09-13T12:00:00Z to=2026-09-14T12:00:00Z
SELECT toString(service_name) AS f0, toString(host_name) AS f1, toInt64(intDiv(toUnixTimestamp64Milli(timestamp), {b_ms:Int64}) * {b_ms:Int64}) AS bk, CAST(count() AS Nullable(Float64)) AS a0, CAST(uniqExact(host_name) AS Nullable(Float64)) AS a1 FROM `openlog`.logs
WHERE (tenant_id = {tenant_id:String}) AND (timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})) AND (body LIKE {p0:String}) AND ((toString(service_name), toString(host_name)) GLOBAL IN (SELECT toString(service_name), toString(host_name) FROM `openlog`.logs
WHERE (tenant_id = {tenant_id:String}) AND (timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})) AND (body LIKE {p0:String}) GROUP BY toString(service_name), toString(host_name) ORDER BY count() DESC, toString(service_name), toString(host_name) LIMIT 5)) GROUP BY f0, f1, bk ORDER BY bk LIMIT 100001
-- b_ms = 300000
-- p0 = %timeout%
-- t_from = 1789300800000000000
-- t_to = 1789387200000000000
-- tenant_id = tenant-a
-- column "count(*)" count number zero=true
-- column "uniqueCount(host.name)" uniqueCount number zero=true
