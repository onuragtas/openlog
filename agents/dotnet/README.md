# openlog .NET agent

Apache-2.0 · NuGet package `OpenLog.Agent` · .NET 8 and 9 (`net8.0`), .NET Framework 4.6.2+ / other runtimes via `netstandard2.0`

This is a thin distribution of [OpenTelemetry .NET](https://github.com/open-telemetry/opentelemetry-dotnet) (D-074),
built like the [Go](../go/README.md) and [Node.js](../node/README.md) agents. One line sends traces, metrics (including
.NET runtime and process metrics) and logs (`ILogger`) to openlog over OTLP. The resource carries the same `host.id` as
the openlog infra agent, so a service is linked to the host it runs on, and sampling decisions interoperate with the Go,
Node.js and Java agents through W3C `tracestate`.

## Quick start

```sh
dotnet add package OpenLog.Agent
export OPENLOG_LICENSE_KEY=dev-license-key OPENLOG_ENDPOINT=http://localhost:4318 OPENLOG_SERVICE_NAME=checkout
```

ASP.NET Core and generic-host applications (worker services):

```csharp
var builder = WebApplication.CreateBuilder(args);
builder.Services.AddOpenLog(o =>
{
    o.ServiceVersion = "1.4.0";                          // every option can also come from OPENLOG_* variables
    o.ActivitySources = new[] { "MyCompany.*" };         // your own ActivitySources
});
var app = builder.Build();
```

`AddOpenLog()` registers OpenTelemetry tracing, metrics and logging (`ILogger` records are exported as OTLP logs,
correlated with the active span) and returns the `IOpenTelemetryBuilder`, so further OpenTelemetry configuration can be
chained (`.WithTracing(b => b.AddSource("…"))`). Providers are flushed when the host stops.

Console applications, background services without a host, and .NET Framework:

```csharp
using OpenLog.Agent;

using var agent = OpenLogAgent.Start(o => o.ServiceName = "invoice-job");   // flushes on Dispose and on process exit
var logger = agent.LoggerFactory.CreateLogger("job");
using var source = new ActivitySource("InvoiceJob");                        // + OPENLOG_ACTIVITY_SOURCES=InvoiceJob
```

`OpenLogAgent.Start` installs the tracer, meter and logger providers, the W3C `tracecontext` (with the Level 2 random
flag) + `baggage` propagators and the instrumentations below. It throws `OpenLogConfigException` for invalid
configuration and `InvalidOperationException` when an agent is already running; `AddOpenLog` only throws for invalid
configuration. Export problems (ingest down, `401`, `503`) are logged to stderr, rate limited, and never throw into or
block the application.

> On .NET 8+ the package references the ASP.NET Core shared framework (`Microsoft.AspNetCore.App`, required by the
> ASP.NET Core instrumentation), so console and worker applications need the ASP.NET Core runtime (for containers the
> `mcr.microsoft.com/dotnet/aspnet` base image instead of `runtime`).
>
> The entry point is `OpenLogAgent.Start()` rather than `OpenLog.Start()`: a static class named `OpenLog` cannot live
> in the `OpenLog.Agent` namespace without shadowing it in every file that imports it.

### Zero-code: OpenTelemetry .NET automatic instrumentation + openlog plugin

For applications you cannot recompile, install the
[OpenTelemetry .NET automatic instrumentation](https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation)
(tested with **1.16.0**) and add the openlog plugin, which ships in this package: copy `lib/net8.0/OpenLog.Agent.dll` from
the `OpenLog.Agent` package into the automatic instrumentation's `net/` directory. (Copying it next to the application
does not work: the application's `deps.json` does not list it, so the plugin type cannot be loaded.)

```sh
cp OpenLog.Agent.dll $HOME/.otel-dotnet-auto/net/
. $HOME/.otel-dotnet-auto/instrument.sh
export OTEL_DOTNET_AUTO_PLUGINS="OpenLog.Agent.AutoInstrumentation.OpenLogPlugin, OpenLog.Agent"
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OPENLOG_LICENSE_KEY=dev-license-key OPENLOG_SERVICE_NAME=checkout
dotnet MyApp.dll
```

The automatic instrumentation owns the providers, instrumentations and exporters (configured with its `OTEL_*`
variables); the plugin adds the openlog resource (host.id chain, container.id, `OPENLOG_SERVICE_*`), the openlog
sampler, the random-flag propagator, the route and DB statement processors, process metrics, the license key header,
exception events and `OPENLOG_HTTP_IGNORE_PATHS` for ASP.NET Core requests. `test/autoinstrumentation.sh` (CI, .NET 8
and 9) downloads the pinned automatic instrumentation release (sha256-verified), installs the plugin, runs an ASP.NET
Core app without any OpenTelemetry reference under the profiler and startup hook, and asserts resource
(`host.id`, `service.*`, `telemetry.distro.*`), license header, sampler (`sampling.ratio` for `ot=th:c` parents),
random-flag propagation on outbound calls, errors, correlated logs, ignored paths and the openlog process metrics.
The plugin is compiled against OpenTelemetry 1.18 while automatic instrumentation 1.16 loads its own 1.16 assemblies;
this works because the plugin's public methods only use `OpenTelemetry`/`OpenTelemetry.Api` types (assembly version
1.0.0.0) and takes instrumentation options as `object`. When the automatic instrumentation is upgraded, re-run the test.

