# openlog web UI

React 19 + TypeScript SPA for openlog (stack decided in D-017). Built with Vite, embedded into the Go
binaries (`openlog-api`, `openlog-allinone`) via `go:embed` and served at `/`.

## Quick start

```sh
cd web
npm ci
npm run dev:mock     # http://localhost:5173 — MSW mocks; sign in as admin@openlog.local / openlog-dev-password
npm run dev          # proxies /api to http://localhost:8080 (a running openlog-api, OPENLOG_AUTH_MODE=postgres)
```

The UI signs in with email and password (`POST /api/v1/auth/login`). The session is an HttpOnly cookie the
browser sends by itself; `src/api/auth.ts` keeps only the CSRF token (in memory, re-read from
`GET /api/v1/auth/me` after a reload) and the selected organization (`localStorage["openlog.orgId"]`, sent as
`X-Openlog-Org-Id`). Against a local backend set `OPENLOG_COOKIE_SECURE=false` (plain HTTP). The mocks emulate
the session in `sessionStorage["openlog.mock.session"]`; in the "Staging" organization the mock user is a viewer.

Embedded build: `make web && make build`, then run `openlog-api` (or `openlog-allinone`) and open
`http://localhost:8080/`. Disable the UI with `OPENLOG_API_UI_ENABLED=false`. Without `make web`,
the binary serves the committed placeholder `dist/index.html`.

## Scripts

| Script | What |
|---|---|
| `dev` / `dev:mock` | Vite dev server (proxy to :8080 / MSW mocks) |
| `build` | `tsc -b` + `vite build` → `dist/` (consumed by `embed.go`) |
| `typecheck` | TypeScript, strict |
| `lint` | ESLint (typescript-eslint, react-hooks) |
| `test` | Vitest + Testing Library + MSW (jsdom) |
| `gen:api` | Regenerate `src/api/schema.gen.ts` from `docs/contracts/openapi.yaml` |
| `e2e` | Playwright smoke test against `dev:mock` (`npx playwright install chromium` once) |

## Layout

```
web/
  embed.go, embed_test.go   Go package github.com/onuragtas/openlog/web (SPA handler)
  dist/index.html           committed placeholder; replaced by `npm run build`
  e2e/                      Playwright tests
  public/                   static files (mockServiceWorker.js is stripped from builds)
  src/
    api/                    schema.gen.ts (generated), client.ts (openapi-fetch + auth/401),
                            queries.ts (TanStack Query options), types.ts
    components/             app components (TimeSeriesChart, TimeRangePicker, InventoryTable, …)
      ui/                   shadcn/ui-style primitives (hand-written; Radix + Tailwind)
      trace/Waterfall.tsx   DOM waterfall renderer (swappable)
    i18n/                   i18next setup, locales/en.ts (source of truth), locales/tr.ts
    lib/                    pure logic: time ranges, series alignment, inventory filters,
                            waterfall layout, formatting, theme
    mocks/                  MSW handlers + fixtures shaped like the real API
    routes/                 screens; router.tsx defines the (code-based) route tree
```

## Conventions

**API contract.** `docs/contracts/openapi.yaml` describes the handlers in `internal/api` (the handlers win
when they differ). After changing the API: update the spec, run `npm run gen:api`, fix type errors.
Never hand-edit `schema.gen.ts`.

**Data fetching.** Add a factory to `src/api/queries.ts` returning `queryOptions({ queryKey, queryFn })`;
call `api.GET(path, { params })` and pass the result through `unwrap()` (throws `ApiError`). Components use
`useQuery(fooQuery(...))`. Query keys include the *range spec* (`range` or `from`/`to` strings) and the
time window is resolved inside `queryFn`, so relative ranges refetch with a fresh "now". A 401 anywhere
clears the stored key and navigates to `/login`.

**URL state.** Filters and time range live in search params. The root route validates `range`/`from`/`to`
(`lib/time.ts`), each route adds its own `validateSearch`. Read with `getRouteApi("/app/…").useSearch()`,
write with `useNavigate({ from }).navigate({ search: (prev) => ({ ...prev, … }) })`.

**i18n.** No user-visible string literals in components. Add keys to `src/i18n/locales/en.ts` (grouped by
screen: `hosts.*`, `logs.*`, …), then the same keys to `tr.ts` — `tr` is typed against `en`, so a missing
key fails `typecheck`. Keys are typed in `t()`; for keys built from API values (categories, statuses) use
`translateOptional(key, fallback)`. Plurals use i18next suffixes (`_one`, `_other`). Turkish is chosen when
the browser prefers it; the choice is stored in `localStorage["openlog.lang"]`.

**Theme.** Colors are CSS variables in `src/index.css` (`.dark` overrides); use Tailwind tokens
(`bg-card`, `text-muted-foreground`, …), never raw colors. Charts read `--chart-axis`/`--chart-grid`.

**Charts.** `TimeSeriesChart` takes `ChartSeriesInput[]` (label + `[ms, value]` points); transforms live in
`lib/series.ts` and are unit-tested. uPlot draws time series; the APM service map uses React Flow
(`@xyflow/react`, MIT) with a dagre layout (`@dagrejs/dagre`, MIT) computed in `lib/apm-map.ts`; small APM
graphics (sparklines, latency histogram) are plain SVG/DOM in `components/apm/Charts.tsx`.

