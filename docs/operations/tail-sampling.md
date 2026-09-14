# Tail-based sampling

openlog can decide which traces to keep **after** they are complete: keep every error and slow trace, keep
chosen services/routes/attributes, sample the rest probabilistically, and cap a tenant's span rate — while APM
counts (requests, errors, throughput, latency percentiles) stay unbiased. Design: D-075 / D-076
(`docs/plan/02-decisions.md`), contracts: `docs/contracts/apm.md` §4.2, `kafka.md`, `config.md`.

Tail sampling is **off by default**. Off means no behavior change at all: ingest produces one record per
export request, the processor consumes the raw traces topic and no sampler runs.

## How it works

```
agents ──OTLP──▶ openlog-ingest ──▶ <prefix>.otlp.traces.v1          (key <tenant>/<trace id>, one record per trace)
                                            │
                                            ▼  consumer group openlog-sampler
                                     openlog-sampler  ── buffer per trace, wait, apply tenant policy
                                            │
                                            ▼  kept spans, tracestate ot=th rewritten
                                   <prefix>.otlp.traces.sampled.v1
                                            │
                                            ▼  consumer group openlog-processor
                                     openlog-processor ──▶ ClickHouse spans / apm_*
```

1. **Partitioning.** With tail sampling enabled, ingest splits each trace export request by trace id and keys each
   record `<tenant>/<hex trace id>`, so every span of a trace lands on the same partition and therefore on the
   same sampler instance. Metrics and logs are unchanged.
2. **Buffering.** The sampler keeps spans per (tenant, trace id). A trace is decided `OPENLOG_TAILSAMPLING_DECISION_WAIT`
   (default 30 s) after its first span arrived. Memory bounds decide traces **early** ("decide now", never drop
   unseen): `MAX_TRACES` (oldest trace first), `MAX_SPANS_PER_TRACE` (that trace), `MAX_BUFFERED_BYTES` (oldest first).
3. **Policy.** Per tenant (Settings → APM sampling, stored in PostgreSQL `tail_sampling_policies`), else
   `OPENLOG_TAILSAMPLING_DEFAULT_POLICY`, else keep everything. Rules are evaluated **in order; the first match wins**
   and gives the keep ratio; traces matching no rule use `baseline_ratio`.
4. **Weights.** Kept spans get tracestate `ot=th:<T>` for the composite probability `head × tail`. The processor
   already turns `th` into `sample_weight = 1/p` (apm.md §4), so every APM aggregate is the Horvitz–Thompson estimate
   of the unsampled traffic. Spans of traces kept with ratio 1 are forwarded byte-for-byte unchanged.
5. **Late spans.** Decisions are cached per trace for `OPENLOG_TAILSAMPLING_DECISION_CACHE_TTL` (default 10 min).
   A span arriving after its trace was decided follows the decision (and gets the same weight). After the TTL, or on
   another instance after a rebalance, a late span starts a new buffered trace and is decided on its own.
6. **Commit.** Offsets of the raw topic are committed only up to the oldest record that still has a buffered span or
   an unproduced kept span.

## Policy reference

```json
{
  "enabled": true,
  "baseline_ratio": 0.1,
  "max_spans_per_second": 20000,
  "rules": [
    { "name": "health",   "type": "route",     "route": "/health*", "ratio": 0 },
    { "name": "errors",   "type": "error" },
    { "name": "slow",     "type": "latency",   "threshold_ms": 2000 },
    { "name": "slow-db",  "type": "latency",   "threshold_ms": 500, "service": "orders-db" },
    { "name": "payments", "type": "service",   "services": ["payments"], "ratio": 0.5 },
    { "name": "vip",      "type": "attribute", "key": "customer.tier", "value": "gold" }
  ]
}
```

| Type | Matches when | Fields |
|---|---|---|
| `error` | any span has status `ERROR` | `service` (optional) |
| `latency` | trace duration (first start → last end) ≥ `threshold_ms`; with `service`: a span of that service ≥ threshold | `threshold_ms`, `service` |
| `service` | any span's `service.name` is in `services` | `services` |
| `route` | any span's `http.route` (span name when it has no route) matches the glob `route`; a trailing `*` is a prefix match | `route`, `service` |
| `attribute` | any span or its resource has `key` (and `value`, when set) | `key`, `value`, `service` |

