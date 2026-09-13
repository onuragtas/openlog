# Contract: PHP agent ↔ infra agent forwarder (v1)

Decisions: D-035 (own C extension), D-036 (PHP 7.1+), D-037 (depth incl. function-level traces), D-038 (forwarder in the
infra agent). Background and measurements: `agents/php/docs/decision.md`. APM semantics (transactions, naming, DB
normalization, errors, sampling): [apm.md](apm.md).

```
PHP worker (openlog.so) --unix datagram, JSON--> openlog-infra-agent (php_forwarder module) --OTLP/HTTP--> ingest
```

The extension never talks to the network, never blocks and never fails the request: if the socket is missing, full or
slow, the message is dropped and counted.

## 1. Transport

- **Unix datagram socket** (`SOCK_DGRAM`), default path `/run/openlog-infra-agent/php.sock`.
  - Created by the forwarder (directory `0755`; socket mode `0660`, group from `php_forwarder.socket_group`, default
    the first existing of `www-data`, `nginx`, `apache`, `php-fpm`; `0666` only if configured explicitly).
  - The extension sends with `sendto(..., MSG_DONTWAIT | MSG_NOSIGNAL)` on an unconnected socket, so a restarted forwarder
    (new inode) is picked up without reconnecting.
- **Datagram size:** at most **60 000 bytes**. Larger messages are split (§2.3).
- Optional fallback for containers that cannot share the socket file: UDP `127.0.0.1:18127`
  (`openlog.transport=udp://127.0.0.1:18127` / `php_forwarder.udp_listen`). Same payload.

## 2. Message

One JSON object (UTF-8) per datagram; all IDs lowercase hex; times unix nanoseconds (integer).

```json
{
  "v": 1,
  "pid": 4711,
  "trace_id": "4c2953622b6086caa46723f43306ffb6",
  "seq": 0,
  "last": true,
  "resource": {
    "service.name": "shop-api",
    "service.namespace": "shop",
    "service.version": "1.4.2",
    "deployment.environment.name": "prod",
    "process.runtime.name": "php",
    "process.runtime.version": "8.3.11",
    "php.sapi": "fpm-fcgi",
    "telemetry.distro.name": "openlog-php",
    "telemetry.distro.version": "0.9.1"
  },
  "sampling_ratio": 1.0,
  "function_trace": false,
  "dropped_spans": 0,
  "spans": [
    {
      "id": "5ccf0fb55209346e",
      "parent": "",
      "name": "GET /users/{id}/orders",
      "kind": 2,
      "start": 1789302480000000000,
      "dur": 12345678,
      "status": 0,
      "status_msg": "",
      "attrs": {"http.request.method": "GET", "http.route": "/users/{id}/orders", "http.response.status_code": 200},
      "events": [{"name": "exception", "time": 1789302480010000000,
                  "attrs": {"exception.type": "RuntimeException", "exception.message": "…", "exception.stacktrace": "…"}}]
    }
  ]
}
```

### 2.1 Fields

| Field | Rules |
|---|---|
| `v` | `1`. Unknown versions are dropped and counted by the forwarder. |
| `pid` | Worker PID; with `trace_id` it identifies a message sequence. |
| `trace_id` | 32 hex. Continued from an incoming `traceparent` header when present, else random. |
| `resource` | String values only. Only keys listed above plus `service.instance.id`, `container.id`, `k8s.*`. The forwarder **adds** host attributes (`host.id`, `host.name`, `os.*`, `openlog.agent.*`) from the infra agent's own resource and never lets the extension override them. |
| `sampling_ratio` | Head sampling ratio applied (0 < r ≤ 1). Sent as `sampling.ratio` on the root span (apm.md §sampling). Unsampled requests send nothing. |
| `function_trace` | `true` when the message contains function segments (§3). |
| `dropped_spans` | Spans discarded by extension limits for this trace; forwarder sets `openlog.php.dropped_spans` on the root span. |
| `spans[].kind` | OTLP `SpanKind` numbers: 1 internal, 2 server, 3 client, 4 producer, 5 consumer. |
| `spans[].status` | 0 unset, 1 ok, 2 error. |
| `spans[].attrs` | Values: string, integer, float, boolean, or arrays of these. Semantic conventions per apm.md / OTel (HTTP, DB, exceptions, `code.*`). |

### 2.2 Span content (extension responsibilities)

