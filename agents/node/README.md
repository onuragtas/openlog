# openlog Node.js agent

Apache-2.0 · package `openlog-node` · Node.js 18.19+, 20, 22, 24

This is a thin distribution of the OpenTelemetry JavaScript SDK (D-060), built like the [Go agent](../go/README.md).
One line sends traces, metrics (including Node.js runtime metrics) and logs to openlog over OTLP. The resource carries
the same `host.id` as the openlog infra agent, so a service is linked to the host it runs on. Sampling decisions
interoperate with the Go agent through W3C `tracestate`.

## Quick start

```sh
npm install openlog-node
```

When a release is not on npm (registry publishing is optional for openlog releases), install the package file attached
to every GitHub release; the "Add data" page shows whichever works for your server's version:

```sh
npm install https://github.com/onuragtas/openlog/releases/download/vX.Y.Z/openlog-node-X.Y.Z.tgz
```

npm records the URL and the tarball's integrity in `package.json` and `package-lock.json`; `openlog-node-X.Y.Z.tgz.sha256`
next to it is the `sha256sum` checksum, and the tarball is listed in the release's signed `manifest.json`.

Zero code changes:

```sh
export OPENLOG_LICENSE_KEY=dev-license-key OPENLOG_ENDPOINT=http://localhost:4318 OPENLOG_SERVICE_NAME=checkout
node --require openlog-node/register server.js     # CommonJS applications
node --import openlog-node/register server.mjs     # ES module applications (also works for CommonJS)
# or: NODE_OPTIONS="--require openlog-node/register"
```

Or from code, before the application loads the libraries to instrument:

```js
// instrumentation.js — first line of the entry point, or node --require ./instrumentation.js
const openlog = require('openlog-node');            // import * as openlog from 'openlog-node'

const agent = openlog.start({ serviceName: 'checkout', serviceVersion: '1.4.0', environment: 'production' });
process.on('SIGTERM', () => agent.shutdown().finally(() => process.exit(0)));  // flushes buffered telemetry (5 s max)
```

`start()` installs the global TracerProvider, MeterProvider and LoggerProvider, the AsyncLocalStorage context manager,
W3C `tracecontext` + `baggage` propagators and the instrumentations below, so any OpenTelemetry API usage
(`@opentelemetry/api`) works unchanged. It only throws for invalid configuration (`ConfigError`) or when an agent is
already running. Export problems (ingest down, `401`, `503`) are logged to stderr, rate limited, and never throw into
or block the application.

The `register` entry point never throws: invalid configuration is reported on stderr and the application runs without
the agent. It flushes on `beforeExit`, `SIGTERM` and `SIGINT`; when the application has no handler of its own for the
signal, the signal is re-raised after the flush, so the process ends as it would without the agent
(`OPENLOG_SHUTDOWN_ON_SIGNAL=false` turns this off).

ES modules: instrumenting `import`ed libraries needs the loader hook that `--import openlog-node/register` registers
(`import-in-the-middle`). With `--require` or `start()`, only `require`d modules are instrumented.

## Configuration

Precedence: **options > `OPENLOG_*` > `OTEL_*` > defaults**. The names are the Go agent's.

