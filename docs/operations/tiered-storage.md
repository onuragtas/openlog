# Tiered storage: local disk → (warm disk) → S3

Old telemetry moves from the ClickHouse data disk to cheaper storage while staying queryable through the same tables
and API. Retention (when data is deleted) does not change. Decisions: D-066 (design), D-067 (TTL ownership).

```
insert ──► volume `default` (hot, local SSD) ──TTL age──► volume `warm` (optional local disk) ──TTL age──► volume `cold` (S3 + local read cache)
                                                                                                         └─ delete at the retention
```

## How it works

- **ClickHouse server configuration** defines the storage policy `openlog_tiered`
  ([storage-tiered.xml](../../deploy/compose/clickhouse/storage-tiered.xml)):
  - volume `default`: disk `default` (`/var/lib/clickhouse`), where every insert lands;
  - volume `warm` (optional, [storage-tiered-warm.xml](../../deploy/compose/clickhouse/storage-tiered-warm.xml)): a
    second local disk;
  - volume `cold`: disk `openlog_s3` (`object_storage`, `s3`, metadata on the local disk) read through the filesystem
    cache disk `openlog_s3_cache` (bounded by `OPENLOG_S3_CACHE_MAX_SIZE`).
- **openlog-migrate** (and `openlog-allinone` with `OPENLOG_MIGRATE_ON_START`), after the schema migrations, when
  `OPENLOG_STORAGE_TIERING_ENABLED=true`:
  1. checks that **every replica** defines the policy with volumes `default`, `cold` (and `warm` if a warm age is set);
  2. `ALTER TABLE openlog.<table>_local ON CLUSTER … MODIFY SETTING storage_policy = 'openlog_tiered',
     materialize_ttl_recalculate_only = 1` for each table that gets a move and is not on the policy yet on all replicas;
  3. `ALTER TABLE … ON CLUSTER … MODIFY TTL <time> + INTERVAL <warm> DAY TO VOLUME 'warm', <time> + INTERVAL <cold> DAY
     TO VOLUME 'cold', <time> + INTERVAL <retention> DAY` and records the applied TTL in `openlog.table_settings`
     (`ttl:<table>`).

  ClickHouse then recomputes the TTL info of existing parts (`MATERIALIZE TTL`, recalculation only — parts are not
  rewritten) and the background mover moves parts whose newest row is older than the age. Every step is idempotent:
  a second run changes nothing, a failed run is completed by the next one. `openlog-migrate -plan` prints the pending
  ALTERs.
- **Inserts never write to S3**: the volumes have `perform_ttl_move_on_insert=false`, so rows older than the move age
  (agent backlog) land on the hot volume and move later.
- **Reads** are unchanged: Distributed tables and the API read parts wherever they are; cold parts are fetched from S3
  in ranges and kept in the cache.
- **Deletes** at the retention remove cold parts from S3 as well (whole parts for tables with `ttl_only_drop_parts`, at
  most `merge_with_ttl_timeout` — 4 h by default — after they expire).
- **Hot disk safety valve**: `move_factor 0.1` moves the largest parts to the next volume early when the hot volume has
  less than 10 % free space.

### Table classes

| Class | Tables (`_local`) | Retention | Move to S3 (default) | Variables |
|---|---|---|---|---|
| `metrics` | `metrics` | 30 d | 7 d | `OPENLOG_STORAGE_COLD_AFTER_DAYS_METRICS`, `…_WARM_AFTER_DAYS_METRICS` |
| `metrics_1m` | `metrics_1m` | 395 d | 30 d | `…_METRICS_1M` |
| `logs` | `logs` | 14 d | 3 d | `…_LOGS` |
| `traces` | `spans`, `trace_index` | 7 d | 3 d | `…_TRACES` |
| `apm` | `apm_transactions_1m`, `apm_service_edges_1m`, `apm_service_links_1m`, `apm_db_queries_1m`, `apm_errors_1m`, `apm_error_groups`, `apm_services`, `apm_service_hosts` | `OPENLOG_APM_RETENTION_DAYS` (30) | 7 d | `…_APM` |
| `alerts` | `alert_evaluations` | 30 d | 7 d | `…_ALERTS` |

