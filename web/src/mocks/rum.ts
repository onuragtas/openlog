// MSW handlers for the real user monitoring endpoints (internal/api/rum.go, docs/contracts/rum.md §7): one
// browser application of the mock shop whose LCP is fine, INP needs work and CLS is poor, with two sessions —
// one that hit a JavaScript error. The vital thresholds here are the published Core Web Vitals boundaries,
// because the server sends those constants rather than a setting.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type { RumEvent, RumFunnelStep, RumPage, RumRelease, RumSession, RumVital } from "@/api/rum";
import { authenticate } from "./account";
import { formatTs } from "./fixtures";

const API = "*/api/v1";

function authed(resolver: HttpResponseResolver): HttpResponseResolver {
  return (info) => {
    const ctx = authenticate(info.request);
    if (ctx instanceof Response) return ctx;
    return resolver(info);
  };
}

export const MOCK_RUM_APP = "shop-web";
export const MOCK_RUM_SESSION_ID = "9f2c41b7a80d4e6fb35c1d8e07a4b620";
const TRACE_ID = "3a7f19c4b2e84d15a6c093f7e2b18d40";

const VITALS: RumVital[] = [
  { name: "lcp", unit: "ms", count: 1840, p50: 1620, p75: 2210, p95: 3480, avg: 1890, good: 0.82, needs_improvement: 0.14, poor: 0.04, rating: "good", good_threshold: 2500, poor_threshold: 4000 },
  { name: "inp", unit: "ms", count: 1210, p50: 140, p75: 260, p95: 520, avg: 190, good: 0.61, needs_improvement: 0.31, poor: 0.08, rating: "needs_improvement", good_threshold: 200, poor_threshold: 500 },
  { name: "cls", unit: "", count: 1840, p50: 0.08, p75: 0.31, p95: 0.52, avg: 0.17, good: 0.44, needs_improvement: 0.21, poor: 0.35, rating: "poor", good_threshold: 0.1, poor_threshold: 0.25 },
  { name: "fcp", unit: "ms", count: 1840, p50: 980, p75: 1340, p95: 2100, avg: 1120, good: 0.91, needs_improvement: 0.07, poor: 0.02, rating: "good", good_threshold: 1800, poor_threshold: 3000 },
  { name: "ttfb", unit: "ms", count: 1840, p50: 320, p75: 480, p95: 910, avg: 390, good: 0.88, needs_improvement: 0.1, poor: 0.02, rating: "good", good_threshold: 800, poor_threshold: 1800 },
];

const PAGES: RumPage[] = [
  { route: "/", views: 940, avg_ms: 1180, p50_ms: 1040, p75_ms: 1520, p95_ms: 2400, max_ms: 5200, ttfb_avg_ms: 310, lcp_p75: 1980, errors: 3 },
  { route: "/products/:id", views: 620, avg_ms: 1640, p50_ms: 1480, p75_ms: 2180, p95_ms: 3600, max_ms: 7400, ttfb_avg_ms: 420, lcp_p75: 2460, errors: 11 },
  { route: "/checkout", views: 280, avg_ms: 2240, p50_ms: 2010, p75_ms: 2980, p95_ms: 4800, max_ms: 9100, ttfb_avg_ms: 560, lcp_p75: 3310, errors: 2 },
];

/**
 * The same five vitals, worse on one route than on the application as a whole: /checkout is where the
 * mock shop's LCP goes bad, which is the whole reason a route has vitals of its own.
 */
function vitalsOf(route: string): RumVital[] {
  if (route !== "/checkout") return VITALS;
  return VITALS.map((v) =>
    v.name === "lcp" ? { ...v, p50: 2800, p75: 4300, p95: 6100, avg: 3200, good: 0.21, needs_improvement: 0.3, poor: 0.49, rating: "poor" } : v,
  );
}

const RELEASES: RumRelease[] = [
  { version: "1.4.2", sessions: 480, error_sessions: 6, crash_free_rate: 0.9875, first_seen: "", last_seen: "" },
  { version: "1.4.1", sessions: 132, error_sessions: 21, crash_free_rate: 0.8409, first_seen: "", last_seen: "" },
  // Older than the trace retention: the deployment is known, the sessions are gone.
  { version: "1.3.9", sessions: 0, error_sessions: 0, crash_free_rate: 1, first_seen: "", last_seen: "" },
];

