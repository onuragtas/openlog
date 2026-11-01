# Contract: Mobile crash reporting (D-151)

**Status: partly built.** Done: the payload (§1), capture on all three SDKs (§2) except what its table marks as a second step,
the crash-free rate (§5) and the UI (§6). Not yet: symbolication (§3) and grouping on the stack (§4), so crashes group on type
and message. User guide: [Crash reporting](../guide/crash-reporting.md). Related: [rum.md](rum.md) §2.7, [apm.md](apm.md) §3.

**The problem in one sentence:** an unhandled crash is not an `error` span the process lives to send, so it has to
be written down by the dying process, or by the operating system, and delivered on the next launch.

**A crash is still an error span.** It goes through `POST /v1/rum` and lands in the existing error inbox with the
same grouping and resolution workflow. `openlog.rum.event` stays `error`, because `rum_sessions` counts errors by
that value and its view cannot be altered (rum.md §3.7). What is new is one attribute saying this error was fatal.

## 1. The payload

An error span as in mobile-agent.md §3, plus:

| Attribute | Required | Value |
|---|---|---|
| `openlog.rum.crash.kind` | yes | `uncaught_exception`, `signal`, `anr` or `out_of_memory`. Anything else is dropped, and the span stays an ordinary error |
| `openlog.rum.crash.id` | yes | 32 random hex characters, fixed when the crash is recorded, so a retried delivery is one crash |
| `openlog.rum.crash.session_unknown` | no | `true` when the session is a guess, because the report named none |

Three rules follow from delivery being late:

- **`session.id` is the crashed session's, read from disk**, not the current one. Otherwise the session that crashed
  looks crash-free and the innocent next one does not.
- **`service.version` is the crashed build's.** After an update, a crash from the old version must not count against
  the new one. Resource attributes apply per request, so **one request per crashed build**. `service.version` must
  therefore be written into the crash file.
- **The span's timestamp is kept as sent, up to 7 days old.** Older than that, it would land past the trace
  retention and is dropped with `crash_too_old`. A timestamp in the future is replaced with the server's time.

A sampled-out session sends nothing, crash included (mobile-agent.md §4). The crash-free rate stays correct because
sessions and crashes are weighted alike. Reporting crashes of unsampled sessions would divide all crashes by a
sampled share of sessions and understate the rate.

## 2. Capture

Off by default in each SDK (`captureCrashes`), because it installs a global handler in somebody else's process.
A previously installed handler is **always chained to and never replaced**, or the application's own crash tool
would stop working silently.

| Platform | Mechanism | Covers | v1 |
|---|---|---|---|
| Kotlin/JVM | `Thread.setDefaultUncaughtExceptionHandler` writes a file | JVM exceptions | ✅ |
| Android | `ApplicationExitInfo` (API 30+) read at next launch | native signals, ANR, low-memory kills | Second step |
| Swift | MetricKit `MXCrashDiagnostic`, delivered by the system | signals, Mach exceptions, Swift traps | ✅ |
| Swift | `NSSetUncaughtExceptionHandler` | Objective-C exceptions, for name and reason | ✅ |
| Unity, Android | `ApplicationExitInfo` (API 30+) through JNI, read at next launch | native crashes, ANR, foreground low-memory kills | ✅ |
| Unity, iOS | MetricKit through a small Objective-C plugin | signals, Mach exceptions | ✅ |
| Dart/Flutter | `recordFatal` wired to `FlutterError.onError` and `PlatformDispatcher.onError` | Dart exceptions | ✅ |

**Why not our own signal handler.** A signal handler may only call async-signal-safe functions, and getting that
wrong hides the crash, or worse, hangs the application at the moment it was going down. MetricKit has the operating
system do the capture, and costs the SDK only a system framework import behind `#if canImport(MetricKit)`.

**The costs of MetricKit, stated.** It needs iOS 14 or newer, and delivery can be later on older releases. It carries
no session id, so the SDK keeps the last session and version on disk and attaches them on a best-effort basis; a
crash it cannot match gets `session.id` of a new one-span session and is flagged by `openlog.rum.crash.session_unknown`.
An uncaught Objective-C exception is seen twice, by the handler and by MetricKit: the session counts once, and the error inbox
twice.

**Android stays plain Kotlin/JVM.** The core SDK has no Android dependency, and that is worth keeping. `ApplicationExitInfo`
goes in an optional `openlog-android` artifact that passes exit records to the core SDK. Until that ships, **an NDK crash
or ANR on Android is not reported**, and the documentation must say so.

**Flutter.** A Dart uncaught error usually does not kill the process, so the span can be sent at once rather than
written to disk. It is sent with `crash.kind = uncaught_exception` as long as the application wired it as fatal; the
SDK stays pure Dart, so the wiring is a documented snippet. A crash of the Flutter engine or a plugin is native, and
is caught by the Swift or Android SDK when the host application includes it.

### 2.1 The crash file

- Directory given by the application (`crashDirectory`): the Kotlin and Dart SDKs have no platform API to find one.
- One JSON file per crash: the span plus resource attributes, written to a temporary name and renamed.
- At most 10 files. When full, the **oldest are deleted**. A crash loop must not fill the device, and the first
  crashes of a loop say what the rest would.
- Deleted only after a `2xx`, or a `4xx` other than `429`. Retries follow mobile-agent.md §6.
- The handler does one write and returns. It never flushes the event queue or touches the network.

## 3. Symbolication