A move age of `0`, or one not below the retention, leaves the move out for that class (and the class's tables keep the
`default` policy). Small entity tables and queues (`hosts`, `inventory_*`, `containers`, `apm_service_containers`,
`apm_relink_queue`, `apm_error_group_dims`, `apm_service_versions_1m`, `table_settings`) stay on the local disk.

## Design choices (D-066)

- **`s3` object storage disk with local metadata + filesystem cache**, not `s3_plain_rewritable`: the plain disks keep
  metadata in the bucket and are meant for single-writer / shared-catalog setups; they do not support every MergeTree
  operation (hard links used by mutations and moves) that the telemetry tables need. With local metadata a part on S3
  behaves like a local part for merges, mutations, TTL and replication.
- **Zero-copy replication stays off** (`allow_remote_fs_zero_copy_replication = 0`, the ClickHouse default). Every
  replica uploads and owns its own copy under its own prefix (`…/{shard}/{replica}/`). This multiplies S3 bytes by the
  replica count, but zero-copy replication is not production-ready upstream (known data-loss and orphaned-object bugs
  around merges, mutations and replica loss) and would couple replicas' lifecycles through Keeper locks.
- **Moves only through TTL**, per table class, independent of retention; openlog owns the whole TTL expression
  (retention + moves) so the two never overwrite each other (D-067).
- **`skip_access_check=true`**: a server starts while S3 is unreachable (reads of cold parts fail, see below). Wrong
  credentials therefore show up as failed moves, not as a start-up failure — check `openlog-admin storage status`
  after enabling.

## Enable it

### Docker Compose (`deploy/compose`)

1. `.env`:
   ```sh
   OPENLOG_CLICKHOUSE_STORAGE_CONFIG=./clickhouse/storage-tiered.xml
   OPENLOG_STORAGE_TIERING_ENABLED=true
   OPENLOG_S3_ENDPOINT=https://my-bucket.s3.eu-central-1.amazonaws.com/openlog/ch-1/
   OPENLOG_S3_REGION=eu-central-1
   OPENLOG_S3_ACCESS_KEY_ID=AKIA...            # or empty + OPENLOG_S3_USE_ENVIRONMENT_CREDENTIALS=true (instance profile)
   OPENLOG_S3_SECRET_ACCESS_KEY=...
   OPENLOG_S3_CACHE_MAX_SIZE=20Gi
   # optional: OPENLOG_STORAGE_COLD_AFTER_DAYS_LOGS=5 ...
   ```
   Local test instead of a bucket: `COMPOSE_PROFILES=tiered` starts MinIO (bucket `openlog-cold`) and the S3 defaults
   point at it.
2. `docker compose up -d --wait` (recreates `clickhouse` with the new config and `openlog`, whose migrations apply the
   moves). Check: `docker compose exec openlog openlog-admin storage status`.

Credentials from a file: copy `clickhouse/storage-tiered-credentials.xml.example`, fill in, `chmod 0640`, set
`OPENLOG_CLICKHOUSE_S3_CREDENTIALS_FILE=/path/to/it` (mounted as `config.d/zz-openlog-s3-credentials.xml`, which must
sort after `openlog-storage.xml`). Warm volume: `OPENLOG_CLICKHOUSE_STORAGE_WARM_CONFIG=./clickhouse/storage-tiered-warm.xml`
(Docker volume `clickhouse-warm`; bind-mount another disk there) and `OPENLOG_STORAGE_WARM_AFTER_DAYS_<CLASS>`.

### Helm, `dependencies.mode: operators`

