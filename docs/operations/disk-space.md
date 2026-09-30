# Disk space

What openlog does to keep the ClickHouse data disk from filling, and the one action an operator has to take
after upgrading an existing server.

Telemetry retention itself is not described here: every telemetry table carries a TTL owned by `openlog-migrate`
(see [tiered-storage.md](tiered-storage.md) for the classes and the move TTLs). This page is about the space that
is *not* telemetry.

## ClickHouse's own system log tables

ClickHouse writes `system.query_log`, `system.part_log`, `system.trace_log`, `system.metric_log`,
`system.asynchronous_metric_log`, `system.text_log` and `system.query_thread_log` to the same disk as the data,
and by default none of them ever expires. `metric_log` and `asynchronous_metric_log` get a row every second, so
on a quiet installation they outgrow the telemetry they are supposed to help debug.

openlog ships bounds for them:

- Compose: [`deploy/compose/clickhouse/openlog-system-logs.xml`](../../deploy/compose/clickhouse/openlog-system-logs.xml),
  mounted into `config.d/` unconditionally.
- Helm (`dependencies.mode: operators`): the same XML as `config.d/openlog-system-logs.xml` in the
  ClickHouseInstallation.
- Helm `external` mode: nothing is applied — put the same file on your own servers.

`query_log` and `part_log` keep 14 days, the rest 3 days. The first two are longer on purpose: the usage collector
reads tenant query compute out of `system.query_log` into `usage_queries_1h`, and `openlog-admin storage status`
reads failed part moves of the last 24 h out of `system.part_log`. Shortening them below the collection window
loses usage data silently.

Server log *files* are bounded in the same file (`100M` × 3). The official image otherwise keeps up to 10 × 1000M
per log kind, on the data disk.

### Why `partition_by` is set together with `ttl`

A TTL alone is not enough, and getting this wrong is worse than doing nothing. The default partitioning of the
system tables is monthly and ClickHouse expires whole parts, so a TTL shorter than the partition span degrades
into a row-level rewrite of the entire month. On `metric_log`, which has around a thousand columns, that merge
runs out of memory, fails, and retries forever with a core pegged at 100 %.

With daily partitions (`partition_by: event_date`) every expiry is a partition drop instead: no rewrite, no merge
pressure.

`engine` is deliberately not set. It is an alternative to `partition_by`/`ttl`, not a companion, and spelling out
`ORDER BY` per table would pin structure that ClickHouse owns and changes between versions.

## After upgrading an existing server: drop the renamed tables

Changing `partition_by` of a system table that already exists does not rewrite it. ClickHouse logs

```
Existing table system.query_log for system log has obsolete or different structure
```

renames it to `system.query_log_0` and creates a fresh one with the new configuration. **The renamed tables keep
all their rows and no TTL applies to them**, so the space is not reclaimed until you drop them. Do this once,
after the first restart with the new configuration:

```sql
SELECT name, formatReadableSize(total_bytes) AS size
FROM system.tables
WHERE database = 'system' AND match(name, '_log_[0-9]+$')
ORDER BY total_bytes DESC;
```

then, for each name listed:

```sql
DROP TABLE system.query_log_0;
```

Tables with a numeric suffix are always leftovers — of this change or of an earlier ClickHouse upgrade that
changed a system table's columns. Nothing reads them.

## What is using the disk

`openlog-admin storage status` prints free and total space per disk per replica, and bytes per table per volume
with the oldest and newest partition of each. For a quick answer straight from ClickHouse:

```sql
SELECT database, table, formatReadableSize(sum(bytes_on_disk)) AS size
FROM system.parts
WHERE active
GROUP BY database, table
ORDER BY sum(bytes_on_disk) DESC
LIMIT 20;
```
