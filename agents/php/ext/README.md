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

## Build and install

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
| `openlog.transaction_tracer.min_segment_ms` | `1` | all | faster calls are aggregated into the parent's `openlog.php.fast_calls` / `openlog.php.fast_calls_ns` |
| `openlog.transaction_tracer.max_memory_kb` | `4096` | all | |
| `openlog.log_level` | `warning` | all | `off`, `error`, `warning`, `info`, `debug`; PHP error log, at most 10 lines per minute per process |

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
Symfony HttpClient are covered through these. `traceparent` (+ `tracestate`) is injected — also for unsampled requests
(flags `00`) — unless the application already set a `traceparent` header. Attributes: `http.request.method`,
`url.full` (credentials removed), `server.address`, `server.port`, `http.response.status_code`, `error.type`.

**Errors**: uncaught exceptions (also when handled by `set_exception_handler`) and fatal errors → root `status=2` +
`exception` event (`exception.type`, `exception.message`, `exception.stacktrace` in PHP trace format). Caught
exceptions only when reported by Laravel (`Handler::report`/`reportThrowable`, honoring the internal dont-report list)
or Symfony (`HttpKernel::handleThrowable`, HTTP exceptions < 500 ignored). Exceptions inside instrumented calls mark
the client span.

**Transaction tracer**: every userland function call of a sampled request is a candidate segment (kind 1,
`Class::method`, `code.function.name`, `code.namespace`, `code.file.path`, `code.line.number`,
`openlog.php.segment=function`); `sleep`/`usleep`/`time_nanosleep` appear as internal spans. Fast calls give their
node back immediately (O(1)); segments are sent only when the transaction is slow or failed.

## Architecture

- `src/ol_hooks.c` — hook registry (lowercase `class::method`, `class::*`, `function`) built at MINIT; the lookup
  result is cached per `zend_function` in its `reserved[]` slot as an integer, so dispatch is O(1) without hashing
  after the first call (hash fallback when `opcache.protect_memory=1`). Two backends as in the table above.
- `src/ol_core.c` — per-request state: node table in fixed chunks (≤ 4096 spans + tracer segments), frame stack
  (separate stacks per fiber), bounded arena (64 KiB chunks; base 8 MiB + `max_memory_kb`), retained between requests.
  Handlers read engine data only (arguments, `$this`, properties without `__get`); the only PHP functions the agent
  calls are `curl_setopt`/`curl_getinfo`/`curl_errno`/`curl_error` and Predis `getId()`, with exceptions suppressed.
- `src/ol_json.c` — dependency-free JSON encoder; messages larger than 60 000 bytes are split (same `pid` +
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
- **Swoole / RoadRunner / Octane** long-running workers: one transaction per process lifetime (phase 2).

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