```yaml
clickhouse:
  tieredStorage:
    enabled: true
    coldAfterDays: {logs: 3, traces: 3, metrics: 7}
    s3:
      endpoint: https://my-bucket.s3.eu-central-1.amazonaws.com/openlog/{shard}/{replica}/
      region: eu-central-1
      credentialsSecret: {name: openlog-s3}          # keys access-key-id, secret-access-key
      # IRSA instead: credentialsSecret.name "", useEnvironmentCredentials: true, serviceAccountName: clickhouse-s3
      cacheMaxSize: 50Gi
      diskSettings: {}                                # e.g. server_side_encryption_kms_key_id
    warm: {enabled: false}
```

The chart adds the storage policy to the ClickHouseInstallation (`config.d/openlog-storage.xml`, values through the
container environment), the credentials from the Secret, the cache on the data volume (size the PVC for it) and, with
`warm.enabled`, a second PVC per replica. The operator restarts the ClickHouse pods for the new configuration.

Enabling on an existing release takes **two** `helm upgrade`s: the migrate Job is a pre-upgrade hook and runs before the
ClickHouseInstallation receives the policy, so on that first upgrade the chart keeps `OPENLOG_STORAGE_TIERING_ENABLED=false`
for migrate (it looks up the live CHI). Wait until `kubectl get chi` shows `Completed`, then run the same `helm upgrade`
again to apply the moves. Before this was handled, the migrate hook failed with `storage policy "openlog_tiered" is not
usable` until its deadline and the CHI change never got applied (kind, 2026-09-14). A fresh install (post-install hook)
applies everything in one go. In-cluster MinIO instead of a bucket: `s3.endpoint: http://minio.<ns>.svc:9000/<bucket>/{shard}/{replica}/`,
`region: us-east-1` (script `deploy/helm/test/kind-validate.sh`, step `tiered`; not yet validated end to end). `openlog-admin storage status` runs in any api pod:
`kubectl exec deploy/<release>-api -- openlog-admin storage status`.

### External ClickHouse (Helm `external` mode, own servers)

Put `storage-tiered.xml` (and optionally `storage-tiered-warm.xml`) into `config.d/` of **every** server, with the
`OPENLOG_S3_*` / `OPENLOG_WARM_PATH` variables in the server's environment (or replace the `from_env` attributes with
values), use `{shard}/{replica}` macros in the endpoint, restart the servers one by one, then set
`clickhouse.tieredStorage.enabled: true` (or `OPENLOG_STORAGE_TIERING_ENABLED=true` for openlog-migrate). The first
volume **must be named `default` and contain the disk `default`**: ClickHouse refuses to change a table's policy to one
that lacks a volume of its current (`default`) policy (`New storage policy … shall contain volumes of the old storage
policy`). openlog-migrate stops with `storage policy "openlog_tiered" is not usable … <host>: policy not defined` when a
replica is missing the configuration.

## Change or disable

- **Other ages**: change the variables and run migrate; only the changed tables are altered. Parts already on S3 are
  not moved back when an age grows.
- **Disable** (`OPENLOG_STORAGE_TIERING_ENABLED=false`): migrate removes the move clauses; the tables **keep** the
  policy (ClickHouse cannot switch back to `default` while parts may be on S3) and parts on S3 stay readable there
  until they expire. To bring data back to local disks (needs the space):
  `ALTER TABLE openlog.logs_local ON CLUSTER openlog MOVE PARTITION '2026-09-10' TO VOLUME 'default'`. Keep the S3
  configuration on the servers as long as any part is on `cold` (`openlog-admin storage status`).
- **APM retention** (`OPENLOG_APM_RETENTION_DAYS`) is applied by the same step and keeps the moves.

## Observe

`openlog-admin storage status [--json]` (OPENLOG_CLICKHOUSE_* writer credentials, OPENLOG_STORAGE_* for the pending
changes) reads all replicas:

