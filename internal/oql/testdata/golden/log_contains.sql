-- SELECT count(*) FROM Log WHERE message CONTAINS '50%_OFF' AND attributes['http.route'] NOT CONTAINS 'Health' AND contains = 'x'
-- kind=single table=logs rollup=false bucket=0s limit=10 from=2026-09-14T11:00:00Z to=2026-09-14T12:00:00Z
SELECT CAST(count() AS Nullable(Float64)) AS a0 FROM `openlog`.logs
WHERE (tenant_id = {tenant_id:String}) AND (timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})) AND (((positionCaseInsensitiveUTF8(body, {p0:String}) > 0) AND (NOT (positionCaseInsensitiveUTF8(attributes[{p1:String}], {p2:String}) > 0))) AND (attributes[{p3:String}] = {p4:String}))
-- p0 = 50%_OFF
-- p1 = http.route
-- p2 = Health
-- p3 = contains
-- p4 = x
-- t_from = 1789383600000000000
-- t_to = 1789387200000000000
-- tenant_id = tenant-a
-- column "count(*)" count number zero=true
