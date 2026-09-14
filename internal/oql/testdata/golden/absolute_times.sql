-- SELECT count(*) FROM Span SINCE '2026-09-14 08:00:00' UNTIL 1789380000000 TIMESERIES 30 minutes
-- kind=timeseries table=spans rollup=false bucket=30m0s limit=10 from=2026-09-14T08:00:00Z to=2026-09-14T10:00:00Z
SELECT toInt64(intDiv(toUnixTimestamp64Milli(timestamp), {b_ms:Int64}) * {b_ms:Int64}) AS bk, CAST(count() AS Nullable(Float64)) AS a0 FROM `openlog`.spans
WHERE (tenant_id = {tenant_id:String}) AND (timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})) GROUP BY bk ORDER BY bk LIMIT 100001
-- b_ms = 1800000
-- t_from = 1789372800000000000
-- t_to = 1789380000000000000
-- tenant_id = tenant-a
-- column "count(*)" count number zero=true
