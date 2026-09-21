# openlog mobile SDK (Swift)

Screens, errors, custom events and identity from an Apple-platform application, over OTLP/JSON. It
implements [mobile-agent.md](../../docs/contracts/mobile-agent.md) and nothing more — that document is the
contract, this is one client of it, and the Dart and Kotlin clients implement the same one.

**No dependencies.** It needs URLSession, timers and JSON, all of which are in Foundation. This is the one
library an application cannot choose to drop, so it must never impose a version of another on it.

## Quick start

```swift
import OpenLog

let openlog = try OpenLog.start(Options(
    key: "olb_…",                        // a mobile key, from Settings → Browser keys
    endpoint: "https://ingest.example.com:4318",
    appID: Bundle.main.bundleIdentifier ?? "",   // must be on the key's application allowlist
    serviceVersion: Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "",
    deviceModel: "iPhone15,2",
    osName: "iOS",
    osVersion: UIDevice.current.systemVersion
))

openlog.record(screen: "/cart", isColdStart: true)
openlog.record(event: "checkout_started", params: ["plan": "pro"])
openlog.record(timing: "cart_priced", milliseconds: 42)
openlog.identify("acct_8f3a2b")          // "" on sign-out
```

`start` throws rather than returning a silently useless SDK: options the server would refuse are refused
here, where a developer sees them.

## Flushing when the app goes away

The only platform-specific thing is *when* to send. iOS gives no warning before a process is killed, so the
batch most likely to be lost is the last one.

UIKit:

```swift
NotificationCenter.default.addObserver(
    forName: UIApplication.didEnterBackgroundNotification, object: nil, queue: nil
) { _ in openlog.onAppBackgrounded() }
```

SwiftUI:

```swift
@Environment(\.scenePhase) private var scenePhase
// …
.onChange(of: scenePhase) { phase in
    if phase == .background { openlog.onAppBackgrounded() }
}
```

For screens, call `record(screen:)` where your navigation already knows the route name. Send the route your
router knows (`/orders/:id`) rather than the interpolated path: openlog normalises whatever arrives, but
only your router knows which segment was the id.

## The key is public — and that is fine

A mobile key ships inside the application binary, so anyone who downloads the app has it. Nothing here
depends on it being secret: the key authorises one endpoint, every payload is rewritten server-side to what
the key is allowed to say, the rate limit is per key, and revoking it takes effect within a minute.

The application id is **self-declared**. It narrows casual reuse of a key lifted from one build; it is not a
second authentication factor, and this SDK will not pretend otherwise. The full threat model is
[rum.md §3.5](../../docs/contracts/rum.md).

## Options

| | Default | |
|---|---|---|
| `key`, `endpoint`, `appID` | — | Required. `appID` must be on the key's allowlist or every request is `403` |
| `serviceVersion` | `""` | The build. `service.name` comes from the key; this only labels a build within it, and is what release health groups by |
| `deviceModel`, `deviceManufacturer`, `osName`, `osVersion` | `""` / `"Apple"` | What the platform reports about itself |
| `sampleRate` | `1` | Used until `GET /v1/rum/config` answers with the operator's value, which wins |
| `maxBatchSize` | `32` | Spans buffered before an early send |
| `flushInterval` | `5` (seconds) | How often a non-empty queue is sent |
| `debug` | `false` | Log what the SDK does |

## Sampling and sessions

A session is a visit, not a person: 32 random characters, never derived from anything about the device or
the user, expiring after 30 minutes of inactivity and capped at 4 hours.

Sampling is decided **once per session**, not per event — half a session is not a cheaper session, it is an
unreadable one. A sampled-out session sends nothing at all. The weight of every stored span is applied
server-side from the key, so an application cannot inflate its own traffic by claiming a rate.

## What it does not do

**No crash reporting.** An unhandled crash is not something the process lives to send, and capturing it for
delivery on the next launch needs its own design. Claiming it here would lose crashes quietly.
`record(error:)` reports what your code catches, which is a different and smaller promise.

**No symbolication.** Browser stacks are un-minified with uploaded source maps; dSYM has no equivalent yet.

**No attestation.** App Attest is the upgrade path for the application allowlist, and is deliberately not
claimed.

## Development

```sh
make build
make test     # runs on macOS; the library also targets iOS, tvOS and watchOS
```