/**
 * Two visits of the same tab-scoped kind. The first carries an identity and a country, the second carries
 * neither — the ordinary case, since user_id is set only when the application calls identify() and country
 * only when a trusted proxy resolved one (rum.md §3.7). Both are filled by the detail endpoint alone.
 */
function sessions(now: number): RumSession[] {
  return [
    {
      session_id: MOCK_RUM_SESSION_ID,
      app: MOCK_RUM_APP,
      environment: "production",
      started_at: formatTs(now - 9 * 60_000),
      ended_at: formatTs(now - 4 * 60_000),
      duration_ms: 302_000,
      page_views: 4,
      errors: 1,
      entry_route: "/",
      exit_route: "/checkout",
      device_type: "desktop",
      browser_name: "Chrome",
      browser_version: "131",
      os_name: "macOS",
      trace_id: TRACE_ID,
      user_id: "acct_8f3a2b",
      country: "TR",
    },
    {
      session_id: "5c81de0742ab4f93b6207e5ac1d9f384",
      app: MOCK_RUM_APP,
      environment: "production",
      started_at: formatTs(now - 41 * 60_000),
      ended_at: formatTs(now - 38 * 60_000),
      duration_ms: 164_000,
      page_views: 2,
      errors: 0,
      entry_route: "/products/:id",
      exit_route: "/products/:id",
      device_type: "mobile",
      browser_name: "Safari",
      browser_version: "18",
      os_name: "iOS",
      trace_id: "b41e7d62f8a34c95d1e07ab68f23c4a1",
      user_id: "",
      country: "",
    },
  ];
}

function events(now: number): RumEvent[] {
  return [
    { timestamp: formatTs(now - 9 * 60_000), event: "page_view", name: "/", route: "/", duration_ms: 1240, trace_id: TRACE_ID, span_id: "1a2b3c4d5e6f7081", error_group_id: "", status_code: 0 },
    { timestamp: formatTs(now - 9 * 60_000 + 400), event: "vital", name: "lcp", route: "/", duration_ms: 0, trace_id: TRACE_ID, span_id: "1a2b3c4d5e6f7082", error_group_id: "", status_code: 0 },
    {
      timestamp: formatTs(now - 8 * 60_000),
      event: "resource",
      name: "GET /api/cart",
      route: "/",
      duration_ms: 182,
      trace_id: TRACE_ID,
      span_id: "1a2b3c4d5e6f7083",
      error_group_id: "",
      status_code: 200,
    },
    {
      timestamp: formatTs(now - 6 * 60_000),
      event: "error",
      name: "TypeError: cart.items is undefined",
      route: "/checkout",
      duration_ms: 0,
      trace_id: TRACE_ID,
      span_id: "1a2b3c4d5e6f7084",
      error_group_id: "7c19ab3f42d05e68",
      status_code: 0,
    },
  ];
}

/** A views series with a working page-load average, at the one-minute step the overview reports. */
function points(from: number, to: number) {
  const step = 60_000;
  const out: { t: number; views: number; avg_ms: number | null }[] = [];
  for (let t = Math.ceil(from / step) * step; t <= to; t += step) {
    const i = out.length;
    out.push({ t, views: 12 + (i % 7) * 3, avg_ms: 1100 + (i % 5) * 120 });
  }
  return out;
}

const range = (url: URL) => {
  const now = Date.now();
  const from = Number(url.searchParams.get("from")) || now - 3_600_000;
  const to = Number(url.searchParams.get("to")) || now;
  return { from, to };
};