```
cluster openlog, tiering enabled, storage policy openlog_tiered: default[default] -> warm[openlog_warm] -> cold[openlog_s3]

HOST        DISK              TYPE           REMOTE  BROKEN  FREE      TOTAL     CACHE
clickhouse  default           Local          false   false   37.4 GiB  87.1 GiB
clickhouse  openlog_s3        ObjectStorage  true    false   -         -
clickhouse  openlog_s3_cache  ObjectStorage  true    false   -         -         /var/lib/clickhouse/openlog_s3_cache/
clickhouse  openlog_warm      Local          false   false   37.4 GiB  87.1 GiB

TABLE       CLASS  VOLUME   DISK          REPLICAS  PARTS  ROWS  SIZE      PARTITIONS              OVERDUE MOVES  POLICY
logs_local  logs   default  default       1         2      525   4.7 KiB   2026-09-13..2026-09-14  -              openlog_tiered
logs_local  logs   cold     openlog_s3    1         7      3467  16.9 KiB  2026-09-04..2026-09-10  -              openlog_tiered
logs_local  logs   warm     openlog_warm  1         2      1008  4.9 KiB   2026-09-11..2026-09-12  -              openlog_tiered

moves in progress: 0, parts past their move TTL still on the hot volume: 0
failed moves (24h): 0
detached parts: 0
pending openlog-migrate changes: none
```

(Compose stack with `COMPOSE_PROFILES=tiered`, warm volume, `OPENLOG_STORAGE_WARM_AFTER_DAYS_LOGS=1`, cold after 5 days.)

- **SIZE** is summed over the replicas (bytes on S3 are billed once per replica).
- **OVERDUE MOVES**: parts on the hot volume whose move TTL has expired. A growing number means moves fail (see
  *failed moves*, from `system.part_log`) or the mover is behind (`background_move_pool_size`, default 8).
- ClickHouse metrics for dashboards (`system.metrics` / `system.events`, exported by the ClickHouse Prometheus endpoint):
  `FilesystemCacheSize`, `CachedReadBufferReadFromCacheBytes` / `…FromSourceBytes` (cache hit ratio), `S3ReadRequestsErrors`,
  `S3WriteRequestsErrors`, `DiskS3PutObject`, `BackgroundMovePoolTask`; `system.moves` for running moves.

## Cost

- **Storage**: compressed part bytes × replicas (no zero-copy). Metrics and logs compress 5–15× in ClickHouse, so S3
  holds a small fraction of the raw volume.
- **Requests**: every part is several objects (one per column file + small metadata files; the test's ~0.9 MiB per
  replica were 634 objects). Moves and merges on the cold volume issue PUTs; reads issue ranged GETs (reduced by the
  cache); deletes are free on AWS. Prefer fewer, larger parts: keep the processor batching defaults.
- **Merges on the cold volume** download and re-upload parts. Parts of daily partitions are usually fully merged before
  they reach the move age.
- **Bucket settings**: no lifecycle expiration or transition (ClickHouse deletes objects itself; Glacier/Deep Archive
  objects cannot be read, S3 Standard-IA charges a 30-day minimum and retrieval); versioning off, or a short
  noncurrent-version expiration, otherwise deleted parts keep costing.
- **Cache**: `OPENLOG_S3_CACHE_MAX_SIZE` on local disk (evicted LRU); size it for the data dashboards and alerts read
  beyond the move age.

## Backups and restore

- A **disk or volume snapshot** of `/var/lib/clickhouse` contains only the *references* to cold parts. It restores
  correctly only while the referenced objects still exist; after merges or TTL deletes removed them, restored cold parts
  are broken. Do not rely on disk snapshots alone once tiering is on.
- Use **`BACKUP … ON CLUSTER … TO S3(…)`** (or `clickhouse-backup` with object-disk support): it copies the data of
  cold parts too (server-side copy when source and target are on the same S3 service), one copy per shard.
