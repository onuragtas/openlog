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
- **Logs correlation (optional):** PHP functions `openlog\trace_id()`, `openlog\span_id()` (innermost non-segment
  span) and `openlog\traceparent()` for log processors; `openlog\set_transaction_name()` overrides the name.
- **Long-running workers (D-058):** one transaction (kind 2, same naming and attributes as a web request) per request
  handled by Laravel Octane (`Worker::handle`, status from the client's `respond`), RoadRunner
  (`Spiral\RoadRunner\Http\HttpWorker::waitRequest` … `respond`/`respondStream`, also below PSR7Worker and Octane on
  RoadRunner) and Swoole/OpenSwoole HTTP servers (the callable registered with `Server::on('request')` or
  `Coroutine\Http\Server::handle` … `Response::end`/`redirect`/`sendfile`, status from `Response::status`) and
  FrankenPHP worker mode (the callable passed to `frankenphp_handle_request()`: its call starts the transaction, its
  return ends it; method, path, host, client, user agent, `traceparent` come from the per-request `$_SERVER` /
  `SG(request_info)` FrankenPHP has already reset, the status from `http_response_code` when the callback returns, an
  exception escaping the callback is recorded; `exit()` in a request and worker restarts are handled, the worker
  script's own run is never sent). FrankenPHP classic mode is a regular SAPI request. The worker
  process's own CLI transaction is dropped when the first request starts; between requests nothing is recorded and no
  header is propagated; connection attributes survive requests. Requests overlapping in one process (Swoole
  coroutines) are never mixed: while more than one is in progress, child spans, route names and the function tracer
  stop for all of them, every request still gets its root span with `openlog.php.concurrent=true`, and outgoing
  `traceparent` headers (and `openlog\traceparent()`) carry the context of the request whose handler is on the
  current coroutine's call stack — none when no handler is. Not covered: `openlog.userland_hooks=0` (the request
  callables are observed userland functions), a request callable that already ran before it was passed to
  `frankenphp_handle_request()` / `Server::on()`.

### 2.3 Splitting

If the JSON exceeds 60 000 bytes, spans are split across datagrams with the same `pid` + `trace_id`, increasing `seq`
from 0, `last: true` only on the final one; `resource`, `sampling_ratio`, `function_trace` are repeated in every part.
Field order inside the object is not significant (the extension writes `seq`, `last` and `dropped_spans` after
`spans`). The kernel's datagram queue is short (`net.unix.max_dgram_qlen`), so for split messages the extension may
retry a part on `EAGAIN` for at most ~2 ms per message, never longer.
The forwarder reassembles per (`pid`, `trace_id`) and exports when `last` arrives or after **5 s**; incomplete traces
get `openlog.php.incomplete=true` on the root span (or on every span if the root is missing).

## 3. Function-level traces (transaction tracer)

- The extension records a **bounded segment tree** of userland function calls for every sampled request, in memory only.
  The tree is built from **wall-clock stack samples** taken every `min_segment_ms` and at the start and end of every
  instrumented span (observing every call costs ~60 ns per call, > 1 ms on a framework request; measured).
- It is **sent only if** the transaction duration ≥ `openlog.transaction_tracer.threshold_ms` (default **500 ms**) or the
  transaction ended with an error; otherwise discarded at request end.
- Limits: `openlog.transaction_tracer.max_segments` (default **2000**), `min_segment_ms` (default **1 ms**; the sampling
  interval after the first 100 ms of a request, 10 ms before (D-057); calls shorter than it appear only when a sample
  hits them, consecutive calls of the same function from the same frame without a sample in between form one segment), memory cap `openlog.transaction_tracer.max_memory_kb`
  (default **4096**), sampled call depth 256 (outermost frames). Reaching a limit stops recording and increments
  `dropped_spans`.
- Segment spans: kind 1, name `Class::method` or `function`, attributes `code.function.name`, `code.namespace`,
  `code.file.path`, `code.line.number`, `openlog.php.segment="function"`, `openlog.php.samples` (number of samples the
  segment is based on; start/end are ± half an interval).
- Internal functions are traced only when in the instrumented set (datastores, HTTP, `sleep`/`usleep`, `file_*` on
  remote streams) — they appear as their own client/internal spans, not as function segments.
- Overhead budget (enforced by benchmarks in CI): tracer disabled ≤ 3 % RPS; enabled with default thresholds ≤ 7 % RPS on
  the reference Laravel app. Measured state and method: `agents/php/ext/README.md` "Overhead" (D-057). On PHP 8 the
  Observer API itself costs every function call ~100 CPU instructions on 8.0–8.3 and ~50–65 on 8.4 once any fcall
  observer is registered, independent of what the observer does; `openlog.userland_hooks=0` avoids it (§4).

## 4. Extension settings (`php.ini`)

| Setting | Default |
|---|---|
| `openlog.enabled` | `1` |
| `openlog.service_name` | empty → `php-app` (web SAPIs) or `php-cli` (CLI); env `OPENLOG_SERVICE_NAME` overrides |
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
| `openlog.userland_hooks` | `1`. `0` (system): lean mode — no fcall observer / `zend_execute_ex` override, only internal functions are instrumented (datastores, HTTP clients, sleep); no framework route names, no framework-reported exceptions, no Predis, no long-running worker transactions; uncaught exceptions are recorded from the fatal error (`exception.type` = `E_ERROR`) |

Extension self-metrics are carried in each message's root span attributes only when non-zero
(`openlog.php.send_errors`, `openlog.php.dropped_messages` since the previous message of this worker).

## 5. Runtime support (D-036)

| PHP | Hook layer |
|---|---|
| 7.1 – 7.4 | `zend_execute_ex` / `zend_execute_internal` replacement (chain previous handlers) |
| 8.0 + | Observer API (`zend_observer_fcall_register`) |

Build targets: every minor version 7.1…8.4 (8.5 when released) × NTS/ZTS × glibc/musl × amd64/arm64 (§7.1: glibc
modules need at most glibc 2.28). The extension must fail open: any internal error disables instrumentation for the
rest of the request (for a long-running worker: of the worker request) and logs once.

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

## 7. Distribution (D-059)

### 7.1 Release artifacts

Built per architecture (amd64, arm64) by `agents/php/packaging/build-artifacts.sh VERSION OUT_DIR` and published with
every product release in the same signed `manifest.json` (releases-updates.md §2):

| File | Content |
|---|---|
| `openlog-php-agent_<v>_linux_<arch>.tar.gz` | one top-level directory `openlog-php-agent_<v>_linux_<arch>/`: `modules/<api>-<nts\|zts>-<glibc\|musl>/openlog.so`, `modules.txt` (one line per module: key, PHP version built against, sha256), `bin/openlog-php-install`, `VERSION`, `ARCH`, `LICENSE`, `README.md` |
| `….deb`, `….rpm` | the tree in `/opt/openlog/php-agent/versions/<v>/` (root-owned), `current` → `versions/<v>`, `/usr/bin/openlog-php-install`; postinstall runs `openlog-php-install install` (no restart), preremove runs `uninstall` |
| `….apk` | the same for Alpine |

- Module key: `ZEND_MODULE_API_NO` (`php -i` "PHP Extension"), thread safety, C library. PHP 7.1–8.4 × NTS/ZTS ×
  glibc/musl = 36 modules per architecture. Debug builds of PHP are not supported.
- glibc modules are compiled on AlmaLinux 8 against headers configured from the PHP source of the official image and
  must not reference a glibc symbol version newer than 2.28 (the build fails otherwise): RHEL/Alma/Rocky 8+, Debian 10+,
  Ubuntu 20.04+, Amazon Linux 2023. musl modules are compiled in the official Alpine images. Every module is
  load-tested in the official PHP image of its ABI.
- Manifest entries: `{"component": "php-agent", "os": "linux", "arch": "<arch>", "format": "tar.gz" | "deb" | "rpm" |
  "apk"}`.

### 7.2 Enabling PHP runtimes (`openlog-php-install`)

`openlog-php-install [install|uninstall|status] [--php BIN]… [--dry-run] [--reload] [--json]`, POSIX sh, runs as root.

- Runtimes: `php`, `php-cgi`, `php-fpm` binaries in the usual places (Debian/Ubuntu `php<v>`, `php-fpm<v>`; RHEL and
  Remi `/opt/remi/php*/root`; Alpine `php<vv>`; `/usr/local` of the official images; `/opt/php*`) or `--php BIN`,
  de-duplicated by real path. For each: `<bin> -i` → API number, thread safety, debug build, ini scan directory.
- Enabling: `<scan dir>/90-openlog.ini` whose first line is `; managed by openlog-php-install` and which loads the module
  by absolute path through the stable link: `extension=/opt/openlog/php-agent/current/modules/<key>/openlog.so` (an
  upgrade only switches `current`; nothing is copied into PHP's `extension_dir`). Debian/Ubuntu:
  `/etc/php/<v>/mods-available/openlog.ini` plus `90-openlog.ini` links in every SAPI's `conf.d` (cli, fpm, apache2,
  cgi). Files without the marker line are never written or removed; settings go into another ini file.
- Fail safe: after writing, `<bin> -m` must list `openlog`; otherwise the file (and links) are removed again and the
  exit status is 1. A runtime that already loads openlog through another ini file, has no matching module or no scan
  directory is left alone and reported.
- No restart unless `--reload` (systemd `reload` of running `php*fpm*`, `httpd*`, `apache2*` units; OpenRC reload).
- `status --json`: one object per line: `bin`, `version`, `api`, `zts`, `debug`, `libc`, `scan_dir`, `module` (key),
  `supported` (module present), `enabled` (managed ini present), `loaded` (`-m` lists openlog).
- `uninstall` removes every managed ini file and link, also those of runtimes that no longer exist.

### 7.3 Fleet installation through the infra agent (D-082, D-083)

The infra agent installs, upgrades and removes the PHP agent, using the release pipeline of its own updates
(releases-updates.md §3, D-042). Code: `agents/infra/internal/phpagent`; backend: `internal/fleet` (policy section,
decision), API: api.md "Fleet". The agent user never writes to the install root or PHP ini directories; all changes
happen in the unit's privileged pre-start step `-apply` (root). The agent requests one and exits once so that systemd
starts it again (D-082: no second privileged entry point; PHP agent changes are rare, the gap is a few seconds of
telemetry and PHP spans arriving during the restart are lost).

1. **Configuration** `php_agent` in `config.yaml`: `mode: off | manual | auto` (default `manual`: inventory only;
   `auto`: install and upgrade where a supported PHP runtime is found; `off`: remove a fleet installation), `version:
   agent | <semver>` (default `agent`: the infra agent's own version), `reload: none | graceful` (default `none`),
   `exclude_bins` (globs, at most 50), `remote_config` (default `true`), `health_check_after` (default `5m`, at least
   `10s`), `install_root` (default `/opt/openlog/php-agent`). The fleet sends mode, version, reload, exclude_bins and the
   resolved target through sync (`php_agent`, releases-updates.md §3 "PHP agent"); with `remote_config: true` they
   replace the local values (persisted in `<state_dir>/php-agent/remote.json`). The privileged step reads only the
   root-owned configuration (install root; it refuses installs when that file says `mode: off` and
   `remote_config: false`).
2. **Inventory** (reported in every sync; a change triggers one): the fields of `openlog-php-install status --json` from
   `<install_root>/current` when installed, otherwise the agent runs `<bin> -i` / `<bin> -m` itself for the installer's
   binary locations (§7.2; `supported` = API number of PHP 7.1–8.4 and no debug build), re-read every 5 minutes, plus
   `excluded`, the installed `version` (`current` → `versions/<v>`) and `managed_by`: `fleet` (the root-owned marker
   `.managed-by-openlog-infra-agent` exists), `package` (`dpkg-query -S`/`dpkg -S`/`rpm -qf`/`apk info -W` own the
   install root), `manual` (anything else) or `none`. `package` and `manual` roots are never changed; the privileged step
   refuses every request for a root without the marker.
3. **Capability**: only when the privileged pre-start step ran for this start (`apply-status.json` of the same
   invocation), the binary is in the versions layout and release keys exist (not in containers or dev builds);
   otherwise the report has `capable=false` and a `reason`.
4. **Staging** (unprivileged, `mode=auto`, target ≠ installed, a supported non-excluded runtime, no self-update in
   progress): the signed manifest of the target comes from the fleet (`php_agent.manifest`) or, locally with
   `version: agent`, from the manifest kept next to the infra agent binary. Verification: signature by a trusted key,
   product/schema, manifest version = target, a `php-agent` `tar.gz` artifact for `linux/<arch>`, and for a downgrade
   target ≥ `rollback_floor` of the installed version's manifest. Download (size and sha256 of the signed manifest;
   license key only for the ingest host) and a trial extraction that must contain `bin/openlog-php-install` and
   `modules/<key>/openlog.so` for at least one runtime of the host; then `<state_dir>/php-agent/staged/<v>/{archive.tar.gz,
   manifest.json,manifest.json.sig}` and `<state_dir>/php-agent/request.json` `{id, action: install, version, reload,
   exclude_bins, at}` (all untrusted) and exit. A failure before the restart is reported as `failed` and retried after
   1 h.
5. **Privileged install** (`-apply`, after the agent's own update step, once per request id): everything is read
   through `os.Root(state_dir)` with size limits; verification again with the keys compiled into the root-owned binary;
   the archive is copied into `versions/.<v>.partial` and checked there; extraction as root (no absolute paths, `..`,
   symlinks, links or devices, files 0644/0755 only) into `versions/<v>/` with the manifest and signature, which must be a
   root-owned tree; marker written; `current` switched atomically; `versions/<v>/bin/openlog-php-install install`
   (`--php <bin>` for the non-excluded root-owned runtimes of its own `status --json` when `exclude_bins` is set,
   `--reload` for `reload: graceful`: the installer reloads running `php*fpm*`, `httpd*`, `apache2*` units; they are
   recorded). Success = every enabled runtime loads openlog and at least one does. Otherwise `current` goes back to
   the previous version (reinstalled) and the new directory is removed (`rolled_back`), or with no previous version the
   installer's `uninstall` runs and the install root is removed (`failed`). On success older versions except the new
   and the previous one are removed. The result is the root-owned `<infra install root>/php-agent-status.json`
   `{request_id, at, action, operation, result: applied|failed|rolled_back|uninstalled|rejected, target, version,
   previous, error, runtimes, units}`; `rejected` = a verification or ownership check failed, nothing changed.
6. **Health check** (agent, `health_check_after` after `applied`): `status --json` of `current` shows every
   non-excluded enabled runtime loaded and at least one, every recorded unit is `active`, and PHP services that sent
   data when the installation was requested (`apm_hint.status=active`, §6) still do. On failure the agent requests
   `rollback` of that version (after a running self-update); `-apply` switches back to the recorded previous version and
   reinstalls it, or runs `uninstall` and removes the install root. A rolled back version is never installed again
   automatically.
7. **Removal**: `mode: off` with `managed_by=fleet` requests `uninstall`: the installer removes every managed ini file
   (`--reload` with `reload: graceful`) and the install root is deleted. A failed removal is retried after 1 h.
8. **Report** (`php_agent` of the sync request): `mode`, `source` (`local` | `remote`), `capable`, `reason`,
   `managed_by`, `version`, `runtimes[]`, `update{operation: install|upgrade|rollback|uninstall, version, state:
   downloading|restarting|confirming|applied|failed|rolled_back|uninstalled, error, changed_at}`.
9. **Self-telemetry**: `openlog.agent.php_agent.operations{operation=install|upgrade|rollback|uninstall,
   result=success|failure}` (install/upgrade count after the health check; a failed installation that `-apply` rolled
   back counts as a failure of the installation, a health rollback as `rollback`); the host resource carries
   `openlog.php_agent.version` (read at start; every change by the infra agent restarts it).
10. **Fleet policy** (backend, D-083): the update policy's `php_agent` section `{mode (default manual), version (default
    agent), reload, exclude_bins, changed_at}` and per-host mode overrides (`agent_php_host_overrides`). A host in
    `auto` gets `target_version` + signed manifest when its report is capable, not managed elsewhere, has a supported
    runtime, the target is a verified release with a `php-agent` artifact for its platform, it did not roll that version
    back, it is in the current wave (`fnv32a(host_id + ":php-agent:" + target) % 100` below the policy wave reached after
    `wave_soak_minutes` per wave since the later of `changed_at` and the release time; a per-host `auto` override skips
    waves) and a maintenance window is open.
