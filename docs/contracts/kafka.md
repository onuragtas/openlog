# Contract: Kafka (v1)

Producer: `openlog-ingest`. Consumer: `openlog-processor` (consumer group `openlog-processor` by default).

## Topics

`<prefix>` defaults to `openlog` (`OPENLOG_KAFKA_TOPIC_PREFIX`).

| Topic | Value | Partition key |
|---|---|---|
| `<prefix>.otlp.metrics.v1` | protobuf `ExportMetricsServiceRequest` | `<tenant_id>/<host.id>` of the first resource having `host.id`, else `<tenant_id>/<service.name>`, else `<tenant_id>/` |
| `<prefix>.otlp.logs.v1` | protobuf `ExportLogsServiceRequest` | same rule as metrics |
| `<prefix>.otlp.traces.v1` | protobuf `ExportTraceServiceRequest` | `<tenant_id>/<hex trace_id of the first span>`; with tail sampling enabled one record **per trace id** (`<tenant_id>/<hex trace_id>`) |
| `<prefix>.otlp.traces.sampled.v1` | protobuf `ExportTraceServiceRequest`: the kept spans of one trace (or of its late spans), tracestate `ot=th` rewritten (apm.md §4.2) | `<tenant_id>/<hex trace_id>` |
| `<prefix>.otlp.profiles.v1` | protobuf `ExportProfilesServiceRequest` (OTLP profiles, still `v1development` upstream; produced byte-for-byte as it arrived, never re-encoded by openlog) | `<tenant_id>/<service.name>` of the first resource having one, else `<tenant_id>/` |

