-- SELECT latest(transaction.name), earliest(duration), max(http.status_code) FROM Transaction
-- kind=single table=spans rollup=false bucket=0s limit=10 from=2026-09-14T11:00:00Z to=2026-09-14T12:00:00Z
SELECT toString(argMax(transaction_name, timestamp)) AS a0, CAST(argMin(duration_ns / 1e9, timestamp) AS Nullable(Float64)) AS a1, CAST(max(http_status_code) AS Nullable(Float64)) AS a2 FROM `openlog`.spans
WHERE (tenant_id = {tenant_id:String}) AND (timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})) AND (is_entry)
-- t_from = 1789383600000000000
-- t_to = 1789387200000000000
-- tenant_id = tenant-a
-- column "latest(transaction.name)" latest string zero=false
-- column "earliest(duration)" earliest number zero=false
-- column "max(http.status_code)" max number zero=false
