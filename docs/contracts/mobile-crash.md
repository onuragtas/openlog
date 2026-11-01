# Contract: Mobile crash reporting (D-151)

**Status: built,** except Flutter symbols (§3). Grouping changed from the proposal: see §4. User guide: [Crash reporting](../guide/crash-reporting.md). Related: [rum.md](rum.md) §2.7, [apm.md](apm.md) §3.

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
| Android | `ApplicationExitInfo` (API 30+) by reflection, read at next launch, with `androidContext` | native signals, ANR, low-memory kills | ✅ |
| Swift | MetricKit `MXCrashDiagnostic`, delivered by the system | signals, Mach exceptions, Swift traps | ✅ |
| Swift | `NSSetUncaughtExceptionHandler` | Objective-C exceptions, for name and reason | ✅ |
| Unity, Android | `ApplicationExitInfo` (API 30+) through JNI, read at next launch | native crashes, ANR, foreground low-memory kills | ✅ |
| Unity, iOS | MetricKit through a small Objective-C plugin | signals, Mach exceptions | ✅ |
| Dart/Flutter | `recordFatal` wired to `FlutterError.onError` and `PlatformDispatcher.onError` | Dart exceptions | ✅ |

**Why not our own signal handler.** A signal handler may only call async-signal-safe functions, and getting that
wrong hides the crash, or worse, hangs the application at the moment it was going down. MetricKit has the operating
system do the capture, and costs the SDK only a system framework import behind `#if canImport(MetricKit)`.

**The costs of MetricKit, stated.** It needs iOS 14 or newer, and delivery can be later on older releases. It names the build number (`CFBundleVersion`) and not the version a
report goes by, so the two can only be matched for the build running now. An earlier build is taken to be the version the last session ran, and a build number that never changes, as
with Unity's default `0`, attributes an old crash to the new version. It carries
no session id, so the SDK keeps the last session and version on disk and attaches them on a best-effort basis; a
crash it cannot match gets `session.id` of a new one-span session and is flagged by `openlog.rum.crash.session_unknown`.
An uncaught Objective-C exception is seen twice, by the handler and by MetricKit: the session counts once, and the error inbox
twice.

**Android stays plain Kotlin/JVM.** The core SDK has no Android dependency, and that is worth keeping. So `androidContext` is typed `Any`
and `ApplicationExitInfo` is read by reflection, in place of the optional `openlog-android` artifact first proposed: a second artifact
would be a second thing to forget. A Java crash in that record is skipped, because the handler already reported it.

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