Every rule has an optional `ratio` (keep probability, default 1). A rule with `ratio: 0` drops matching traces —
put it **before** the error rule only if you really want to drop their errors too.

`max_spans_per_second` (0 = unlimited) is enforced **per sampler instance**: the sampler estimates the tenant's
expected kept span rate and lowers the keep probability of every trace by `limit / rate` when it is exceeded. The
reduction is folded into the weights, so counts stay unbiased; rate-limited traces include errors. With N sampler
replicas the tenant's effective limit is about N × the value.

`enabled: false` stores the policy but keeps everything.

### Preview

Settings → APM sampling → **Preview** (`POST /api/v1/apm/sampling/preview`) applies the edited, unsaved policy to
the traces stored in the last hour (at most 20 000, a hash sample of trace ids) and shows the kept trace and span
ratio per rule. Stored traces count with their adjusted count, so the ratios refer to the original traffic even
when they were already sampled. The rate limit is not simulated.

## Enabling

The flag must be **the same on ingest, processor and sampler** (allinone: one process).

| Deployment | Steps |
|---|---|
| Compose (`single`) | `OPENLOG_TAILSAMPLING_ENABLED=true` in `.env`, `docker compose up -d`. allinone runs the sampler in-process. |
| Helm | `tailSampling.enabled: true` (sets the variable on ingest and processor and deploys `openlog-sampler` with its HPA). |

Enabling or disabling is a rolling change with a short mixed period:

- **Enable.** The sampled topic already exists (created by `openlog-migrate` since this version). Roll the sampler out
  first, then processor, then ingest. Until the processor has switched, spans reach ClickHouse twice (raw path +
  sampled path) for a few seconds; until ingest has switched, old multi-trace records are still split by the
  sampler (a trace whose spans were sent in requests keyed by different traces can be split across instances and
  decided in parts — errors/latency rules may then keep only part of the trace; the probabilistic baseline is still
  consistent, see below).
- **Disable.** Roll ingest and processor first (they go back to the raw topic; the processor group resumes from its
  last committed raw offset, which may be old → re-reads and duplicates since enabling, or data within the raw
  topic retention). To avoid that, reset the processor group's raw traces offsets to the end before disabling:
  `kafka-consumer-groups.sh --group openlog-processor --topic openlog.otlp.traces.v1 --reset-offsets --to-latest --execute`
  (processors stopped). Stop the sampler after its lag reached 0.

## Sizing

Memory ≈ `MAX_BUFFERED_BYTES` (default 512 MiB, protobuf span bytes) + decision cache (~150 B per entry × 500 000 ≈ 75 MiB)
+ decoded record overhead. Traces are held for the decision wait, so the buffer holds `span rate × wait` spans:
e.g. 20 000 spans/s × 30 s ≈ 600 000 spans ≈ 300–600 MiB. Set the container memory limit ≥ 1.5 × `MAX_BUFFERED_BYTES`.
When `openlog_tailsampling_evictions_total{reason="max_bytes"|"max_traces"}` grows, add replicas (up to the raw
traces topic partition count) or shorten the wait.

## Horizontal scaling and rebalances