export const rumHandlers = [
  http.get(`${API}/rum/apps`, authed(() => {
    const now = Date.now();
    return HttpResponse.json({
      apps: [{ app: MOCK_RUM_APP, environment: "production", views: 1840, sessions: 612, errors: 16, last_seen: formatTs(now - 30_000) }],
    });
  })),

  http.get(`${API}/rum/overview`, authed(({ request }) => {
    const url = new URL(request.url);
    if ((url.searchParams.get("app") ?? "") === "") return HttpResponse.json({ error: { code: "invalid_argument", message: "app is required" } }, { status: 400 });
    const { from, to } = range(url);
    const series = points(from, to);
    return HttpResponse.json({
      from: formatTs(from),
      to: formatTs(to),
      step: "60s",
      vitals: VITALS,
      points: series,
      totals: { views: 1840, sessions: 612, errors: 16, avg_ms: 1280 },
    });
  })),

  http.get(`${API}/rum/pages`, authed(({ request }) => {
    const url = new URL(request.url);
    const sort = url.searchParams.get("sort") ?? "views";
    // One kind of visitor: the mock shop is slower on phones, which is the
    // whole reason the facet exists.
    if (url.searchParams.get("device_type") === "mobile") {
      return HttpResponse.json({ pages: PAGES.map((p) => ({ ...p, views: Math.round(p.views / 3), avg_ms: (p.avg_ms ?? 0) * 2 })) });
    }
    const pages = [...PAGES].sort((a, b) =>
      sort === "avg" ? (b.avg_ms ?? 0) - (a.avg_ms ?? 0) : sort === "slowest" ? b.views * (b.avg_ms ?? 0) - a.views * (a.avg_ms ?? 0) : b.views - a.views,
    );
    return HttpResponse.json({ pages });
  })),

  http.get(`${API}/rum/vitals`, authed(({ request }) => {
    const url = new URL(request.url);
    const { from, to } = range(url);
    // The server refuses it: the rollup is keyed by route and device only.
    if (url.searchParams.get("browser")) {
      return HttpResponse.json({ error: { code: "invalid_argument", message: "browser is not available for vitals" } }, { status: 400 });
    }
    return HttpResponse.json({ from: formatTs(from), to: formatTs(to), vitals: vitalsOf(url.searchParams.get("route") ?? "") });
  })),

  http.get(`${API}/rum/releases`, authed(({ request }) => {
    const now = Date.now();
    const url = new URL(request.url);
    if ((url.searchParams.get("app") ?? "") === "") return HttpResponse.json({ error: { code: "invalid_argument", message: "app is required" } }, { status: 400 });
    const releases = RELEASES.map((r, i) => ({ ...r, first_seen: formatTs(now - (i + 1) * 86_400_000), last_seen: formatTs(now - i * 3_600_000) }));
    const deployments = [
      { timestamp: formatTs(now - 3 * 3_600_000), t: now - 3 * 3_600_000, service_namespace: "", environment: "production", version: "1.4.2", previous_version: "1.4.1", initial: false, rollback: false },
      // The version before it came back for a while: a rollback, which the row says out loud.
      { timestamp: formatTs(now - 20 * 3_600_000), t: now - 20 * 3_600_000, service_namespace: "", environment: "production", version: "1.4.1", previous_version: "1.3.9", initial: false, rollback: true },
    ];
    return HttpResponse.json({ releases, deployments });
  })),

  http.get(`${API}/rum/funnel`, authed(({ request }) => {
    const url = new URL(request.url);
    const names = url.searchParams.getAll("step");
    if (names.length < 2) return HttpResponse.json({ error: { code: "invalid_argument", message: "at least two steps" } }, { status: 400 });
    // Each step keeps three quarters of the one before it, which is enough shape to read a funnel by.
    const first = 400;
    const steps: RumFunnelStep[] = names.map((step, i) => {
      const sessions = Math.round(first * 0.75 ** i);
      return { step, sessions, rate: sessions / first };
    });
    return HttpResponse.json({ steps, window_seconds: 1800 });
  })),

  http.get(
    `${API}/rum/sessions`,
    authed(({ request }) => {
      const url = new URL(request.url);
      const all = sessions(Date.now());
      const user = url.searchParams.get("user_id");
      // The identity is on the spans, so the server resolves it to session
      // ids; the mock stands in for that with the one session that has one.
      const rows = user ? all.filter((s) => s.session_id === MOCK_RUM_SESSION_ID) : all;
      const device = url.searchParams.get("device_type");
      return HttpResponse.json({ sessions: device ? rows.filter((s) => s.device_type === device) : rows });
    }),
  ),

  http.get(`${API}/rum/sessions/:sessionId`, authed(({ params }) => {
    const now = Date.now();
    const session = sessions(now).find((s) => s.session_id === params.sessionId);
    if (!session) return HttpResponse.json({ error: { code: "not_found", message: "session not found" } }, { status: 404 });
    // Only the newest session still has its spans; the older one shows the retention notice.
    return HttpResponse.json({ session, events: session.session_id === MOCK_RUM_SESSION_ID ? events(now) : [] });
  })),
];
