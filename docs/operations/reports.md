# Scheduled report chart images (`openlog-renderer`)

Scheduled dashboard reports (api.md "Scheduled reports", D-087) are e-mailed with HTML tables. With the optional
`openlog-renderer` service each widget is shown as a PNG image instead (D-097). The renderer is a separate image
(`ghcr.io/onuragtas/openlog-renderer:<version>`, Alpine + Chromium, ≈ 780 MB unpacked) so the main image stays small.
Configuration: [config.md "Report chart images"](../contracts/config.md#report-chart-images-openlog-renderer-api-allinone-d-097).

## How a report gets its images

```
api leader (report job)                      openlog-renderer                          any api pod
  │ due report: run widget queries (tables)
  │ sign render token (5 min, report+period)
  ├── POST /v1/render ─────────────────────▶ │ Bearer OPENLOG_RENDERER_TOKEN
  │   {path:/print/dashboard, token}         │ start Chromium (fresh profile)
  │                                          ├── GET <UI origin>/print/dashboard#token=… ─▶ web UI
  │                                          ├── GET /api/v1/render/dashboard (Bearer olrt_…) ─▶ verify token,
  │                                          ├── GET …/widgets/{id}/result ───────────────────▶ run stored query
  │                                          │ wait for data-render-state=ready
  │                                          │ capture each [data-render-id] as PNG, kill Chromium
  │ ◀──────────────── {images, errors} ───────┤
  │ embed PNGs (cid:), tables for the rest
  └── SMTP
```

- The job queries the widgets itself first (tables, plain text part), then asks the renderer; the browser queries them
  again through the render endpoints. The period is fixed, so both show the same data.
- Any failure (renderer down, timeout, `429`, invalid response) → the e-mail is sent with tables only; per widget, a
  failed query or an image over the caps (1 MiB each, 10 MiB per e-mail, 2000 px high) keeps its table.
- Logs: `dashboard report images` (count, dropped, bytes, duration) or `dashboard report images unavailable, sending
  tables` on the api leader; `rendered` / `render failed` on the renderer (path only — the token is never logged).

## Security model

**Render API (api → renderer).** Internal only: `POST /v1/render` on `OPENLOG_RENDERER_ADDR` (8090), authenticated
with the shared secret `OPENLOG_RENDERER_TOKEN` (constant-time comparison), optional TLS and mTLS
(`OPENLOG_RENDERER_TLS_*`). Never expose it through an ingress, gateway or published port: the Helm chart creates a
ClusterIP Service whose NetworkPolicy admits only api pods; the Compose profile publishes no port. The request can only
name a print route of the web app (`/print/<segment>`; no scheme, host, query or dot segments) and a token-shaped
string; the origin is fixed by the renderer's own `OPENLOG_RENDERER_UI_ORIGIN`, so a caller cannot make the browser
open arbitrary URLs. Request bodies are ≤ 8 KiB, unknown fields are refused.

**Render tokens (browser → api).** The print view authenticates with a render token, not a session, API key or share
link:

- `olrt_` + claims + HMAC-SHA256. The key is derived from `OPENLOG_KEY_HASH_SECRET` (or `OPENLOG_SECRETS_KEY`), which
  the renderer never receives, so a compromised renderer cannot mint tokens; it can only replay the tokens it was given
  until they expire.
- Lifetime ≤ 10 minutes (5 minutes issued). Bound to one organization and its tenant, one dashboard, one scheduled report
  that must still exist and be enabled, and the report period. Queries are the dashboard's stored queries with the
  report's variables, the organization's tenant scope and query limits; the request carries no OQL, variables or time
  range.
- Accepted only by `GET /api/v1/render/dashboard` and `…/widgets/{id}/result`; every other endpoint refuses it.
  Responses are redacted like share links (no query text, organization/user identifiers or execution statistics),
  `no-store`, restrictive CSP.
- The token travels in the URL fragment (`#token=`), which is not sent to servers or written to access logs; the page
  removes it from the address bar and sends it only in the `Authorization` header. `/print/*` pages are served with
  `Referrer-Policy: no-referrer` and `X-Robots-Tag: noindex`.
- It is not a share link: it is never stored, listed, shown to users or usable outside the render endpoints, and it
  works even when share links are disabled for the organization (the report owner already chose the recipients).

**Browser isolation.**

- One Chromium process per render with a fresh temporary profile (deleted afterwards), killed at
  `OPENLOG_RENDERER_RENDER_TIMEOUT`; at most `OPENLOG_RENDERER_MAX_CONCURRENCY` at a time, further requests wait
  `OPENLOG_RENDERER_QUEUE_TIMEOUT` and then get `429`. V8 heap capped at 512 MiB per renderer process, one renderer
  process per browser, extensions, sync, background networking, component updates, crash reporting and pings disabled.
- Network: DNS inside Chromium resolves only the UI origin's host (`--host-resolver-rules`), no proxy, and every
  HTTP(S) request of the page is intercepted (DevTools Fetch domain) and failed unless it targets the UI origin exactly.
  Deployment layer: Helm NetworkPolicy egress to api pods on 8080 and DNS only; Compose attaches the renderer to an
  `internal: true` network shared only with `openlog` (no internet, no Kafka/ClickHouse/PostgreSQL). Keep an equivalent
  restriction in your own deployments — WebSockets and IP-literal connections are not covered by the Fetch interception.
- Sandbox: Chromium's sandbox is on by default (`OPENLOG_RENDERER_CHROMIUM_NO_SANDBOX=false`). It needs unprivileged user
  namespaces, which Docker's and Kubernetes' default seccomp profiles (`RuntimeDefault`) refuse, so the Compose profile and
  the Helm chart set `true` and lock the container down instead: non-root (UID 10001 / chart `podSecurityContext`),
  read-only root filesystem with size-limited `/tmp` and memory-backed `/dev/shm`, all capabilities dropped,
  `no-new-privileges`, no service account token, memory and PID limits. Only the openlog UI origin is ever loaded, so
  the renderer never processes third-party content. To keep the sandbox, run the container with a seccomp profile that
  allows user namespaces (for example Chromium's `chrome.json` profile) and set the variable to `false`.
- The renderer holds no database credentials, license or API keys: only `OPENLOG_RENDERER_TOKEN` and optional TLS files.

## Deploy

**Compose.** In `.env`: `OPENLOG_RENDERER_URL=http://openlog-renderer:8090`, `OPENLOG_RENDERER_TOKEN=$(openssl rand -hex 32)`
and `OPENLOG_KEY_HASH_SECRET` (or `OPENLOG_SECRETS_KEY`). Then
`docker compose --profile renderer up -d --build openlog-renderer` and recreate `openlog` (`docker compose up -d openlog`).

**Helm.** `renderer.enabled=true`, `renderer.token` (or `renderer.existingSecret`), and `auth.keyHashSecret` or
`alert.secretsKey`. The chart sets `OPENLOG_RENDERER_URL/_TOKEN/_TIMEOUT` on api pods and creates the renderer
Deployment, ClusterIP Service and NetworkPolicy (needs a CNI that enforces NetworkPolicies; DNS egress assumes CoreDNS
pods labelled `k8s-app: kube-dns`, add `renderer.networkPolicy.extraEgress` otherwise). mTLS: `renderer.tls.secretName`
(Secret with `tls.crt`, `tls.key`, `ca.crt`, mounted into renderer and api pods; the certificate must cover the renderer
Service name and allow client authentication). Resources: requests 250m/512Mi, limit 1.5Gi for two concurrent renders;
raise the memory limit with `maxConcurrency`.

**Sizing.** Reports are sent by one api leader, one report after another, so one renderer replica with concurrency 2
serves most installations.

## Troubleshooting

| Symptom (api leader log) | Cause |
|---|---|
| `renderer: HTTP 401` | `OPENLOG_RENDERER_TOKEN` differs between api and renderer |
| `renderer: HTTP 429` | all render slots busy longer than the queue timeout |
| `renderer: HTTP 502: the page could not be rendered` | renderer log `render failed`: UI origin unreachable, page reported an error (e.g. render token refused: `OPENLOG_KEY_HASH_SECRET` differs between api pods, or clocks are minutes apart), Chromium crashed (memory limit, `/dev/shm`) |
| `renderer: HTTP 502: the render timed out` | slow widget queries; raise `OPENLOG_RENDERER_RENDER_TIMEOUT` (and `OPENLOG_RENDERER_TIMEOUT` above it) |
| images `dropped` > 0 | captures over the size caps; the widgets are sent as tables |
| renderer does not start: `No usable sandbox` | the sandbox is on but the seccomp profile refuses user namespaces; see "Sandbox" above |