- Scale by consumer lag of the raw traces topic (`openlog_tailsampling_consumer_lag_records`); replicas ≤ partitions.
  Helm: the sampler HPA scales on CPU out of the box (needs the resource metrics API, e.g. metrics-server).
  `sampler.autoscaling.lag.enabled` adds an `External` metric that Kubernetes cannot serve by itself: install an
  external metrics adapter (prometheus-adapter, KEDA's Prometheus scaler) that exposes
  `openlog_tailsampling_consumer_lag_records` summed per sampler pod; without one the HPA reports
  `FailedGetExternalMetric` and keeps scaling on CPU only.
- Validated on kind (2026-09-14, `deploy/helm/test/kind-validate.sh` step `sampling`, operators mode, 3 partitions): sampler
  Deployment 2/2, PDB `maxUnavailable: 1`, CPU HPA reading `cpu: 3%/70%` (metrics-server 3.13.1). loadgen 100 spans/s for
  2 min (5 spans per trace, 2 % error traces) with the default policy `baseline_ratio: 0.1` + an `error` rule: 12 000 spans
  decided (1 355 kept + 10 645 dropped, all in ClickHouse), baseline kept 223 of 2 352 traces (9.5 %), `errors` kept all
  48 error traces; weights 10 for baseline traces and 1 for error traces (`sum(sample_weight)` 11 390 vs 12 000 sent).
  The lag HPA was not tried (no adapter).
- **Revoked partitions are decided immediately**: in the revoke callback the sampler decides every trace with spans
  from those partitions, produces the kept spans and commits, then gives the partitions up. Spans of such a trace
  that arrive later are handled by the new owner as a new trace (late-span case). A rebalance therefore cuts
  traces that were in flight at that moment into two decisions; the trace-id-consistent baseline makes both parts
  agree for probabilistically sampled traces, rule-based decisions may differ.
- **Lost partitions** (session timeout) are decided and produced the same way, but the commit fails: the new owner
  re-reads those records → duplicates in ClickHouse for that range.
- **Shutdown** (SIGTERM) decides everything, produces and commits within 25 s; set
  `terminationGracePeriodSeconds` ≥ 40.

### Consistency of the probabilistic decision

For traces with a consistent head sampler (tracestate `ot=th`), the sampler keeps a trace when its 56-bit randomness
`R` (tracestate `ot=rv`, else the last 7 bytes of the trace id) is ≥ the threshold of `head × tail`. Given head
sampling kept it (`R ≥ T(head)`), this happens with probability exactly `tail`, and every instance, restart or
re-delivery reaches the same result for the same ratio. Traces without `ot=th` (no head sampling, or only
`sampling.ratio`) use a hash of the trace id instead, so they are not correlated with a trace-id-ratio head sampler.

## Delivery semantics (D-076)

At-least-once, like the rest of the pipeline: kept spans are produced (acks=all, idempotent producer) **before**
the raw offsets are committed. Duplicates in ClickHouse are possible when the sampler crashes between produce and
commit, a commit fails, partitions are lost, or a produce is retried after a partial failure; the processor's
deduplication tokens do not cover them (they are per sampled-topic offset range). Kafka transactions
(read_committed processor + transactional sampler) would remove these duplicates but were not chosen: see D-076.

## Metrics

| Metric | Meaning |
|---|---|
| `openlog_tailsampling_traces_buffered`, `_spans_buffered`, `_buffered_bytes` | current buffer |
| `openlog_tailsampling_decisions_total{policy, decision}` | decisions by first matching rule (`baseline`) and `kept`/`dropped` |
| `openlog_tailsampling_spans_total{decision}` | kept/dropped spans (including late spans) |
| `openlog_tailsampling_late_spans_total{decision}` | spans that followed a cached decision |
| `openlog_tailsampling_evictions_total{reason}` | early decisions: `max_traces`, `max_spans`, `max_bytes`, `revoked`, `shutdown` |
| `openlog_tailsampling_rate_limited_traces_total` | traces whose probability was lowered by the rate limit |
| `openlog_tailsampling_decision_delay_seconds` | first span → decision |
| `openlog_tailsampling_decision_cache_entries` | remembered decisions |
| `openlog_tailsampling_consumer_lag_records{topic, partition}` | lag of this member's raw traces partitions (HPA) |
| `openlog_tailsampling_records_rejected_total{reason}` | undecodable records, spans with invalid ids |
| `openlog_tailsampling_produce_failures_total` | failed produce attempts (retried) |
| `openlog_tailsampling_policy_load_failures_total` | failed PostgreSQL policy reloads (previous policies stay) |

Useful queries:

```promql
# kept span share
sum(rate(openlog_tailsampling_spans_total{decision="kept"}[5m])) / sum(rate(openlog_tailsampling_spans_total[5m]))
# traces decided early because memory was full
sum by (reason) (rate(openlog_tailsampling_evictions_total{reason=~"max_.*"}[5m]))
```

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| no traces in the UI after enabling | processor not switched (`OPENLOG_TAILSAMPLING_ENABLED` missing on it) or no sampler running: check lag of group `openlog-sampler` |
| duplicated spans for a few seconds | mixed rollout while enabling (see above) |
| error traces partially stored | trace longer than the decision wait, `MAX_SPANS_PER_TRACE` reached, or a rebalance cut it; raise the wait / limit |
| APM throughput changed after enabling | the adjusted counts are estimates; with small baseline ratios low-traffic transactions get noisy. Raise `baseline_ratio` or add a rule for them |
| policy change not applied | samplers reload every `OPENLOG_TAILSAMPLING_POLICY_REFRESH` (30 s) |
