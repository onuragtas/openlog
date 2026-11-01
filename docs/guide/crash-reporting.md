# Crash reporting

See what crashed your mobile application, on which version, and how many sessions it hit. It runs on the
[iOS](/integrations/ios), [Android](/integrations/android) and [Flutter](/integrations/flutter) SDKs.

Results appear per application under **Browser**: the **Crashes** tab lists what crashed, and the **Releases** tab shows the
crash-free rate of each version. A crash is also an ordinary error, so assigning and resolving it happens in the APM error
inbox.

Capture is **off by default**, because it installs a global handler in your process. A handler that was already there keeps
working: it is always called afterwards, so this can run next to another crash tool.

## What is covered

| Platform | Reported | Not reported yet |
|---|---|---|
| iOS, macOS | Uncaught Objective-C exceptions. Signals, Mach exceptions and Swift traps through MetricKit, on iOS 14 and macOS 12 or later | Running out of memory |
| Android | Uncaught JVM exceptions | **NDK crashes and ANRs** |
| Flutter | Uncaught Dart errors, once you wire them as below | A crash of the Flutter engine or a plugin, unless the host app also runs the iOS or Android SDK |

::: warning Stacks are not symbolicated yet
Release builds report their stacks as the device sent them: obfuscated names on Android, and addresses on iOS. Uploading
R8 mappings and dSYMs is not available yet. For the same reason, crashes are grouped by type and message only and not by
stack, so that a new build does not reopen every group. Different crashes with the same type and message, such as a plain
`SIGSEGV`, share one group.
:::

## Set it up

::: tip Which endpoint?
These examples use the hosted service, `https://apm.resoft.org`. If you run openlog yourself, use your own server instead.
:::

### iOS

```swift
let openlog = try OpenLog.start(Options(
    key: "olb_…",
    endpoint: "https://apm.resoft.org",
    appID: Bundle.main.bundleIdentifier ?? "",
    serviceVersion: Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "",
    captureCrashes: true
))
```

The system delivers MetricKit reports on a later launch, and the delay depends on the iOS version. The report names no
session, so the SDK attaches it to the session the last launch ended in when the build matches. Otherwise it is marked as
having an unknown session.

### Android

```kotlin
val openlog = OpenLog.start(
    Options(
        key = "olb_…",
        endpoint = "https://apm.resoft.org",
        appId = BuildConfig.APPLICATION_ID,
        serviceVersion = BuildConfig.VERSION_NAME,
        captureCrashes = true,
        crashDirectory = context.filesDir.path,
    )
)
```

Without `crashDirectory`, nothing is captured. Start the SDK early, in `Application.onCreate`, so that a crash at start-up
is seen too.

### Flutter

The SDK is pure Dart and cannot install Flutter's handlers itself, so connect them:

```dart
final openlog = Openlog.init(OpenlogOptions(
  key: 'olb_…',
  endpoint: 'https://apm.resoft.org',
  appId: 'com.example.shop',
  serviceVersion: '4.2.1',
  captureCrashes: true,
));

FlutterError.onError = (details) {
  FlutterError.presentError(details);
  openlog.recordFatal(details.exception, stack: details.stack);
};
PlatformDispatcher.instance.onError = (error, stack) {
  openlog.recordFatal(error, stack: stack);
  return true;
};
```

## How a crash reaches the server

1. The crashing process writes one file and does nothing else. It does not touch the network.
2. The next launch sends the file, and deletes it once the server has answered for good. If there is no connection or the
   server says to wait, the file stays for the launch after that.
3. The crash keeps its own time, session and version. A crash in version 1.0 reported after the update to 1.1 counts against
   1.0.

Limits:

- At most 10 reports wait on a device. In a crash loop, the first ones are kept.
- A report older than 7 days when it arrives is dropped, because it would land outside what is kept.
- A sampled-out session sends nothing, crash included. The crash-free rate stays correct, because sessions and crashes are
  sampled together.

## Crash-free rate

The **Releases** tab shows two numbers for each version:

| Column | Meaning |
|---|---|
| **Crash-free** | Share of sessions with no crash. **`—` when the version cannot report crashes**, because 100% would claim something nobody measured |
| **Error-free** | Share of sessions with no error at all, including those your code handled |

Session counts and rates reach back 30 days. They start from when you upgrade to this version of openlog.

## Alert on it

Crashes are spans, so the `oql` alert rule type can watch them with no new rule type:

```sql
SELECT count(*) FROM Span WHERE openlog.rum.crash.kind != ''
```

## Use the API

`GET /api/v1/rum/crashes?app=` lists crashes by group, and `GET /api/v1/rum/releases?app=` returns the rates. See
[the API reference](/contracts/api). The design is in [the contract](/contracts/mobile-crash).

If you send the data yourself rather than through an SDK, see [the mobile protocol](/contracts/mobile-agent).
