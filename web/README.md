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
`lib/series.ts` and are unit-tested. Only uPlot is used today; add ECharts/React Flow/CodeMirror when a
screen needs them.

**Accessibility.** Every input has a label, icon-only buttons have `aria-label`, toggles use `aria-pressed`
/ `aria-expanded`, interactive rows are reachable by keyboard (links/buttons inside rows).

## Adding a screen

1. Create `src/routes/<screen>.tsx` exporting a component; use `PageHeader`, `LoadingState`/`ErrorState`/`EmptyState`.
2. Register it in `src/router.tsx` under `appRoute` (authenticated) with a typed `validateSearch`.
3. Add a nav entry in `components/AppShell.tsx` if it is top-level.
4. Add query factories to `api/queries.ts` and MSW handlers/fixtures in `src/mocks/` for any new endpoint.
5. Add strings to `en.ts` and `tr.ts`; add unit tests for new pure logic in `src/lib/*.test.ts`.