- Rehearsed with `TIEREDTEST_BACKUP=1 make tieredtest` (2 shards × 2 replicas, MinIO, parts on `default`, `warm` and
  `cold`): the test phase backs up 132 cold parts in 2.8 s and restores in 3.3 s; a manual run on a stack under heavy host
  load (≈370 000 rows, 146 of 328 parts on S3) took 37 s and 63 s:

  ```sql
  -- a separate bucket or prefix, never the data prefix of a replica
  BACKUP DATABASE openlog ON CLUSTER 'openlog'
    TO S3('https://backup-bucket.s3.eu-central-1.amazonaws.com/openlog/2026-09-14/', '<key id>', '<secret>');
  SELECT name, status, error, total_size FROM system.backups ORDER BY start_time DESC LIMIT 1;   -- BACKUP_CREATED

  -- restore (openlog stopped: docker compose stop openlog, or scale api/ingest/processor to 0)
  DROP DATABASE openlog ON CLUSTER 'openlog' SYNC;
  RESTORE DATABASE openlog ON CLUSTER 'openlog'
    FROM S3('https://backup-bucket.s3.eu-central-1.amazonaws.com/openlog/2026-09-14/', '<key id>', '<secret>');
  SYSTEM SYNC REPLICA ON CLUSTER 'openlog' openlog.logs_local;
  ```

  Then check rows per table (`SELECT table, sum(rows) FROM clusterAllReplicas('openlog', system.parts) WHERE database =
  'openlog' AND active AND hostName() IN (<one replica per shard>) GROUP BY table`) against the same query before the
  backup, and `openlog-admin storage status`. That comparison only holds for plain `ReplicatedMergeTree` tables: the
  Summing/Replacing rollups (`usage_signals_1h`, `usage_entities_1d`, `schema_migrations`, `table_settings`) merge
  during the restore and show fewer part rows (1 670 → 1 103 rows in `usage_entities_1d_local` in the test) with the same
  content — compare `sum(items)` / distinct keys instead. Caveats found in the rehearsal:
  - **Restored parts land on the hot `default` volume**, cold ones included; the tables keep the `openlog_tiered` policy and
    their TTL, so the mover sends old parts to `warm`/`cold` again. The hot disks need room for the whole database
    during a restore (or restore table by table).
  - **Keeper paths are fixed** (`/clickhouse/tables/{shard}/openlog/<table>`): `RESTORE` into the running cluster needs the
    database dropped with `SYNC` first; restoring next to it (another database name) collides on those paths. Rehearse
    on a separate cluster with its own Keeper.
  - Right after an S3 outage (the test's MinIO stop and start) `BACKUP … TO S3` kept failing for a while although reads,
    moves and deletes of the data disk had recovered: `Code: 499 … S3_ERROR` with an empty message, when opening the
    backup (`BackupWriterS3::fileExists`) or writing its first metadata objects — three attempts within 23 s failed on an
    idle host; the same statement on the same cluster succeeded minutes later, and every backup without a preceding
    outage succeeded at once. Do not schedule backups right after an S3 incident; retry a failed backup later, check
    `status`/`error` in `system.backups`, and restore only from a backup whose status is `BACKUP_CREATED`. (The test's
    `backup_restore` phase therefore runs before its outage phases.)
  - openlog-migrate's TTL step is a no-op after the restore (`openlog.table_settings` is restored with the tables).
- Never point two clusters (or a restored copy) at the same prefix; restore into a new prefix/bucket. A replica whose
  local disk is lost is rebuilt by fetching from its healthy peer (the fetched parts are uploaded again under its prefix);
  the objects of its old incarnation become orphans — delete that prefix after the replica has re-synced, or give the
  new replica a new prefix.
- PostgreSQL backups (`openlog-updater`, CloudNativePG) are unaffected.

## Failure modes

Measured with `make tieredtest` (MinIO stopped while ClickHouse runs):

| Situation | Effect |
|---|---|
| S3 unavailable, inserts | **Unaffected** (hot volume), including rows older than the move age (270 ms for 1 000 rows in the test) |
| S3 unavailable, queries on hot data only | Unaffected when partition pruning skips cold parts (14 ms in the test) |
| S3 unavailable, queries touching cold parts | Fail with `Code: 499 … S3_ERROR` (`This error happened for S3 disk …`); the API answers `503 storage_unavailable` (`"retryable": true`, `Retry-After`) and the UI shows "archive storage cannot be read right now" instead of a generic error. Connection refused fails within seconds; an unreachable endpoint can block until the S3 client's retries give up (minutes) — API queries are cut by `OPENLOG_API_QUERY_TIMEOUT` (`504 timeout`), alert evaluations by `OPENLOG_ALERT_QUERY_TIMEOUT` (evaluation error, `no data` rules may fire) |
| S3 unavailable, moves / merges of cold parts / TTL deletes of cold parts | Fail and are retried (`failed moves (24h)`); parts stay on the hot volume, which keeps filling — watch *FREE* and *OVERDUE MOVES* |
| ClickHouse restart while S3 is unavailable | The server starts (`skip_access_check`). Empty leftovers of recently dropped parts may be detached as `ignored_*` (`detached parts`); they carry no rows and can be removed with `ALTER TABLE … DROP DETACHED PART '…' SETTINGS allow_drop_detached = 1` |
| S3 back | Reads, moves and deletes resume without intervention; row counts and both replicas of every shard were identical after recovery in the test |
| Wrong credentials / bucket policy | Start-up succeeds; moves fail with `Access Denied` (`failed moves`), data stays hot |
| Bucket or prefix deleted | Cold parts are lost for that replica; the other replica of the shard still has its own copy — rebuild the replica (drop its local table data and let it fetch) |

Minimal IAM permissions on the prefix: `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject`, `s3:ListBucket`
(and `kms:GenerateDataKey`, `kms:Decrypt` with SSE-KMS).

## Security

- Static keys: environment (`OPENLOG_S3_ACCESS_KEY_ID` / `_SECRET_ACCESS_KEY`), a credentials XML file, or a Kubernetes
  Secret (Helm). ClickHouse writes the merged configuration including keys to
  `/var/lib/clickhouse/preprocessed_configs/config.xml` (mode 0640, user clickhouse).
- IAM roles: `OPENLOG_S3_USE_ENVIRONMENT_CREDENTIALS=true` with empty keys uses the AWS default chain (environment
  `AWS_*`, web identity / IRSA, EKS Pod Identity, instance profile).
- Encryption: bucket default encryption (SSE-S3) needs nothing; SSE-KMS / SSE-C:
  [storage-tiered-sse.xml.example](../../deploy/compose/clickhouse/storage-tiered-sse.xml.example) (Helm:
  `tieredStorage.s3.diskSettings`). Use HTTPS endpoints.

## Test

`make tieredtest` (`test/e2e/tieredtest`, compose project `openlog-tiered`, ~6 min): MinIO and a 2-shard × 2-replica
cluster using the unchanged `storage-tiered.xml` + `storage-tiered-warm.xml`, each replica with a different credential
source (environment keys, `use_environment_credentials` with `AWS_*`, credentials file, no region). It applies the
schema, inserts old-timestamped metrics, logs, spans and APM rows through the Distributed tables and checks: tiering
disabled is a no-op; enabling switches exactly the managed tables on every replica and a second run changes nothing;
parts reach `warm` and `cold` on all four replicas and objects appear under every `{shard}/{replica}` prefix; counts
through Distributed tables are unchanged after dropping the cache and after restarting all servers; lowering the APM
retention deletes cold parts; an S3 outage (inserts, hot and cold queries, a server restart) and recovery; disabling
removes the moves and keeps the data. `TIEREDTEST_KEEP=1` leaves the stack running. `TIEREDTEST_BACKUP=1` adds the
`backup_restore` phase before disabling ([Backups and restore](#backups-and-restore)): `BACKUP DATABASE openlog ON CLUSTER
… TO S3` into the MinIO bucket `openlog-backup` (retried after the outage phases), `DROP DATABASE … SYNC`, `RESTORE`,
then part rows of the plain tables and logical checks of the Summing/Replacing rollups, the Distributed counts, the moves
back to `warm`/`cold` and a no-op TTL step.
