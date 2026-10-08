# openlog mobile console

iOS and Android, one Flutter codebase: the on-call engineer's openlog in their
pocket. Design and phases: [docs/plan/11-mobile-console.md](../docs/plan/11-mobile-console.md).

Not to be confused with the **mobile SDKs** — `agents/swift`, `agents/kotlin`
and `agents/dart` — which send telemetry *from* someone's app *to* openlog and
implement [mobile-agent.md](../docs/contracts/mobile-agent.md). This reads an
openlog installation; it touches none of them.

## What is here

| Path | What |
|---|---|
| `lib/src/api/schema.g.dart` | **Generated.** Data classes from `docs/contracts/openapi.yaml` |
| `tool/gen_api.dart` | The generator, and `--check` for CI |
| `lib/src/api/client.dart` | One installation over HTTP: address handling, bearer token, org header, errors |
| `lib/src/session.dart` | Which installation, which person, which organization — the only mutable state above the widgets |
| `lib/src/storage/token_store.dart` | The device token in Keychain / EncryptedSharedPreferences |
| `lib/src/list_controller.dart` | What every list screen does the same way when things go wrong |
| `lib/src/alerts.dart` | What is firing, and taking one of them |
| `lib/src/services.dart`, `logs.dart`, `dashboards.dart` | The other three lists |
| `lib/src/ui/` | Server address, sign in and sign up, the alerts list, the account drawer |
| `lib/l10n/` | `app_en.arb`, `app_tr.arb`, and what gen-l10n makes of them |
| `tool/check_l10n.dart` | Fails when the two dictionaries disagree |

```
flutter pub get
dart run tool/gen_api.dart        # regenerate after a contract change
dart run tool/gen_api.dart --check
dart run tool/check_l10n.dart
dart analyze --fatal-infos
dart format --output=none --set-exit-if-changed lib test tool
flutter test
```

`dart analyze`, not `flutter analyze`: the latter crashes on some setups with
`analysis server exited with code 64`. CI runs `dart analyze`.

## The generated schema, and why the generator is ours

The web app gets its types from the same contract (`npm run gen:api` →
`web/src/api/schema.gen.ts`) and `tsc` breaks when the contract moves. A store
build cannot be fixed in step with a deploy, so the phone needs that safety net
more than the browser does, not less.

Off-the-shelf generators were tried first and do not fit:

- **swagger_parser 1.45.0** crashes on this spec —
  `type 'List<Object?>' is not a subtype of type 'Map<String, dynamic>' in type cast`,
  0 files generated. The spec is OpenAPI **3.1** and uses `type: [string, "null"]`
  in 108 places; the parser reads `type` as a string.
- **openapi-generator**'s Dart targets want a JVM in this job and treat 3.1 as
  experimental.

Both would also emit all 278 endpoints when the app parses about ten. And a
generator that quietly falls back to `dynamic` on the constructs this spec is
full of would leave the CI guard claiming a safety it does not provide, which is
worse than having no guard at all.

So `tool/gen_api.dart` handles exactly what the spec uses — `$ref`, `oneOf` with
a null branch, `type: [x, "null"]`, inline objects, enums, arrays, maps,
`Timestamp` — and **fails loudly** on anything else. If it stops on a construct,
teach it the construct. Do not widen a field to `dynamic` to get past it.

Two decisions inside it are worth knowing:

- **An unknown enum value becomes `unknown` instead of throwing.** The server
  can be newer than the app in the store, so a role or a session kind added
  there must leave the app readable rather than taking down a screen that would
  otherwise have worked.
- **A required field that is missing or of the wrong type throws `ApiShapeError`
  naming its path** (`DeviceSession.me.organizations[0].name`). "Unexpected
  null" three screens later is how a contract break turns into a bug report
  about something unrelated.

Add a type by putting it in `schemaTargets` (named schemas) or `responseTargets`
(bodies declared inline on a path), then regenerating. Only what the app parses
is generated, so the file stays the size of the app's needs.

## A dashboard is drawn by what its query answered

The contract offers eight visualizations and four result kinds, and on a phone
the kinds are the useful distinction: a `facets` result is a ranked list
whether the dashboard called it a pie or a bar, and at this width a ranked list
is both readable and precise where a five-slice pie is neither. So widgets
render by kind — a number, a sparkline, a ranked list — and `histogram` and any
kind this build does not know say "best read on the web" rather than leaving a
card that looks broken.

The sparkline is a `CustomPainter`. It draws one polyline with no axes, legend,
tooltip or interaction, and a charting package would be a dependency, a licence
and an upgrade treadmill for forty lines.

## Severity colours are not themeable

`critical`, `warning` and `info` are spelled out as constants rather than taken
from the theme's container slots. Taking `warning` from `tertiaryContainer`
against this app's blue seed produced a magenta chip, which reads as a second
kind of critical. Severity is semantic the way an error colour is: it has to
mean the same thing at a glance in light and dark, which a generated palette
does not promise.

## Turkish and English, without a bridge from the web

The plan ([11-mobile-console.md](../docs/plan/11-mobile-console.md) §4.2) called
for generating these dictionaries from the web app's `en.ts`/`tr.ts` so the two
could not drift. That is not what is here, and the reason is a measurement: the
web dictionaries are **4985 lines**, and this app shows about 56 strings.
Generating from them would carry thousands of keys no screen reads, to protect
against drift in a handful.

So the ARB files are the app's own, and where a string exists on both sides the
web's wording is used verbatim — "openlog'a giriş yap", "E-posta veya parola
hatalı." — so the two products speak the same Turkish. The risk that is real,
a key translated in one language and not the other, is caught directly:
`tool/check_l10n.dart` fails when the key sets differ, and when the same key
uses different placeholders in the two files (`{min}` against `{sayi}` throws
on the one screen that formats it). gen-l10n only warns and falls back to
English, which is a Turkish screen with English words on it that a user finds
before CI does.

Revisit this when the app grows into screens the web already has — alerts,
services, logs. The overlap will be larger there than it is for a sign-in form.

## Errors are the one shape the contract cannot promise

`client.dart` parses error bodies tolerantly, on purpose. A 502 can come from a
proxy as an HTML page and a captive portal can answer anything at all, so a
strict generated reader is the wrong tool there: `ApiException` carries
openlog's `code` and `message` when the body is an openlog error and a plain
description of the status when it is not. `ApiUnreachable` is kept separate
because what to tell the person differs — check the address, not your password.

## The address

`defaultBaseUrl` is `https://apm.resoft.org`, the hosted installation, and it is
a pre-filled box rather than a requirement. It is compiled in, so moving that
domain would strand installed copies; the app therefore assumes nothing else
about it — no certificate pinning, no code path of its own — and the field stays
editable. To the app, the hosted installation is a self-hosted one with the box
already filled in.

## What is not verified here

CI runs the tests on Linux. It does **not** build for iOS or Android, so the
native side of `flutter_secure_storage` — Keychain entitlements, the Android
`minSdk`, the CocoaPods deployment target — is not exercised there.
`flutter build apk --debug` was run once by hand and passed; iOS has not been
built at all.

If a build fails with `Invalid resource directory name` on something like
`mipmap-hdpi 2`, that is not this project: the repository lives in iCloud Drive,
which makes `<name> 2` copies of files and directories. They are not tracked by
git, so CI stays green while the local build breaks. Delete them and build
again.

## Releases

The server cuts a release on every push to master. A store build cannot, so this
app gets its own version scheme and its own workflow rather than riding that one
([11-mobile-console.md](../docs/plan/11-mobile-console.md) §7). Not set up yet.
