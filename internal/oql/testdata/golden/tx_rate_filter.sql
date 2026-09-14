-- SELECT rate(count(*), 1 minute), filter(count(*), WHERE http.status_code >= 500) AS errors, filter(percentile(duration.ms, 95), WHERE http.status_code < 500) FROM Transaction TIMESERIES AUTO SINCE 6 hours ago
-- kind=timeseries table=spans rollup=false bucket=5m0s limit=10 from=2026-09-14T06:00:00Z to=2026-09-14T12:00:00Z
SELECT toInt64(intDiv(toUnixTimestamp64Milli(timestamp), {b_ms:Int64}) * {b_ms:Int64}) AS bk, CAST((count()) * {p0:Float64} AS Nullable(Float64)) AS a0, CAST(countIf(http_status_code >= {p1:Float64}) AS Nullable(Float64)) AS a1, CAST(quantileIf(0.95)(duration_ns / 1e6, http_status_code < {p2:Float64}) AS Nullable(Float64)) AS a2 FROM `openlog`.spans
WHERE (tenant_id = {tenant_id:String}) AND (timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})) AND (is_entry) GROUP BY bk ORDER BY bk LIMIT 100001
-- b_ms = 300000
-- p0 = 0.2
-- p1 = 500
-- p2 = 500
-- t_from = 1789365600000000000
-- t_to = 1789387200000000000
-- tenant_id = tenant-a
-- column "rate(count(*), 1 minute)" rate number zero=true
-- column "errors" filter number zero=true
-- column "filter(percentile(duration.ms, 95), WHERE http.status_code < 500)" filter number zero=false
