# openlog mobile SDK (Dart)

Screens, errors, custom events and identity from a mobile application, over OTLP/JSON. It implements
[mobile-agent.md](../../docs/contracts/mobile-agent.md) and nothing more — that document is the contract,
this is one client of it.

**Pure Dart, no dependencies.** It needs HTTP, timers and JSON, all of which are in the Dart SDK. Depending
on Flutter would keep it out of Dart servers and CLIs for no gain, and depending on `package:http` would put
a version constraint on the one library an application cannot choose to drop.

## Quick start

```dart
import 'package:openlog/openlog.dart';

final openlog = Openlog.init(OpenlogOptions(
  key: 'olb_…',                       // a mobile key, from Settings → Browser keys
  endpoint: 'https://ingest.example.com:4318',
  appId: 'com.example.shop',          // must be on the key's application allowlist
  serviceVersion: '4.2.1',
  osName: 'Android', osVersion: '15',
  deviceManufacturer: 'Samsung', deviceModel: 'SM-S911B',
));

openlog.recordScreen('/cart', isColdStart: true);
openlog.recordEvent('checkout_started', params: {'plan': 'pro'});
openlog.recordTiming('cart_priced', 42);
openlog.identify('acct_8f3a2b');      // '' on sign-out
```

## Using it from Flutter

The only thing Flutter changes is when to send. A mobile platform gives no warning before a process is
killed, so the batch most likely to be lost is the last one — flush when the app is backgrounded:

```dart
class _AppLifecycle extends WidgetsBindingObserver {
  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.paused) openlog.onAppBackgrounded();
  }
}

WidgetsBinding.instance.addObserver(_AppLifecycle());
```

For screens, call `recordScreen` from a `NavigatorObserver`, or wherever your router already knows the route
name. Send the route your navigator knows (`/orders/:id`) rather than the interpolated path: openlog
normalises whatever arrives, but only your router knows which segment was the id.

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
| `key`, `endpoint`, `appId` | — | Required. `appId` must be on the key's allowlist or every request is `403` |
| `serviceVersion` | `''` | The build. `service.name` comes from the key; this only labels a build within it, and is what release health groups by |
| `deviceModel`, `deviceManufacturer`, `osName`, `osVersion` | `''` | What the platform reports about itself |
| `sampleRate` | `1` | Used until `GET /v1/rum/config` answers with the operator's value, which wins |
| `maxBatchSize` | `32` | Spans buffered before an early send |
| `flushInterval` | `5s` | How often a non-empty queue is sent |
| `debug` | `false` | Log what the SDK does |

## Sampling and sessions

A session is a visit, not a person: 32 random characters, never derived from anything about the device or
the user, expiring after 30 minutes of inactivity and capped at 4 hours.

Sampling is decided **once per session**, not per event — half a session is not a cheaper session, it is an
unreadable one. A sampled-out session sends nothing at all. The weight of every stored span is applied
server-side from the key, so an application cannot inflate its own traffic by claiming a rate.

## What it does not do

**No crash reporting.** An unhandled native crash is not something the process lives to send, and capturing
it offline for delivery on the next launch needs its own design. Claiming it here would lose crashes
quietly. `recordError` reports what your code catches, which is a different and smaller promise.

**No symbolication.** Browser stacks are un-minified with uploaded source maps; dSYM and ProGuard mappings
have no equivalent yet.

**No attestation.** Play Integrity and App Attest are the upgrade path for the application allowlist, and
are deliberately not claimed.

## Development

```sh
make install     # dart pub get, inside a container (DART=local to use the host SDK)
make analyze     # typecheck and lint in one command, warnings fatal
make test
```
