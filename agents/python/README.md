# openlog Python agent

Apache-2.0 · PyPI package `openlog-agent` · import `openlog_agent` · Python 3.9 – 3.13

This is a thin distribution of the OpenTelemetry Python SDK and contrib instrumentations (D-073), built like the
[Go](../go/README.md) and [Node.js](../node/README.md) agents. One command sends traces, metrics (including Python
runtime metrics) and logs to openlog over OTLP. The resource carries the same `host.id` as the openlog infra agent, so
a service is linked to the host it runs on. Sampling decisions interoperate with the other agents through W3C
`tracestate`.

(The PyPI name `openlog` belongs to an unrelated project; install `openlog-agent`.)

## Quick start

```sh
pip install openlog-agent
```

Zero code changes: prefix the command that starts the application.

```sh
export OPENLOG_LICENSE_KEY=dev-license-key OPENLOG_ENDPOINT=http://localhost:4318 OPENLOG_SERVICE_NAME=checkout
openlog-instrument python app.py
openlog-instrument gunicorn -w 4 -b 0.0.0.0:8000 shop.wsgi:application
openlog-instrument uvicorn main:app --host 0.0.0.0 --workers 4
openlog-instrument celery -A proj worker --concurrency 8
openlog-instrument uwsgi --master --processes 4 --threads 2 --die-on-term --http :8000 --wsgi-file app.py
DJANGO_SETTINGS_MODULE=shop.settings openlog-instrument python manage.py runserver --noreload
```

Or from code, before the application imports its framework and creates the app, engine or clients:

```python
# first lines of the entry point (wsgi.py, main.py, manage.py, celery app module)
import openlog_agent

openlog_agent.start(service_name="checkout", service_version="1.4.0", environment="production")
```

`start()` installs the global TracerProvider, MeterProvider and LoggerProvider and the OpenTelemetry instrumentation
of every supported library that is installed, so OpenTelemetry API usage (`opentelemetry.trace.get_tracer(...)`) works
unchanged. It only raises for invalid configuration (`ConfigError`) or when an agent started from code is already
running; under `openlog-instrument` it returns the running agent. `openlog-instrument` never raises into the
application: invalid configuration is reported on stderr and the program runs without the agent. Export problems
(ingest down, `401`, `503`) are logged to stderr, rate limited, and never block the application.

Buffered telemetry is flushed at interpreter exit (bounded by `OPENLOG_SHUTDOWN_TIMEOUT`) and on `SIGTERM` when the
program has no handler of its own; the signal is then re-raised, so the process ends as it would without the agent
(`OPENLOG_SHUTDOWN_ON_SIGNAL=false` turns this off). Servers that handle `SIGTERM` themselves (gunicorn, uvicorn, Celery)
exit normally and flush at exit. `openlog_agent.shutdown()` and `get_agent().force_flush()` are available for other
cases (e.g. short-lived jobs that end with `os._exit`).

`openlog-instrument` is a wrapper of `opentelemetry-instrument`: it puts OpenTelemetry's `sitecustomize` on
`PYTHONPATH` and selects the openlog distro/configurator (`OTEL_PYTHON_DISTRO=openlog`,
`OTEL_PYTHON_CONFIGURATOR=openlog`), so it also works when `opentelemetry-distro` is installed. Child Python processes
inherit it. An application with its own `sitecustomize` module should call `openlog_agent.start()` instead.

## Configuration

Precedence: **`start()` options > `OPENLOG_*` > `OTEL_*` > defaults**. The names are the Go and Node.js agents'.

