# Disk space

What openlog does to keep the ClickHouse data disk from filling, and the one action an operator has to take
after upgrading an existing server.

Telemetry retention itself is not described here: every telemetry table carries a TTL owned by `openlog-migrate`
(see [tiered-storage.md](tiered-storage.md) for the classes and the move TTLs). This page is about the space that
is *not* telemetry.

## ClickHouse's own system log tables

ClickHouse writes a dozen log tables of its own to the same disk as the data, and by default none of them ever
expires. `metric_log` and `asynchronous_metric_log` get a row every second; `processors_profile_log` gets one per
query *operator*. On a quiet installation they outgrow the telemetry they are supposed to help debug: one measured
server had 4.2 GiB of `processors_profile_log` and 1.9 GiB of `query_views_log` against 36 KiB of logs and 26 KiB
of spans.

openlog ships bounds for them:

- Compose: [`deploy/compose/clickhouse/openlog-system-logs.xml`](../../deploy/compose/clickhouse/openlog-system-logs.xml),
  mounted into `config.d/` unconditionally.
- Helm (`dependencies.mode: operators`): the same XML as `config.d/openlog-system-logs.xml` in the
  ClickHouseInstallation.
- Helm `external` mode: nothing is applied — put the same file on your own servers.

Twelve tables are bounded: `query_log`, `part_log` and `error_log` keep 14 days, `crash_log` 90, and the volume
ones — `trace_log`, `metric_log`, `asynchronous_metric_log`, `text_log`, `query_thread_log`,
`processors_profile_log`, `query_views_log`, `query_metric_log` — keep 3.

`query_log` and `part_log` are longer on purpose: the usage collector reads tenant query compute out of
`system.query_log` into `usage_queries_1h`, and `openlog-admin storage status` reads failed part moves of the last
24 h out of `system.part_log`. Shortening them below the collection window loses usage data silently. `crash_log`
is tiny and the one table you want history for.

Only tables that ClickHouse enables by default are listed. For some log tables the presence of a config section is
what *enables* them (`text_log` is the known case), so naming one that is off would create volume rather than
bound it.

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

## How full the disks are

The api leader asks ClickHouse every 5 minutes how full its local disks are (`system.disks`, one row per replica)
and publishes the result three ways:

| Where | What |
|---|---|
| `openlog_clickhouse_disk_used_ratio{host,disk}` on `/metrics` | used fraction, 0..1 — the series to alert on |
| `openlog_clickhouse_disk_free_bytes`, `openlog_clickhouse_disk_total_bytes` | the raw numbers behind it |
| `openlog-admin storage status` | a `USED` column per disk |

Only local disks are measured. For an object-storage disk and the cache in front of it ClickHouse reports
`free_space`/`total_space` as a placeholder, so a percentage there would be a lie; those rows print `-`.

The percentage is derived from ClickHouse's `free_space`, which on ext4 excludes the blocks reserved for root.
Those blocks are not available to ClickHouse, so they count as used — otherwise the number would sit a few
percent below the pressure the server actually feels.

A cluster is as full as its fullest replica, never the average: an average hides the one node that is about to
stop accepting parts.

### Thresholds

A disk that reaches **80 %** is logged as a warning and one that reaches **90 %** as an error, with the host, the
disk, the percentage and the free and total bytes. 80 is early enough that shortening retention still works; at
90 a merge can fail for want of scratch space, and a failed merge stops ingest rather than slowing it down.

`openlog_clickhouse_disk_reported_level{host,disk}` carries the level a disk is currently reported at, `0` when
it is under all of them.

Each level is reported **once**, not on every check. A disk has to fall **5 points** below the level it was
reported at before that level is armed again, so a disk drifting either side of 80 % does not report every five
minutes, and a disk that only dipped a point has not recovered. The levels are stored with the snapshot, so a
restart or a change of leader does not report a disk that has not moved.

## Dropping the oldest day when the disk fills

Table TTLs bound how *old* the data gets. They cannot bound how *much* of it there is: double the ingest and the
same retention fills the disk with data that is still inside its TTL, and nothing in ClickHouse will give it up.

Shedding is the answer to that, and it is **off until you turn it on**. It deletes telemetry and cannot be undone.

| Setting | Built-in | What it does |
|---|---|---|
| `shed_enabled` | **off** | Nothing is ever dropped while this is off |
| `shed_start_percent` | 90 | Dropping begins at or above this |
| `shed_stop_percent` | 85 | Dropping stops once the disk is back under this |
| `shed_min_partitions` | 3 | Every table keeps at least this many days, however full the disk is |
| `shed_max_drops_per_run` | 20 | A single round drops no more days than this |

`shed_start_percent` may not be set below the critical reporting level: data must not disappear at a percentage
the same page still calls healthy. The API and the database both refuse it.

### What is given up, in order

Least valuable per byte first, so a disk under pressure loses debugging detail before it loses the record of what
happened:

1. profiles
2. metric exemplars
3. traces — `spans` and `trace_index` **together**, since an index into rows that are gone is worse than no index
4. database session samples
5. logs
6. database query statistics and plans
7. raw metrics

**Never dropped, at any pressure:** `metrics_1m` (the long-term memory, and partitioned by month, so one drop
would take a whole month of it), `usage_*` (billing), the `apm_*` rollups (what survives when the spans they came
from expire), and every inventory, alert, RUM and vulnerability table.

### How it drops

`ALTER TABLE … ON CLUSTER … DROP PARTITION ID '<YYYYMMDD>'`, oldest day first, one day at a time until the
estimate reaches the stop level.

A whole partition rather than rows on purpose. `ALTER … DELETE` is a mutation: it rewrites parts, which needs free
space to write the new ones, so it is at its most expensive exactly when the disk is at its fullest. Dropping a
partition frees the space at once, and every sheddable table is partitioned by day.

### What it leaves behind

Every dropped day is logged as a warning naming the day, the tables and the bytes, counted in
`openlog_clickhouse_disk_days_dropped_total{unit}` and `openlog_clickhouse_disk_dropped_bytes_total`, and written
to `audit_log` as `disk_space.partition_dropped` with the actor `system:disk-space`.

If the disk is past the level and nothing may be given up — every table at its floor, or only protected tables
left — that is logged as an error rather than passed over in silence. At that point only you can act.

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