## Configuration

Precedence: **options > `OPENLOG_*` > `OTEL_*` > defaults**. The names are the Go and Node.js agents'.

| Variable | Option | Default | Meaning |
|---|---|---|---|
| `OPENLOG_LICENSE_KEY` | `LicenseKey` | — | Ingest license key, sent as header `openlog-license-key`. Without it, openlog ingest answers `401` (a warning is logged) |
| `OPENLOG_ENDPOINT` (`OTEL_EXPORTER_OTLP_ENDPOINT`) | `Endpoint` | `http://localhost:4318` (grpc: `http://localhost:4317`) | OTLP base URL. `http/protobuf` appends `/v1/traces`, `/v1/metrics`, `/v1/logs`. `http://` = plaintext, `https://` or no scheme = TLS |
| `OPENLOG_PROTOCOL` (`OTEL_EXPORTER_OTLP_PROTOCOL`) | `Protocol` | `http/protobuf` | or `grpc` (on `netstandard2.0`/.NET Framework the OpenTelemetry exporter needs an `HttpClientFactory` for gRPC; use `http/protobuf` there) |
| `OPENLOG_COMPRESSION` (`OTEL_EXPORTER_OTLP_COMPRESSION`) | `Compression` | `gzip` | or `none` |
| `OTEL_EXPORTER_OTLP_HEADERS` | `Headers` | — | Extra headers (the license key header wins) |
| `OPENLOG_SERVICE_NAME` (`OTEL_SERVICE_NAME`) | `ServiceName` | `unknown_service:<entry assembly>` | `service.name` |
| `OPENLOG_SERVICE_VERSION` | `ServiceVersion` | — | `service.version` |
| `OPENLOG_SERVICE_NAMESPACE` | `ServiceNamespace` | — | `service.namespace` |
| `OPENLOG_ENVIRONMENT` | `Environment` | — | `deployment.environment.name` |
| `OPENLOG_SAMPLING_RATIO` (`OTEL_TRACES_SAMPLER_ARG` with a `*traceidratio` sampler) | `SamplingRatio` | `1` | Parent-based head sampling of new traces (0..1), see [Sampling](#sampling) |
| `OPENLOG_SAMPLING_RV` | `SamplingRV` | `false` | New traces write explicit randomness `ot=rv` |
| `OPENLOG_RESOURCE_ATTRIBUTES` (merged over `OTEL_RESOURCE_ATTRIBUTES`) | `ResourceAttributes` | — | `k=v,k2=v2` (percent-encoded values allowed) |
| `OPENLOG_HOST_ID` | `HostId` | detected | Explicit `host.id` |
| `OPENLOG_HOST_ROOT` | `HostRoot` | `/` | Prefix for host files when the host root is mounted (e.g. `/host`) |
| `OPENLOG_INFRA_RUNTIME_DIR` | `InfraRuntimeDir` | `/run/openlog-infra-agent` | Where a running infra agent publishes its host id (`host-id`) |
| `OPENLOG_INFRA_STATE_DIR` | `InfraStateDir` | `/var/lib/openlog-infra-agent` | Where the infra agent persists a generated host id |
| `OPENLOG_STATE_DIR` | `StateDir` | user cache dir `/openlog` | Where this agent persists a generated host id (last resort) |
| `OPENLOG_RUNTIME_METRICS` | `RuntimeMetrics` | `true` | .NET runtime and process metrics |
| `OPENLOG_METRIC_EXPORT_INTERVAL` (Go duration; `OTEL_METRIC_EXPORT_INTERVAL` in ms) | `MetricExportInterval` | `60s` | Metric export interval |
| `OPENLOG_SHUTDOWN_TIMEOUT` (Go duration) | `ShutdownTimeout` | `5s` | Bound of the final flush (`OpenLogAgent.Dispose`) |
| — | `ExportTimeout` | `10s` | Timeout of one export request |
| `OPENLOG_LOG_LEVEL` (`OTEL_LOG_LEVEL`) | `LogLevel` | `warn` | Agent diagnostics (and OpenTelemetry self-diagnostics) on stderr: `debug`, `info`, `warn`, `error`, `off` |
| `OPENLOG_ENABLED` (`OTEL_SDK_DISABLED`) | `Enabled` | `true` | `false`: nothing is installed |
| `OPENLOG_DB_QUERY_TEXT` | `DbQueryText` | `sanitized` | `sanitized`, `raw` or `off` for `db.query.text` / `db.statement` |
| `OPENLOG_LOGS_EXPORT` | `LogsExport` | `true` | Export `ILogger` records as OTLP logs |
| `OPENLOG_INSTRUMENTATIONS_DISABLED` | `DisabledInstrumentations` | — | Comma-separated short names: `aspnetcore`, `httpclient`, `sqlclient`, `npgsql`, `mysqlconnector`, `redis`, `masstransit`, `grpc` |
| `OPENLOG_HTTP_IGNORE_PATHS` | `HttpIgnorePaths` | — | Incoming request paths without spans (exact match), e.g. `/healthz,/readyz` |
| `OPENLOG_ACTIVITY_SOURCES` | `ActivitySources` | — | Extra `ActivitySource` names (wildcards allowed) |
| `OPENLOG_METERS` | `Meters` | — | Extra `Meter` names |
| `OPENLOG_SHUTDOWN_ON_SIGNAL` | `ShutdownOnExit` | `true` | `OpenLogAgent.Start` flushes on process exit (SIGTERM, Ctrl+C) |
| — | `ConfigureTracing`, `ConfigureMetrics`, `ConfigureLogging` | — | Callbacks with the OpenTelemetry builders, e.g. `b => b.AddAspNetInstrumentation()` |

**Export behaviour.** Spans are batched (queue 4096, batch 512, every 5 s), logs likewise (every 2 s); requests time out
after 10 s. The OpenTelemetry OTLP exporter retries transient failures (`429`, `502`, `503`, `504`, gRPC
`UNAVAILABLE`/`RESOURCE_EXHAUSTED`) with backoff, honouring `Retry-After` (D-014). When a queue is full, new data is
dropped rather than blocking the application. Metrics are cumulative.

## Framework and library support

The OpenTelemetry packages are pinned exactly (`Directory.Packages.props`: OpenTelemetry 1.18.0). A library outside the
supported range is simply not instrumented.

| Name (`OPENLOG_INSTRUMENTATIONS_DISABLED`) | Library | Spans / signals | Verified by the test suite |
|---|---|---|---|
| `aspnetcore` | ASP.NET Core 8/9: minimal APIs, MVC/Web API, Razor Pages | SERVER spans named `<METHOD> <route>`, `http.route` from the endpoint (`/users/{id:int}`, `orders/{id:int}`), exceptions, W3C extraction, `http.server.request.duration` | minimal API + MVC attribute routes, unhandled exception |
| `grpc` | gRPC for ASP.NET Core (`Grpc.AspNetCore`) | SERVER spans `greet.Greeter/SayHello` with `rpc.system.name`, `rpc.method`, `rpc.response.status_code`, `http.route` (needs `OTEL_DOTNET_EXPERIMENTAL_ASPNETCORE_ENABLE_GRPC_INSTRUMENTATION=true`, read when the host is built) | server + `Grpc.Net.Client` call |
| `httpclient` | `HttpClient` / `IHttpClientFactory` (also `Grpc.Net.Client`) | CLIENT spans, propagation, `http.client.request.duration` | outbound call, sampled and unsampled propagation |
| `sqlclient` | `Microsoft.Data.SqlClient`, `System.Data.SqlClient` | CLIENT spans, `db.query.text` sanitized | Microsoft.Data.SqlClient 7.0 against SQL Server 2022 (CI, amd64) and Azure SQL Edge (arm64, local) |
| `npgsql` | Npgsql 6+ (built-in `ActivitySource`) | CLIENT spans, `db.query.text` sanitized, Npgsql metrics | Npgsql 8/9 against PostgreSQL 16 |
| — | Entity Framework Core | EF Core queries appear as spans of the ADO.NET provider (Npgsql, SqlClient, MySqlConnector) with sanitized SQL; no extra EF spans | EF Core 8/9 + Npgsql provider |
| `mysqlconnector` | MySqlConnector 2.x (built-in `ActivitySource`) | CLIENT spans, statement sanitized with MySQL rules (`"…"` is a string, `#` comments) | MySqlConnector 2.6 against MySQL 8.4 |
| `redis` | StackExchange.Redis via `OpenTelemetry.Instrumentation.StackExchangeRedis` | CLIENT spans, `db.query.text` = `SET ? ?` — enabled automatically when the application references that (prerelease) package; the multiplexer is taken from DI | StackExchange.Redis 3.2 against Redis 7 |
| `masstransit` | MassTransit 8+ (built-in `ActivitySource`) | PRODUCER span `OrderPlaced publish` in the request trace (`messaging.system=rabbitmq`, `messaging.destination.name`), CONSUMER span `OrderPlaced process` in the same trace (`messaging.operation=process`, `messaging.masstransit.*`), consumer logs correlated | MassTransit 8.5 with the RabbitMQ transport against RabbitMQ 4.1 |
| — | Confluent.Kafka via `OpenTelemetry.Instrumentation.ConfluentKafka` (prerelease; `InstrumentedProducerBuilder`/`InstrumentedConsumerBuilder` + `AddKafkaProducerInstrumentation`/`AddKafkaConsumerInstrumentation` in `ConfigureTracing`) | PRODUCER `send <topic>` below the request span, CLIENT `poll <topic>` continuing or linking the producer's trace, `messaging.system=kafka`, `messaging.destination.name` | Confluent.Kafka 2.15 + instrumentation 0.3.0-alpha.1 against Kafka 4.1 |
| (`OPENLOG_LOGS_EXPORT`) | `Microsoft.Extensions.Logging` (`ILogger`) | OTLP logs with trace/span ids, formatted message, scopes and state values; `TraceId`/`SpanId` also added to the application's own log scopes (`ActivityTrackingOptions`) | minimal API, MVC and gRPC handler logs |

MassTransit 9 is commercially licensed; the tests use MassTransit 8 (Apache-2.0). Other libraries with an
`ActivitySource` or `Meter` (Azure SDK, your own code) are added with `OPENLOG_ACTIVITY_SOURCES` / `OPENLOG_METERS` or
`ConfigureTracing`.

**.NET Framework (ASP.NET 4.x).** The `netstandard2.0` build runs on .NET Framework 4.6.2+ with `OpenLogAgent.Start`
(HttpClient, SqlClient, runtime metrics). Incoming ASP.NET requests need the OpenTelemetry HTTP module: add
`OpenTelemetry.Instrumentation.AspNet` (it registers `TelemetryHttpModule` in `web.config`) and start the agent in
`Application_Start` with `ConfigureTracing = b => b.AddAspNetInstrumentation()`
([samples/OpenLog.AspNetFramework.Sample](samples/OpenLog.AspNetFramework.Sample): `Global` + `Web.config.sample`). CI
compiles that sample for `net48` against the `netstandard2.0` agent build (`make build-netfx`, reference assemblies from
`Microsoft.NETFramework.ReferenceAssemblies`), but it is not run: IIS and .NET Framework need a Windows runner.

**Transaction names.** The APM backend names a web transaction `<METHOD> <http.route>`
([apm.md §2.1](../../docs/contracts/apm.md)). ASP.NET Core puts the endpoint's route template on the server span. When
only a descendant span carries `http.route` (custom routing middleware), the agent copies the longest such route onto
the entry span and renames it before export.

**Database statements.** With `OPENLOG_DB_QUERY_TEXT=sanitized` (default) SQL is normalized exactly like the Go and
Node.js agents (shared test cases): string, numeric, hex and dollar-quoted literals → `?`, `IN (…)` and multi-row
`VALUES` → `(?)`, comments removed, placeholders (`@p0`, `$1`, `:name`, `?`) kept; MySQL `"…"` is a string, otherwise
an identifier. Redis/Memcached commands become `CMD ? ?`. Statements are capped at 4096 characters. `raw` keeps the
text, `off` removes it.

## Sampling

`OPENLOG_SAMPLING_RATIO=p` samples new traces with probability `p`; downstream services follow the decision
(parent-based). APM counts stay correct because each stored span is weighted by `1/p` ([apm.md §4](../../docs/contracts/apm.md)).
The semantics are identical to the Go agent ([README](../go/README.md#sampling)), verified against the fixtures
generated from the Go agent's sampler (`agents/node/test/interop/go-sampler-fixtures.json`):

- **Root spans.** `T = (1 − p) · 2^56`; the randomness is `ot=rv` when present, else the lower 56 bits of the trace id;
  sampled when `R ≥ T`. A sampled root gets `sampling.ratio = p` and `tracestate` `ot=th:<T>` (`p = 0.25` → `ot=th:c`).
- **Downstream.** A local entry span whose sampled remote parent carries `ot=th` (or legacy `ot=p`) with `p < 1` gets
  `sampling.ratio = p`.
- **Random flag.** Root spans carry the W3C Level 2 random flag (`traceparent` flags `03`/`02`); children inherit it.
  OpenTelemetry .NET only models the sampled bit, so the agent sets the flag on each new `Activity` and uses its own W3C
  propagator (for the SDK and for the runtime's `DistributedContextPropagator`).
- **Unsampled children.** OpenTelemetry .NET creates no `Activity` below an unsampled *local* parent (Go and Node.js
  create a non-recording span). Outgoing calls then propagate the parent's span id with the unsampled flags and
  `tracestate` — the decision downstream is the same; only a span id that is never exported differs.
- **Limits.** The `ot` value is capped at 256 characters (unknown sub-keys dropped first) and `tracestate` at 32 members.

## Resource

| Attribute | Source |
|---|---|
| `service.name`, `service.version`, `service.namespace`, `deployment.environment.name` | configuration |
| `host.id` | see [Host linking](#host-linking) |
| `host.name`, `host.arch` | Linux: `/proc/sys/kernel/hostname` → `/etc/hostname`; `/proc/sys/kernel/arch` (OTel values); else `Environment.MachineName`, OS architecture |
| `os.type`, `os.name`, `os.version`, `os.description`, `openlog.os.kernel_release` | platform; `/etc/os-release`; `/proc/sys/kernel/osrelease` |
| `process.pid`, `process.executable.{name,path}`, `process.command` (entry path, no arguments), `process.owner`, `process.runtime.{name,version,description}` | the process (command-line arguments are not sent, they may contain secrets) |
| `container.id` | 64-hex id from `/proc/self/cgroup`; with a private cgroup namespace, from Docker/Podman container paths in `/proc/self/mountinfo` |
| `k8s.pod.name`, `k8s.pod.uid`, `k8s.namespace.name`, `k8s.node.name`, `k8s.container.name`, `k8s.deployment.name`, `k8s.cluster.name` | downward-API env `K8S_POD_NAME`, … (inside a pod the namespace falls back to the service-account file and the pod name to `HOSTNAME`) |
| `telemetry.distro.name` = `openlog`, `telemetry.distro.version`, `telemetry.sdk.*` | agent |

Precedence: detected < resource attributes from the environment or options < explicit service, environment and host id settings.

## Host linking

Identical to the Go agent ([details](../go/README.md#host-linking)): the id a running infra agent publishes in
`/run/openlog-infra-agent/host-id` → `/etc/machine-id` → `/var/lib/dbus/machine-id` → `/sys/class/dmi/id/product_uuid`
→ the infra agent's generated id in `/var/lib/openlog-infra-agent/host-id` → (macOS/Windows) the platform machine id →
a UUID generated and persisted in `OPENLOG_STATE_DIR`. In containers, mount the infra agent's runtime directory
read-only: `-v /run/openlog-infra-agent:/run/openlog-infra-agent:ro` (Kubernetes: `hostPath` of type `Directory`), or
set `OPENLOG_HOST_ID`.

## Runtime metrics

| Metric | Source |
|---|---|
| .NET 9+: `dotnet.gc.collections`, `dotnet.gc.heap.total_allocated`, `dotnet.gc.last_collection.heap.size`, `dotnet.gc.last_collection.memory.committed_size`, `dotnet.gc.pause.time`, `dotnet.jit.compilation.time`, `dotnet.jit.compiled_methods`, `dotnet.thread_pool.{thread.count,queue.length,work_item.count}`, `dotnet.monitor.lock_contentions`, `dotnet.assembly.count`, `dotnet.timer.count`, `dotnet.process.{cpu.time,memory.working_set}` | the runtime's built-in `System.Runtime` meter, enabled by `OpenTelemetry.Instrumentation.Runtime` |
| .NET 8 and `netstandard2.0`: `process.runtime.dotnet.gc.{collections.count,allocations.size,heap.size,committed_memory.size,duration}`, `process.runtime.dotnet.jit.*`, `process.runtime.dotnet.thread_pool.*`, `process.runtime.dotnet.monitor.lock_contention.count`, `process.runtime.dotnet.assemblies.count`, `process.runtime.dotnet.timer.count` | `OpenTelemetry.Instrumentation.Runtime` 1.18 (the older names; they change to `dotnet.*` when the application moves to .NET 9+) |
| `process.cpu.time` {`cpu.mode`=`user`/`system`} (s), `process.memory.usage` (By, working set), `process.memory.virtual` (By), `process.thread.count` | agent, meter `OpenLog.Agent.Process` |
| `http.server.request.duration`, `http.client.request.duration`, `kestrel.*`, Npgsql/MySqlConnector pool metrics | ASP.NET Core, HttpClient and database libraries |

## Overhead

`make bench` (`bench/OpenLog.Agent.Bench`): the sample app's minimal API route `GET /users/{id:int}` (one `ILogger`
call, JSON response) in its own process, saturated by a keep-alive load generator (16 connections, 8 s × 3 rounds,
median, 3 s warmup), with the real agent exporting to a local OTLP sink (ASP.NET Core + HttpClient instrumentation,
HTTP and runtime metrics, ILogger → OTLP logs). Measured in `mcr.microsoft.com/dotnet/sdk:9.0` (.NET 9.0.20, server GC)
on Docker Desktop (Apple M1 Pro, 10 CPUs); the load generator runs on the same machine. Two runs:

| Scenario | run 1 req/s | vs. no agent | run 2 req/s | vs. no agent | p50 ms | p99 ms | RSS MB |
|---|---|---|---|---|---|---|---|
| no agent | 157 850 | — | 201 596 | — | 0.06–0.08 | 0.44–0.60 | 74–98 |
| agent, sampled (ratio 1) | 120 288 | −23.8 % | 112 945 | −44.0 % | 0.10 | 0.84–0.88 | 117–148 |
| agent, not sampled (ratio 0) | 110 373 | −30.1 % | 127 718 | −36.6 % | 0.09–0.10 | 0.86–0.97 | 128–136 |
| agent, sampled, `OPENLOG_LOGS_EXPORT=false` | — | — | 124 578 | −38.2 % | 0.09 | 0.74 | 126 |

How to read this honestly:

- The handler does almost nothing, so the server spends only a few microseconds per request and any fixed per-request
  work shows up as a large relative drop. Here the agent adds roughly **20–40 µs of latency at the median and
  0.3–0.4 ms at p99**, plus 20–50 MB RSS. For a request that spends milliseconds in application code or I/O, the
  relative cost is small.
- The runs disagree by up to 20 points (the baseline itself moved from 158k to 202k req/s) because other containers
  were running on the same Docker VM. These are indicative micro-benchmark numbers, not a dedicated benchmark host;
  run `make bench` on your own hardware.
- Not sampling saves less than in the Go and Node.js agents. ASP.NET Core still creates its request `Activity` for
  propagation, the HTTP metrics histogram is recorded for every request, and each `ILogger` call is still exported (the
  run produced about 800 MB of OTLP). Turning log export off saved a few percent.
- To reduce overhead: sample (`OPENLOG_SAMPLING_RATIO`), keep hot-path log levels above `Information` or turn off
  `OPENLOG_LOGS_EXPORT`, exclude health checks (`OPENLOG_HTTP_IGNORE_PATHS`), and disable unused instrumentations.

## Versioning and upgrading

The package is versioned with the openlog product (D-025): every product release `vX.Y.Z` publishes
`OpenLog.Agent X.Y.Z` to NuGet (`vX.Y.Z-beta.N` as a NuGet prerelease). The backend accepts agents from its last three
minor versions. OpenTelemetry dependencies are pinned per release; if the application references OpenTelemetry
packages itself, keep them on the same minor version (1.18).

## Development

Everything runs in SDK containers (no local .NET needed):

```sh
make test                         # unit + end-to-end tests without databases (.NET 9; DOTNET_VERSION=8.0 for .NET 8)
make test-integration             # + PostgreSQL, MySQL, Redis, RabbitMQ, Kafka, SQL Server (compose project openlog-m4-dotnet-test, removed afterwards)
make test-autoinstrumentation     # plugin under the pinned OpenTelemetry .NET automatic instrumentation
make build-netfx                  # .NET Framework 4.8 / ASP.NET 4.x sample, compile only
make bench                        # overhead benchmark
make pack VERSION=0.1.9           # artifacts/nupkg/OpenLog.Agent.0.1.9.nupkg
```

Layout: `src/OpenLog.Agent` (the package), `test/OpenLog.Agent.Tests` (xUnit: Go sampler fixtures, sanitizer with the
Go test cases, configuration, resource detection, processors, propagator), `test/OpenLog.Agent.IntegrationTests`
(the sample app with the real agent against an in-process OTLP capture server: spans, metrics, logs, resource,
headers, propagation, ingest outage, `OpenLogAgent.Start`, MassTransit/Kafka/SQL Server in `MessagingTests`, the
automatic instrumentation plugin in `AutoInstrumentationTests`), `test/OpenLog.AutoInstrumentation.TestApp` (no
OpenTelemetry reference), `samples/OpenLog.SampleApp` (ASP.NET Core minimal API + MVC + gRPC + HttpClient + Npgsql/EF
Core + MySqlConnector + StackExchange.Redis + SqlClient + MassTransit + Confluent.Kafka),
`samples/OpenLog.AspNetFramework.Sample` (net48, compile only), `bench/OpenLog.Agent.Bench`. SQL Server has no arm64
`mssql/server` image: the Makefile picks `mcr.microsoft.com/azure-sql-edge` on arm64 (retired by Microsoft but still
pullable; the SqlClient test passes against it) and CI (amd64) uses `mcr.microsoft.com/mssql/server:2022-latest`. The
release flow is described in [releasing.md](../../docs/operations/releasing.md#net-agent-package).
