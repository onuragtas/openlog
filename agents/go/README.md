# openlog Go agent

Apache-2.0 · module `github.com/onuragtas/openlog/agents/go` · Go 1.26+

This is a thin distribution of the OpenTelemetry Go SDK (D-033). One call sends traces, metrics (including Go runtime
metrics) and logs to openlog over OTLP. The resource carries the same `host.id` as the openlog infra agent, so a
service is linked to the host it runs on.

## Quick start

```sh
go get github.com/onuragtas/openlog/agents/go
```

```go
import openlog "github.com/onuragtas/openlog/agents/go"

func main() {
	shutdown, err := openlog.Start(context.Background(), openlog.WithServiceName("checkout"))
	if err != nil {
		log.Fatal(err)
	}
	defer shutdown(context.Background()) // flushes buffered telemetry (5 s max by default)
	// ...
}
```

```sh
OPENLOG_LICENSE_KEY=dev-license-key OPENLOG_ENDPOINT=http://localhost:4318 go run .
```

`Start` installs the global TracerProvider, MeterProvider and LoggerProvider, plus W3C `tracecontext` and `baggage`
propagators. Any OpenTelemetry instrumentation library therefore works unchanged. `Start` only returns an error for
invalid configuration. Export problems (ingest down, `401`, `503`) are logged to stderr, rate limited, and never
panic or block the application.

Runnable examples live in the `examples` module (`cd examples`):

| Example | What |
|---|---|
| `go run ./basic` | span + linked log + custom counter |
| `go run ./http-sql -load 2m` | ServeMux routes, SQLite through `openlogsql`, slog bridge, instrumented client load generator |
| `go run ./grpc -n 50` | gRPC server and client (health service) with linked spans |

## Configuration

Precedence: **options > `OPENLOG_*` > `OTEL_*` > defaults**.