**APM.** `routes/apm.tsx` (services list, service page with tabs, service map), tab components in
`components/apm/`, query factories in `api/apm.ts`, pure helpers in `lib/apm.ts`, mocks in `mocks/apm.ts`.
Definitions: `docs/contracts/apm.md`.

**Integrations.** Metrics panels for services the infra agent discovers and monitors (nginx, Redis,
MySQL/MariaDB, PostgreSQL). See "Integrations" below.

**Accessibility.** Every input has a label, icon-only buttons have `aria-label`, toggles use `aria-pressed`
/ `aria-expanded`, interactive rows are reachable by keyboard (links/buttons inside rows).

## Integrations

Metrics panels for services that `openlog-infra-agent` discovers and monitors (nginx, Redis, MySQL/MariaDB,
PostgreSQL). Definitions: `docs/contracts/semantic-conventions.md` §6.

**Routes** (`routes/integrations.tsx`)

| Route | What |
|---|---|
| `/integrations` | Overview across hosts: status counts/filter (`?status=`), text search (`?q=`), cards on phones, table from 768px |
| `/hosts/$hostId/integrations/$discoveryId/$instance` | Panel of one instance: header, config help, recommended alerts, charts |
| `/hosts/$hostId?tab=services` | Service cards (`routes/host/services-tab.tsx`) link to the panel |

**Data flow.**

1. The overview reads `discovered_service` inventory items on all hosts (`inventorySearchQuery`);
   `summarizeIntegrations` keeps items with an `integration.id` and counts statuses. The panel reads the host's
   latest snapshot (`servicesQuery`) and finds the item by key `<rule_id>:<instance>` (`serviceKey`).
2. An instance is `{ hostId, discoveryId (= rule_id), instance }` (`InstanceRef`). Its metrics are selected with
   resource attribute filters: `instanceResourceFilter` returns
   `{ "openlog.discovery.id": …, "openlog.discovery.instance": … }`, which `metricQuery` sends as
   `resource.openlog.discovery.id=…&resource.openlog.discovery.instance=…` to `GET /hosts/{id}/metrics`.
3. `integrationOf` normalizes `integration.status` (`enabled`, `needs_configuration`, `error`, `not_available`).
   `needs_configuration`/`error` show the agent's `hint` (a config.yaml snippet); only `enabled` shows charts.
   A panel URL for a service missing from the latest snapshot still shows charts of earlier data.
4. Names: `instance` is the resolved executable (`/usr/bin/redis-check-rdb` on Debian) or a container id; the
   optional `command` (argv0 basename, `redis-server`) is shown as the primary name via `instanceLabel`, with
   the path as secondary text. Older agents omit `command`; then the instance is the name. URLs and metric
   filters always use `instance`.

**Charts.** `components/integrations/panels.ts` defines `PANELS[integrationId]`: each `PanelChart` has an `id`
(title key `integrations.charts.<id>`), `queries` (`{ key: { name, agg, groupBy? } }`, one request each),
`unit`, optional `stacked`/`order`/`yMax`, a `build(data, L)` that turns the query results into
`ChartSeriesInput[]` (using the pure helpers in `lib/integrations.ts`: `sumSeries`, `hitRatio`,
`differencePoints`, `pgCacheHitRatio`; `L(key)` translates `integrations.series.<key>`), and `alert` (the query
key a "create alert from this metric" link uses).

**Recommended alerts (presets).** `ALERT_PRESETS` in `lib/integrations.ts`: metric, aggregation, operator,
window, severity, optional data point attribute filters, and a threshold that is fixed or a ratio of another
metric's latest value (`{ ratioOf: "redis.maxmemory", ratio: 0.9 }`; unresolvable → the preset shows
"unavailable"). `presetSearch` turns a preset into `/alerts/rules/new` search params (host + instance resource
filters, grouped by host) that the rule editor applies with `applyPrefill` (`lib/alerts.ts`). Texts:
`integrations.alerts.presets.<id>.title|body`. Keep 2–4 presets per integration (unit-tested).

**Adding a panel for a new integration**

1. `lib/integrations.ts`: add the id to `INTEGRATION_IDS`; map discovery rules that use it in
   `integrationForRule` (e.g. `mariadb` → `mysql`).
2. `components/integrations/panels.ts`: add `PANELS.<id>` (new `PanelChartId`/`SeriesLabelKey` members);
   put non-trivial series math in `lib/integrations.ts`.
3. Optional: presets in `ALERT_PRESETS` (+ `PresetId`); integration-specific cards go next to
   `TopTablesCard` in `routes/integrations.tsx`.
4. i18n: `integrations.charts.*`, `integrations.series.*`, `integrations.alerts.presets.*` in `en.ts` and `tr.ts`.
5. Mocks: a discovered service with an `integration` object in `mocks/fixtures.ts` and metric series for every
   chart query (MSW applies the `resource.*` filters).
6. Tests: chart `build` and preset cases in `lib/integrations.test.ts`; `e2e/integrations.spec.ts` for the panel.

## Adding a screen

1. Create `src/routes/<screen>.tsx` exporting a component; use `PageHeader`, `LoadingState`/`ErrorState`/`EmptyState`.
2. Register it in `src/router.tsx` under `appRoute` (authenticated) with a typed `validateSearch`.
3. Add a nav entry in `components/AppShell.tsx` if it is top-level.
4. Add query factories to `api/queries.ts` and MSW handlers/fixtures in `src/mocks/` for any new endpoint.
5. Add strings to `en.ts` and `tr.ts`; add unit tests for new pure logic in `src/lib/*.test.ts`.
