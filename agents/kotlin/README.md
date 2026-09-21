# openlog mobile SDK (Kotlin/JVM)

Screens, errors, custom events and identity from an Android or JVM application, over OTLP/JSON. It
implements [mobile-agent.md](../../docs/contracts/mobile-agent.md) and nothing more — that document is the
contract, this is one client of it, and the Dart and Swift clients implement the same one.

**Plain Kotlin/JVM, not an Android library.** It needs HTTP, timers and JSON, none of which are Android
APIs. An `com.android.library` project would put the Android SDK into CI to test code that does not use it,
and would keep this out of JVM services and desktop clients for no gain. Consume it from Android like any
other JVM dependency.

**No dependencies**, including for JSON: `HttpURLConnection` and a small writer in the package are enough.
This is the one library an application cannot choose to drop, so it must not impose a version of another on
it — not OkHttp, not kotlinx-serialization.

## Quick start

```kotlin
val openlog = OpenLog.start(
    Options(
        key = "olb_…",                    // a mobile key, from Settings → Browser keys
        endpoint = "https://ingest.example.com:4318",
        appId = BuildConfig.APPLICATION_ID,   // must be on the key's application allowlist
        serviceVersion = BuildConfig.VERSION_NAME,
        deviceManufacturer = Build.MANUFACTURER,
        deviceModel = Build.MODEL,
        osVersion = Build.VERSION.RELEASE,
    )
)

openlog.recordScreen("/cart", isColdStart = true)
openlog.recordEvent("checkout_started", mapOf("plan" to "pro"))
openlog.recordTiming("cart_priced", 42)
openlog.identify("acct_8f3a2b")           // "" on sign-out
```

`start` throws `ConfigException` rather than returning a silently useless SDK: options the server would
refuse are refused here, where a developer sees them.

## Flushing when the app goes away

The only platform-specific thing is *when* to send. Android gives no warning before a process is killed, so
the batch most likely to be lost is the last one:

```kotlin
class App : Application() {
    override fun onCreate() {
        super.onCreate()
        registerActivityLifecycleCallbacks(object : ActivityLifecycleCallbacks {
            override fun onActivityStopped(activity: Activity) = openlog.onAppBackgrounded()
            // the other callbacks can stay empty
        })
    }
}
```

With `androidx.lifecycle`, observing `ProcessLifecycleOwner` for `ON_STOP` does the same in one line and
fires once per app rather than once per activity.

For screens, call `recordScreen` where your navigation already knows the route name. Send the route your
navigator knows (`/orders/:id`) rather than the interpolated path: openlog normalises whatever arrives, but
only your navigator knows which segment was the id.

## The key is public — and that is fine

A mobile key ships inside the APK, so anyone who downloads the app has it. Nothing here depends on it being
secret: the key authorises one endpoint, every payload is rewritten server-side to what the key is allowed
to say, the rate limit is per key, and revoking it takes effect within a minute.

The application id is **self-declared**. It narrows casual reuse of a key lifted from one build; it is not a
second authentication factor, and this SDK will not pretend otherwise. The full threat model is
[rum.md §3.5](../../docs/contracts/rum.md).

## Options

| | Default | |
|---|---|---|
| `key`, `endpoint`, `appId` | — | Required. `appId` must be on the key's allowlist or every request is `403` |
| `serviceVersion` | `""` | The build. `service.name` comes from the key; this only labels a build within it, and is what release health groups by |
| `deviceModel`, `deviceManufacturer`, `osName`, `osVersion` | `""` / `"Android"` | What the platform reports about itself |
| `sampleRate` | `1.0` | Used until `GET /v1/rum/config` answers with the operator's value, which wins |
| `maxBatchSize` | `32` | Spans buffered before an early send |
| `flushIntervalMs` | `5000` | How often a non-empty queue is sent |
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
`recordError` reports what your code catches, which is a different and smaller promise.

**No symbolication.** Browser stacks are un-minified with uploaded source maps; ProGuard mappings have no
equivalent yet.

**No attestation.** Play Integrity is the upgrade path for the application allowlist, and is deliberately
not claimed.

## Development

```sh
make test     # ./gradlew test — the wrapper pins Gradle, so there is nothing to install first
make build
```

The library targets **Java 11 bytecode** on whatever JDK runs the build: that is what Android can load, and
a newer target would make the artifact unusable where it is aimed.