Without this, release builds report `a.b.c()` and `0x1042f3a8c`. It follows source maps ([api.md](api.md#source-maps)):
index in PostgreSQL, documents in `internal/objstore`, applied at read time.

| Kind | Input | Key | Parsed by |
|---|---|---|---|
| `r8` | `mapping.txt` | app, version | Go, new (text format) |
| `dsym` | a compact table, made by the CLI from a dSYM | image UUID | Go, new |
| `flutter` | `--split-debug-info` symbols | build id | **Second step** |

**None of this section is built yet.**

```
POST   /api/v1/symbols?app=&kind=&version=&image=     raw body
GET    /api/v1/symbols
DELETE /api/v1/symbols/{id}
```

Migration `0106_crash_symbols.sql`. Upload rights match source maps.

**Why a CLI step for dSYM.** A dSYM can run to hundreds of megabytes, well beyond the 32 MiB source map limit, and most
of it is type information a stack does not need. `openlog symbols upload` reads it with `debug/macho` and `debug/dwarf`
from the Go standard library and uploads only address range → function, file and line. It comes with a Gradle task and
an Xcode build phase snippet, because a step someone must remember to run is a step that gets skipped.

## 4. Grouping: the hard part

Today's group id hashes the error type, the message and the top in-app frame **at write time** (apm.md §3.1), while
source maps apply at read time. The frame key is `function@file` with build-specific parts removed, such as the content
hash of a bundle (`frameKey` in `internal/apm/errors.go`). That holds as long as names survive between builds, which a
minifier does not promise either, but mobile release builds break it outright:

- R8 renames classes differently per build, so `a.b.c` changes every release, and **every release would reopen every
  group**.
- A native frame is an address, which moves in each build.

**Proposal.** Crash spans get their fingerprint frame in the processor from the same symbol files:

```
frame = top in-app frame after symbolication, if symbols for this build exist
      | "" otherwise
error_group_id = as apm.md §3.1, with that frame
```

- With symbols uploaded **before release**: stable groups across versions, as expected.
- Without them: grouped by type and message alone. Coarser, but it does not produce a new group per release. A native
  crash's type and message are often generic (`SIGSEGV`), so such groups will be broad.
- **Symbols arriving late do not regroup old occurrences.** Group ids are written once, so the UI warns on a version
  with crashes and no symbols.

Symbol lookup on the write path is new: the processor has not needed PostgreSQL or object storage for this until now.
It needs an in-memory cache keyed by build, with absence cached as well, and it **must never block ingestion**: on a
miss or an error, use the fallback above. This is the riskiest part of the design and should get its own review.

## 5. Crash-free rate

**`crash_free_rate` measured something else.** It counted sessions with any `error` span, so one handled exception made a
session "not crash-free", and native crashes were not counted at all. The response now separates them:

| Field | Meaning | When |
|---|---|---|
| `error_free_rate` | sessions without an `error` span | always, `1` with no sessions |
| `session_crash_free_rate` | capturing sessions without a crash | **null** for a version with no capturing sessions |
| `crash_free_rate` | the same as `error_free_rate` | kept for one release |

Null replaces the `1` that rum.md §2.7 chose for errors, because for a version that **cannot** report crashes, 100% is a false
statement about the number a release is judged on. The web console draws it as "—".

The capture signal is `openlog.rum.crash.capture = "1"` on the resource of each request, sent when `captureCrashes` is on.

**The old name is deprecated rather than changed,** because the store's mobile console updates more slowly than a server, and
a number changing meaning under the same name is worse than a redundant field. The mobile console labels it "Error-free"
from now on. Removing the field, and moving `session_crash_free_rate` to the name it should have had, is a later release.

**The new table,** `rum_release_sessions_1h` (`0103_release_sessions.sql`, class `rum`, 30 days, no personal data): per tenant,
app, environment, version and hour: sessions, error sessions, crashed sessions and capturing sessions, as `uniqCombined64`
sketches. This also ends the gap in rum.md §2.7, where releases reached back 30 days and sessions 7. Sessions before the
migration are not in it, and counts past a few thousand are estimates.

## 6. UI

- A **Crashes** tab per application, from `GET /api/v1/rum/crashes`: groups with occurrences, affected sessions, last version
  and the newest stack. It reads spans, so it reaches 7 days. The workflow stays in the APM inbox.
- The **Releases** tab shows the crash-free and error-free rates side by side.
- OQL: `SELECT count(*) FROM Span WHERE openlog.rum.crash.kind != ''` is alertable with no new rule type.
- Not yet: a warning on a version with crashes and no symbols (§4), which needs §3.

## 7. Order of work

1. ~~Payload, sanitize, the new table and the API fields (§1, §5).~~ Done.
2. ~~Capture on Kotlin, Dart and Swift, with file delivery (§2).~~ Done.
3. R8 and dSYM symbols, with the CLI (§3).
4. Grouping on the write path (§4). **Until this lands, crashes group by type and message only.**
5. Optional `openlog-android` for NDK crashes and ANR. Flutter symbols.

## 8. Open questions

1. **Is breadcrumb data in scope?** The screens and events before a crash already exist on the session timeline for 7
   days. Proposed: no separate breadcrumb trail in v1, link to the session.
2. **Is `out_of_memory` honest on iOS?** MetricKit reports exits by reason in `MXAppExitMetric`, as counts and not as
   per-session events. Proposed: Android only until proven otherwise.
3. **Are crash sessions enough for 30 days?** Group detail reads raw spans for 7 days, while `apm_error_groups` outlives
   them. It needs checking against a real upload whether a crash older than a week still opens with a useful stack.