- **Transaction (root, kind 2):** name `METHOD route` using the framework's route template when known (Laravel, Symfony,
  Slim, WordPress, CodeIgniter 3/4, Yii 2, plain PHP → normalized script/path per apm.md). CLI scripts: kind 1, name
  `php <script>` with `openlog.php.cli=true`.
- **Datastores (kind 3):** PDO, mysqli, pgsql functions, phpredis, Predis; `db.system.name`, `db.namespace`,
  `server.address`/`server.port`, `db.query.text` sanitized per apm.md unless `openlog.capture_query_text=raw|off`.
- **Outbound HTTP (kind 3):** curl (`curl_exec`, `curl_multi_*`), streams (`file_get_contents` on http(s)), Guzzle;
  injects `traceparent` (+ `tracestate`) into the outgoing request.
- **Errors:** uncaught exceptions and fatal errors → root span `status=2` + `exception` event; caught exceptions only
  when reported through framework handlers (Laravel `report()`, Symfony kernel exception).
- **Logs correlation (optional):** `openlog.trace_id`/`openlog.span_id` exposed as PHP functions for log processors.

### 2.3 Splitting

If the JSON exceeds 60 000 bytes, spans are split across datagrams with the same `pid` + `trace_id`, increasing `seq`
from 0, `last: true` only on the final one; `resource`, `sampling_ratio`, `function_trace` are repeated in every part.
The forwarder reassembles per (`pid`, `trace_id`) and exports when `last` arrives or after **5 s**; incomplete traces
get `openlog.php.incomplete=true` on the root span (or on every span if the root is missing).

## 3. Function-level traces (transaction tracer)

- The extension records a **bounded segment tree** of userland function calls for every sampled request, in memory only.
- It is **sent only if** the transaction duration ≥ `openlog.transaction_tracer.threshold_ms` (default **500 ms**) or the
  transaction ended with an error; otherwise discarded at request end.
- Limits: `openlog.transaction_tracer.max_segments` (default **2000**), `min_segment_ms` (default **1 ms**; faster calls
  are not individual spans but add to the parent's `openlog.php.fast_calls` and `openlog.php.fast_calls_ns`), memory cap
  `openlog.transaction_tracer.max_memory_kb` (default **4096**). Reaching a limit stops recording and increments
  `dropped_spans`.
- Segment spans: kind 1, name `Class::method` or `function`, attributes `code.function.name`, `code.namespace`,
  `code.file.path`, `code.line.number`, `openlog.php.segment="function"`.
- Internal functions are traced only when in the instrumented set (datastores, HTTP, `sleep`/`usleep`, `file_*` on
  remote streams) — they appear as their own client/internal spans, not as function segments.
- Overhead budget (enforced by benchmarks in CI): tracer disabled ≤ 3 % RPS; enabled with default thresholds ≤ 7 % RPS on
  the reference Laravel app.

## 4. Extension settings (`php.ini`)

| Setting | Default |
|---|---|
| `openlog.enabled` | `1` |
| `openlog.service_name` | `PHP_SAPI`-based fallback `php-app`; env `OPENLOG_SERVICE_NAME` overrides |
| `openlog.service_namespace`, `openlog.service_version`, `openlog.environment` | empty; env `OPENLOG_*` overrides |
| `openlog.transport` | `unix:///run/openlog-infra-agent/php.sock` |
| `openlog.sampling_ratio` | `1.0` (parent-based: an incoming sampled `traceparent` is always recorded) |
| `openlog.capture_query_text` | `sanitized` (`raw`, `off`) |
| `openlog.transaction_tracer.enabled` | `1` |
| `openlog.transaction_tracer.threshold_ms` | `500` |
| `openlog.transaction_tracer.max_segments` | `2000` |
| `openlog.transaction_tracer.min_segment_ms` | `1` |
| `openlog.transaction_tracer.max_memory_kb` | `4096` |
| `openlog.log_level` | `warning` (to the PHP error log, rate-limited) |

Extension self-metrics are carried in each message's root span attributes only when non-zero
(`openlog.php.send_errors`, `openlog.php.dropped_messages` since the previous message of this worker).

## 5. Runtime support (D-036)

| PHP | Hook layer |
|---|---|
| 7.1 – 7.4 | `zend_execute_ex` / `zend_execute_internal` replacement (chain previous handlers) |
| 8.0 + | Observer API (`zend_observer_fcall_register`) |

Build targets: every minor version 7.1…8.4 (8.5 when released) × NTS/ZTS × glibc/musl × amd64/arm64. The extension
must fail open: any internal error disables instrumentation for the rest of the request and logs once.

## 6. Forwarder (infra agent module `php_forwarder`)

| Config | Default |
|---|---|
| `php_forwarder.enabled` | `true` when discovery finds a PHP runtime (php-fpm, mod_php, php CLI binary), else `false`; explicit value wins |
| `php_forwarder.socket` | `/run/openlog-infra-agent/php.sock` (empty = no unix listener; then `udp_listen` is required) |
| `php_forwarder.socket_group` | auto (see §1); `auto`, a group name or a numeric gid |
| `php_forwarder.socket_mode` | `"0660"`; `"0666"` is the explicit opt-in of §1 (any local user may send) |
| `php_forwarder.udp_listen` | empty (disabled) |
| `php_forwarder.max_pending_traces` | `10000` |
| `php_forwarder.reassembly_timeout` | `5s` |

- Converts messages to OTLP spans (one ResourceSpans per distinct resource) and hands them to the agent's existing export
  pipeline (queue, disk buffer, retries, `Retry-After`) as `/v1/traces` payloads — PHP data survives backend outages
  like host metrics. Spans are batched for at most 1 s and up to half of `export.max_request_bytes` per request.
- Validates input (sizes, hex IDs, value types, attribute count ≤ 128 per span, string values ≤ 4 KiB) and drops
  malformed messages; never trusts `resource` host keys. Details of the validation (v1):
  - a datagram longer than 60 000 bytes, invalid JSON or trailing data → `malformed`; valid JSON with `v` ≠ 1 →
    `unsupported_version`; unknown fields are ignored (additive changes stay compatible within v1);
  - IDs must be lowercase hex of the exact length and not all zeros; span ids unique within a message; `pid` > 0;
    `seq` in 0…255; `sampling_ratio` absent = 1; `kind` 1…5, `status` 0…2, `start` > 0;
  - attribute keys 1…256 bytes; ≤ 128 attributes per span and per event; every string value (attributes, array
    elements, resource values, names, `status_msg`) ≤ 4 KiB — the extension must truncate, the forwarder does not;
    arrays must not nest and must be homogeneous (integers and floats mixed become floats); `null` and objects are
    invalid;
  - one invalid span drops the whole message.
- Resource: the allowed extension keys (§2.1) are kept, everything else is dropped silently; the forwarder adds
  `telemetry.sdk.language=php` and the infra agent's `host.*`, `os.*` and `openlog.agent.*` attributes (not
  `openlog.entity.type` or `host.extra_attributes`).