| Variable | Option | Default | Meaning |
|---|---|---|---|
| `OPENLOG_LICENSE_KEY` | `licenseKey` | — | Ingest license key, sent as header `openlog-license-key`. Without it, openlog ingest answers `401` (a warning is logged) |
| `OPENLOG_ENDPOINT` (`OTEL_EXPORTER_OTLP_ENDPOINT`) | `endpoint` | `http://localhost:4318` (grpc: `http://localhost:4317`) | OTLP base URL. `http/protobuf` appends `/v1/traces`, `/v1/metrics`, `/v1/logs`. `http://` = plaintext, `https://` or no scheme = TLS |
| `OPENLOG_PROTOCOL` (`OTEL_EXPORTER_OTLP_PROTOCOL`) | `protocol` | `http/protobuf` | or `grpc` (install the optional peers `@opentelemetry/exporter-{trace,metrics,logs}-otlp-grpc@0.222.0`) |
| `OPENLOG_COMPRESSION` (`OTEL_EXPORTER_OTLP_COMPRESSION`) | `compression` | `gzip` | or `none` |
| `OTEL_EXPORTER_OTLP_HEADERS` | `headers` | — | Extra headers (the license key header wins) |
| `OPENLOG_SERVICE_NAME` (`OTEL_SERVICE_NAME`) | `serviceName` | `unknown_service:<npm package or script>` | `service.name` |
| `OPENLOG_SERVICE_VERSION` | `serviceVersion` | — | `service.version` |
| `OPENLOG_SERVICE_NAMESPACE` | `serviceNamespace` | — | `service.namespace` |
| `OPENLOG_ENVIRONMENT` | `environment` | — | `deployment.environment.name` |
| `OPENLOG_SAMPLING_RATIO` (`OTEL_TRACES_SAMPLER_ARG` with a `*traceidratio` sampler) | `samplingRatio` | `1` | Parent-based head sampling of new traces (0..1), see [Sampling](#sampling) |
| `OPENLOG_SAMPLING_RV` | `samplingRV` | `false` | New traces write explicit randomness `ot=rv` |
| `OPENLOG_RESOURCE_ATTRIBUTES` (merged over `OTEL_RESOURCE_ATTRIBUTES`) | `resourceAttributes` | — | `k=v,k2=v2` (percent-encoded values allowed) |
| `OPENLOG_HOST_ID` | `hostId` | detected | Explicit `host.id` |
| `OPENLOG_HOST_ROOT` | `hostRoot` | `/` | Prefix for host files when the host root is mounted (e.g. `/host`) |
| `OPENLOG_INFRA_RUNTIME_DIR` | `infraRuntimeDir` | `/run/openlog-infra-agent` | Where a running infra agent publishes its host id (`host-id`) |
| `OPENLOG_INFRA_STATE_DIR` | `infraStateDir` | `/var/lib/openlog-infra-agent` | Where the infra agent persists a generated host id |
| `OPENLOG_STATE_DIR` | `stateDir` | user cache dir `/openlog` | Where this agent persists a generated host id (last resort) |
| `OPENLOG_RUNTIME_METRICS` | `runtimeMetrics` | `true` | Node.js runtime metrics |
| `OPENLOG_METRIC_EXPORT_INTERVAL` (Go duration; `OTEL_METRIC_EXPORT_INTERVAL` in ms) | `metricIntervalMs` | `60s` | Metric export interval |
| `OPENLOG_SHUTDOWN_TIMEOUT` (Go duration) | `shutdownTimeoutMs` | `5s` | Bound of the final flush |
| `OPENLOG_LOG_LEVEL` (`OTEL_LOG_LEVEL`) | `logLevel` | `warn` | Agent diagnostics on stderr: `debug`, `info`, `warn`, `error`, `off` |
| `OPENLOG_ENABLED` (`OTEL_SDK_DISABLED`) | `enabled` | `true` | `false`: nothing is installed |
| `OPENLOG_DB_QUERY_TEXT` | `dbQueryText` | `sanitized` | `sanitized`, `raw` or `off` for `db.query.text` / `db.statement` |
| `OPENLOG_LOGS_EXPORT` | `logsExport` | `true` | Export pino/winston records as OTLP logs (trace ids are added to their output either way) |
| `OPENLOG_LOGS_CONSOLE` | `logsConsole` | `false` | Also export `console.*` calls as OTLP logs |
| `OPENLOG_INSTRUMENTATIONS_DISABLED` (`OTEL_NODE_DISABLED_INSTRUMENTATIONS`) | `disabledInstrumentations` | — | Comma-separated short names (`graphql,aws-sdk`) or package names |
| — | `instrumentationConfig` | — | Per-instrumentation OTel options merged over the agent defaults, e.g. `{ http: { headersToSpanAttributes: … } }` |
| `OPENLOG_HTTP_IGNORE_PATHS` | `httpIgnorePaths` | — | Incoming request paths without spans (exact match), e.g. `/healthz,/readyz` |
| `OPENLOG_SHUTDOWN_ON_SIGNAL` | `shutdownOnSignal` | `true` | `register` flushes on `SIGTERM`/`SIGINT`/`beforeExit` |

**Export behaviour.** Spans are batched: queue 4096, batch 512, flushed every 5 s. Logs are batched the same way,
flushed every 2 s. Export requests time out after 10 s. HTTP `429/502/503/504` and gRPC `UNAVAILABLE`/`RESOURCE_EXHAUSTED`
are retried with exponential backoff by the OpenTelemetry exporters, honouring `Retry-After` (D-014). When the queue is
full, new spans are dropped rather than blocking the application. Metrics are cumulative.

## Framework and library support

Instrumentations are pinned OpenTelemetry JS packages (see `package.json`); a library outside the supported range is
simply not instrumented.

| Name (`OPENLOG_INSTRUMENTATIONS_DISABLED`) | Library | Versions | Spans / signals |
|---|---|---|---|
| `http` | `http`, `https` | Node.js built-in | SERVER spans (entry spans, `http.route` from the framework), CLIENT spans, W3C propagation, `http.server.request.duration` / `http.client.request.duration` |
| `undici` | `fetch`, `undici` | Node.js 18+ `fetch`, undici ≥ 5 | CLIENT spans with `server.address`, propagation |
| `express` | `express` | 4, 5 | middleware/handler spans, route → server span name `GET /users/:id` |
| `fastify` | `fastify` (`@fastify/otel`) | 4.x, 5.x | hook/handler spans, route |
| `koa` | `koa` + `@koa/router` | koa 2–3 | middleware/router spans, route |
| `nestjs` | `@nestjs/core` | 4 – 11 (OTel); 12 via openlog, see [NestJS 12](#nestjs-12) | 4 – 11: request context and handler spans, route. 12: server span named by the express/fastify route; optional `OpenLogNestInterceptor` adds `nestjs.controller`/`nestjs.callback` and names routes without a platform instrumentation; no Nest handler spans |
| `pg` | `pg`, `pg-pool` | 8.x | CLIENT spans, `db.query.text` sanitized |
| `mysql2` | `mysql2` | 1.4 – 3 | CLIENT spans, values masked + sanitized |
| `redis` | `redis`, `@redis/client` | 2 – 5 | CLIENT spans, `db.query.text` = `GET ?` |
| `ioredis` | `ioredis` | 2 – 5 | CLIENT spans, `HSET ? ? ?` |
| `mongodb` | `mongodb` | 3.3 – 7 | CLIENT spans, statement values replaced by `?` |
| `graphql` | `graphql` | 14 – 16 | parse/validate/execute spans (trivial resolvers skipped) |
| `grpc` | `@grpc/grpc-js` | 1.x | SERVER/CLIENT spans (`rpc.*`) |
| `aws-sdk` | AWS SDK v3 (`@aws-sdk/*`, `@smithy/*`) | v3 | CLIENT spans (`rpc.system=aws-api`, SQS/SNS messaging, DynamoDB) |
| `pino` | `pino` | 5 – 10 | `trace_id`/`span_id`/`trace_flags` in each record + OTLP log export |
| `winston` | `winston` | 1 – 3 | `trace_id`/`span_id` in each record + OTLP log export |
| (`OPENLOG_LOGS_CONSOLE`) | `console` | built-in | OTLP log export correlated with the active span |

Exact version ranges are those of the pinned instrumentation packages; the CI suite runs express 5, fastify 5, koa 3,
NestJS 11 and 12 (ESM, express and fastify platforms), pg 8, mysql2 3, redis 6, ioredis 6, pino 10 and winston 3 on
Node.js 20 and 22.

**Transaction names.** The APM backend names a web transaction `<METHOD> <http.route>` ([apm.md §2.1](../../docs/contracts/apm.md)).
Express, Koa and Fastify pass the matched route to the HTTP server span. For frameworks that only put `http.route` on
their own spans (NestJS on other platforms, custom routers), the agent copies the longest route seen below the entry
span onto it before it ends. Requests without a route (404s, plain `http` servers) are grouped by the backend's path
normalization.

### NestJS 12

NestJS 12 is ESM-only and outside the range of `@opentelemetry/instrumentation-nestjs-core` (`< 12`), so there are no
Nest request-context/handler spans. Transactions are still route-named with no code change: start the application with
`node --import openlog-node/register main.js`. The express and fastify instrumentations then patch the platform
Nest 12 loads, and the SERVER span is `GET /users/:id` with `http.route` equal to the full registered path
(URI version included, e.g. `/v2/orders/:orderId`). Verified with `@nestjs/*` 12.0.1 on express 5
and fastify 5 (`test/e2e/nest12.test.ts`). With `--require`, ES module imports are not patched and routes stay unnamed.

Optionally, register the openlog interceptor (no dependency on `@nestjs/*`; works on NestJS 8–12):

```js
import { OpenLogNestInterceptor } from 'openlog-node/nest';   // require('openlog-node/nest')

app.useGlobalInterceptors(new OpenLogNestInterceptor());
// or in a module: providers: [{ provide: APP_INTERCEPTOR, useClass: OpenLogNestInterceptor }]
```

It reads the matched route from the platform request (`req.route.path` on express, `request.routeOptions.url` on
fastify), sets `http.route` on the entry SERVER span (through the HTTP instrumentation's RPC metadata, as the
framework instrumentations do), renames it `<METHOD> <route>` and adds `nestjs.controller` and `nestjs.callback`.
Routes are then named even with `OPENLOG_INSTRUMENTATIONS_DISABLED=express` (or `fastify`), which also removes the
per-middleware spans. Not covered: requests that never reach an interceptor (guard rejections, 404s, errors thrown in
middleware) keep the platform instrumentation's name (or none); GraphQL, microservice and WebSocket contexts are
ignored; no Nest handler/guard/pipe spans.

**Database statements.** With `OPENLOG_DB_QUERY_TEXT=sanitized` (default) SQL is normalized exactly like the Go agent
(`openlogsql.Sanitize`, shared test cases): string, numeric, hex and dollar-quoted literals → `?`, `IN (…)` and
multi-row `VALUES` → `(?)`, comments removed, placeholders kept; MySQL `"…"` is a string, otherwise an identifier.
Redis/Memcached commands become `CMD ? ?`. Statements are capped at 4096 characters. `raw` keeps the text, `off`
removes it.

## Sampling

`OPENLOG_SAMPLING_RATIO=p` samples new traces with probability `p`; downstream services follow the decision
(parent-based). APM counts stay correct because each stored span is weighted by `1/p` ([apm.md §4](../../docs/contracts/apm.md)).
The semantics are identical to the Go agent ([README](../go/README.md#sampling)), verified against fixtures generated
from the Go agent's sampler (`test/interop/go-sampler-fixtures.json`, `scripts/gen-go-fixtures.sh`):

- **Root spans.** `T = (1 − p) · 2^56`; the randomness is `ot=rv` when present, else the lower 56 bits of the trace id;
  sampled when `R ≥ T`. A sampled root gets `sampling.ratio = p` and `tracestate` `ot=th:<T>` (e.g. `p = 0.25` → `ot=th:c`).
- **Downstream.** A local entry span whose sampled remote parent carries `ot=th` (or legacy `ot=p`) with `p < 1` gets
  `sampling.ratio = p`; the local ratio does not change an upstream decision.
- **Random flag.** Root spans carry the W3C Level 2 random flag (`traceparent` flags `03`/`02`); children inherit it,
  so a Level 1 parent is continued unchanged. `OPENLOG_SAMPLING_RV=true` writes explicit `ot=rv:<14 hex>` on new traces.
- **Limits.** The `ot` value is capped at 256 characters (unknown sub-keys dropped first) and `tracestate` at 32 members.

## Resource

| Attribute | Source |
|---|---|
| `service.name`, `service.version`, `service.namespace`, `deployment.environment.name` | configuration |
| `host.id` | see [Host linking](#host-linking) |
| `host.name`, `host.arch` | Linux: `/proc/sys/kernel/hostname` → `/etc/hostname`; `/proc/sys/kernel/arch` (OTel values, same as the infra agent); else `os.hostname()`, `process.arch` |
| `os.type`, `os.name`, `os.version`, `os.description`, `openlog.os.kernel_release` | platform; `/etc/os-release`; `/proc/sys/kernel/osrelease` |
| `process.pid`, `process.executable.{name,path}`, `process.command` (script path, no arguments), `process.owner`, `process.runtime.{name,version,description}` | the process (command-line arguments are not sent, they may contain secrets) |
| `container.id` | 64-hex id from `/proc/self/cgroup`; with a private cgroup namespace, from Docker/Podman container paths in `/proc/self/mountinfo` |
| `k8s.pod.name`, `k8s.pod.uid`, `k8s.namespace.name`, `k8s.node.name`, `k8s.container.name`, `k8s.deployment.name`, `k8s.cluster.name` | downward-API env `K8S_POD_NAME`, … (inside a pod the namespace falls back to the service-account file and the pod name to `HOSTNAME`) |
| `telemetry.distro.name` = `openlog`, `telemetry.distro.version`, `telemetry.sdk.*` | agent |

Precedence: detected < resource attributes from the environment or options < explicit service, environment and host
id settings.

## Host linking

Identical to the Go agent ([details](../go/README.md#host-linking)): the id a running infra agent publishes in
`/run/openlog-infra-agent/host-id` → `/etc/machine-id` → `/var/lib/dbus/machine-id` → `/sys/class/dmi/id/product_uuid`
→ the infra agent's generated id in `/var/lib/openlog-infra-agent/host-id` → (macOS/Windows) the platform machine id →
a UUID generated and persisted in `OPENLOG_STATE_DIR`. In containers, mount the infra agent's runtime directory
read-only: `-v /run/openlog-infra-agent:/run/openlog-infra-agent:ro` (Kubernetes: `hostPath` of type `Directory`), or
set `OPENLOG_HOST_ID`.

## Runtime metrics

Read on every metric collection (every 60 s by default) plus an event-loop delay monitor and a GC performance
observer. Names follow the OpenTelemetry semantic conventions (the Go agent's policy: semconv names where they exist,
`openlog.` prefix otherwise); scope `@openlog/node/runtime`.

| Metric | Type | Unit | Attributes |
|---|---|---|---|
| `nodejs.eventloop.delay.{min,max,mean,stddev,p50,p90,p99}` | Gauge (since last collection) | `s` | — |
| `nodejs.eventloop.utilization` | Gauge (since last collection) | `1` | — |
| `nodejs.eventloop.time` | Sum, monotonic | `s` | `nodejs.eventloop.state` = `active`/`idle` |
| `v8js.gc.duration` | Histogram (Go agent bounds, 10 µs – 1 s) | `s` | `v8js.gc.type` = `major`/`minor`/`incremental`/`weakcb` |
| `v8js.memory.heap.used`, `v8js.memory.heap.space.size`, `v8js.memory.heap.space.available_size`, `v8js.memory.heap.space.physical_size` | Sum, non-monotonic | `By` | `v8js.heap.space.name` |
| `v8js.memory.heap.limit` | Sum, non-monotonic | `By` | — (isolate heap size limit) |
| `v8js.resource.active` | Sum, non-monotonic | `{resource}` | `v8js.resource.type` (active handles/requests: `TCPSocketWrap`, `Timeout`, …) |
| `process.cpu.time` | Sum, monotonic | `s` | `cpu.mode` = `user`/`system` |
| `process.memory.usage` | Sum, non-monotonic | `By` | — (RSS) |

The HTTP instrumentation also records `http.server.request.duration` and `http.client.request.duration`.

## Overhead

`npm run bench` (`test/bench/overhead.ts`): an Express 5 app (`express.Router`, one JSON route) saturated by a
keep-alive load generator (16 concurrent connections, 8 s × 3 runs, median) with the real agent exporting to a local
OTLP capture server (http, express, pino/winston hooks and runtime metrics enabled). Measured in `node:22-alpine` on
Docker Desktop (Apple M1 Pro, 10 CPUs, load generator on the same machine):

| Scenario | req/s | vs. no agent | p50 ms | p99 ms | RSS MB |
|---|---|---|---|---|---|
| no agent | 5214 | — | 2.39 | 11.62 | 128 |
| agent, sampled (ratio 1) | 2469 | −52.6 % | 5.47 | 23.58 | 178 |
| agent, not sampled (ratio 0) | 3578 | −31.4 % | 3.98 | 12.29 | 170 |

The server is a single saturated event loop, so its service time per request is `1/rps`: the agent adds about
**0.21 ms per sampled request** (server span, express router/handler spans, HTTP metrics histogram, batching and
export) and **0.09 ms per unsampled request** (context propagation, instrumentation hooks, metrics), plus ~50 MB RSS.
The relative drop is large here only because the handler itself does almost nothing (0.19 ms); for a request that
spends 10 ms in application code and I/O it is about 2 %. This is comparable to the upstream OpenTelemetry JS
instrumentations the agent is built on, and higher than the Go agent (~4 µs). Ways to reduce it: sampling
(`OPENLOG_SAMPLING_RATIO`), disabling instrumentations you do not need (`OPENLOG_INSTRUMENTATIONS_DISABLED`, e.g.
`express` keeps route names but its per-middleware spans cost the most), and `OPENLOG_HTTP_IGNORE_PATHS` for health
checks. These are micro-benchmark numbers from a shared development machine, not a dedicated benchmark host.

## Versioning and upgrading

The package is versioned with the openlog product (D-025): every product release `vX.Y.Z` publishes
`openlog-node@X.Y.Z` to npm (pre-releases `vX.Y.Z-beta.N` under the dist-tag `beta`). The backend accepts agents from
its last three minor versions. OpenTelemetry dependencies are pinned exactly per release; if the application also
depends on `@opentelemetry/api`, keep it on `^1.9` (one global API instance is shared).

```sh
npm install openlog-node@X.Y.Z
```

## Development

Local Node tooling on iCloud Drive may hang; run everything in a container:

```sh
docker volume create openlog-node-agent-nm
docker run --rm -v "$PWD":/src -v openlog-node-agent-nm:/src/node_modules -w /src node:22-alpine \
  sh -c 'npm ci && npm run test:apps:install && npm run lint && npm run typecheck && npm test'
# real PostgreSQL/MySQL/Redis
docker compose -p openlog-nodeagent-test -f test/integration/docker-compose.yml --profile runner run --rm runner
docker compose -p openlog-nodeagent-test -f test/integration/docker-compose.yml down -v
npm run bench                     # overhead micro-benchmark
scripts/gen-go-fixtures.sh        # regenerate the Go sampler fixtures (needs Go)
```

`npm test` builds the package (`dist/cjs` with tsc plus an ES module facade in `dist/esm` that re-exports the same
instance), compiles the tests and runs unit tests plus end-to-end tests that start real Express, Fastify, Koa, NestJS
and plain `http` applications with `--require`/`--import` against an OTLP capture server (spans, metrics, logs,
resource, headers, SIGTERM flush, ingest outage). The NestJS 12 suite needs Node.js 20+ and the separately pinned app
in `test/apps/nest12` (`npm run test:apps:install`, i.e. `npm ci --prefix test/apps/nest12`); without it the suite is
skipped with that hint. The release flow is described in
[releasing.md](../../docs/operations/releasing.md#nodejs-agent-package).
