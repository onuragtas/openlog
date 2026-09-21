# Contract: Mobile applications (v1)

Decisions: D-136 (real user monitoring). Server: `internal/rum`, `internal/ingest/rum.go`. Related:
[rum.md](rum.md) (the whole RUM design), [semantic-conventions.md](semantic-conventions.md) §10 (the
attribute registry this restates for mobile).

**This is the contract, not an SDK.** A mobile application talks to openlog over plain HTTP with OTLP/JSON,
and any SDK — the Flutter package, a native Android or iOS one, or an application that posts the JSON
itself — implements exactly what is below and gets nothing extra for it. Writing it down first is what keeps
three clients from speaking three dialects.

Everything in [rum.md](rum.md) applies unchanged: the payload is rewritten server-side to what the key is
allowed to say, and an attribute this document does not list is dropped rather than stored.

## 1. The key is a mobile key

A mobile application has no `Origin`, so the origin allowlist that bounds a browser key cannot bound it. It
needs a key of `kind: "mobile"`, scoped by an **application allowlist** — Android package names and iOS
bundle identifiers ([rum.md](rum.md) §3.6, at most 50 per key).

```
POST /v1/rum
openlog-browser-key: olb_…
openlog-app-id: com.example.shop
content-type: application/json
```

The key header may instead be the query parameter `k` (the value is public by construction; this exists for
transports that cannot set headers). `openlog-app-id` is **required for a mobile key**: a request that
declares no application never matches an allowlist, because a blank field must not be the permissive one.

**The application id is self-declared and openlog cannot verify it.** It narrows casual reuse of a key
lifted from one build; it is not a second authentication factor, and this document will not pretend
otherwise. What actually bounds the key is what bounds a copied browser key: one endpoint, every payload
rewritten server-side, a per-key rate limit, and revocation within the auth cache TTL.

Presenting a browser key here, or a mobile key without a matching app id, answers `403` with
`app_not_allowed`. An unknown or revoked key answers `401`.

## 2. Configuration

```
GET /v1/rum/config      → {"service_name", "environment", "sample_rate"}
```

Read it at start-up and use what it returns. `sample_rate` is the operator's, set on the key, so an
application's volume can be turned down without shipping a release. Until it answers — or if it fails —
report with the compiled-in default; never block start-up on it.

## 3. The payload

An OTLP/JSON `ExportTraceServiceRequest`: one `resourceSpans` entry, its `resource.attributes`, and
`scopeSpans[].spans[]`. `application/json` is the normal content type; `text/plain` is accepted for
transports that would otherwise preflight.

Three kinds of attribute, and the difference is the security model rather than naming:

- **Forced** — the server writes them from the key or the request, overwriting whatever was sent:
  `service.name`, `deployment.environment.name`, `openlog.entity.type`, `geo.country.iso_code`,
  `sampling.ratio`.
- **Assigned** — derived from what was sent, so the sender cannot choose its own buckets:
  `openlog.rum.route` (normalised), `openlog.rum.vital.rating`, `device.type`.
- **Accepted** — taken as sent, bounded: everything else below.

### 3.1 Every span

| Attribute | Required | Value |
|---|---|---|
| `openlog.rum.event` | yes | `page_view`, `vital`, `error`, `resource` or `custom`. A span without one is not RUM and is dropped |
| `session.id` | yes | 32 hex characters. A span without one is unattributable and is dropped |
| `openlog.rum.page_view.id` | no | 16 hex characters, tying one screen's spans together |
| `openlog.rum.route` | no | The screen. Send the route your navigator knows (`/orders/:id`); openlog normalises whatever arrives |
| `user.id` | no | Your own identifier for the person, set on sign-in ([rum.md](rum.md) §3.7). **Send an opaque, stable id — not an e-mail address or a name.** openlog stores it and never interprets it |

A **screen** is a `page_view` span: send one per screen the person opens, with
`openlog.rum.page_view.kind = "route_change"` for navigation within the app and `"load"` for a cold start.

### 3.2 What the application says about itself (resource attributes)

| Attribute | Value |
|---|---|
| `service.version` | The build. `service.name` comes from the key and this does not, deliberately: the name decides whose data this is, the version only labels a build within it |
| `device.model.identifier` | `iPhone15,2`, `SM-S911B` |
| `device.manufacturer` | `Apple`, `Samsung` |
| `os.name`, `os.version` | `iOS` / `Android`, and the version |

`device.id` is **not accepted**. A persistent device identifier is a privacy decision, not an oversight, and
it must not arrive simply by being sent. The browser-shaped attributes (`browser.*`, `user_agent.original`)
are meaningless here and are dropped.

## 4. Sessions and sampling

A session is a visit, not a person ([rum.md](rum.md) §1.1): 32 random hex characters, never derived from
anything about the user or the device. Start a new one after 30 minutes of inactivity, and cap it at 4
hours.

**Sample once per session, not per event.** Half a session is not a cheaper session, it is an unreadable
one. A sampled-out session sends nothing at all. Do not put a sampling ratio in the payload: the server
stamps the weight from the key, so weighted counts cannot be inflated by a client claiming one.

## 5. Limits

| | |
|---|---|
| Request body | 512 KiB (`413` above it) |
| Spans per request | 1000 |
| Attributes per span | 64 |
| Attribute value | 2048 bytes (`url.full` 1024, `user.id` 128, route 256) |
| Span name | 256 bytes |
| Error message / stack | 1024 / 16384 bytes |
| Custom event name / unit | 128 / 32 bytes |
| Custom parameters per event | 16, keys `[a-z0-9_.-]` up to 64 bytes, under `openlog.rum.custom.param.` |
| Rate | Per key, set by the operator (`429` with `Retry-After`) |

Over-long values are truncated, not rejected; too many attributes are dropped. Batch and send on a timer and
on the app going to the background — the last batch of a session is the one most likely to be lost.

## 6. Errors

`401` unknown or missing key · `403` `app_not_allowed` · `413` body too large · `429` rate limited, with
`Retry-After` · `503` authentication temporarily unavailable, retry later. Bodies are
`{"error": {"code", "message"}}`.

Retry `429` and `503` with backoff; never retry `401` or `403` — nothing about the next attempt will differ.

## 7. Not in this contract yet

- **Crash reporting.** An unhandled native crash is not an `error` span the process lives to send. Offline
  capture and next-launch delivery need their own design, and pretending otherwise would lose crashes.
- **Symbolication.** Browser stacks are un-minified with uploaded source maps ([rum.md](rum.md) §8); dSYM
  and ProGuard mapping have no equivalent yet.
- **Attestation.** Play Integrity and App Attest are the upgrade path for §1, deliberately not claimed here.