| Variable | Option | Default | Meaning |
|---|---|---|---|
| `OPENLOG_LICENSE_KEY` | `license_key` | — | Ingest license key, sent as header `openlog-license-key`. Without it, openlog ingest answers `401` (a warning is logged) |
| `OPENLOG_ENDPOINT` (`OTEL_EXPORTER_OTLP_ENDPOINT`) | `endpoint` | `http://localhost:4318` (grpc: `http://localhost:4317`) | OTLP base URL. `http/protobuf` appends `/v1/traces`, `/v1/metrics`, `/v1/logs`. `http://` = plaintext, `https://` or no scheme = TLS |
| `OPENLOG_PROTOCOL` (`OTEL_EXPORTER_OTLP_PROTOCOL`) | `protocol` | `http/protobuf` | or `grpc` (`pip install 'openlog-agent[grpc]'`) |
| `OPENLOG_COMPRESSION` (`OTEL_EXPORTER_OTLP_COMPRESSION`) | `compression` | `gzip` | or `none` |
| `OTEL_EXPORTER_OTLP_HEADERS` | `headers` | — | Extra headers (the license key header wins) |
| `OPENLOG_SERVICE_NAME` (`OTEL_SERVICE_NAME`) | `service_name` | `unknown_service:<script>` | `service.name` |
| `OPENLOG_SERVICE_VERSION` | `service_version` | — | `service.version` |
| `OPENLOG_SERVICE_NAMESPACE` | `service_namespace` | — | `service.namespace` |
| `OPENLOG_ENVIRONMENT` | `environment` | — | `deployment.environment.name` |
| `OPENLOG_SAMPLING_RATIO` (`OTEL_TRACES_SAMPLER_ARG` with a `*traceidratio` sampler) | `sampling_ratio` | `1` | Parent-based head sampling of new traces (0..1), see [Sampling](#sampling) |
| `OPENLOG_SAMPLING_RV` | `sampling_rv` | `false` | New traces write explicit randomness `ot=rv` |
| `OPENLOG_RESOURCE_ATTRIBUTES` (merged over `OTEL_RESOURCE_ATTRIBUTES`) | `resource_attributes` | — | `k=v,k2=v2` (percent-encoded values allowed) |
| `OPENLOG_HOST_ID` | `host_id` | detected | Explicit `host.id` |
| `OPENLOG_HOST_ROOT` | `host_root` | `/` | Prefix for host files when the host root is mounted (e.g. `/host`) |
| `OPENLOG_INFRA_RUNTIME_DIR` | `infra_runtime_dir` | `/run/openlog-infra-agent` | Where a running infra agent publishes its host id (`host-id`) |
| `OPENLOG_INFRA_STATE_DIR` | `infra_state_dir` | `/var/lib/openlog-infra-agent` | Where the infra agent persists a generated host id |
| `OPENLOG_STATE_DIR` | `state_dir` | user cache dir `/openlog` | Where this agent persists a generated host id (last resort) |
| `OPENLOG_RUNTIME_METRICS` | `runtime_metrics` | `true` | Python runtime and process metrics |
| `OPENLOG_METRIC_EXPORT_INTERVAL` (Go duration; `OTEL_METRIC_EXPORT_INTERVAL` in ms) | `metric_interval` (seconds) | `60s` | Metric export interval |
| `OPENLOG_SHUTDOWN_TIMEOUT` (Go duration) | `shutdown_timeout` (seconds) | `5s` | Bound of the final flush |
| — | `export_timeout` (seconds) | `10` | Timeout of one export request |
| `OPENLOG_LOG_LEVEL` (`OTEL_LOG_LEVEL`) | `log_level` | `warn` | Agent and OpenTelemetry SDK diagnostics on stderr: `debug`, `info`, `warn`, `error`, `off` |
| `OPENLOG_ENABLED` (`OTEL_SDK_DISABLED`) | `enabled` | `true` | `false`: nothing is installed, no library is instrumented |
| `OPENLOG_DB_QUERY_TEXT` | `db_query_text` | `sanitized` | `sanitized`, `raw` or `off` for `db.query.text` / `db.statement` |
| `OPENLOG_LOGS_EXPORT` | `logs_export` | `true` | Export stdlib `logging` records as OTLP logs |
| `OPENLOG_LOGS_CORRELATION` | `logs_correlation` | `true` | Add `trace_id`, `span_id`, `trace_flags` to every `LogRecord` |
| `OPENLOG_INSTRUMENTATIONS_DISABLED` (`OTEL_PYTHON_DISABLED_INSTRUMENTATIONS`) | `disabled_instrumentations` | — | Comma-separated names (`celery,grpc`), aliases or package names |
| — | `instrumentation_config` | — | Keyword arguments per instrumentation merged over the agent defaults, e.g. `{"flask": {"excluded_urls": "static/.*"}, "requests": {"request_hook": hook}}` (start from code only) |
| `OPENLOG_HTTP_IGNORE_PATHS` | `http_ignore_paths` | — | Incoming request paths without spans (exact match), e.g. `/healthz,/readyz` |
| `OPENLOG_SHUTDOWN_ON_SIGNAL` | `shutdown_on_signal` | `true` | Flush on `SIGTERM` when the program has no handler |

The agent also sets these upstream variables unless they are already set: `OTEL_SEMCONV_STABILITY_OPT_IN=http,database`
(stable HTTP and database semantic conventions: `http.request.method`, `http.server.request.duration` in seconds,
`db.query.text`, `db.system.name` — the names the Go and Node.js agents send), `OTEL_PYTHON_<FRAMEWORK>_EXCLUDED_URLS`
from `OPENLOG_HTTP_IGNORE_PATHS`, and `OTEL_PYTHON_LOG_AUTO_INSTRUMENTATION=false` when `OPENLOG_LOGS_EXPORT=false`.
Other `OTEL_PYTHON_*` instrumentation variables (captured headers, excluded URLs, …) work as documented upstream.

**Export behaviour.** Spans are batched: queue 4096, batch 512, flushed every 5 s. Logs are batched the same way,
flushed every 2 s. Export requests time out after 10 s. The OpenTelemetry exporters retry transient failures
(`429/502/503/504`, gRPC `UNAVAILABLE`) with exponential backoff. When the queue is full, new spans are dropped rather
than blocking the application. Metrics are cumulative. The agent's own export requests are never traced.

## Framework and library support

Instrumentations are pinned OpenTelemetry contrib packages installed with the agent (see `pyproject.toml`). An
instrumentation is activated only when its library is installed in a supported version.

| Name (`OPENLOG_INSTRUMENTATIONS_DISABLED`) | Library | Spans / signals |
|---|---|---|
| `django` | Django | SERVER spans named `<METHOD> /<route>` (the agent adds the leading `/` to resolver routes), exceptions, `http.server.request.duration`. Needs `DJANGO_SETTINGS_MODULE` |
| `flask` | Flask | SERVER spans `<METHOD> <url rule>`, `http.route` set by the agent, exceptions, HTTP metrics |
| `fastapi`, `starlette` | FastAPI, Starlette (ASGI) | SERVER spans `<METHOD> <route>`, exceptions, HTTP metrics; the per-message `http send`/`http receive` spans are turned off |
| `tornado` | Tornado ≥ 5.1 | SERVER spans `<METHOD> <route>` from the matched rule (the agent sets `http.route`, which upstream only puts on the metrics), exceptions, HTTP metrics; `AsyncHTTPClient` CLIENT spans |
| `falcon` | Falcon 1.4 – 4.x | SERVER spans `<METHOD> <uri template>` with `http.route`; the agent records the responder's exception as an `exception` event (upstream only sets the status; `HTTPError`/`HTTPStatus` are not recorded); HTTP metrics without `http.route` |
| `pyramid` | Pyramid ≥ 1.7 | SERVER spans `<METHOD> <route pattern>` with `http.route` (upstream names them with the pattern only; the agent adds the method), exceptions, HTTP metrics without `http.route` |
| `requests`, `httpx`, `aiohttp-client`, `urllib`, `urllib3` | HTTP clients | CLIENT spans, W3C propagation, `http.client.request.duration` |
| `psycopg2`, `psycopg`, `asyncpg` | PostgreSQL | CLIENT spans, `db.query.text` sanitized |
| `pymysql`, `mysqlclient` | MySQL / MariaDB | CLIENT spans, `db.query.text` sanitized (`"…"` is a string) |
| `sqlalchemy` | SQLAlchemy 1.x, 2.0 | CLIENT spans per statement (plus the driver's spans) |
| `redis` | redis-py (sync and asyncio) | CLIENT spans, `db.query.text` = `SET ? ?` |
| `celery` | Celery | PRODUCER `apply_async/<task>` and CONSUMER `run/<task>` spans with propagation through the broker |
| `aio-pika` (alias `rabbitmq`) | aio-pika 7.2 – 9.x (RabbitMQ) | PRODUCER spans for `Exchange.publish`, CONSUMER spans around `Queue.consume` callbacks as children of the producer span (W3C context in the message headers), `messaging.system=rabbitmq` |
| `confluent_kafka` | confluent-kafka 1.8 – 2.x | PRODUCER `<topic> send` spans; CONSUMER spans for `poll`/`consume` **linked** to the producer spans (one poll can return messages of several traces), `messaging.system=kafka` |
| `kafka` (`kafka-python`) | kafka-python 2.x (3.x is not supported upstream yet) | PRODUCER `<topic> send`, CONSUMER `<topic> receive` per record (iteration) as a child of the producer span |
| `aiokafka` | aiokafka 0.8 – 0.x | PRODUCER `<topic> send`, CONSUMER `<topic> receive` (`getone`/`getmany`) as a child of the producer span |
| `grpc` (`grpc_client`, `grpc_server`, `grpc_aio_client`, `grpc_aio_server`) | grpcio | SERVER/CLIENT spans (`rpc.system`, `rpc.service`, `rpc.method`, `rpc.grpc.status_code`) |
| `logging` | stdlib `logging` | OTLP log export of records that pass the logger levels, with trace context |

`kafka-all` disables the three Kafka instrumentations at once.

The CI suite runs on Python 3.9, 3.12 and 3.13: Flask 3.1, Django 4.2 (Python 3.9) and 6.1 (3.12+), FastAPI
0.128/0.141 with uvicorn, Tornado 6.5, Falcon 4.3, Pyramid 2.0/2.1, gunicorn 23/26, uWSGI 2.0.31, gevent 25.9/26.8,
eventlet 0.40/0.41, psycopg2 2.9, psycopg 3.3, asyncpg 0.31, PyMySQL 1.2, mysqlclient 2.2, SQLAlchemy 2.0, redis 8.1,
Celery 5.6, grpcio 1.83, aiohttp 3.14, aio-pika 9.6 (RabbitMQ 4.1), confluent-kafka 2.15, kafka-python 2.3 and aiokafka
0.14 (Kafka 4.1) (the database, broker, Celery and gRPC suite on Python 3.12).

**Transaction names.** The APM backend names a web transaction `<METHOD> <http.route>`
([apm.md §2.1](../../docs/contracts/apm.md)). Requests without a route (404s) are grouped by the backend's path
normalization. gRPC transactions are `<rpc.service>/<rpc.method>`; Celery tasks and RabbitMQ/Kafka consumers are
messaging (`consumer`) entry spans.

**Database statements.** With `OPENLOG_DB_QUERY_TEXT=sanitized` (default) SQL is normalized exactly like the Go and
Node.js agents (shared test cases): string, numeric, hex and dollar-quoted literals → `?`, `IN (…)` and multi-row
`VALUES` → `(?)`, comments removed, placeholders (`%s`, `%(name)s`, `$1`, `?`) kept; MySQL `"…"` is a string, otherwise
an identifier. Redis commands become `CMD ? ?`. Statements are capped at 4096 UTF-8 bytes. `raw` keeps the text, `off`
removes it. Sanitization runs on the exporter thread, not in the request. Statement parameters are never captured.

## Logs

stdlib `logging` records that pass the logger's level are exported as OTLP logs with the trace and span id of the
active span (the OpenTelemetry logging handler on the root logger). The application's own output gets the ids too:
every `LogRecord` has `trace_id`, `span_id` and `trace_flags` (empty outside a span), so a format such as

```python
logging.basicConfig(format="%(asctime)s %(levelname)s %(message)s trace_id=%(trace_id)s span_id=%(span_id)s")
```

or a JSON formatter shows them. structlog:

```python
import structlog, openlog_agent

structlog.configure(processors=[openlog_agent.structlog_processor, structlog.processors.JSONRenderer()])
```

With `structlog.stdlib.LoggerFactory()` the records also go through stdlib logging and are exported as OTLP logs.

## Pre-fork servers and workers

The agent is safe to start before the server forks:

- The OpenTelemetry SDK restarts its export threads in each child (`os.register_at_fork`), and the agent updates
  `process.pid` in the resource, so every worker exports its own spans and metrics under its own pid. The e2e suite
  checks this with gunicorn (`openlog-instrument gunicorn -w 3`, and `--preload` with `start()` in the app module) and
  Celery's prefork pool: no span is lost and every worker pid appears.
- gunicorn and Celery workers flush at exit on graceful shutdown. Celery prefork children that are killed or recycled
  (`--max-tasks-per-child`) lose at most the last 5 s of buffered spans.
- `uvicorn --workers N` and `multiprocessing` with the `spawn` start method start fresh interpreters; under
  `openlog-instrument` each initializes the agent itself (inherited `PYTHONPATH`). With `start()` from code, call it in
  the module the worker imports.

When the agent should start only in the workers (for example, the application creates threads or connections at import
time that must not be shared across fork), start it from gunicorn's `post_fork` hook and do not use `--preload`:

```python
# gunicorn.conf.py
from openlog_agent import post_fork  # starts the agent in each worker
```

### uWSGI

uWSGI forks its workers in C, without Python's fork hooks, so export threads started in the master would not run in the
workers (requests hang on locks held by the missing threads). The agent therefore detects the uWSGI master, under
`openlog-instrument` and with `start()` in a module the master imports, and only installs the instrumentations there;
the providers and export threads start in each worker from `uwsgi.post_fork_hook` (kept first in `uwsgidecorators`'
`@postfork` chain when the application imports it). On Python < 3.12 the `uwsgi` module does not exist yet when
`openlog-instrument` starts; the agent sets the hook at the first `import` statement that runs once uWSGI created it
(loading the application), and then removes its import hook. `get_agent()` returns a pending agent in the master.

```ini
[uwsgi]
master = true
processes = 4
threads = 2          ; optional
lazy-apps = false    ; both work
die-on-term = true   ; uWSGI 2.0 reloads on SIGTERM otherwise
; no py-call-osafterfork: not needed, and with Python 3.13 it aborts the workers (Fatal Python error: PyMutex_Unlock)
```

```sh
openlog-instrument uwsgi --ini uwsgi.ini --http :8000 --wsgi-file app.py
```

The e2e suite checks `--master --processes 3` with and without `lazy-apps` and `threads`, with `uwsgidecorators`, and
with `start()` from code: every worker exports under its own `process.pid`, the master exports nothing, W3C propagation
works between workers, and a graceful stop (`SIGINT`/`SIGTERM` with `die-on-term`) loses no span, because workers flush
at exit. If uWSGI logs `Python threads support is disabled` (older versions), add `enable-threads = true`, otherwise the
export threads do not run between requests. Workers recycled with `max-requests` flush at exit like any other.

### gevent and eventlet

Monkey-patched servers work under `openlog-instrument` (the agent starts before the patch) and with
`openlog_agent.start()` called right after `gevent.monkey.patch_all()` / `eventlet.monkey_patch()`; patching first is
the order gevent and eventlet recommend. The e2e suite checks `gevent.pywsgi`, `eventlet.wsgi` and
`gunicorn -k gevent` workers with concurrent requests: no span is lost, the active span follows each greenlet (the
spans of concurrent requests never mix), and `SIGTERM` flushes (the flush runs in a greenlet, because the event loop
runs signal handlers where blocking is not allowed).

A greenlet spawned inside a request starts with an empty context, like a new thread: its spans start new traces. Run it
in a copy of the request's context to keep it in the trace, one copy per greenlet:

```python
import contextvars, gevent

gevent.spawn(contextvars.copy_context().run, fetch_price, item_id)   # eventlet.spawn(...) likewise
```

Under `openlog-instrument`, gevent may print harmless `KeyError` tracebacks from `threading` when the process exits
(threads started before the patch). gunicorn 26 no longer has an eventlet worker.

## Sampling

`OPENLOG_SAMPLING_RATIO=p` samples new traces with probability `p`; downstream services follow the decision
(parent-based). APM counts stay correct because each stored span is weighted by `1/p`
([apm.md §4](../../docs/contracts/apm.md)). The semantics are identical to the Go agent
([README](../go/README.md#sampling)) and verified against the fixtures generated from its sampler
(`agents/node/test/interop/go-sampler-fixtures.json`):

- **Root spans.** `T = (1 − p) · 2^56`; the randomness is `ot=rv` when present, else the lower 56 bits of the trace id;
  sampled when `R ≥ T`. A sampled root gets `sampling.ratio = p` and `tracestate` `ot=th:<T>` (e.g. `p = 0.25` → `ot=th:c`).
- **Downstream.** A local entry span whose sampled remote parent carries `ot=th` (or legacy `ot=p`) with `p < 1` gets
  `sampling.ratio = p`; the local ratio does not change an upstream decision.
- **Random flag.** Root spans carry the W3C Level 2 random flag (`traceparent` flags `03`/`02`); children inherit it.
  `OPENLOG_SAMPLING_RV=true` writes explicit `ot=rv:<14 hex>` on new traces.
- **Limits.** The `ot` value is capped at 256 characters (unknown sub-keys dropped first) and `tracestate` at 32 members.

## Resource

| Attribute | Source |
|---|---|
| `service.name`, `service.version`, `service.namespace`, `deployment.environment.name` | configuration |
| `host.id` | see [Host linking](#host-linking) |
| `host.name`, `host.arch` | Linux: `/proc/sys/kernel/hostname` → `/etc/hostname`; `/proc/sys/kernel/arch` (OTel values, same as the infra agent); else `socket.gethostname()`, `platform.machine()` |
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

Read on every metric collection (every 60 s by default); scope `openlog_agent.runtime`. Names follow the OpenTelemetry
semantic conventions (the Go agent's policy: semconv names where they exist, `openlog.` prefix otherwise).

| Metric | Type | Unit | Attributes |
|---|---|---|---|
| `cpython.gc.collections` | Sum, monotonic | `{collection}` | `cpython.gc.generation` = 0/1/2 |
| `cpython.gc.collected_objects`, `cpython.gc.uncollectable_objects` | Sum, monotonic | `{object}` | `cpython.gc.generation` |
| `openlog.cpython.gc.time` | Sum, monotonic | `s` | `cpython.gc.generation` (total time in collections) |
| `openlog.cpython.gc.pause.max` | Gauge | `s` | `cpython.gc.generation` (longest collection since the previous export) |
| `process.cpu.time` | Sum, monotonic | `s` | `cpu.mode` = `user`/`system` |
| `process.memory.usage`, `process.memory.virtual` | Sum, non-monotonic | `By` | — (RSS / virtual; Linux, other platforms when `psutil` is installed) |
| `process.thread.count` | Sum, non-monotonic | `{thread}` | — |
| `process.open_file_descriptor.count` | Sum, non-monotonic | `{file_descriptor}` | — (Linux) |
| `process.context_switches` | Sum, monotonic | `{context_switch}` | `process.context_switch.type` = `voluntary`/`involuntary` (Linux) |

GC time is a counter plus a max gauge rather than a histogram: the `gc.callbacks` hook only updates plain numbers,
because recording into an SDK instrument from inside a garbage collection could re-enter SDK locks. The web framework
instrumentations also record `http.server.request.duration`, the HTTP clients `http.client.request.duration`.

## Overhead

`make bench` (`tests/bench/overhead.py`): single-process servers saturated by 8 load generator processes on persistent
connections (8 s × 3 runs, median), with the real agent exporting to a local OTLP capture server (all instrumentations,
logging handler and runtime metrics enabled; the route does not log). Measured with Python 3.12 in Docker Desktop
(arm64, 10 CPUs, load generator on the same machine):

Flask 3.1 / gunicorn (1 `gthread` worker, 1 thread):

| Scenario | req/s | vs. no agent | p50 ms | p99 ms | RSS MB | added per request |
|---|---|---|---|---|---|---|
| no agent | 2615 | — | 2.30 | 9.34 | 37 | — |
| agent, sampled (ratio 1) | 1651 | −36.9 % | 3.99 | 26.24 | 70 | 0.22 ms |
| agent, not sampled (ratio 0) | 2638 | +0.9 % | 2.40 | 6.26 | 65 | ≈ 0 (within noise) |

FastAPI 0.141 / uvicorn (1 process):

| Scenario | req/s | vs. no agent | p50 ms | p99 ms | RSS MB | added per request |
|---|---|---|---|---|---|---|
| no agent | 7726 | — | 0.88 | 3.57 | 47 | — |
| agent, sampled (ratio 1) | 1923 | −75.1 % | 2.39 | 28.04 | 84 | 0.39 ms |
| agent, not sampled (ratio 0) | 3826 | −50.5 % | 1.63 | 8.81 | 78 | 0.13 ms |

The server is saturated, so its service time per request is `1/rps`: the agent adds about **0.2 ms (WSGI) to 0.4 ms
(ASGI) per sampled request** (server span, HTTP metrics, batching and export on a thread that shares the GIL) plus
~30–40 MB RSS. Unsampled requests are nearly free with Flask; with FastAPI the upstream ASGI middleware still costs
~0.13 ms per request. The relative drop is large only because these handlers do almost nothing (0.1–0.4 ms); for a
request that spends 20 ms in application code and I/O the sampled overhead is 1–2 %. This is in line with the upstream
OpenTelemetry Python instrumentations the agent is built on, and higher than the Go (~4 µs) and Node.js (~0.2 ms)
agents. Ways to reduce it: sampling (`OPENLOG_SAMPLING_RATIO`), `OPENLOG_INSTRUMENTATIONS_DISABLED` for libraries you do
not need, and `OPENLOG_HTTP_IGNORE_PATHS` for health checks. These are micro-benchmark numbers from a shared development
machine, not a dedicated benchmark host.

## Versioning and upgrading

The package is versioned with the openlog product (D-025): every product release `vX.Y.Z` publishes
`openlog-agent==X.Y.Z` to PyPI (pre-releases `vX.Y.Z-beta.N` as `X.Y.ZbN`, installed only with `pip install --pre`).
The backend accepts agents from its last three minor versions. OpenTelemetry dependencies are pinned exactly per
release: Python ≥ 3.10 gets SDK 1.44.0 / contrib 0.65b0; Python 3.9, which OpenTelemetry dropped in SDK 1.42, gets the
last supporting line (1.41.1 / 0.62b1) and no newer upstream fixes. If the application pins OpenTelemetry packages
itself, use the same versions.

```sh
pip install openlog-agent==X.Y.Z
```

## Development

Everything runs in containers (local Python tooling on iCloud Drive may hang):

```sh
make install test                     # Python 3.12: venv volume, unit + end-to-end tests (installs gcc for uWSGI)
make install test PYTHON_VERSION=3.13
make install test PYTHON_VERSION=3.9  # the Python 3.9 line
make lint
make test-integration                 # real PostgreSQL/MySQL/Redis/RabbitMQ/Kafka, Celery, gRPC (compose project openlog-m4-python-test)
make bench                            # overhead micro-benchmark
make build                            # wheel + sdist in dist/
```

Unit tests cover configuration, resource detection, the sampler (Go fixtures), the SQL sanitizer (cases shared with
the Go and Node.js agents), export-time span rewriting, runtime metrics and instrumentation defaults. End-to-end tests
start Flask, Django, FastAPI, Tornado, Falcon and Pyramid applications under `openlog-instrument` (and with `start()`)
against an OTLP capture server: route names, errors, propagation, HTTP and runtime metrics, correlated logs, resource,
license header, ignored paths, SIGTERM flush, disabled agent, invalid configuration, ingest outage, gunicorn and uWSGI
pre-fork workers, and gevent/eventlet servers. The integration suite produces and consumes through real RabbitMQ and
Kafka brokers (producer and consumer in one trace, or linked for confluent-kafka). The release
flow is described in [releasing.md](../../docs/operations/releasing.md#python-agent-package).