Without this, release builds report `a.b.c()` and `0x1042f3a8c`. It follows source maps ([api.md](api.md#crash-symbols)): index in
PostgreSQL (`0106_crash_symbols.sql`), files in `internal/objstore`, under the same storage settings, applied at read time.

| Kind | Input | Key | Parsed by |
|---|---|---|---|
| `r8` | `mapping.txt` | app, version | `internal/symbols` |
| `native` | a table the CLI makes from a dSYM or an ELF | app, image id | `internal/symbols` |
| `flutter` | `--split-debug-info` symbols | build id | **Not built** |

**The native frame format,** which every SDK writes and the server reads:

```
#00 libil2cpp.so 0123abcd… +0x1a2b Game::Update()
```

An image id (a Mach-O UUID or an ELF build id, dashes and case ignored) and an offset, with the name when the device knew one. The
binary's name has no spaces, and a path holds a random install directory, so it is the file name alone. An address would mean
nothing, because where an image loads changes each launch.

- **Android, API 31+:** `getTraceInputStream` is a protobuf tombstone (AOSP `tombstone.proto`), decoded by hand for the few fields a report
  uses, so no SDK carries a protobuf library. `rel_pc` of the crashing thread is the offset, and it is an ELF virtual address.
  At API 30 there is none, and the report has no stack.
- **iOS:** MetricKit's call stack tree, flattened to the attributed thread. The root is the innermost frame, and `subFrames` hold callers.
  `offsetIntoBinaryTextSegment` is relative to `__TEXT`, so the table is too. Checked against real reports from an iPhone on iOS 26. A tree that can't be read is sent as
  it came.
- **ANR:** the system's thread dump, text, from the `"main"` thread on. It opens with memory figures and an event log, which would spend the
  stack limit. Its native lines are not symbolicated. Its Java frames are retraced like any other.

**Why a CLI step for native symbols.** A dSYM can run to hundreds of megabytes, and most of it is type information a stack does not need.
`openlog-symbols` reads it with `debug/macho`, `debug/elf` and `debug/dwarf` and uploads only address range → function, file and line, as a gzip
text table. Names are demangled there, so the server needs no demangler. Swift's mangling is not covered. A build directory holds an image
twice, stripped and not, under one id, so the copy with the most functions is the one sent.

**Upload rights differ from source maps,** deliberately: admin or owner, API keys allowed, because the caller is a build machine and a step
someone must sign in for is a step that gets skipped.

**Not done:** symbol files have no retention, unlike source maps' `OPENLOG_SOURCE_MAPS_RETENTION_DAYS`: a build still in the field needs its
symbols however old it is. They last until deleted, or until the organization is. A Gradle task and an Xcode build phase were proposed and
not written; the guide shows the commands.

## 4. Grouping: the hard part

The group id hashes the error type, the message and the top in-app frame **at write time** (apm.md §3.1), while symbols apply at read time.
Mobile release builds break the frame half: R8 renames classes differently per build, and a native frame is an address. So a crash's
fingerprint frame is empty (`frame = ""` in `internal/apm/errors.go`), and the inbox groups by type and message. That does not produce a new group
per release, but groups are broad.

**The proposal here was to read symbols on the write path. It was rejected.** The processor would have needed PostgreSQL and object storage, with
a risk of blocking ingestion, and symbols arriving late would never have regrouped anything.

**What is built instead:** `GET /rum/crashes` reads up to 500 distinct stacks, symbolicates them and groups as it reads:

```
key = type + first function symbols resolved     when symbols resolved a frame
    | error group + the device's name for a frame  otherwise
```

- An image with symbols uploaded is the application's own, so `abort` in `libc.so` does not outrank `Game::Load`.
- Without symbols a device-named frame only parts a group and never joins two, or every abort would become one crash.
- Symbols arriving late regroup old crashes, which a write-path design could not do.
- **The cost:** the inbox stays coarse, and one inbox group can show as several crashes on this tab. An R8-renamed type also has one inbox group
  per release. The stack in the inbox's detail view is symbolicated for native frames, whose symbols need no version.
- Past 2,000 sessions per stack the session count reads low: sessions come back as ids, because a session can crash on two stacks.

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
- Each crash names what to upload (`missing_symbols`). A frame the device named and a `.dylib` are the system's and are left out. iOS frameworks look just like the application
  in a MetricKit report, so once symbols resolved anything the other images are taken to be the system's. An application's second framework with no symbols then goes unlisted, though
  its frames still read as addresses. With nothing resolved, all of them are listed.

## 7. Order of work

1. ~~Payload, sanitize, the new table and the API fields (§1, §5).~~ Done.
2. ~~Capture on Kotlin, Dart and Swift, with file delivery (§2).~~ Done.
3. ~~R8 and native symbols, with the CLI (§3).~~ Done.
4. ~~Grouping (§4).~~ Done at read time.
5. ~~NDK crashes and ANR on Android.~~ Done. Flutter symbols are not.

## 8. Open questions

1. **Is breadcrumb data in scope?** The screens and events before a crash already exist on the session timeline for 7
   days. Proposed: no separate breadcrumb trail in v1, link to the session.
2. **Is `out_of_memory` honest on iOS?** MetricKit reports exits by reason in `MXAppExitMetric`, as counts and not as
   per-session events. Proposed: Android only until proven otherwise.
3. **Are crash sessions enough for 30 days?** Group detail reads raw spans for 7 days, while `apm_error_groups` outlives
   them. It needs checking against a real upload whether a crash older than a week still opens with a useful stack.
