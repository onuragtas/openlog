# Contract: Real user monitoring (RUM, D-136)

Browser telemetry: Core Web Vitals, page load timing, SPA route changes, JavaScript errors and `fetch`/`XHR`
calls, sent by the openlog browser SDK (`agents/browser`, package `openlog-browser`).

This file is binding for the SDK, the ingest endpoint (`internal/ingest/rum.go`), the validation and query
layer (`internal/rum`), the ClickHouse schema (`schema/clickhouse/0094_rum.sql`), the browser key store
(`migrations/postgres/0093_browser_keys.sql`) and the API (`/api/v1/rum/*`, [api.md](api.md#real-user-monitoring)).

**RUM is not a parallel stack.** The SDK sends OTLP spans, so a page view, a vital, an error and a fetch call
are ordinary rows of `spans` and inherit the trace model, the error fingerprinting and the error inbox that
already exist. Three materialized views (§5) add what spans answer too slowly — the p75 of LCP per route over
30 days, the slowest pages, how many sessions there were. Nothing else about RUM is new machinery.

## 1. The SDK

```js
import { init } from 'openlog-browser';
init({ key: 'olb_…', endpoint: 'https://ingest.example.com:4318' });
```

No dependencies, ES modules, side-effect free, ~10 KB gzipped with a budget enforced by
`agents/browser/scripts/size.mjs`. `init()` never throws: invalid configuration is logged and the page runs
without monitoring. A second `init()` returns the first instance.

### 1.1 Sessions and sampling

A session is a **visit, not a person**: 32 random hex characters in `sessionStorage`, expiring after 30
minutes of inactivity and capped at 4 hours. It is never derived from anything about the visitor, is per tab,
and disappears with the tab — so it cannot recognise someone across visits, which is what makes it collectable
without a consent banner. `sessionStorage` being unavailable (private modes, sandboxed frames) degrades to an
in-memory id; it never throws.

Sampling is decided **once per session**, not per event. Half a session is not a cheaper session, it is an
unreadable one: a page view whose vitals were dropped tells you nothing. A sampled-out session sends nothing
at all.

The rate comes from the **browser key**, not the page: the SDK reads it from `GET /v1/rum/config` and the
ingest stamps the matching `sampling.ratio` on every span (§3.3), so counts extrapolate correctly and a page
cannot inflate its own traffic. An admin lowers the rate in the UI and pages pick it up without a deploy.

### 1.2 Delivery

Events are queued and flushed when the batch fills (32), on a timer (5 s), and whenever the page is going
away. The queue is capped at 256 events, dropping the oldest.

The unload flush uses `navigator.sendBeacon`, which is the only send a browser guarantees to finish after the
document goes away. A beacon can only carry a **CORS-simple** content type, so the body is posted as
`text/plain` with the key in the query string — `application/json` would make the beacon preflight, and a
preflight cannot complete during unload, so the last batch of every visit would be lost. `fetch` with
`keepalive` is the fallback. The ingest parses the body as OTLP/JSON either way; the content type is a
transport detail, never a trust signal.

The lifecycle hooks are `visibilitychange`→hidden and `pagehide`. `beforeunload` is deliberately not used: it
breaks the back/forward cache and fires unreliably on mobile.

### 1.3 SPA route changes

`history.pushState`/`replaceState` are patched (preserving and forwarding to the originals) and `popstate`
and `hashchange` are listened to. A route change **starts a new page view and a new trace**, and finalizes
the current vitals: the requests a route causes belong to that route, not to the document load from minutes
ago.

## 2. What is collected

### 2.1 Core Web Vitals

| Vital | Good ≤ | Poor > | Unit | Notes |
|---|---|---|---|---|
| `lcp` | 2500 | 4000 | ms | Largest Contentful Paint; the last (largest) candidate wins |
| `inp` | 200 | 500 | ms | Interaction to Next Paint |
| `cls` | 0.1 | 0.25 | — | Cumulative Layout Shift: the **worst burst**, not the sum |
| `fcp` | 1800 | 3000 | ms | First Contentful Paint |
| `ttfb` | 800 | 1800 | ms | Time to First Byte |

The thresholds are the published Core Web Vitals boundaries and are **constants, not settings**: the point of
the programme is that everyone measures against the same bar, and an installation that could move it would be
reporting a number meaning nothing to anyone else. The rating is always computed server-side (§3.3).

LCP, INP and CLS are only knowable when the page is hidden — a larger element, a later shift or a slower
interaction can always still happen — so they are held until then. CLS is reported **even at 0**: "this page
does not shift" is a real measurement, and omitting it would make a perfect page indistinguishable from an
unmeasured one.

**Known simplification.** INP is the worst interaction latency of the page, not the 98th-percentile-with-
outlier-correction the official definition uses above 50 interactions. On such pages openlog's INP is
pessimistic — never optimistic — and it is the same computation for every page, so routes remain comparable.
Replacing it is a contained change in `agents/browser/src/vitals.ts`.

### 2.2 Page load timing

From the Navigation Timing entry, in milliseconds, as `openlog.rum.timing.*`: `ttfb_ms`, `dns_ms`,
`connect_ms`, `tls_ms`, `response_ms`, `dom_interactive_ms`, `dom_content_loaded_ms`, `load_event_ms`. A phase
the browser skipped (a warm DNS cache, a plain-http origin, a back/forward-cache restore) is **left out**
rather than reported as 0, so an average of a phase averages the loads that performed it.

The page view span's duration is `loadEventEnd`, or how far the load got when the visitor left earlier.

### 2.3 Errors

`error` and `unhandledrejection` listeners (added, never assigned over an application's own `onerror`), plus
`console.error` when `captureConsoleErrors` is on — off by default, because it is noisy and often deliberate.
A rejected non-`Error` is still reported. When there is no `Error` object (a cross-origin script gives only
"Script error."), the file, line and column are synthesised into one frame.

Errors become **span events named `exception`** with `exception.type`/`message`/`stacktrace`, which is exactly
what the APM error fingerprint reads ([apm.md](apm.md) §3.1). Fingerprint v2 already strips content hashes
from bundle file names (`main.3f2a1b9c.js` → `main.js`), so a deploy does not reopen every browser error group.

### 2.4 Requests

Every `fetch` and `XMLHttpRequest` becomes a `client` span with `http.request.method`,
`http.response.status_code`, `server.address` and `url.full`. The URL keeps **path only** — query string and
fragment are dropped before sending, because that is where applications put tokens and personal data.

## 3. The browser key

**A browser key is public by construction.** It ships inside a web page; everyone who can open the page has
it. Every design decision below follows from accepting that rather than pretending otherwise.

It is a **separate credential type** from the ingest license key, in its own table, with its own prefix
(`olb_`) and its own endpoint. Reusing the license key would give a value printed in HTML the power to write
every signal and to drive agent fleet sync.

### 3.1 Presenting the key

`POST /v1/rum` takes the key from the `openlog-browser-key` header, or from the `k` query parameter. The query
parameter exists because `sendBeacon` cannot set headers (§1.2). Putting a credential in a URL is normally
wrong; here the value is public anyway, so the cost is that it appears in access logs, not that it leaks.

### 3.2 Origin allowlist

Each key lists the origins it may be used from: exact (`https://app.example.com`, port included when it is
not the scheme default) or a subdomain wildcard (`https://*.example.com`, covering any subdomain depth). The
syntax is the same as `OPENLOG_INGEST_CORS_ALLOWED_ORIGINS`. An empty list is **refused** — a key without an
allowlist accepts data from any page on the internet, and a blank field must not be the unsafe setting. `*`
is refused for the same reason.

`/v1/rum` answers its own CORS, using the key's allowlist rather than the server-wide CORS variable: a browser
key works without any operator CORS configuration, and is not silently widened by one either. The preflight is
answered permissively without resolving the key, because a preflight authorizes nothing — the POST that
follows does the real check, and a hard-failing preflight would show up in the console as an unexplained
network error instead of a readable 403.

### 3.3 What the ingest does to a payload

**Rewrite, do not inspect.** A payload is taken apart and rebuilt from the key's configuration plus an
allowlist of fields (`internal/rum.Sanitize`), so a field openlog learns to interpret later cannot already be
under an attacker's control:

| Rule | Why |
|---|---|
| The **resource is rebuilt**: `service.name` and `deployment.environment.name` come from the key; only `browser.*`, `user_agent.original`, `os.*` survive from the payload | A copied key cannot write under a backend service's name, and cannot inject `host.id` — which drives host↔service linkage and, in SaaS mode, the plan's host limit |
| A span without a known `openlog.rum.event` is **dropped** | The endpoint cannot be used as a general-purpose trace writer |
| A span without a well-formed `session.id` is **dropped** | An unattributable row is not worth storing |
| The **route is re-normalized** (§4) | A page cannot mint rollup keys |
| The **vital rating is recomputed**, unknown vitals and non-numeric values dropped | The rating decides the good/poor counters |
| `tracestate` is cleared and `sampling.ratio` set from the key | Otherwise a page could claim a weight of 10⁶ and multiply every count in the UI |
| **Span kind is assigned** — `internal`, or `client` for requests | A page view must never be an APM entry span, or a browser app would manufacture transactions and Apdex |
| Only `exception` events survive, at most 4 | Bounded storage |
| Every count and length is capped: 1000 spans/request, 64 attributes/span, 2 KiB values, 16 KiB stacks, 512 KiB body | A single request can never be expensive |

Dropped spans are reported back in the response (`rejected`, `message`) and counted in
`openlog_rum_rejected_events_total{reason}`.

### 3.4 Rate limiting

Each key carries `rate_limit_per_minute` (default 6000 events, i.e. roughly 750 page views a minute). It is a
token bucket **per ingest pod**, with one minute of burst: a cluster-wide counter would mean a PostgreSQL
round trip on a path that exists to absorb traffic from the open internet. The consequence is stated rather
than hidden — with *p* ingest pods the effective ceiling is *p* × the configured limit. Over the limit the
endpoint answers `429` with `Retry-After`.

### 3.5 Threat model

**Assume the key is known.** It is in the page source, in the browser devtools network tab, and in any CDN
cache of the bundle. Nothing below depends on it being secret.

What someone with a copied key **can** do:

- Send fabricated RUM data for **that one application**: fake page views, invented vitals, made-up errors.
  This pollutes the RUM screens and the error inbox of that application.
- Spend the key's rate limit, denying capacity to real visitors of that application until the bucket refills.
- Do the above from anywhere, if they send a matching `Origin` header — **the origin allowlist is not a
  defence against a program.** `Origin` cannot be forged by page JavaScript, so the allowlist genuinely stops
  a copied key working on another *website*; `curl` sends whatever it likes. Treat it as preventing casual
  reuse, not attack.

What they **cannot** do, by construction:

- Write logs, metrics, or arbitrary traces — the key authenticates one endpoint, and every span is validated
  as RUM (§3.3).
- Write under another service's name, or appear as a host, or consume host quota (§3.3).
- Inflate weighted counts through sampling claims (§3.3).
- Read anything at all. A browser key is write-only; it is not an API key and cannot query.
- Escape the organization. The tenant comes from the key, and the query layer binds every read to it.
- Survive revocation for more than `OPENLOG_AUTH_CACHE_TTL` (60 s by default).

**How an operator responds.** Revoke the key (Settings → Browser keys, or `DELETE /api/v1/browser-keys/{id}`)
and issue a new one in the page. Revocation is a soft delete: the value stays permanently unusable, which
matters because the plaintext is still cached in browsers that loaded the old page. Ingest pods stop accepting
it within `OPENLOG_AUTH_CACHE_TTL`, and pages already loaded keep trying until they are reloaded. For a
narrower response, tighten the origin list or lower the rate limit — both take effect on the next cache
refresh without touching the site. To stop all RUM immediately, `OPENLOG_RUM_ENABLED=false`.

**What is deliberately not claimed.** This design bounds damage; it does not prevent a determined attacker
from writing junk into one application's RUM data. That is inherent to accepting telemetry from a public
client, and any vendor claiming otherwise with a public key is describing the same origin check.

## 4. Route normalization

The cardinality of the rollups is the cardinality of the stored route, and its input is a URL chosen by a page
running a public key — so this is a bound, not a convenience:

1. The SDK may send a route it knows (`openlog.rum.route`); a framework router knows `/orders/:id` and no
   inspection recovers that from `/orders/8f3a`.
2. Whatever arrives is normalized anyway with the **same segment rules as an APM transaction name**
   ([apm.md](apm.md) §2.2) — UUIDs, ids, hex, tokens and e-mail addresses become placeholders, and more than 8
   segments collapse to `/…`. So a browser route and a backend transaction of the same URL look alike.
3. The result is truncated to 256 bytes.

The query string and fragment are dropped first, always.

## 5. Storage

No raw RUM table: the spans are the raw data (7-day trace retention). Three aggregating views on
`spans_local`, all keyed by tenant, mergeable in every column, and always re-aggregated with `GROUP BY`
through the Distributed table (the sharding argument of [apm.md](apm.md) §8 applies unchanged).

| Table | Key (ORDER BY) | Content | TTL |
|---|---|---|---|
| `rum_page_views_1m` | tenant, app, environment, minute, route, kind, device, browser | views, samples, duration sum/max/histogram, the eight Navigation Timing phase sums | 30 d |
| `rum_vitals_1m` | tenant, app, environment, minute, vital, route, device | count, samples, value sum/max/histogram, good / needs_improvement / poor | 30 d |
| `rum_sessions` | tenant, app, environment, session_id | first/last seen, page views, errors, entry and exit route, newest trace id, device, browser, OS | 30 d after last_seen |

TTL is a **fixed 30 days**. The three tables form their own storage class, `rum`, in
`internal/migrate.TTLTables`, and that is what makes the fixed retention true rather than aspirational:
per-tenant retention (D-081) widens the delete TTL of a whole class, so borrowing the `apm` class would have
let a tenant's setting push RUM to 400 days while this table still said 30. The `apm` class escapes that only
because its rollups carry no schema TTL of their own and follow `OPENLOG_APM_RETENTION_DAYS`, which is defined
over the eight `apm_*` tables of [apm.md](apm.md) §8 (`APMRetentionStatements` emits only `openlog.apm_*`
statements) — RUM tables are not among them.

Tiered-storage moves apply through the class as usual: `OPENLOG_STORAGE_COLD_AFTER_DAYS_RUM` (default 7).

`0094_rum.sql` also adds a bloom filter skip index on `spans_local` for `attributes['session.id']`, so the
session detail can read a session's own spans without scanning the tenant's range — the treatment
`k8s.pod.uid` got on logs in `0043_k8s_logs_indexes`.

### 5.1 Vital histogram buckets

The APM log-linear scheme (8 buckets per power of two, [apm.md](apm.md) §4.1) widened downwards to
`MinBucket = -160`, because CLS is a unitless score whose *good* values are around 0.05 while APM clamps at
-80 to ignore sub-microsecond durations. Clamping CLS at the APM floor would put almost every good
measurement in one bucket and make its p75 meaningless. Page load durations keep the APM bucket exactly — they
are milliseconds like any other span duration.

```
bucket(v) = clamp(ceil(8 · log2(v)), −160, 200)     v ≤ 0 → −160
```

Reference implementations: `rum.Bucket`, `rum.Hist.Quantile` (`internal/rum/hist.go`); the ClickHouse
expression is in `0094_rum.sql` and the two are checked against each other in `internal/rum/rum_test.go`.

`good`/`needs_improvement`/`poor` are counted at write time rather than derived from the histogram, because
the thresholds do not line up with bucket boundaries and the Core Web Vitals assessment is defined on the
exact share.

## 6. Correlation

**Browser → backend.** Every `fetch`/`XHR` span is a child of the page view span and carries W3C
`traceparent`, so the backend's server span continues the same trace. One trace runs from the click to the
database, and the trace view shows both halves.

Propagation defaults to **same-origin** and is opt-in per target beyond that: adding the header to a
cross-origin request turns it into a preflighted request (an extra round trip, a hard failure if that server
does not allow the header) and hands trace ids to whoever runs it.

**Backend → browser.** The page view span is the **root of the trace**, so a backend trace already names its
browser origin: `GET /api/v1/traces/{trace_id}` returns the browser spans alongside the server ones, and the
session id is an attribute on them. `rum_sessions` keeps the newest trace id per session, so a session opens
as a trace even after the page view spans have expired with the trace retention.

This is the trace model, not a second join: RUM did not add a correlation mechanism, it reused the one that
makes APM work.

## 7. API

Base `/api/v1/rum`, tenant-scoped like every telemetry read (viewer role, API keys allowed). Shapes:
[api.md](api.md#real-user-monitoring), [openapi.yaml](openapi.yaml) (tag `rum`).

| Endpoint | Content |
|---|---|
| `GET /rum/apps` | browser applications with views, sessions, errors, last seen |
| `GET /rum/overview?app=` | the five vitals, the page view series and totals |
| `GET /rum/pages?app=&sort=views\|slowest\|avg` | routes with views and p50/p75/p95 load time |
| `GET /rum/vitals?app=&route=` | the vital distribution, optionally for one route |
| `GET /rum/sessions?app=` | sessions, newest first |
| `GET /rum/sessions/{session_id}` | one session with its event timeline and trace links |

Key management is `/api/v1/browser-keys` ([api.md](api.md#browser-keys)).

**There is deliberately no RUM error endpoint.** A browser error is an error span of the browser app's
service, so it is already in the APM error inbox with the same fingerprint, grouping, assignment and
resolution state; the UI links to `GET /api/v1/apm/errors?service=<app>`. A parallel inbox would mean two
places to resolve the same error.

A consequence worth stating: a browser application **appears under APM as a service** (`apm_services` records
any span with a `service.name`), with errors but no transactions, throughput or Apdex — page views are
`internal` spans by design (§3.3). That is intended: full-stack means one service list and one error inbox,
not a separate browser product.

## 8. Not in this slice

Deliberately left for later, with the shape they would take:

- **Source maps are now built** ([api.md](api.md#source-maps)): a map is uploaded per bundle file name and
  applied at read time, so a browser stack reads in the developer's own files. The keying follows from §3.3
  — a RUM span carries no build identifier, but the content hash in `main.3f2a1b9c.js` names one build
  exactly, and the fingerprint strips it from the group key so groups stay stable across deploys.
- **Geography and network.** No IP-derived country/region and no `connection.effectiveType`. Country needs a
  GeoIP database and a privacy decision that deserves its own review.
- **User identity.** No `user.id` attribute: RUM sessions are deliberately anonymous (§1.1), and adding an
  identity field is a data protection question, not a schema one.
- **Resource timing for static assets.** Only `fetch`/`XHR` are captured, not every image and script; the
  volume is an order of magnitude larger and needs its own sampling.
- **Custom events and timings.** No `recordEvent`/`recordTiming` API yet.
- **Alerting on vitals.** ~~Not in this slice.~~ Done, and without a `rum` rule type: `RumPageView`,
  `RumVital` and `RumSession` are OQL event types ([oql.md](oql.md)), and the `oql` alert rule type compiles
  any OQL query — so `SELECT sum(value.sum) / sum(count) FROM RumVital WHERE vital = 'lcp'` is an alert
  condition. An event type OQL knows is alertable the moment it is added, which is why signals are wired into
  OQL rather than given a rule type each.
- **Session replay.** Out of scope by a wide margin.
