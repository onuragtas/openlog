# openlog browser SDK

Apache-2.0 · package `openlog-browser` · no dependencies · ~10 KB gzipped

Real user monitoring for web applications: Core Web Vitals, page load timing, SPA route changes, uncaught
JavaScript errors and `fetch`/`XHR` tracing that continues into your backend traces.

Contract: [docs/contracts/rum.md](../../docs/contracts/rum.md).

## Quick start

```sh
npm install openlog-browser
```

```js
import { init } from 'openlog-browser';

init({
  key: 'olb_…',                                   // browser key, from Add data → Browser
  endpoint: 'https://ingest.example.com:4318',
});
```

That is the whole setup. From the first page load it reports page views with Navigation Timing, LCP, INP,
CLS, FCP and TTFB, uncaught errors and unhandled rejections, and every `fetch`/`XHR` call — with a W3C
`traceparent` header on same-origin requests, so a slow page opens as the backend trace that made it slow.

## The key is public — and that is fine

The `key` ships inside your page. Anyone who can open the page can read it. That is not a leak, it is how
browser monitoring works, and openlog is built around it:

- a browser key authenticates **one endpoint** (`POST /v1/rum`) and nothing else — it cannot write logs,
  metrics or arbitrary traces, and it is not an ingest license key;
- it only works from the **origins you list** on the key;
- every payload is **rewritten server-side** to the application the key belongs to, so a copied key cannot
  put data on another service's page, become a host, or inflate your traffic counts;
- it has its own **rate limit**, and revoking it takes effect within a minute.

Never put an `olk_…` ingest license key here. The SDK refuses it, because that one *is* a secret.

The full threat model — what someone who copies your key can and cannot do, and how to respond — is in
[rum.md §3.5](../../docs/contracts/rum.md).

## Custom events and timings

Everything above is automatic. When you want to record something only your application knows:

```js
const openlog = init({ key: 'olb_…', endpoint: 'https://ingest.example.com:4318' });

openlog.recordEvent('checkout_started', { plan: 'pro', step: 2 });
openlog.recordTiming('cart_priced', 42, { currency: 'try' });
```

Parameters are stored under `openlog.rum.custom.param.<key>`, so they can never collide with a field openlog
defines. At most 16 per event; keys are lower-case `[a-z0-9_.-]`, values are stored as text. Anything else is
dropped by the SDK because the server drops it too.

They are stored as spans and have no rollup of their own — what your application counts is not something the
server can pre-aggregate without knowing what it means — so you read them with OQL:

```sql
SELECT count(*) FROM Span WHERE openlog.rum.custom.name = 'checkout_started' FACET openlog.rum.route
```

The API takes a name and, for a timing, a number. It deliberately does not take arbitrary attributes: the
server keeps an allowlist of what a browser key may write ([rum.md §3.3](../../docs/contracts/rum.md)), so
anything else would be accepted here and dropped there.

## Options

| Option | Default | What it does |
|---|---|---|
| `key` | — | Browser key (`olb_…`). Required. |
| `endpoint` | — | OTLP/HTTP base URL of openlog ingest. Required. |
| `serviceName`, `environment` | from the key | Informational; the server uses the key's values. |
| `sampleRate` | `1` | Share of **sessions** kept. The key's server-side value wins once it is fetched. |
| `propagateTraceHeaders` | `'same-origin'` | Where `traceparent` is added. See below. |
| `maxBatchSize` | `32` | Events buffered before an early flush. |
| `flushIntervalMs` | `5000` | How often a non-empty queue is flushed. |
| `captureVitals` | `true` | LCP, INP, CLS, FCP, TTFB. |
| `capturePageViews` | `true` | Document loads and SPA route changes. |
| `captureErrors` | `true` | `error` and `unhandledrejection`. |
| `captureConsoleErrors` | `false` | Also report `console.error(...)`. Off: it is noisy and often deliberate. |
| `captureRequests` | `true` | `fetch` and `XMLHttpRequest` spans. |
| `debug` | `false` | Log what the SDK does. |

### Tracing your own API on another origin

`'same-origin'` is the default because adding a header to a cross-origin request makes the browser
**preflight** it (an extra round trip, and a hard failure if that server does not allow the header) and sends
your trace ids to whoever runs it. To trace your own API on another host, list it:

```js
init({
  key: 'olb_…',
  endpoint: 'https://ingest.example.com:4318',
  propagateTraceHeaders: ['https://api.example.com', /^https:\/\/.*\.internal\.example\.com/],
});
```

That backend must allow the `traceparent` request header in its CORS configuration.

## Sampling

Sampling is decided **once per session**, not per event: half a session is not a cheaper session, it is an
unreadable one — a page view whose vitals were dropped tells you nothing. A sampled-out session sends
nothing at all. openlog scales the stored counts by the key's sample rate, so the numbers in the UI are
estimates of real traffic, not of what was kept.

Change the rate on the key (Settings → Browser keys) and pages pick it up without a redeploy.

## What it collects

Page views, web vitals, errors and requests — with the URL **path only**: the query string and fragment are
dropped before anything is sent, because that is where applications put tokens and personal data. The
session id is random, per tab, and expires; it is never derived from anything about the visitor, so it
cannot identify anyone across visits.

Details and the full attribute list: [rum.md §2](../../docs/contracts/rum.md),
[semantic-conventions.md §10](../../docs/contracts/semantic-conventions.md).

## API

```js
const rum = init({ … });
rum.recordError(err);   // report an error you caught yourself
rum.flush();            // send what is buffered now
rum.sessionId();        // current session id ("" when not sampled)
rum.shutdown();         // remove every listener and patch, and flush
```

`init()` never throws: a configuration mistake is logged and the page runs without monitoring. Calling it
twice returns the first instance.

## Browser support

Any browser with `PerformanceObserver` (all current versions). Vitals the browser does not implement are
simply not reported — INP needs Chromium 96+, and the rest degrade the same way. The SDK does nothing at all
during server-side rendering.

## Development

```sh
make install     # or NODE=local npm ci
make typecheck
make test        # node:test suites against a minimal DOM double
make lint
make build
make size        # gzipped size against the budget in scripts/size.mjs
```
