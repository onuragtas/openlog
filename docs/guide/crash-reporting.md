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
| Android | Uncaught JVM exceptions. With `androidContext`, NDK crashes, ANRs and foreground low-memory kills on Android 11 and later, with a native stack from Android 12 | Below Android 11, only JVM exceptions |
| Unity | Native crashes, ANRs and out-of-memory kills on Android 11 and later, native crashes on iOS 14 and later. See [the Unity guide](/integrations/unity) | C# exceptions are errors and not crashes |
| Flutter | Uncaught Dart errors, once you wire them as below | A crash of the Flutter engine or a plugin, unless the host app also runs the iOS or Android SDK |

Stacks of a release build arrive obfuscated or as addresses. [Upload your symbols](#read-your-stacks) to read them.

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
        androidContext = context, // NDK crashes, ANRs and low-memory kills
    )
)
```

Without `crashDirectory`, nothing is captured. Without `androidContext`, only JVM exceptions are. Android records the other kinds
itself, so they are reported on the next launch. Start the SDK early, in `Application.onCreate`, so that a crash at start-up
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

## Read your stacks

A release build reports `a.b.c` on Android and an image with an offset for native code. `openlog-symbols` uploads what turns these
back into your function names, files and lines. Add it as the last step of your release build, because symbols belong to one exact
build and can't be recreated later.

**You need an API key with the admin role.** Create it under **Settings → API keys** and put it in your CI's secrets: it is shown
once. This is not the `olb_…` key from your app, which is public and can only send data.

```sh
# Needs Go. Build it once, and keep the binary with your CI tools.
git clone --depth 1 https://github.com/onuragtas/openlog && (cd openlog && go build -o ../openlog-symbols ./cmd/openlog-symbols)

export OPENLOG_API_KEY=ola_…

# iOS: the dSYMs of the build
./openlog-symbols upload -endpoint https://apm.resoft.org -app my-app build/MyApp.xcarchive/dSYMs

# Android and Unity: native libraries, or the folder Unity's symbols.zip extracts to
./openlog-symbols upload -endpoint https://apm.resoft.org -app my-app app/build/intermediates/merged_native_libs

# Android: the R8 mapping, which needs the same version the app reports
./openlog-symbols upload -endpoint https://apm.resoft.org -app my-app -version 4.2.1 app/build/outputs/mapping/release/mapping.txt
```

`-app` is the application name on your mobile key. Point the command at a whole folder: it finds the binaries, and when the same
library appears twice, stripped and with symbols, it uploads the better copy. It reduces each binary to a small table first, so a
75 MB `libil2cpp.so` uploads as about 3 MB. Add `-dry-run` to see what would be sent.

**In Unity,** set **Build Settings → Create symbols.zip** to *Debugging*, and extract the zip next to the APK before uploading.

**Symbols apply when a stack is read,** so uploading them later also fixes crashes that have already arrived. A native library is
matched by its build id and a mapping by its version, so uploading again for the same one replaces it. The **Crashes** tab
says which of your libraries still have no symbols.

**How crashes are grouped.** Once symbols have named a frame, crashes group on their type and that function, so a new build does not
reopen them. Until then they group on type and message, and different crashes with the same message, such as a plain `SIGSEGV`,
share a group. The error inbox always groups on type and message, because it can't read symbols as data arrives.

::: warning What is not covered
Flutter's `--split-debug-info` symbols are not supported, so a release build's Dart stack stays obfuscated. Swift function names are
shown in their mangled form. Symbol files are kept until you delete them with `DELETE /api/v1/symbols/{id}`.
:::

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
