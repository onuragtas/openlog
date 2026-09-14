# openlog PHP agent extension (`openlog.so`)

License: Apache-2.0. Contract: [docs/contracts/php-agent.md](../../../docs/contracts/php-agent.md) (wire format v1),
APM semantics: [docs/contracts/apm.md](../../../docs/contracts/apm.md). Decisions D-035…D-038.

```
PHP worker (openlog.so) --unix datagram, JSON--> openlog-infra-agent (php_forwarder) --OTLP/HTTP--> openlog
```

The extension never talks to the network, never blocks and never fails a request: when the forwarder socket is
missing, full or slow, the message is dropped and counted. No license key or endpoint is configured in PHP.

## Supported runtimes

| PHP | Hook layer |
|---|---|
| 7.1 – 7.4 | `zend_execute_ex` (userland) + `zend_execute_internal` (internal functions), previous handlers chained |
| 8.0 – 8.1 | Observer API (userland) + `zend_execute_internal` (the 8.0/8.1 observer does not see internal calls) |
| 8.2 – 8.4 | Observer API for userland and internal functions |

NTS and ZTS, glibc and musl, amd64 and arm64 (CI: `php-ext` job; locally `build/run-matrix.sh`). SAPIs: PHP-FPM,
Apache mod_php (`apache2handler`), CGI/FastCGI, CLI.

### Tested matrix (`build/run-matrix.sh`, arm64, 2026-09-13)

| Image | phpt passed / failed / skipped | Skips |
|---|---|---|
| `php:7.1-cli` … `php:7.3-cli` | 25 / 0 / 1 | fibers (8.1+) |
| `php:7.4-cli`, `php:8.0-cli` | 24 / 0 / 2 | fibers; pgsql (bullseye arm64 mirror has no libpq-dev) |
| `php:8.1-cli` … `php:8.4-cli` | 26 / 0 / 0 | — |
| `php:8.3-zts` | 26 / 0 / 0 | — |
| `php:7.4-zts` | 15 / 0 / 11 | web-SAPI tests (image has no `php-cgi`), fibers, pgsql |
| `php:7.4-cli-alpine` / `php:8.3-cli-alpine` | 25 / 0 / 1 · 26 / 0 / 0 | fibers (7.4) |

