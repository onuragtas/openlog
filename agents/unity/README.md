# openlog Unity SDK

Apache-2.0 · package `io.github.onuragtas.openlog` · iOS and Android · no dependencies

Crash reporting, product analytics and error monitoring for Unity games: native crashes, ANRs, C# exceptions, scenes, custom
events, DAU, retention and the crash-free rate of each version.

It implements [the mobile protocol](../../docs/contracts/mobile-agent.md), and the Swift, Kotlin and Dart SDKs are other clients
of the same one. Tested on Unity 6 with IL2CPP and high managed stripping. It uses no reflection, so it needs no `link.xml`.

## Quick start

Add the package in **Window → Package Manager → + → Install package from git URL**:

```
https://github.com/onuragtas/openlog.git?path=agents/unity#v0.1.140
```

The part after `#` pins a release, so a game's build doesn't change by itself. Without it you get whatever is on `master`. The package is in releases from `v0.1.139` on.

Create a mobile key under **Settings → Browser keys**, with your package name and bundle identifier on its allowlist. Then
start the SDK when the game starts:

```csharp
using OpenLogUnity;
using UnityEngine;

public static class Telemetry
{
    [RuntimeInitializeOnLoadMethod(RuntimeInitializeLoadType.BeforeSceneLoad)]
    private static void Start()
    {
        OpenLog.Init(new OpenLogOptions
        {
            Key = "olb_…",
            Endpoint = "https://apm.resoft.org",
            CaptureCrashes = true,
        });
    }
}
```

If you run openlog yourself, use your own server as the endpoint. That is the whole setup: each scene loaded is reported as a
screen, exceptions are reported as errors, and the version and application id come from your player settings.

Nothing is sent from the editor, so working on the game is not counted as players. Set `EnableInEditor` to try it there.

## Record what matters

```csharp
OpenLog.RecordEvent("level_completed", new Dictionary<string, object> { ["level"] = 12, ["stars"] = 3 });
OpenLog.RecordTiming("level_loaded", 840);
OpenLog.Identify("acct_8f3a2b");   // your own opaque id, never an e-mail address or a name; "" clears it

try { Save(); }
catch (Exception e) { OpenLog.RecordError(e); }
```

Use a small, fixed set of event names, and put what varies into parameters. Results are in the console, under **Browser**, in the
**Analytics** and **Crashes** tabs of your game. See [Product analytics](../../docs/guide/product-analytics.md).

## Options

| | Default | |
|---|---|---|
| `Key`, `Endpoint` | none | Required. An `olk_…` license key is refused: it is a secret, and this ships in the game |
| `CaptureCrashes` | `false` | Report native crashes, ANRs and out-of-memory kills on the next launch |
| `CaptureExceptions` | `true` | Report the exceptions Unity logs, as errors |
| `TrackScenes` | `true` | Report a screen for each scene loaded as a single scene |
| `AnalyticsCollection` | `true` | Recognise a player across launches, for DAU, MAU and retention. This stores a persistent id, which may need consent |
| `SampleRate` | `1` | Used until `GET /v1/rum/config` answers with the operator's value, which wins |
| `FlushIntervalMs` | `5000` | How often a non-empty queue is sent |
| `EnableInEditor` | `false` | Send from the editor too |

## Consent

With `AnalyticsCollection` on, the SDK keeps a persistent id on the device, and whether that needs your players' consent is your
question. If it does, start with it off and turn it on from your consent dialog:

```csharp
OpenLog.Init(new OpenLogOptions { Key = "olb_…", Endpoint = "https://apm.resoft.org", AnalyticsCollection = false });

OpenLog.SetAnalyticsCollection(true);
```

Turning it off deletes the id from the device and is remembered across launches. It does not delete what was already sent:
[see how to do that](../../docs/guide/product-analytics.md).

## Crashes

| | Android | iOS |
|---|---|---|
| Native crash | ✅ From Android 11, with a stack from Android 12 | ✅ From iOS 14, through MetricKit, with a stack |
| ANR | ✅ From Android 11, with the thread dump | No |
| Out of memory | ✅ From Android 11, in the foreground | No |
| C# exception | Reported as an error | Reported as an error |

**A C# exception does not count as a crash.** Unity keeps the game running after one, so counting it would make the crash-free rate
say nothing. When an error really does end the game for the player, say so:

```csharp
catch (SaveCorruptedException e) { OpenLog.RecordFatal(e); }
```

**The same exception thrown every frame is sent once every 10 seconds,** so one line in an `Update` cannot use up your rate limit
or your players' data.

**Reports arrive on the next launch,** because the crashing process does nothing: the operating system records the crash and the
SDK reads it later. So no signal handler is installed, and Unity's or another SDK's keeps working. MetricKit decides when it delivers,
and that can be later than the next launch on older iOS versions.

**To read native stacks, upload your symbols** as part of the release build: see [Read your stacks](../../docs/guide/crash-reporting.md#read-your-stacks).

**How far this has been tested.** On an Android 16 emulator with IL2CPP, a real native crash, an abort and an ANR were reported end to end, and on a real
iPhone with iOS 26, so were a real native crash and an abort, through MetricKit. Native stacks were read back with their function names after uploading `symbols.zip` on
Android and the dSYMs on iOS. On that iPhone the reports arrived on the next launch or two, but Apple promises no timing. Out-of-memory kills use the same path as the ANR, but were
not triggered, and neither was a report delivered from the phone to a server over the network: it was read from the device and sent on by hand.

## Not covered

WebGL, consoles and standalone builds: the package compiles there, but crash capture does nothing, and they are not tested.

## Development

The core has nothing of Unity in it, and its tests run with no editor licence:

```sh
cd 'agents/unity/Tests~/dotnet' && dotnet test
```

They also run in the Unity Test Runner, once the package is listed under `testables` in a project's `manifest.json`. What talks to
the operating system (`Runtime/NativeCrashes.cs`, `Plugins/iOS`) runs only on a device or an emulator.
