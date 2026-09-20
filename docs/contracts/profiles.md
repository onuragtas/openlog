# Continuous profiling

Continuous profiling answers a question the other signals cannot: *inside* the slow span, which function spent
the time. A trace says `GET /orders` took 900 ms and the database took 40 ms of it; a profile says the
remaining 860 ms went to `json.Marshal` called from `renderOrders`.

openlog carries profiles as **OTLP profiles**, not pprof. The signal is still `v1development` upstream, and
that was the deliberate choice: pprof is stable but is Go's format with a Go-shaped model, while OTLP profiles
are the format the rest of the ecosystem is converging on, carry OTel resource attributes natively (so a
profile lands beside the APM service it belongs to without a mapping table), and will stabilise. The cost of
the choice is contained: `internal/profiles` is the only package that knows the wire types.

## 1. Ingest

| | |
|---|---|
| HTTP | `POST /v1/profiles`, `application/x-protobuf` or `application/json`, optional gzip |
| gRPC | `opentelemetry.proto.collector.profiles.v1development.ProfilesService/Export` |
| Kafka topic | `<prefix>.otlp.profiles.v1` ([kafka.md](kafka.md)) |
| Partition key | `<tenant_id>/<service.name>` of the first resource having one |

Profiles take a path of their own in `internal/ingest` (`profiles.go`) rather than another arm of
`httpExport`, and the reason is a type, not a preference: an OTLP profiles request is a pdata wrapper and does
not implement `proto.Message`, which `newRequest`, `prepare` and `partialSuccess` are built on.

What that path does **not** do differently is everything that matters: license key resolution, the SaaS
organization gate, the body limit, the tenant quota and the produce are the same calls in the same order. A
profile is never a way around a check the other signals pass.

The payload reaches Kafka **byte for byte**. OTLP/JSON is converted to protobuf once at ingest (so the
processor never has to guess at the encoding), but a protobuf payload is produced exactly as it arrived:
re-encoding an unstable wire format would make openlog, rather than the specification, the thing that decides
what the bytes mean.

A payload carrying no profile at all is accepted and produces nothing — an agent whose process was idle has
nothing to report, and an error would make it retry forever.

## 2. Storage

`schema/clickhouse/0095_profiles.sql`. One row per sample: the resolved call stack and the value measured on
it.

| Column | Notes |
|---|---|
| `stack` | `Array(LowCardinality(String))`, **root first**, the way a flame graph reads |
| `leaf` | the innermost frame, stored rather than derived |
| `profile_type`, `unit` | from the profile's own `ValueType` (`cpu`/`nanoseconds`, `alloc_space`/`bytes`) |
| `value` | the sample's measurement in `unit` |
| `duration_ns` | wall time the profile covers, so a rate needs no second query |

**Why the stack is expanded rather than stored as the wire's indices.** OTLP profiles are a dictionary plus
indices (sample → stack → location → line → function → string). That is the right shape for a payload and the
wrong one for a column store: keeping it would make every flame graph five joins. The dictionary is small, so
the expansion is done once, at the processor (`internal/profiles.FromOTLP`), and a flame graph becomes
`SELECT stack, sum(value) GROUP BY stack`.

`leaf` is stored rather than computed because "self time by function" is the first question asked of a
profile, and `arrayElement(stack, -1)` in a `GROUP BY` would defeat the index.

Sharded by `cityHash64(tenant_id, service_name)`: a flame graph is always one service over one window, so a
service's samples stay on one shard and the read is single-shard. Sharding by host would scatter every service
across the cluster and make each flame graph a fan-out.

**Bounds** (`internal/profiles`): `MaxFrames = 128` per stack (deeper stacks keep their *innermost* frames,
which is where the time is), `MaxFrameBytes = 512` per frame name (truncated, not dropped, so the frame still
groups), `MaxAttrBytes = 1024` per attribute, `MaxRowsPerRequest = 200 000` per payload.

**Dropped whole, not in part.** A payload that cannot be expanded — a profile with no sample type, a request
past the row cap — is refused entirely. The percentages a flame graph shows are of the samples it holds, so
half a profile is not a smaller truth but a wrong one.

## 3. Retention

Profiles are the widest rows openlog stores: a minute of CPU samples from one process is thousands of stacks.
The schema TTL is **7 days** and they carry a storage class of their own (`profiles`), not the `traces` class,
because per-tenant retention (D-081) widens a class as a whole — a tenant that lengthens its trace retention
must not silently multiply its profiling bill. Per-tenant retention applies to `profiles_local` like any other
raw table ([usage.md](usage.md)).

## 4. Metering

`usage_signals_1h` counts stored samples and estimated uncompressed bytes through a materialized view on
`profiles_local` (`0097_usage_profiles.sql`); `usage_ingest_1h` counts the OTLP bytes the processor consumed.
Billing therefore counts stored rows like every other signal: a payload ingest accepted but the processor
dropped is not billed. A service that only profiles also counts as an active service, so it is not invisible
to the entity counts.

## 5. Reads

All reads go through the tenant-scoped query layer; see [api.md](api.md) for parameters and response bodies.

| Endpoint | Answers |
|---|---|
| `GET /api/v1/profiles/services` | what has been profiled in the window, and **which profile types each service has** |
| `GET /api/v1/profiles/flame` | the flame graph tree for one service and type |
| `GET /api/v1/profiles/functions` | functions ranked by self time |

A **profile type is required** wherever values are summed, and is deliberately not defaulted to `cpu`.
Nanoseconds and bytes do not add up: a chart over an unstated type would be a number with no unit. The
services endpoint is what tells a caller which types exist, and every response repeats the `unit` the
profile's own `ValueType` declared, so a chart never has to hard-code "ns".

Identical stacks are folded in ClickHouse rather than in Go — that is the whole reason `stack` is a column.
The flame graph is capped at 20 000 distinct stacks ordered by value, so what falls off the end is the
narrowest slivers, which a flame graph could not draw a pixel of anyway.