Tests (`tests/*.phpt`, a child PHP process + a unix datagram receiver validating every message against the
forwarder's rules): CLI/web transactions, traceparent (valid, unsampled, 8 malformed variants), sampling,
fail-open (missing socket, invalid transport, receiver that never reads), splitting (2001 spans), span limit, tracer
(threshold, fast calls, limits, 3000-deep recursion), generators, fibers, errors (uncaught, handler, fatal, 4 KiB
truncation, exit), PDO, SQL sanitizing, mysqli, pgsql, phpredis, Predis, curl, curl_multi, streams (incl. context
restore and opcache), Laravel, Symfony, Slim, WordPress, CodeIgniter, Yii (stub classes with the real names), several
requests per process (php-cgi -T), log correlation. Real framework versions are exercised by `agents/php/demo`.

### Overhead (2026-09-14, PHP 8.3 NTS, arm64 VM shared with other workloads; D-057)

`before` = extension source before D-057, `after` = current; `off` = tracer disabled, `on` = defaults,
`lean` = `openlog.userland_hooks=0` (+ tracer off).

**Instructions per request** (callgrind, `bench/ext/micro/run.sh`, `php-cgi -T`, deterministic):

| App | base | before off | after off | before on | after on | lean |
|---|---|---|---|---|---|---|
| plain PHP (57 PDO SQLite statements, ~2k calls) | 2.59 M | +1.12 M (+43 %) | +0.58 M (+23 %) | +1.16 M | +0.63 M | +0.35 M (+13 %) |
| Laravel 12 `GET /health` (~18k calls) | 7.26 M | +1.78 M (+24.5 %) | +1.79 M | +1.80 M | +1.81 M | +0.27 M (+3.7 %) |

Where the Laravel cost is: the engine's Observer API, ~100 instructions per userland *and* internal call on 8.0–8.3
(8.4: 50–65) once an fcall observer is registered, even for functions it does not observe, plus
`zend_init_internal_run_time_cache` per request (8.2/8.3). The extension cannot remove it except by not registering the
observer (lean mode). Our own code was the main cost only in span-heavy requests (JSON escaping, SQL sanitizing,
UTF-8 cleaning); that part halved.

**HTTP** (`bench/ext/http/run.sh`: PHP-FPM 8 static workers on 4 pinned CPUs, nginx, wrk 16 connections, datagram
sink instead of a forwarder, 5 interleaved rounds × 15 s). Medians, p25–p75 (min–max):

| Laravel `GET /bench/{id}` (Eloquent + 2 Redis) | RPS | PHP CPU µs/req | Δ CPU vs base |
|---|---|---|---|
| base | 1068 (1002–1172, 946–1218) | 2909 (2899–2923) | — |
| before, tracer off | 826 (810–868) | 3364 (3161–3562) | +455 µs (+15.6 %) |
| before, tracer on | 898 (752–902) | 3348 (3323–3510) | +439 µs (+15.1 %) |
| after, tracer off | 893 (879–1005) | 3309 (3191–3375) | +400 µs (+13.8 %) |
| after, tracer on | 1044 (986–1118) | 3106 (3042–3229) | +197 µs (+6.8 %) |
| after, lean | 1008 (988–1062) | 3032 (3007–3137) | +123 µs (+4.2 %) |

| plain PHP (57 statements; earlier run, before the sampler warm-up change) | RPS | PHP CPU µs/req | Δ CPU |
|---|---|---|---|
| base | 10428 (10258–10907) | 295 (289–303) | — |
| before off / on | 7880 / 7482 | 431 / 463 | +136 / +168 µs |
| after off / on / lean | 7652 / 7610 / 8160 | 400 / 409 / 394 | +105 / +114 / +99 µs |

**Budget (≤ 3 % tracer off / ≤ 7 % on) still not met in full mode.** RPS spread between rounds is ±10–20 % on this
VM (other agents' containers were loading it), so RPS deltas below ~10 % are not significant; PHP CPU per request is
the stable signal. "After, on" measuring lower CPU than "after, off" in the Laravel run is inside that noise (both
have the same instruction count). Lean mode is within the budget by instructions (+3.7 %) and CPU (+4.2 %) but gives
up framework hooks. A dedicated benchmark host is still needed to confirm the RPS budget.

**Soak** (`bench/ext/soak/run.sh`, 10 min, 4 FPM workers without recycling, tracer on, threshold 50 ms, Laravel mix
incl. exceptions and slow traces + plain app): 332 k requests, no worker replaced, no signal in the FPM log, RSS
after the first quarter −376…−420 KiB per worker (no growth): PASS.

## Build and install

Packages (D-059, contract §7): `openlog-php-agent_<v>_linux_<arch>.deb|.rpm|.apk` contain `openlog.so` for every PHP
ABI (7.1–8.4 × NTS/ZTS × glibc/musl; glibc modules need glibc ≥ 2.28, i.e. RHEL/Alma/Rocky 8+, Debian 10+, Ubuntu
20.04+) and enable it for every PHP runtime found (`php`, `php-cgi`, `php-fpm`; Debian `mods-available`, RHEL/Remi,
Alpine, `/usr/local`). PHP-FPM / Apache are not restarted; reload them, or:

```sh
openlog-php-install status            # runtimes, ABI, enabled/loaded (--json for tooling)
openlog-php-install install --reload  # (re)enable and reload php-fpm/apache units
openlog-php-install uninstall
```

The tarball `openlog-php-agent_<v>_linux_<arch>.tar.gz` has the same tree (`modules/<api>-<nts|zts>-<libc>/openlog.so`,
`bin/openlog-php-install`) for hosts without a package manager or for images; release builds:
`agents/php/packaging/build-artifacts.sh <version> <out>` (package install test: `agents/php/packaging/test.sh`).
The fleet installation through the infra agent is specified in the contract (§7.3).

From source:

```sh
cd agents/php/ext
phpize
./configure --enable-openlog
make -j"$(nproc)"
make install            # or copy modules/openlog.so into `php-config --extension-dir`
echo 'extension=openlog.so' > "$(php-config --ini-dir 2>/dev/null || echo /usr/local/etc/php/conf.d)/90-openlog.ini"
php -m | grep openlog
```

Docker (official images): add the build to your image, e.g.

```dockerfile
FROM php:8.3-fpm
COPY agents/php/ext /usr/src/openlog-ext
RUN cd /usr/src/openlog-ext && phpize && ./configure --enable-openlog && make -j"$(nproc)" && make install \
 && echo 'extension=openlog.so' > /usr/local/etc/php/conf.d/90-openlog.ini
```

The forwarder is the infra agent's `php_forwarder` module (`agents/infra`, enabled automatically when PHP is
discovered). In containers, share `/run/openlog-infra-agent` (volume) between the PHP container and the agent, or use
the UDP fallback (`openlog.transport=udp://127.0.0.1:18127`).

Development helpers (copy the source to a scratch directory, build inside the official image):

| Script | What |
|---|---|
| `build/dev-build.sh 8.3` | compile only (`8.3`, `7.4-zts`, `8.3-cli-alpine`, …) |
| `build/test-one.sh 8.3 [tests/…]` | compile + phpt (datastore tests skip) |
| `build/run-matrix.sh` | full matrix: 7.1…8.4 NTS glibc + 7.4/8.3 ZTS and musl, with MariaDB/PostgreSQL/Redis containers (network `openlog-php-test`) |
| `VERSIONS="" EXTRA="8.3-asan" build/run-matrix.sh` | AddressSanitizer + UBSan debug PHP built from source (`build/Dockerfile.asan`), openlog.so with the same sanitizers; any sanitizer report fails (`make -C agents/php ext-asan`) |
| `VERSIONS="" EXTRA="8.3-xdebug 8.3-ddtrace 8.3-newrelic 8.3-jit 8.3-swoole" build/run-matrix.sh` | full suite with another agent / debugger / JIT / Swoole loaded (`build/Dockerfile.compat`, downloads from pecl, GitHub, download.newrelic.com) |
| `VERSIONS="" EXTRA="8.4-frankenphp" build/run-matrix.sh` | full suite in the official FrankenPHP image (ZTS PHP 8.4), including a real FrankenPHP server in worker and classic mode (`tests/044-frankenphp.phpt`) |
| `fuzz/run.sh` | libFuzzer + ASan/UBSan on `src/ol_text.c` (`FUZZ_SECONDS`, default 60) |
| `../bench/ext/micro/run.sh` | per-request cost: `php-cgi -T` timing + callgrind instructions, plain PHP and Laravel; `SRC_A=<older ext>` for A/B |
| `../bench/ext/http/run.sh` | PHP-FPM + nginx + wrk: RPS, latency, PHP CPU per request; base / off / on / lean (+ A/B) |
| `../bench/ext/soak/run.sh` | `MINUTES` of mixed load on 4 long-lived FPM workers; fails on worker RSS growth or crashes |
| `../packaging/build-artifacts.sh` | release tarball + deb/rpm/apk for the host architecture |

## Settings (`php.ini`)

| Setting | Default | Scope | Notes |
|---|---|---|---|
| `openlog.enabled` | `1` | system | `0`: no hooks are installed at all |
| `openlog.service_name` | empty → `php-app` (web SAPIs) / `php-cli` (CLI) | all | env `OPENLOG_SERVICE_NAME` overrides (FPM pool `env[OPENLOG_SERVICE_NAME]`) |
| `openlog.service_namespace`, `openlog.service_version`, `openlog.environment` | empty | all | env `OPENLOG_SERVICE_NAMESPACE`, `OPENLOG_SERVICE_VERSION`, `OPENLOG_ENVIRONMENT` override |
| `openlog.transport` | `unix:///run/openlog-infra-agent/php.sock` | system | `unix:///path` or `udp://127.0.0.1:18127` (numeric address) |
| `openlog.sampling_ratio` | `1.0` | all | head sampling; a sampled incoming `traceparent` is always recorded, an unsampled one never |
| `openlog.capture_query_text` | `sanitized` | all | `raw`, `off` |
| `openlog.transaction_tracer.enabled` | `1` | system, perdir | function-level segments |
| `openlog.transaction_tracer.threshold_ms` | `500` | all | segments are sent when the transaction takes at least this long or failed |
| `openlog.transaction_tracer.max_segments` | `2000` | all | |
| `openlog.transaction_tracer.min_segment_ms` | `1` | all | stack sampling interval; calls shorter than it appear only when a sample hits them |
| `openlog.transaction_tracer.max_memory_kb` | `4096` | all | |
| `openlog.log_level` | `warning` | all | `off`, `error`, `warning`, `info`, `debug`; PHP error log, at most 10 lines per minute per process |
| `openlog.userland_hooks` | `1` | system | `0` = lean mode: no fcall observer (PHP 8) / `zend_execute_ex` override (7.x), so the engine's per-call observer cost disappears; only internal functions are instrumented (PDO, mysqli, pgsql, phpredis, curl, streams, sleep). Lost: framework route names, framework-reported exceptions, Predis, long-running worker transactions; uncaught exceptions are recorded from the fatal error. See "Overhead" |

PHP functions (log correlation, custom naming): `openlog\trace_id(): string`, `openlog\span_id(): string`,
`openlog\traceparent(): string`, `openlog\set_transaction_name(string $name): bool`, `openlog\is_sampled(): bool`,
`openlog\stats(): array` (process counters: requests, messages, datagrams, send_errors, dropped_messages,
dropped_spans, failopen). `phpinfo()` shows the hook layer and counters.

## What is recorded

**Transaction** (root span): web requests are kind 2 (`http.request.method`, `url.path`, `url.scheme`,
`server.address`, `server.port`, `client.address`, `user_agent.original`, `network.protocol.version`,
`http.response.status_code`, `http.route`); CLI scripts are kind 1, named `php <script>` with `openlog.php.cli=true`
(and `process.exit.code` when not 0). The name is `METHOD route` with the first available of:

| Priority | Source |
|---|---|
| 1000 | `openlog\set_transaction_name()` |
| 50 | Laravel `Router::runRoute` → `Route::$uri` (`/users/{id}/orders`); Symfony `ControllerResolver::getController` → path template rebuilt from `_route_params` (`/api/products/{id}`, route name in `openlog.php.route_name`); Slim 3 `Slim\Route::run` / Slim 4 `Slim\Routing\Route::run` → `$pattern`; CodeIgniter 3 `CI_Router::_set_routing` → `/dir/class/method`; CodeIgniter 4 `Router::handle` → `matchedRoute` pattern; Yii 2 `yii\web\Application::runAction` → `/controller/action` |
| 45 / 40 | WordPress REST: `WP_REST_Server::match_request_to_handler` route regex (`/wp-json/wp/v2/posts/{id}`), else the normalized REST path |
| 30 | WordPress template: `apply_filters('template_include')` → `template/single.php` |
| — | plain PHP: path normalized per apm.md §2.2 (`/orders/{id}/items/{hex}`), no `http.route` |

**Datastores** (kind 3): PDO (`exec`, `query`, `PDOStatement::execute`; `prepare` only when it fails; DSN →
`server.address`, `server.port`, `db.namespace`), mysqli procedural and OO (`query`, `real_query`, `multi_query`,
`execute_query`, prepared statements; connection attributes from `mysqli_connect`/`new mysqli`/`real_connect`,
`select_db`), pgsql (`pg_query`, `pg_query_params`, `pg_prepare` + `pg_execute`, default connection), phpredis (every
`Redis` command except connection/option helpers; `select` updates `db.namespace`), Predis
(`Client::executeCommand`). Attributes: `db.system.name` (`mysql`, `postgresql`, `sqlite`, `redis`, …),
`db.operation.name`, `db.collection.name`, `db.query.text`; name `OPERATION table`. Sanitizing replaces string,
numeric and hex literals with `?`, removes comments and collapses whitespace; placeholders stay. Redis arguments are
`?` unless `raw`.

**Outbound HTTP** (kind 3): `curl_exec`, `curl_multi_add_handle` … `curl_multi_remove_handle`, `file_get_contents` /
`fopen` on `http(s)://`. Guzzle (sync `CurlHandler`, `CurlMultiHandler`, `StreamHandler`), Laravel `Http::` and
Symfony HttpClient are covered through these; with Guzzle 7 async requests (promises, `Pool`) every request is one
client span from `curl_multi_add_handle` to `curl_multi_remove_handle` carrying its own `traceparent`
(`tests/043-guzzle-async.phpt`, real library: `build/test-guzzle.sh`). `traceparent` (+ `tracestate`) is injected — also for unsampled requests
(flags `00`) — unless the application already set a `traceparent` header. Attributes: `http.request.method`,
`url.full` (credentials removed), `server.address`, `server.port`, `http.response.status_code`, `error.type`.

**Errors**: uncaught exceptions (also when handled by `set_exception_handler`) and fatal errors → root `status=2` +
`exception` event (`exception.type`, `exception.message`, `exception.stacktrace` in PHP trace format). Caught
exceptions only when reported by Laravel (`Handler::report`/`reportThrowable`, honoring the internal dont-report list)
or Symfony (`HttpKernel::handleThrowable`, HTTP exceptions < 500 ignored). Exceptions inside instrumented calls mark
the client span.

**Transaction tracer** (stack sampling): a sampler thread per PHP process (idle between requests) sets
`EG(vm_interrupt)` every `min_segment_ms` — every 10 ms during the first 100 ms of a request, since function traces are
only sent for slow or failed requests and a wake-up per millisecond measurably cost fast requests (~150 µs PHP CPU on a
7 ms Laravel request); at the next safe point the userland call stack is walked and merged into
a segment tree (kind 1, `Class::method`, `code.function.name`, `code.namespace`, `code.file.path`,
`code.line.number`, `openlog.php.segment=function`, `openlog.php.samples`). Every instrumented span takes a sample at
its start and end, so blocking DB/HTTP/sleep time is attributed to the right functions and the span's parent is the
innermost function segment. `sleep`/`usleep`/`time_nanosleep` appear as internal spans. No per-call cost (observing
every call measured ~60 ns/call, > 1 ms per framework request). Calls shorter than the interval appear only when a
sample hits them; consecutive calls of one function from the same frame without a sample in between form one
segment; depth is limited to the outermost 256 frames. Segments are sent only when the transaction is slow or
failed.

**Long-running workers** (D-058, `src/inst_workers.c`): one transaction per request, named and attributed like a web
request, for Laravel Octane (`Laravel\Octane\Worker::handle`; status from `SwooleClient`/`RoadRunnerClient::respond`),
RoadRunner (`Spiral\RoadRunner\Http\HttpWorker::waitRequest` … `respond`/`respondStream`; covers PSR7Worker and Octane
on RoadRunner) and Swoole/OpenSwoole HTTP servers (the callable registered with `Server::on('request', …)` or
`Coroutine\Http\Server::handle()` … `Response::end`/`redirect`/`sendfile`, status from `Response::status`) and
FrankenPHP worker mode (the callable passed to `frankenphp_handle_request()` is the request: call → return; request
attributes from the per-request `$_SERVER` FrankenPHP resets, status from `http_response_code()` when the callback
returns, 500 for an escaping exception when `display_errors` is off — what FrankenPHP answers — and the exception
event; `exit()` inside a request and worker restarts after N requests work; the worker script's boot and waiting are
never sent, detected through `FRANKENPHP_WORKER`). FrankenPHP classic mode needs nothing special (regular SAPI
request, `sapi_module.name` `frankenphp`). The worker
process's own CLI transaction is dropped at the first request, nothing is recorded or propagated between requests, and
connection attributes (DSN, host, database) survive requests. When requests overlap in one process (Swoole coroutines)
they are never mixed: while more than one is in progress, child spans, route names and the function tracer stop for
all of them and each request is recorded with its root span only (`openlog.php.concurrent=true`); outgoing
`traceparent` headers and `openlog\traceparent()` use the request whose handler is on the current coroutine's stack,
and nothing when none is. Not covered: lean mode (request callables are observed userland functions), a request
callable that already ran before it was passed to `frankenphp_handle_request()` / `Server::on()`. Tests:
`tests/041-workers.phpt` (Octane and RoadRunner classes), `tests/042-swoole.phpt` (real Swoole server, 4 concurrent
requests; image `8.3-swoole`), `tests/044-frankenphp.phpt` (real FrankenPHP 1.x server, worker mode with restart and
`exit()`, classic mode; tag `8.4-frankenphp`, official `dunglas/frankenphp` image).

## Architecture

- `src/ol_hooks.c` — hook registry (lowercase `class::method`, `class::*`, `function`) built at MINIT; the lookup
  result is cached per `zend_function` in its `reserved[]` slot as an integer, so dispatch is O(1) without hashing
  after the first call (hash fallback when `opcache.protect_memory=1`). Two backends as in the table above.
- `src/ol_core.c` — per-request state: node table in fixed chunks (≤ 4096 spans + tracer segments), frame stack
  (separate stacks per fiber), bounded arena (64 KiB chunks; base 8 MiB + `max_memory_kb`), retained between requests.
  Handlers read engine data only (arguments, `$this`, properties without `__get`); the only PHP functions the agent
  calls are `curl_setopt`/`curl_getinfo`/`curl_errno`/`curl_error` and Predis `getId()`, with exceptions suppressed.
- `src/ol_text.c` — PHP-independent text code on every span's path: JSON writer, UTF-8 cleaning/truncation, SQL
  sanitizing and operation extraction, path/route normalization, `traceparent` parsing (table-driven character
  classes, 8-byte scans; fuzzed by `fuzz/run.sh`). Query analysis is cached per request by query text (`ol_util.c`).
- `src/inst_workers.c` — transactions of long-running workers (Octane, RoadRunner, Swoole) and concurrency rules.
- `src/ol_json.c` — message encoding; messages larger than 60 000 bytes are split (same `pid` +
  `trace_id`, `seq` 0…255, `last` on the final part, `resource`/`sampling_ratio`/`function_trace` repeated). Every
  string is UTF-8 cleaned and ≤ 4 KiB, ≤ 64 attributes per span. `sendto(MSG_DONTWAIT)` on an unconnected socket;
  on `EAGAIN` a split message may retry for at most ~2 ms (the kernel's datagram queue is short), never more.
- `src/ol_context.c` — W3C `traceparent`/`tracestate` (strict parsing; malformed → new trace), sampling
  (`ot=th:` threshold added to `tracestate` when the ratio is < 1), root span, `container.id`.
- Fail-open: an internal inconsistency disables instrumentation for the rest of the request and logs once
  (rate-limited); send errors are counted and reported on the next message's root span (`openlog.php.send_errors`,
  `openlog.php.dropped_messages`).

## Compatibility with other extensions

- **Xdebug, New Relic, Datadog, Blackfire, ext-opentelemetry**: on PHP 8 every Observer is independent. On PHP 7.x
  and for internal functions on 8.0/8.1 the extension replaces `zend_execute_ex` / `zend_execute_internal` /
  `zend_error_cb` and always calls the previously installed handler, so whichever extension loads later wraps the
  earlier one. Running two APM agents at once doubles overhead and both inject `traceparent` into curl; the second
  agent sees the first one's header and keeps it. Not recommended in production.
- **Opcache / JIT**: supported. On 7.x, overriding `zend_execute_ex` makes every userland call recursive in C (same as
  Xdebug/New Relic); very deep recursion needs a larger C stack.

Tested together (full phpt suite with the other extension loaded first, PHP 8.3 NTS, arm64, 2026-09-14;
`build/run-matrix.sh` tags `8.3-xdebug`, `-ddtrace`, `-newrelic`, `-jit`, `-swoole`):

| Loaded with | phpt | Notes |
|---|---|---|
| Xdebug 3.4 `xdebug.mode=coverage` | 28 / 0 / 1 | `develop,coverage`: 23 / 6 — all six are Xdebug behaviour, no crash, openlog data unchanged: `var_dump()` gains a `file:line` prefix (5 tests) and `xdebug.max_nesting_level=512` aborts the 3000-deep recursion test |
| Datadog ddtrace (latest `datadog-setup.php`, CLI tracing on) | 26 / 2 / 1 | ddtrace replaces the `traceparent` header openlog injected into curl (the last tracer wins); ddtrace re-dispatches `Predis\Client::executeCommand`, so openlog records nested duplicate Predis spans. Fixed while testing: ddtrace's closures (without `ZEND_ACC_CLOSURE`) matched `redis::*` |
| New Relic PHP agent (latest, daemon not running) | 28 / 0 / 1 | — |
| OPcache JIT (`opcache.jit=tracing`, CLI and CGI) | 28 / 0 / 1 | — |
| Swoole 6 | 3 / 0 / 0 (worker, lean, Swoole tests) | `tests/042-swoole.phpt`: 4 concurrent coroutine requests |

## Robustness

- **AddressSanitizer + UBSan** (`VERSIONS="" EXTRA="8.3-asan" build/run-matrix.sh`, CI job `php-ext (8.3-asan)`): PHP
  8.3 debug build from source with `--enable-address-sanitizer --enable-undefined-sanitizer`, openlog.so and phpredis
  built with the same flags, `USE_ZEND_ALLOC=0`, leak detection on; reports go to files and fail the run. Result:
  28 passed / 0 failed / 1 skipped (Swoole), 0 sanitizer reports (image build ~3 min).
- **Fuzzing** (`fuzz/run.sh`, CI job `php-ext-fuzz`, 120 s): libFuzzer + ASan/UBSan on `src/ol_text.c` — UTF-8
  cleaning/truncation, JSON string writer (decoded output must equal the input; a writer with a too small capacity must
  stop inside its buffer), numbers, SQL sanitizing and operation extraction, path/route normalization, traceparent
  parse/format round trip. 60 s local run: 6.7 M executions, 12 405 corpus units, no finding.
- **Soak** (`../bench/ext/soak/run.sh`): see "Overhead".
- **Swoole / RoadRunner / Octane / FrankenPHP worker mode**: one transaction per handled request, see "Long-running
  workers".

## Troubleshooting

| Symptom | Check |
|---|---|
| No data | `php -m \| grep openlog`; `php -i \| grep openlog`; socket exists and is writable by the PHP user (`ls -l /run/openlog-infra-agent/php.sock`); `openlog\stats()['send_errors']` |
| `send_errors` grows | forwarder not running / wrong path / permissions (socket group, `php_forwarder.socket_mode`); containers must share the socket directory |
| Wrong transaction names | framework not detected: use `openlog\set_transaction_name()`; plain PHP uses normalized paths |
| Traces cut | `dropped_spans` on the root span: > 4096 spans, `max_segments` or `max_memory_kb` reached |
| No function segments | request faster than `threshold_ms` and not failed; tracer disabled; unsampled request |
| Overhead concern | `openlog.transaction_tracer.enabled=0`, lower `openlog.sampling_ratio` |
| Crash suspected | `openlog.enabled=0` disables all hooks without removing the extension |