- Span flags: a span whose parent is in the same trace message gets `SPAN_FLAGS_CONTEXT_HAS_IS_REMOTE` (local parent);
  a span of a complete trace whose parent is not in it (continued from `traceparent`) additionally gets
  `SPAN_FLAGS_CONTEXT_IS_REMOTE`, so APM entry detection (apm.md §2) does not depend on how spans are batched.
- Root span: the span without parent; else, for a complete trace, the earliest span whose parent is not in the trace;
  for an incomplete trace only a single such span. `dropped_spans` is the maximum over the parts.
- Reassembly limits: `max_pending_traces` and 64 MiB of pending datagrams; beyond either the oldest pending trace is
  evicted (its parts counted as `dropped`), a duplicate `seq` or a part after `last` is `dropped`. On agent shutdown
  or when the module is switched off, pending traces are exported as incomplete.
- Self-telemetry: `openlog.agent.php.messages{result=accepted|malformed|unsupported_version|dropped}`,
  `openlog.agent.php.spans`, `openlog.agent.php.reassembly_timeouts`, `openlog.agent.php.pending_traces`
  (semantic-conventions §2 agent self-telemetry). `accepted` is counted when the message's trace is exported.
- Discovery: the `php-fpm` rule's `apm_hint` becomes actionable — the UI shows whether `openlog.so` is loaded
  (detected via the forwarder having received data from that host/service in the last 10 min).
  The agent sets `apm_hint.status` = `active` when the forwarder accepted a valid message in the last 10 min, else
  `not_installed` (semantic-conventions §3.4), for every discovered service whose hint names `openlog-agent-php`; a
  status change triggers an inventory snapshot.
- Permissions: the agent runs as `openlog-agent`; `chgrp` of the socket to the PHP group only works when that user is
  a member of the group (e.g. systemd drop-in `SupplementaryGroups=www-data`). Otherwise the socket keeps the agent's
  group, a warning is logged and `openlog.agent.permission_denied{collector=php_forwarder}` is incremented.