**Tail sampling (D-075, D-076).** The sampled topic is created with the same settings as the others, whether or not tail sampling is enabled. With `OPENLOG_TAILSAMPLING_ENABLED=true`:
- ingest splits trace export requests by trace id, so all spans of a trace go to one partition;
- `openlog-sampler` (consumer group `openlog-sampler`, `OPENLOG_TAILSAMPLING_GROUP`) consumes `<prefix>.otlp.traces.v1` and produces to `<prefix>.otlp.traces.sampled.v1` with the same four headers (tenant, schema version, the first record's receive time and request id). It commits a raw partition only up to the oldest record that still has a buffered span or an unproduced kept span. Partitions it gives up in a rebalance are decided, produced and committed inside the revoke callback. Delivery is at-least-once; sampler crash, commit failure or lost partitions can duplicate kept spans, and processor dedup tokens don't cover these duplicates because the tokens are per sampled-topic offset range. See docs/operations/tail-sampling.md;
- the processor consumes `<prefix>.otlp.traces.sampled.v1` instead of `<prefix>.otlp.traces.v1` (the chunk, token and commit rules below apply to it unchanged).

Keying metrics/logs by host keeps inventory snapshot items and their snapshot-complete record in order within one partition.

| Setting | `single` profile | `cluster` profile |
|---|---|---|
| Partitions | 6 | 48 (scale with processor replicas) |
| Replication factor | 1 | 3 |
| `min.insync.replicas` | 1 | 2 |
| `retention.ms` | 86400000 (24h) | 86400000 |
| `max.message.bytes` | 12582912 (12 MiB) | 12582912 |

Topics are created by `openlog-migrate` (or by the Helm chart's Strimzi `KafkaTopic` resources) — services never auto-create topics. `openlog-allinone` runs the same migration (topics included) when `OPENLOG_MIGRATE_ON_START=true`, unless `OPENLOG_MIGRATE_SKIP_KAFKA=true`. Topic settings come from the `OPENLOG_KAFKA_*` variables in [config.md](config.md).

## Message

- **Value:** the OTLP export request serialized as protobuf, **uncompressed** (the Kafka producer applies `zstd` batch compression). Ingest converts OTLP/JSON to protobuf before producing.
- **Headers:**

| Header | Value |
|---|---|
| `openlog-schema-version` | `1` |
| `openlog-tenant-id` | tenant id resolved from the license key |
| `openlog-received-at` | ingest receive time, unix nanoseconds, decimal string |
| `openlog-request-id` | unique id per ingest request |

- Producer settings: `acks=all`, idempotent producer enabled, compression `zstd`.
- Ingest returns success to the client **only after** the produce is acknowledged.

## Consumption semantics

- At-least-once. Offsets are committed only after all rows derived from those records are inserted into ClickHouse.
- **Chunks.** The processor groups records per partition into *chunks*: contiguous offset ranges. A chunk is closed
  1. before a record whose timestamp window `floor(record timestamp / OPENLOG_PROCESSOR_FLUSH_INTERVAL)` is **later** than the chunk's window (a record with an earlier window, e.g. from an ingest pod with a lagging clock, joins the open chunk) — `window`;
  2. after the record that brings the chunk's uncompressed record bytes to `OPENLOG_PROCESSOR_BATCH_BYTES` — `bytes`;
  3. when the partition is caught up to its high watermark and the window end plus half a flush interval has passed (or the chunk has been open for 2.5 flush intervals, for producer clocks ahead of the processor) — `idle`;
  4. when all buffered record bytes of the processor reach `OPENLOG_PROCESSOR_MAX_BUFFERED_BYTES` — `memory`;
  5. on shutdown — `shutdown`.

  The record timestamp is the producer's CreateTime, set by ingest when it produces. Rules 1 and 2 depend only on the records, so a range that is delivered again (to the same or another group member) is cut into the same chunks. `openlog_processor_chunk_cuts_total{reason}` counts cuts per rule.
- Each closed chunk is inserted table by table (hosts last); for every partition the offset after the last chunk whose tables are all inserted is committed. Chunks are never combined across partitions, so each insert block has exactly one owner partition.
- **Deduplication token** per block: `<topic>:<partition>:<first_offset>-<last_offset>:<table>`.
  - If a table's rows of one chunk exceed `OPENLOG_PROCESSOR_BATCH_ROWS`, they are split in order into blocks of that size and the token gets `#<block_index>/<batch_rows>`.
  - `OPENLOG_PROCESSOR_INSERT_MODE=direct` inserts one block per shard into `<table>_local` and appends `:<shard_num>/<layout>`, where `<layout>` is 8 hex digits (FNV-1a 32) of the shard numbers and weights. Deduplication block ids live in Keeper per shard, so a retry on another replica of the same shard is deduplicated.
  - Inserts set `insert_deduplicate=1` and `deduplicate_blocks_in_dependent_materialized_views=1`. Without the latter a retried block that the source table drops is still added to `metrics_1m` and `trace_index` again (verified on ClickHouse 25.8, both insert modes).
- **Rebalances.** A rebalance is allowed whenever no closed chunk is waiting to be inserted. Records buffered for a partition that is revoked or lost are dropped without inserting or committing them; the new owner reads from the committed offset and cuts the same chunks.
- **Remaining duplicate cases.** Duplicates need a range whose rows were inserted but whose offsets were not committed (crash or kill between insert and commit, a commit that failed, a member that lost its partitions while retrying) **and** one of:
  1. the inserted chunk was cut by rule 3, 4 or 5 and the re-delivered range is cut elsewhere (`idle`: a record of the same window reached the partition more than half a flush interval after the window ended; `memory`: buffer limit; `shutdown`: the final flush inserted but its commit was lost);
  2. the re-delivery happens after ClickHouse forgot the token: a table keeps the last `replicated_deduplication_window` block ids per shard (1000 by default on 25.8, and at most `replicated_deduplication_window_seconds` = 7 days). At low load one block per active partition per table per shard is written per flush interval, e.g. 48 partitions / 2 s = 24 blocks/s, i.e. a horizon of ~40 s. Raise `replicated_deduplication_window` on the `*_local` tables if re-deliveries after longer outages must still be deduplicated;
  3. the shard layout changed (shards added, weights changed) between the insert and the re-delivery (`direct` mode), or `OPENLOG_PROCESSOR_INSERT_MODE`, `OPENLOG_PROCESSOR_FLUSH_INTERVAL` or `OPENLOG_PROCESSOR_BATCH_BYTES` changed, or a processor version with different row conversion re-delivers the range.

  None of these can drop rows: two blocks only share a token if they come from the same records, the same split and the same shard layout.
- Records with an unknown `openlog-schema-version` or undecodable values are logged, counted in `openlog_processor_records_rejected_total{reason}`, and skipped (no dead-letter topic in M0).
