# openlog Java agent

Apache-2.0 · `openlog-javaagent-<version>.jar` · Java 8+ applications (tested on 17 and 21)

A distribution of the [OpenTelemetry Java agent](https://github.com/open-telemetry/opentelemetry-java-instrumentation)
(D-072), built like the [Go](../go/README.md) and [Node.js](../node/README.md) agents. One JVM flag sends traces,
metrics (including JVM runtime metrics) and logs to openlog over OTLP, with no code changes. The jar is the unmodified
upstream agent (pinned, currently **2.31.1**, SDK 1.65.0) plus the openlog extension in its `extensions/` directory. The
extension adds:

- the `OPENLOG_*` configuration shared by all openlog agents;
- the infra agent's `host.id` and `container.id`, so services are linked to their hosts and containers;
- the Go agent's consistent probability sampler (`ot=th`, `sampling.ratio`, verified against the Go fixtures);
- the Go agent's DB statement sanitizer;
- the license key header and openlog defaults.

## Quick start

Download `openlog-javaagent-<version>.jar` and its `.sha256` from the [GitHub release](https://github.com/onuragtas/openlog/releases), then:

```sh
sha256sum -c openlog-javaagent-X.Y.Z.jar.sha256
export OPENLOG_LICENSE_KEY=dev-license-key OPENLOG_ENDPOINT=http://localhost:4318 OPENLOG_SERVICE_NAME=checkout
java -javaagent:/opt/openlog/openlog-javaagent-X.Y.Z.jar -jar app.jar
# or: JAVA_TOOL_OPTIONS="-javaagent:/opt/openlog/openlog-javaagent.jar"
```

Every setting can also be a system property: `OPENLOG_SERVICE_NAME` ⇔ `-Dopenlog.service.name=checkout`. System
properties win over environment variables, as they do in OpenTelemetry Java. Container images:

```dockerfile
ADD --checksum=sha256:<sha256> https://github.com/onuragtas/openlog/releases/download/vX.Y.Z/openlog-javaagent-X.Y.Z.jar /opt/openlog/openlog-javaagent.jar
ENV JAVA_TOOL_OPTIONS="-javaagent:/opt/openlog/openlog-javaagent.jar"
```

Export problems (ingest down, `401`, `503`) are logged by the agent and never throw into or block the application.
Invalid `OPENLOG_*` values are reported on stderr (`[openlog] WARN …`) and ignored.

## Configuration

Precedence: **`openlog.*` system properties > `OPENLOG_*` > `otel.*` system properties / `OTEL_*` > openlog defaults**.
The names are the Go and Node.js agents'. Every [OpenTelemetry Java agent option](https://opentelemetry.io/docs/zero-code/java/agent/configuration/)
keeps working.

| Variable (system property `openlog.…`) | OpenTelemetry equivalent | Default | Meaning |
|---|---|---|---|
| `OPENLOG_LICENSE_KEY` | header in `otel.exporter.otlp.headers` | — | Ingest license key, sent as header `openlog-license-key` (merged with `OTEL_EXPORTER_OTLP_HEADERS`; the license key wins). Without it, openlog ingest answers `401` (a warning is logged) |
| `OPENLOG_ENDPOINT` | `otel.exporter.otlp.endpoint` | `http://localhost:4318` (grpc: `:4317`) | OTLP base URL; `/v1/traces`, `/v1/metrics`, `/v1/logs` are appended for http/protobuf. No scheme = `https://` |
| `OPENLOG_PROTOCOL` | `otel.exporter.otlp.protocol` | `http/protobuf` | or `grpc` |
| `OPENLOG_COMPRESSION` | `otel.exporter.otlp.compression` | **`gzip`** (upstream: none) | or `none` |
| `OPENLOG_SERVICE_NAME` | `otel.service.name` | upstream detection (Spring Boot name, jar name, `unknown_service:java`) | `service.name` |
| `OPENLOG_SERVICE_VERSION` | `service.version` in `otel.resource.attributes` | — | `service.version` (deployments, apm.md §12) |
| `OPENLOG_SERVICE_NAMESPACE` | `service.namespace` | — | `service.namespace` |
| `OPENLOG_ENVIRONMENT` | `deployment.environment.name` | — | `deployment.environment.name` |
| `OPENLOG_RESOURCE_ATTRIBUTES` | merged over `otel.resource.attributes` | — | `k=v,k2=v2` (percent-encoded values) |
| `OPENLOG_HOST_ID` | `host.id` | detected | Explicit `host.id` |
| `OPENLOG_HOST_ROOT` | — | `/` | Prefix for host files when the host root is mounted (e.g. `/host`) |
| `OPENLOG_INFRA_RUNTIME_DIR` | — | `/run/openlog-infra-agent` | Where a running infra agent publishes its host id (`host-id`) |
| `OPENLOG_INFRA_STATE_DIR` | — | `/var/lib/openlog-infra-agent` | Where the infra agent persists a generated host id |
| `OPENLOG_STATE_DIR` | — | user cache dir `/openlog` | Where this agent persists a generated host id (last resort) |
| `OPENLOG_SAMPLING_RATIO` | `otel.traces.sampler.arg` of `(parentbased_)traceidratio` | `1` | Parent-based head sampling of new traces (0..1), see [Sampling](#sampling) |
| `OPENLOG_SAMPLING_RV` | — | `false` | New traces write explicit randomness `ot=rv` |
| `OPENLOG_HTTP_IGNORE_PATHS` | — | — | Incoming request paths without spans (exact match), e.g. `/healthz,/actuator/health` |
| `OPENLOG_DB_QUERY_TEXT` | — | `sanitized` | `sanitized`, `raw` or `off` for `db.query.text` / `db.statement` |
| `OPENLOG_RUNTIME_METRICS` | `otel.instrumentation.runtime-telemetry.enabled` | `true` | JVM runtime metrics |
| `OPENLOG_METRIC_EXPORT_INTERVAL` (Go duration) | `otel.metric.export.interval` (ms) | `60s` | Metric export interval |
| `OPENLOG_LOGS_EXPORT` | `otel.logs.exporter=none` when `false` | `true` | Export Logback/Log4j2/JUL records as OTLP logs (trace ids stay in the MDC either way) |
| `OPENLOG_INSTRUMENTATIONS_DISABLED` | `otel.instrumentation.<name>.enabled=false` | — | Comma-separated upstream instrumentation names, e.g. `jdbc,kafka,spring-webmvc` |
| `OPENLOG_ENABLED` | `otel.sdk.disabled=true` when `false` | `true` | `false`: no telemetry (instrumentation stays installed as no-op; `OTEL_JAVAAGENT_ENABLED=false` skips the agent entirely) |
| `OPENLOG_LOG_LEVEL` (`OTEL_LOG_LEVEL`) | — | `warn` | openlog diagnostics on stderr: `debug`, `info`, `warn`, `error`, `off` (agent internals: `OTEL_JAVAAGENT_DEBUG=true`) |

Not applicable to Java: `OPENLOG_SHUTDOWN_TIMEOUT` (the agent flushes in a JVM shutdown hook), `OPENLOG_SHUTDOWN_ON_SIGNAL`,
`OPENLOG_LOGS_CONSOLE` (`System.out` is not a logging API).

**Other defaults.** The openlog defaults are the OTLP/HTTP protobuf exporter for traces, metrics and logs, `gzip`, W3C
`tracecontext` + `baggage` propagation, runtime metrics, and the upstream DB sanitizer turned off (see
[Database statements](#database-statements)). Export behavior is upstream's: spans are batched (queue 2048, batch
512, 5 s), log records likewise (1 s), exports time out after 10 s, and retryable HTTP/gRPC responses are retried
with exponential backoff. When the queue is full, spans are dropped rather than blocking the application. Metrics
are cumulative.

## Framework and library support

Every library the [upstream agent 2.31.1 supports](https://github.com/open-telemetry/opentelemetry-java-instrumentation/blob/v2.31.1/docs/supported-libraries.md)
is instrumented: 100+ frameworks, servers, clients, databases, messaging systems and logging libraries. The
integration tests (`integration-tests/`) run these with the openlog jar against real services:

| Area | Verified with | What is asserted |
|---|---|---|
| Spring Boot 3 Web MVC (Tomcat) | Spring Boot 3.5 | SERVER span `GET /users/{id}` with `http.route`; `POST /orders`; 500 → status ERROR + `exception` event with Java stack trace |
| Spring Boot 3 WebFlux (Reactor Netty) | Spring Boot 3.5 | `GET /items/{id}` with `http.route`; errors; head sampling at ratio 0.5 |
| JDBC + Hibernate/JPA | PostgreSQL 16 (pgJDBC), MySQL 8.4 (Connector/J), Hibernate 6.6 | CLIENT spans with statements sanitized exactly like the Go agent, Hibernate spans, JDBC spans inside the request trace, no literal values in any attribute |
| Redis | Lettuce (Spring Data Redis), Jedis | `SET ? ?`, `GET ?` for both clients, values never exported |
| Kafka | kafka-clients 3.9 | PRODUCER span in the request trace, CONSUMER span linked to it, consumer log records correlated |
| gRPC | grpc-java 1.84 (Netty) | CLIENT and SERVER spans `demo.Greeter/SayHello`, parent/child across the call |
| Logback | Spring Boot default | `trace_id`/`span_id` in the MDC (application output) and OTLP log records with trace/span ids |
| Log4j2 | spring-boot-starter-log4j2 | same, through the Log4j2 context data provider |
| JVM runtime | JDK 17, 21 | `jvm.memory.used`, `jvm.thread.count`, `jvm.class.loaded`, `jvm.cpu.time`; `http.server.request.duration`; disabling runtime metrics |
| JAX-RS | Jersey 3.1 `ServletContainer` on embedded Jetty 12 (ee10) | SERVER span `GET /users/{id}` with `http.route`, W3C parent, JDBC CLIENT span in the request trace with the sanitized statement, 500 → ERROR, ignored paths, openlog resource |
| Quarkus (JVM mode) | Quarkus 3.39, Quarkus REST, `quarkus-run.jar` | same as JAX-RS (`@Blocking` resource method) |
| Vert.x Web | Vert.x 4.5 router, JDBC in `executeBlocking` | same, `http.route` `/users/:id` (Vert.x syntax) |
| Micronaut | Micronaut 4.10 (Netty), `@ExecuteOn(BLOCKING)` | JDBC span in the request trace, errors, resource; **no `http.route`** (see below) |

Servlet containers, JMS, RabbitMQ, MongoDB, Cassandra, Elasticsearch, AWS SDK, OkHttp, Apache HttpClient and the rest of
the upstream list are instrumented by the same agent but are not part of the openlog test suite.

**Transaction names.** The APM backend names a web transaction `<METHOD> <http.route>` ([apm.md §2.1](../../docs/contracts/apm.md)).
The upstream instrumentations set `http.route` on the HTTP server span for Spring MVC, WebFlux, JAX-RS on a servlet
container, Quarkus REST, Vert.x Web, Ktor, Play, Grails, Struts and servlet mappings, and rename the span to
`GET /users/{id}`. Requests without a route are grouped by the backend's path normalization of `url.path`. Verified
gaps of the upstream agent 2.31.1: **Micronaut** has no instrumentation (the SERVER span comes from Netty and is named
`GET`), and **Jersey on Grizzly or Jersey's Jetty handler container** (`jersey-container-grizzly2-http`,
`jersey-container-jetty-http`) gets a SERVER span without `http.route`; deploy Jersey as a servlet
(`jersey-container-servlet`) to get route names.

## Database statements

With `OPENLOG_DB_QUERY_TEXT=sanitized` (default), `db.query.text` and `db.statement` are rewritten on the export path
with the Go agent's algorithm (`openlogsql.Sanitize`, same test cases as the Go and Node.js agents):
- string, numeric, hex and dollar-quoted literals → `?`;
- `IN (…)` and multi-row `VALUES` → `(?)`;
- comments removed, whitespace collapsed, placeholders kept;
- MySQL/MariaDB `"…"` is a string, otherwise an identifier.

`redis`, `valkey` and `memcached` statements become the upper-cased command followed by one `?` per word (`SET ? ?`).
All statements are capped at 4096 characters.

Differences from upstream, stated precisely:

- The upstream sanitizer (`otel.instrumentation.common.db.query-sanitization.enabled`, formerly
  `…db-statement-sanitizer.enabled`) is **turned off by default** so the openlog sanitizer sees the original statement.
  The statement is never exported unsanitized: the rewrite runs in the span exporter chain, which every configured
  span exporter passes through. Turning the upstream sanitizer back on is allowed and is harmless (both run). Its
  output differs, though: it keeps `IN (?, ?)` lists and comments.
- `mongodb`, `elasticsearch` and `opensearch` statements are left to their upstream sanitizers (MongoDB:
  `otel.instrumentation.mongo.statement-sanitizer.enabled`, on by default).
- A Redis argument that contains spaces counts as several `?`.
- `raw` exports statements unchanged (capped); `off` removes both attributes. Span names (`SELECT app_users`) come from
  upstream and never contain values.

## Sampling

`OPENLOG_SAMPLING_RATIO=p` samples new traces with probability `p`, and downstream services follow the decision
(parent-based). APM counts stay correct because each stored span is weighted by `1/p`
([apm.md §4](../../docs/contracts/apm.md)). The sampler is a port of the Go agent's. It is verified against the
fixtures generated from the Go sampler (`agents/node/test/interop/go-sampler-fixtures.json`, run by
`SamplerFixturesTest`: `traceparent`, `tracestate`, sampled flag and `sampling.ratio` of a span and its child):

- **Root spans.** `T = (1 − p) · 2^56`. The randomness is `ot=rv` when present, else the lower 56 bits of the trace
  id; the span is sampled when `R ≥ T`. A sampled root gets `sampling.ratio = p` and `tracestate` `ot=th:<T>` (e.g.
  `p = 0.25` → `ot=th:c`).
- **Downstream.** A local entry span whose sampled remote parent carries `ot=th` (or legacy `ot=p`) with `p < 1`
  gets `sampling.ratio = p`.
- **Random flag.** Root spans carry the W3C Level 2 random flag (`traceparent` flags `03`/`02`, exported span flags
  too), set by the OpenTelemetry Java SDK itself. Children inherit it, so a Level 1 parent is continued unchanged.
  `OPENLOG_SAMPLING_RV=true` writes explicit `ot=rv:<14 hex>` on new traces.
- **Limits.** The `ot` value is capped at 256 characters (unknown sub-keys dropped first) and `tracestate` at 32
  members.
- **Explicit samplers.** `OTEL_TRACES_SAMPLER` = `always_on`, `parentbased_always_on`, `traceidratio`,
  `parentbased_traceidratio` or `openlog` is replaced by the openlog sampler (a ratio sampler's argument becomes the
  ratio). Any other sampler (`always_off`, `jaeger_remote`, `xray`, …) is kept as configured.

## Resource

| Attribute | Source |
|---|---|
| `service.name`, `service.version`, `service.namespace`, `deployment.environment.name` | configuration (upstream service name detection as fallback) |
| `host.id` | see [Host linking](#host-linking); replaces upstream's machine-id detection |
| `host.name`, `host.arch` | Linux: `/proc/sys/kernel/hostname` → `/etc/hostname`, `/proc/sys/kernel/arch` (OTel values `amd64`/`arm64`, same as the infra agent); else upstream |
| `os.type`, `os.name`, `os.version`, `os.description`, `openlog.os.kernel_release` | upstream; Linux `/etc/os-release`, `/proc/sys/kernel/osrelease` |
| `process.pid`, `process.executable.path`, `process.runtime.{name,version,description}` | upstream; **`process.command_line`/`process.command_args` are removed** (JVM arguments may contain secrets) |
| `container.id` | 64-hex id from `/proc/self/cgroup`; with a private cgroup namespace, from Docker/Podman container paths in `/proc/self/mountinfo` |
| `k8s.pod.name`, `k8s.pod.uid`, `k8s.namespace.name`, `k8s.node.name`, `k8s.container.name`, `k8s.deployment.name`, `k8s.cluster.name` | downward-API env `K8S_POD_NAME`, … (inside a pod the namespace falls back to the service-account file and the pod name to `HOSTNAME`) |
| `telemetry.distro.name` = `openlog`, `telemetry.distro.version`, `telemetry.sdk.*` | agent |

Attributes set in `OTEL_RESOURCE_ATTRIBUTES` / `OPENLOG_RESOURCE_ATTRIBUTES` are never replaced by detection.

## Host linking

Identical to the Go and Node.js agents ([details](../go/README.md#host-linking)), in this order:

1. the id a running infra agent publishes in `/run/openlog-infra-agent/host-id`;
2. `/etc/machine-id`, `/var/lib/dbus/machine-id`, `/sys/class/dmi/id/product_uuid`;
3. the infra agent's generated id in `/var/lib/openlog-infra-agent/host-id`;
4. (macOS/Windows) the platform machine id;
5. a UUID generated and persisted in `OPENLOG_STATE_DIR`.

In containers, mount the infra agent's runtime directory read-only
(`-v /run/openlog-infra-agent:/run/openlog-infra-agent:ro`; Kubernetes: `hostPath` of type `Directory`), or set
`OPENLOG_HOST_ID`.

## Logs and runtime metrics

**Log ↔ trace.** For Logback and Log4j2 (also Log4j 1, JBoss LogManager), the agent puts `trace_id`, `span_id` and
`trace_flags` into the MDC / context data, so a pattern like `[trace_id=%X{trace_id} span_id=%X{span_id}]` shows them
in the application's own output. The records of Logback, Log4j2 and `java.util.logging` are also exported as OTLP log
records carrying the trace and span ids, which gives [log ↔ trace navigation](../../docs/contracts/apm.md) (§11).

**Runtime metrics** (upstream `runtime-telemetry`, OpenTelemetry semantic convention names; scope
`io.opentelemetry.runtime-telemetry-java8`/`-java17`):
- memory: `jvm.memory.used`, `jvm.memory.committed`, `jvm.memory.limit`, `jvm.memory.used_after_last_gc`;
- GC: `jvm.gc.duration`;
- threads and classes: `jvm.thread.count`, `jvm.class.loaded`, `jvm.class.unloaded`, `jvm.class.count`;
- CPU: `jvm.cpu.time`, `jvm.cpu.count`, `jvm.cpu.recent_utilization`;
- the HTTP instrumentations add `http.server.request.duration` and `http.client.request.duration`.

## Overhead

`agents/java/test/run.sh bench` (`integration-tests/…/OverheadBench.java`) saturates the Spring Boot MVC sample's
`/hello` (Tomcat, one JSON route) with a keep-alive load generator (16 connections). Three copies of the application
run side by side: no agent, agent sampling everything, agent sampling nothing. Each is warmed up for 45 s, then loaded
for 10 s in 5 interleaved rounds, so host noise affects all three alike. The agent exports to a local OTLP receiver
that accepts and discards the data. The benchmark reports min/median/max and marks the result `INCONCLUSIVE` when the
run-to-run spread exceeds 1.5× or an agent scenario beats no agent.

**Current result: inconclusive. No overhead figure is claimed yet.** Measured in `eclipse-temurin:21-jdk` on Docker
Desktop (Apple M1 Pro VM, 10 CPUs), while other projects' containers on the same VM (a ClickHouse at ~77 % CPU, a kind
cluster) were busy:

| Scenario | req/s median (min–max over 5 rounds) | p50 ms | p99 ms | RSS MiB |
|---|---|---|---|---|
| no agent | 23 355 (10 907–26 978) | 0.43 | 4.84 | 732 |
| agent, sampled (ratio 1) | 17 931 (9 519–24 992) | 0.62 | 4.67 | 855 |
| agent, not sampled (ratio 0) | 17 041 (14 218–25 723) | 0.53 | 6.25 | 830 |

The three ranges overlap almost completely. Within one scenario, throughput varies 2.5×, while the medians differ
by less than that. The medians also have "not sampled" slower than "sampled", which only noise can produce. So these
numbers do not measure the agent. What does hold across both benchmark runs:
- the agent adds about **100–125 MiB RSS**, which is class metadata, the bytecode instrumentation and the SDK;
- in the quiet rounds, the median latency of this ~0.4 ms request is within 0.05–0.2 ms of the no-agent run.

An earlier sequential version of the benchmark (one scenario after another) was even noisier: an agent scenario
came out +191 % faster than no agent.

A reliable per-request cost needs a dedicated, idle host. Run `BENCH_RUNS=10 agents/java/test/run.sh bench` there
(nothing else loading the CPUs; `NO_SERVICES=1` with PostgreSQL started keeps Kafka and MySQL idle). For
orientation until then, the upstream OpenTelemetry Java agent documents
[its own overhead](https://opentelemetry.io/docs/zero-code/java/agent/performance/); the openlog extension adds
work only at startup (configuration, resource detection) and per exported span batch (the DB statement rewrite of
spans that carry a statement). To reduce the cost:
- sampling (`OPENLOG_SAMPLING_RATIO`);
- `OPENLOG_HTTP_IGNORE_PATHS` for health checks;
- disabling instrumentations that are not needed (`OPENLOG_INSTRUMENTATIONS_DISABLED`).

## Versioning and releases

The jar is versioned with the openlog product (D-025). Every release `vX.Y.Z` publishes `openlog-javaagent-X.Y.Z.jar`
and `openlog-javaagent-X.Y.Z.jar.sha256` with the GitHub release (`release.yml`, job `java-agent-jar`; see
[releasing.md](../../docs/operations/releasing.md#java-agent-jar)). The jar is listed in the signed release manifest
(`manifest.json`, component `java-agent`, format `jar`): `openlog-release verify --keys <key> --check-artifacts
manifest.json` in a directory with the jar checks signature and sha256, or compare with the `.sha256` file. The manifest `Openlog-Javaagent-Version` and
`Openlog-Upstream-Javaagent-Version` attributes, plus `telemetry.distro.version`, identify a jar. The upstream agent
version is pinned in `gradle.properties` and moves with openlog releases. **Maven Central** publishing
(`io.github.onuragtas.openlog:openlog-javaagent`, for build tools that download agents) is planned, not done.

## Development

Build tooling runs in containers (`eclipse-temurin` + the Gradle wrapper). The runner copies the sources into a Docker
volume, so nothing is written into the checkout:

```sh
agents/java/test/run.sh                                  # ./gradlew check integrationTest (starts PostgreSQL, MySQL, Redis, Kafka)
NO_SERVICES=1 agents/java/test/run.sh :extension:test :agentJar
JAVA_TEST_JDK=17 agents/java/test/run.sh check
agents/java/test/run.sh :integration-tests:integrationTest --tests '*QuarkusIT'   # one framework
JAVA_TEST_PROJECT=my-prefix JAVA_TEST_KAFKA_PORT=59192 agents/java/test/run.sh   # other compose/volume names, Kafka host port
agents/java/test/run.sh bench                            # overhead micro-benchmark
agents/java/test/run.sh down                             # remove containers and volumes
```

| Path | Content |
|---|---|
| `extension/` | the openlog extension (Java 8 bytecode, no runtime dependencies): `OpenlogCustomizerProvider` (SPI), config mapping, `sampling/`, `db/`, `resource/`; unit tests incl. the Go sampler fixtures and SDK autoconfiguration |
| `build.gradle.kts` | `agentJar`: upstream agent + `extensions/openlog-extension.jar` → `build/libs/openlog-javaagent-<version>.jar`, `agentJarChecksum` |
| `testapps/mvc`, `testapps/webflux` | Spring Boot sample applications |
| `testapps/jaxrs`, `testapps/quarkus`, `testapps/micronaut`, `testapps/vertx` | the same small application (`/users/{id}` with JDBC, `/fail`) per framework; Quarkus is built by the Quarkus Gradle plugin (`quarkusBuild`) |
| `integration-tests/` | OTLP capture server, application launcher, `SpringMvcIT`, `SpringWebfluxIT`, `FrameworkIT` (`JaxrsJerseyIT`, `QuarkusIT`, `MicronautIT`, `VertxWebIT`), `OverheadBench` |
| `scripts/release-jar.sh` | release build of the jar + `.sha256` into a release directory (`make release-java-agent`, `release.yml`) |
| `test/docker-compose.yml` | throwaway services (project `openlog-m4-java-test`) and the Gradle runner |

Upgrading the upstream agent: change `otelJavaagentVersion`/`otelSdkVersion`/`otelProtoVersion` in `gradle.properties`,
run the full suite, and check the upstream release notes for renamed properties the extension sets (DB sanitizer, runtime
telemetry).