| Variable | Option | Default | Meaning |
|---|---|---|---|
| `OPENLOG_LICENSE_KEY` | `WithLicenseKey` | — | Ingest license key, sent as header `openlog-license-key`. Without it, openlog ingest answers `401` (a warning is logged) |
| `OPENLOG_ENDPOINT` (`OTEL_EXPORTER_OTLP_ENDPOINT`) | `WithEndpoint` | `http://localhost:4318` (grpc: `http://localhost:4317`) | OTLP base URL. `http/protobuf` appends `/v1/traces`, `/v1/metrics`, `/v1/logs`. `http://` = plaintext, `https://` or no scheme = TLS |
| `OPENLOG_PROTOCOL` (`OTEL_EXPORTER_OTLP_PROTOCOL`) | `WithProtocol` | `http/protobuf` | or `grpc` |
| `OPENLOG_COMPRESSION` (`OTEL_EXPORTER_OTLP_COMPRESSION`) | `WithCompression` | `gzip` | or `none` |
| `OTEL_EXPORTER_OTLP_HEADERS` | `WithHeaders` | — | Extra headers (the license key header wins) |
| `OPENLOG_SERVICE_NAME` (`OTEL_SERVICE_NAME`) | `WithServiceName` | `unknown_service:<executable>` | `service.name` |
| `OPENLOG_SERVICE_VERSION` | `WithServiceVersion` | — | `service.version` |
| `OPENLOG_SERVICE_NAMESPACE` | `WithServiceNamespace` | — | `service.namespace` |
| `OPENLOG_ENVIRONMENT` | `WithEnvironment` | — | `deployment.environment.name` |
| `OPENLOG_SAMPLING_RATIO` (`OTEL_TRACES_SAMPLER_ARG` with a `*traceidratio` sampler) | `WithSamplingRatio` | `1` | Parent-based head sampling of new traces (0..1), see [Sampling](#sampling) |
| `OPENLOG_RESOURCE_ATTRIBUTES` (merged over `OTEL_RESOURCE_ATTRIBUTES`) | `WithResourceAttributes` | — | `k=v,k2=v2` (percent-encoded values allowed) |
| `OPENLOG_HOST_ID` | `WithHostID` | detected | Explicit `host.id` |
| `OPENLOG_HOST_ROOT` | — | `/` | Prefix for host files when the host root is mounted (e.g. `/host`) |
| `OPENLOG_SAMPLING_RV` | `WithSamplingRV` | `false` | New traces write explicit randomness `ot=rv`, see [Sampling](#sampling) |
| `OPENLOG_INFRA_RUNTIME_DIR` | — | `/run/openlog-infra-agent` | Where a running infra agent publishes its host id (`host-id`) |
| `OPENLOG_INFRA_STATE_DIR` | — | `/var/lib/openlog-infra-agent` | Where the infra agent persists a generated host id |
| `OPENLOG_STATE_DIR` | — | user cache dir `/openlog` | Where this agent persists a generated host id (last resort) |
| `OPENLOG_RUNTIME_METRICS` | `WithRuntimeMetrics` | `true` | Go runtime metrics |
| `OPENLOG_METRIC_EXPORT_INTERVAL` (`OTEL_METRIC_EXPORT_INTERVAL`, ms) | `WithMetricInterval` | `60s` | Metric export interval |
| `OPENLOG_SHUTDOWN_TIMEOUT` | `WithShutdownTimeout` | `5s` | Final flush bound when the shutdown context has no deadline |
| `OPENLOG_LOG_LEVEL` (`OTEL_LOG_LEVEL`) | `WithLogLevel` | `warn` | Agent diagnostics on stderr: `debug`, `info`, `warn`, `error`, `off` |
| `OPENLOG_ENABLED` (`OTEL_SDK_DISABLED`) | `WithEnabled` | `true` | `false`: `Start` installs nothing |
| `OPENLOG_DB_QUERY_TEXT` | `openlogsql.WithQueryText` | `sanitized` | `sanitized`, `raw` or `off` |

**Export behaviour.** Spans are batched: queue 4096, batch 512, flushed every 5 s. Logs are batched the same way,
flushed every 2 s. Exports time out after 10 s. HTTP `429/502/503/504` and gRPC `UNAVAILABLE`/`RESOURCE_EXHAUSTED`
are retried with exponential backoff (1 s → 30 s, for up to 1 min), and `Retry-After` / `RetryInfo` from ingest are
honoured (D-014). When the queue is full, new spans are dropped rather than blocking the application.

## Sampling

`OPENLOG_SAMPLING_RATIO=p` samples new traces with probability `p`. The decision is made once, at the root, and every
downstream service follows it (parent-based). APM counts stay correct because each stored span is weighted by `1/p`
([apm.md §4](../../docs/contracts/apm.md)).

The agent uses OpenTelemetry
[consistent probability sampling](https://opentelemetry.io/docs/specs/otel/trace/tracestate-probability-sampling/):

- **Root spans.** The rejection threshold is `T = (1 − p) · 2^56`. The randomness `R` is the tracestate value
  `ot=rv:<14 hex>` when present, otherwise the lower 56 bits of the trace id. A trace is sampled when `R ≥ T`.
  A sampled root gets the attribute `sampling.ratio = p`, and `ot=th:<T>` is written to `tracestate`: 14 hex digits,
  trailing zeros removed. Examples: `p = 0.25` gives `ot=th:c`, `p = 0.5` gives `ot=th:8`, and `p = 0.1` gives
  `ot=th:e6666666666666`. Other `ot` sub-keys (such as `rv`) and other vendors' members are kept. When a root is not
  sampled, any stale `th` is removed.
- **Downstream services.** W3C `tracestate` travels with `traceparent`, so services in any language can compute `1/p`
  from `ot=th`. When a Go service's entry span has a sampled remote parent whose tracestate carries `ot=th` (or the
  legacy `ot=p:<n>`) with `p < 1`, the agent also sets `sampling.ratio = p` on that local entry span. The local
  `OPENLOG_SAMPLING_RATIO` does not change a decision that was already made upstream.
- **W3C limits.** The `ot` value is capped at 256 characters: unknown sub-keys are dropped, and `th`/`rv` are kept.
  The list is capped at 32 members, and the right-most member is dropped when `ot` is added.

- **Randomness.** Trace ids from the SDK are random, so root spans carry the W3C Trace Context Level 2 random flag
  (`traceparent` flags `03` when sampled, `02` when not) and the trace id supplies the randomness. Children inherit the
  flag from their parent: a remote W3C Level 1 parent without it is continued unchanged. With
  `OPENLOG_SAMPLING_RV=true` (`WithSamplingRV(true)`) a new trace also gets explicit randomness `ot=rv:<14 hex>`, which
  the root decision uses and every downstream service keeps; an incoming `rv` is never replaced. Use it when trace ids
  may be rewritten on the way (e.g. by a proxy that starts new traces).

## Resource

| Attribute | Source |
|---|---|
| `service.name`, `service.version`, `service.namespace`, `deployment.environment.name` | configuration |
| `host.id` | see [Host linking](#host-linking) |
| `host.name`, `host.arch` | `/proc/sys/kernel/hostname` → `/etc/hostname` → `os.Hostname`; `/proc/sys/kernel/arch` (OTel values, same as the infra agent) |
| `os.type`, `os.name`, `os.version`, `os.description`, `openlog.os.kernel_release` | `GOOS`; `/etc/os-release` `ID`/`VERSION_ID`/`PRETTY_NAME`; `/proc/sys/kernel/osrelease` |
| `process.pid`, `process.executable.name`, `process.executable.path`, `process.owner`, `process.runtime.{name,version,description}` | the process (command-line arguments are not sent, they may contain secrets) |
| `container.id` | 64-hex id from `/proc/self/cgroup`; with a private cgroup namespace (`0::/`), from Docker/Podman container paths in `/proc/self/mountinfo` |
| `k8s.pod.name`, `k8s.pod.uid`, `k8s.namespace.name`, `k8s.node.name`, `k8s.container.name`, `k8s.deployment.name`, `k8s.cluster.name` | downward-API env `K8S_POD_NAME`, `K8S_POD_UID`, `K8S_NAMESPACE_NAME`, `K8S_NODE_NAME`, `K8S_CONTAINER_NAME`, `K8S_DEPLOYMENT_NAME`, `K8S_CLUSTER_NAME` (inside a pod the namespace falls back to the service-account file and the pod name to `HOSTNAME`) |
| `telemetry.distro.name` = `openlog`, `telemetry.distro.version` = product version, `telemetry.sdk.*` | agent |

Precedence: detected < resource attributes from the environment or options < explicit service, environment and
host id settings.

## Host linking

The APM UI links a service to a host when both report the same `host.id`. This agent uses the infra agent's
resolution chain ([semantic-conventions.md §1](../../docs/contracts/semantic-conventions.md)), so both agents
produce the same id on one machine:

1. The id a running infra agent publishes in `/run/openlog-infra-agent/host-id` (`OPENLOG_INFRA_RUNTIME_DIR`; read under
   `OPENLOG_HOST_ROOT`, then at the plain path). It is exactly the infra agent's `host.id`, whatever source it used.
2. `/etc/machine-id` → `/var/lib/dbus/machine-id` → `/sys/class/dmi/id/product_uuid`. A value is used only if it is at
   least 8 characters of `[0-9A-Za-z-]` and not all zeros; it is lower-cased.
3. The UUID the infra agent generated and persisted in `<state_dir>/host-id` (`/var/lib/openlog-infra-agent/host-id`).
4. Non-Linux only: the platform machine id (macOS `IOPlatformUUID`, Windows `MachineGuid`).
5. A UUID generated and persisted by this agent in `OPENLOG_STATE_DIR`. This cannot match an infra agent, and
   `OPENLOG_LOG_LEVEL=info` says so at start.

**Containers and Kubernetes.** A container's `/etc/machine-id` is usually missing or belongs to the image, not the
host. Link containers to the host in one of these ways:

- recommended with an infra agent on the host: mount its runtime directory read-only,
  `-v /run/openlog-infra-agent:/run/openlog-infra-agent:ro` (Kubernetes: a `hostPath` volume of type `Directory`). The
  id always matches the infra agent, also when it uses `product_uuid` or a generated id. The infra agent writes the file
  at start when the directory exists (systemd creates it; for a containerized infra agent, mount a host directory there)
- mount the host's id read-only: `-v /etc/machine-id:/etc/machine-id:ro` (Kubernetes: a `hostPath` volume of type `File`)
- set `OPENLOG_HOST_ID` explicitly
- mount the host root and set `OPENLOG_HOST_ROOT=/host`, like the infra agent's `host.root_path`

When the container runs as root, `/sys/class/dmi/id/product_uuid` is usually the host's and resolves the same id
without a mount, because sysfs is not namespaced.

## Instrumentation

| Package | Module | Covers |
|---|---|---|
| `openloghttp` | core | `Middleware(mux)` (ServeMux patterns → `http.route`, span `GET /users/{id}`), `Handler(h, route)`, `RouteMiddleware(fn)`, `SetRoute(r, route)`, `Transport(rt)`, `Client()` |
| `openlogsql` | core | `Open(driver, dsn)`, `Register(driver)`, `OpenDB(connector, driver)`, `WrapDriver`. Client spans with `db.system.name` (+ legacy `db.system`), `db.namespace`, `db.operation.name`, `db.query.text` (sanitized), `server.address`, `server.port`; errors recorded |
| `openlogslog` | core | `Wrap(handler)`: exports records as OTLP logs with trace/span ids and adds `trace_id`/`span_id` to your own handler's output. `NewHandler()`: OTLP only |
| `openloggrpc` | `instrumentation/grpc` | `ServerOption()`, `DialOption()` (otelgrpc stats handlers) |
| `openlogchi` | `instrumentation/chi` | `r.Use(openlogchi.Middleware())`, route = chi pattern |
| `openloggin` | `instrumentation/gin` | `r.Use(openloggin.Middleware())` (otelgin), route = `c.FullPath()` |
| `openlogecho` | `instrumentation/echo` | `e.Use(openlogecho.Middleware())` (otelecho), route = `c.Path()` |

Router and gRPC helpers are separate modules, so only their users pull gin, echo, chi or grpc into `go.sum`.

```go
mux := http.NewServeMux()
mux.HandleFunc("GET /users/{id}", getUser)
db, _ := openlogsql.Open("pgx", os.Getenv("DATABASE_URL"))
slog.SetDefault(slog.New(openlogslog.Wrap(slog.NewJSONHandler(os.Stdout, nil))))
http.ListenAndServe(":8080", openloghttp.Middleware(mux))
```

**SQL details.** Statements get spans only inside an existing span, such as a request or a job; pass `WithRootSpans()`
to change that. A query span ends when `Query` returns, not after the rows are read. The sanitizer replaces string,
numeric, hex and dollar-quoted literals with `?`, collapses `IN (…)` and multi-row `VALUES` to `(?)`, removes comments
and keeps placeholders. MySQL `"…"` is treated as a string; for other databases `"…"` is an identifier. Query text is
capped at 4096 bytes.

### Runtime metrics

The metrics are read from `runtime/metrics` at every export (every 60 s by default). There is no extra goroutine and
no `ReadMemStats` stop-the-world.

| Metric | Type | Unit |
|---|---|---|
| `go.memory.used` (`go.memory.type` = `stack`/`other`) | Sum, non-monotonic | `By` |
| `go.memory.limit` (only when `GOMEMLIMIT` is set) | Sum, non-monotonic | `By` |
| `go.memory.allocated` | Sum, monotonic | `By` |
| `go.memory.allocations` | Sum, monotonic | `{allocation}` |
| `go.memory.gc.goal` | Sum, non-monotonic | `By` |
| `go.goroutine.count` | Sum, non-monotonic | `{goroutine}` |
| `go.processor.limit` | Sum, non-monotonic | `{thread}` |
| `go.config.gogc` | Sum, non-monotonic | `%` |
| `go.schedule.duration` | Histogram | `s` |
| `openlog.go.gc.cycles` | Sum, monotonic | `{gc_cycle}` |
| `openlog.go.gc.pause.duration` | Histogram | `s` |

The two histograms fold the runtime's fine-grained buckets into 15 bounds from 10 µs to 1 s. Their `sum` is estimated
from bucket midpoints.

## Overhead

`make bench` (Apple M1 Pro, Go 1.26, real SDK with a batch processor and a discarding exporter):

| Benchmark | ns/op | B/op | allocs/op |
|---|---|---|---|
| HTTP handler, plain ServeMux | 955 | 1056 | 12 |
| HTTP handler, instrumented, sampled | 5257 | 6900 | 50 |
| HTTP handler, instrumented, not sampled | 2427 | 3897 | 42 |
| SQL query (fake driver), plain | 337 | 241 | 7 |
| SQL query, instrumented, sanitized text | 3267 | 2776 | 19 |
| SQL query, instrumented, raw text | 2505 | 2616 | 17 |
| `Sanitize` alone | 436 | 160 | 2 |

In short: about 4 µs per sampled HTTP request and about 3 µs per traced SQL statement. The HTTP figure includes
the server metrics histogram. Both are negligible next to real network and database latency. At high rates, use
`OPENLOG_SAMPLING_RATIO` to cut the per-span cost.

## Versioning and upgrading

The agent is versioned with the openlog product (D-025). Backend, UI and all agents share one `X.Y.Z`. Every product
release `vX.Y.Z` also publishes Go module tags on the same commit: `agents/go/vX.Y.Z` and
`agents/go/instrumentation/{grpc,chi,gin,echo}/vX.Y.Z`. Pre-releases (`vX.Y.Z-beta.N`) are tagged the same way.
`go get …@latest` skips them, so request them explicitly. The backend accepts agents from its last three minor
versions, so upgrade the agent within that window:

```sh
go get github.com/onuragtas/openlog/agents/go@vX.Y.Z
go get github.com/onuragtas/openlog/agents/go/instrumentation/grpc@vX.Y.Z   # if used (likewise chi, gin, echo)
```

Keep the core and instrumentation modules on the same version. `telemetry.distro.version` reports the version in use.
OpenTelemetry dependencies are pinned per release (see `go.mod`). When your application also requires OTel
packages, Go's minimal version selection picks the higher version; stay on the same OTel minor to avoid surprises.

## Development

```sh
make -C agents/go test        # every module
make -C agents/go vet fmt-check bench
```

Each directory with a `go.mod` is its own module. `instrumentation/chi` and `examples` require the in-repo modules at
the release version and use `replace` directives that point at the local directories. A `replace` only applies to the
main module, so it affects development in this repository and is ignored by consumers. `make go-agent-verify` (repo
root, also run by CI) builds these modules without their `replace` directives against a local proxy of the tree. This
catches requires that would break for consumers. The release flow is described in
[releasing.md](../../docs/operations/releasing.md#go-agent-modules).
