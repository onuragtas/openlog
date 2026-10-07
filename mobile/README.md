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
| `lib/main.dart` | Placeholder screen. The sign-in flow is the next phase |

```
flutter pub get
dart run tool/gen_api.dart        # regenerate after a contract change
dart run tool/gen_api.dart --check
dart analyze --fatal-infos
flutter test
```

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

## Releases

The server cuts a release on every push to master. A store build cannot, so this
app gets its own version scheme and its own workflow rather than riding that one
([11-mobile-console.md](../docs/plan/11-mobile-console.md) §7). Not set up yet.
