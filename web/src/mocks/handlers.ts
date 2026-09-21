// MSW handlers emulating openlog-api (docs/contracts/openapi.yaml), including
// auth, error envelopes, defaults and filters.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import { accountHandlers, authenticate } from "./account";
import { alertHandlers } from "./alerts";
import { apmHandlers, transactionTraceIds } from "./apm";
import { cloudHandlers } from "./cloud";
import { containerHandlers, containerLogs } from "./containers";
import { costHandlers } from "./costs";
import { kubernetesHandlers, kubernetesLogs } from "./kubernetes";
import { fleetHandlers } from "./fleet";
import { integrationSettingsHandlers } from "./integrationSettings";
import { dashboardHandlers } from "./dashboards";
import { dashboardSharingHandlers } from "./dashboardSharing";
import { oqlHandlers } from "./oql";
import { browserKeyHandlers } from "./browserKeys";
import { profileHandlers } from "./profiles";
import { rumHandlers } from "./rum";
import { dbHandlers } from "./db";
import { sloHandlers } from "./slos";
import { jobHandlers } from "./jobs";
import { vulnerabilityHandlers } from "./vulnerabilities";
import { syntheticHandlers } from "./synthetics";
import { usageHandlers } from "./usage";
import { operatorHandlers } from "./operator";
import { ssoHandlers } from "./sso";
import { onboardingHandlers, onboardingHosts } from "./onboarding";
import { privacyHandlers } from "./privacy";
import { explorerHandlers } from "./explorer";
import * as fx from "./fixtures";

type ErrorCode = "invalid_argument" | "unauthenticated" | "not_found" | "internal" | "timeout";
const STATUS: Record<ErrorCode, number> = { invalid_argument: 400, unauthenticated: 401, not_found: 404, internal: 500, timeout: 504 };

export function apiError(code: ErrorCode, message: string) {
  return HttpResponse.json({ error: { code, message } }, { status: STATUS[code] });
}

/** Attribute keys accepted as attr.<key> log filters (internal/api/filters.go). */
const LOG_ATTR_FILTERS = ["openlog.log.source", "log.file.path", "log.file.name", "openlog.discovery.id", "openlog.systemd.unit", "openlog.syslog.identifier", "log.iostream"];

/** Wraps a resolver with the authentication done by internal/api wrap(): session (see mocks/account.ts) or API key. */
function authed(resolver: HttpResponseResolver): HttpResponseResolver {
  return (info) => {
    const ctx = authenticate(info.request);
    if (ctx instanceof Response) return ctx;
    return resolver(info);
  };
}

function parseTime(v: string): number | null {
  if (/^-?\d+$/.test(v)) return Number(v);
  const t = Date.parse(v);
  return Number.isNaN(t) ? null : t;
}

function timeRange(url: URL): { from: number; to: number } | Response {
  const now = Date.now();
  let from = now - 3_600_000;
  let to = now;
  const f = url.searchParams.get("from");
  const t = url.searchParams.get("to");
  if (f) {
    const v = parseTime(f);
    if (v === null) return apiError("invalid_argument", `from: invalid time "${f}" (use RFC3339 or unix milliseconds)`);
    from = v;
  }
  if (t) {
    const v = parseTime(t);
    if (v === null) return apiError("invalid_argument", `to: invalid time "${t}" (use RFC3339 or unix milliseconds)`);
    to = v;
  }
  if (from >= to) return apiError("invalid_argument", "from must be before to");
  return { from, to };
}

function limit(url: URL, max = 10000): number | Response {
  const v = url.searchParams.get("limit");
  if (!v) return Math.min(100, max);
  const n = Number(v);
  if (!Number.isInteger(n) || n <= 0) return apiError("invalid_argument", "limit must be a positive integer");
  return Math.min(n, max);
}

const SEVERITY: Record<string, number> = { TRACE: 1, DEBUG: 5, INFO: 9, WARN: 13, WARNING: 13, ERROR: 17, FATAL: 21 };

const API = "*/api/v1";

export const handlers = [
  http.get(`${API}/hosts`, authed(({ request }) => {
    const url = new URL(request.url);
    const lim = limit(url);
    if (lim instanceof Response) return lim;
    const now = Date.now();
    const list = [...fx.hosts(now), ...onboardingHosts(now)].filter((h) => Date.parse(h.last_seen) >= now - 24 * 3_600_000);
    return HttpResponse.json({ hosts: list.slice(0, lim) });
  })),

  http.get(`${API}/hosts/:hostId`, authed(({ params }) => {
    const now = Date.now();
    const h = [...fx.hosts(now), ...fx.osHosts(now), ...onboardingHosts(now)].find((x) => x.host_id === params.hostId);
    return h ? HttpResponse.json(h) : apiError("not_found", "host not found");
  })),

  http.get(`${API}/metrics/names`, authed(({ request }) => {
    const r = timeRange(new URL(request.url));
    if (r instanceof Response) return r;
    const names = Object.entries(fx.METRICS)
      .map(([name, d]) => ({ name, type: d.type, unit: d.unit }))
      .sort((a, b) => a.name.localeCompare(b.name));
    return HttpResponse.json({ names });
  })),

  // What else changed in a window (internal/api/correlate.go, D-146). The fixture answers with a handful of
  // series whose means moved, so the panel can be read end to end; the baseline defaults to four window
  // lengths before it, exactly as the server computes it.
  http.get(`${API}/metrics/correlate`, authed(({ request }) => {
    const url = new URL(request.url);
    const r = timeRange(url);
    if (r instanceof Response) return r;
    const span = r.to - r.from;
    if (span > 6 * 3600_000) return apiError("invalid_argument", "the window is longer than 6h0m0s; correlate the part of it that matters");
    const limit = Number(url.searchParams.get("limit") ?? 20);
    if (!Number.isInteger(limit) || limit < 1 || limit > 200) return apiError("invalid_argument", "limit must be between 1 and 200");
    const hostId = url.searchParams.get("host_id") ?? "";
    const all = fx.correlations(hostId);
    return HttpResponse.json({
      from: fx.formatTs(r.from),
      to: fx.formatTs(r.to),
      baseline_from: fx.formatTs(r.from - 4 * span),
      baseline_to: fx.formatTs(r.from),
      series_compared: 1842,
      correlations: all.slice(0, limit),
    });
  })),

  http.get(`${API}/hosts/:hostId/metrics`, authed(({ request, params }) => {
    const url = new URL(request.url);
    const name = url.searchParams.get("name") ?? "";
    if (!name) return apiError("invalid_argument", "name is required");
    const r = timeRange(url);
    if (r instanceof Response) return r;
    const agg = url.searchParams.get("agg") ?? "";
    if (agg && !["avg", "min", "max", "sum", "last", "rate"].includes(agg)) return apiError("invalid_argument", "agg must be one of avg, min, max, sum, last, rate");
    const groupBy = (url.searchParams.get("group_by") ?? "").split(",").map((s) => s.trim()).filter(Boolean);
    for (const g of groupBy) {
      if (g.startsWith("resource.") && !fx.METRIC_RESOURCE_KEYS.includes(g.slice(9))) return apiError("invalid_argument", `group_by: ${g}: unsupported resource attribute`);
    }
    // resource.<key>=<value>: allowlisted resource attribute filters (api.md).
    const resFilters: [string, string][] = [];
    for (const [pname, value] of url.searchParams.entries()) {
      if (!pname.startsWith("resource.")) continue;
      const key = pname.slice("resource.".length);
      if (!fx.METRIC_RESOURCE_KEYS.includes(key)) return apiError("invalid_argument", `resource.${key}: unsupported resource attribute filter`);
      if (!value || url.searchParams.getAll(pname).length !== 1) return apiError("invalid_argument", `resource.${key}: exactly one non-empty value is required`);
      resFilters.push([key, value]);
    }
    const hasResource = resFilters.length > 0 || groupBy.some((g) => g.startsWith("resource."));
    const rollup = r.to - r.from > 6 * 3_600_000 && !hasResource;
    const unit = rollup ? 60 : 10;
    let step = Math.max(unit, Math.floor((r.to - r.from) / 1000 / 300));
    if (step % unit) step += unit - (step % unit);

    const baseDef = fx.METRICS[name];
    // win-iis-1 has SQL Server and IIS integration metrics (its resource filters select only those series).
    const known = (Object.values(fx.HOST_IDS).includes(params.hostId as never) && params.hostId !== fx.HOST_IDS.worker) || params.hostId === fx.OS_HOST_IDS.win;
    const matching = baseDef?.series.filter((s) => resFilters.every(([k, v]) => (s.resource ?? {})[k] === v)) ?? [];
    const def = baseDef && matching.length > 0 ? { ...baseDef, series: matching } : undefined;
    if (!def || !known) {
      return HttpResponse.json({ metric: { name, type: "", unit: "" }, step: `${step}s`, series: [] });
    }
    const effectiveAgg = agg || (def.type === "sum" ? (def.monotonic ? "rate" : "last") : "avg");
    // Like the real API: avg/min/max merge series by averaging/min/max, sum/last/rate by summing.
    const averaged = effectiveAgg === "avg";
    const merged = new Map<string, { attributes: Record<string, string>; points: Map<number, number>; counts: Map<number, number> }>();
    // db-1's agent "started" 5 minutes ago: short-lived data inside long ranges (charts show "data since").
    const dataFrom = params.hostId === fx.HOST_IDS.db ? Math.max(r.from, Date.now() - 5 * 60_000) : r.from;
    const startBucket = Math.ceil(dataFrom / 1000 / step) * step;
    for (const s of def.series) {
      const all = { ...s.attributes, ...Object.fromEntries(Object.entries(s.resource ?? {}).map(([k, v]) => [`resource.${k}`, v])) };
      const attrs = groupBy.length > 0 ? Object.fromEntries(Object.entries(all).filter(([k]) => groupBy.includes(k))) : s.attributes;
      const key = JSON.stringify(Object.entries(attrs).sort());
      const entry = merged.get(key) ?? { attributes: attrs, points: new Map<number, number>(), counts: new Map<number, number>() };
      merged.set(key, entry);
      for (let t = startBucket; t * 1000 <= r.to; t += step) {
        const ms = t * 1000;
        entry.points.set(ms, (entry.points.get(ms) ?? 0) + s.value(t, effectiveAgg === "rate"));
        entry.counts.set(ms, (entry.counts.get(ms) ?? 0) + 1);
      }
    }
    const series = [...merged.values()]
      .sort((a, b) => JSON.stringify(a.attributes).localeCompare(JSON.stringify(b.attributes)))
      .map((s) => ({
        attributes: s.attributes,
        points: [...s.points.entries()]
          .sort((a, b) => a[0] - b[0])
          .map(([t, v]): [number, number] => [t, averaged ? v / (s.counts.get(t) ?? 1) : v]),
      }));
    return HttpResponse.json({ metric: { name, type: def.type, unit: def.unit }, step: `${step}s`, series });
  })),

  http.get(`${API}/hosts/:hostId/inventory`, authed(({ request, params }) => {
    const url = new URL(request.url);
    const h = fx.hosts(Date.now()).find((x) => x.host_id === params.hostId);
    if (!h || !fx.hasSnapshot(h.host_id)) return HttpResponse.json({ snapshot_id: "", snapshot_time: null, items: [] });
    const category = url.searchParams.get("category");
    const items = fx.inventory(h).filter((it) => !category || it.category === category);
    return HttpResponse.json({ snapshot_id: "0191e0a4-7b7e-7c3a-9d52-5f0c8e7a1b2c", snapshot_time: fx.formatTs(Date.now() - 20 * 60_000), items });
  })),

  http.get(`${API}/hosts/:hostId/services`, authed(({ params }) => {
    // The Windows host's services (IIS, SQL Server) resolve here too; it stays out of lists and inventory search.
    const h = [...fx.hosts(Date.now()), ...fx.osHosts(Date.now()).filter((x) => x.host_id === fx.OS_HOST_IDS.win)].find((x) => x.host_id === params.hostId);
    if (!h || !fx.hasSnapshot(h.host_id)) return HttpResponse.json({ snapshot_id: "", snapshot_time: null, items: [] });
    const items = fx.inventory(h).filter((it) => it.category === "discovered_service");
    return HttpResponse.json({ snapshot_id: "0191e0a4-7b7e-7c3a-9d52-5f0c8e7a1b2c", snapshot_time: fx.formatTs(Date.now() - 20 * 60_000), items });
  })),

  http.get(`${API}/inventory/search`, authed(({ request }) => {
    const url = new URL(request.url);
    const category = url.searchParams.get("category") ?? "";
    if (!category) return apiError("invalid_argument", "category is required");
    const lim = limit(url);
    if (lim instanceof Response) return lim;
    const q = (url.searchParams.get("q") ?? "").toLowerCase();
    const items = fx
      .hosts(Date.now())
      .filter((h) => fx.hasSnapshot(h.host_id))
      .flatMap((h) =>
        fx
          .inventory(h)
          .filter((it) => it.category === category && (!q || it.key.toLowerCase().includes(q)))
          .map((it) => ({ host_id: h.host_id, host_name: h.host_name, ...it })),
      )
      .sort((a, b) => a.key.localeCompare(b.key) || a.host_id.localeCompare(b.host_id))
      .slice(0, lim);
    return HttpResponse.json({ items });
  })),

  http.get(`${API}/logs`, authed(({ request }) => {
    const url = new URL(request.url);
    const r = timeRange(url);
    if (r instanceof Response) return r;
    const lim = limit(url);
    if (lim instanceof Response) return lim;
    const p = url.searchParams;
    let sevMin: number | null = null;
    const sev = p.get("severity_min");
    if (sev) {
      sevMin = /^\d+$/.test(sev) ? Number(sev) : (SEVERITY[sev.toUpperCase()] ?? null);
      if (sevMin === null) return apiError("invalid_argument", "severity_min must be a number 1-24 or a severity name");
    }
    const q = (p.get("q") ?? "").toLowerCase();
    const traceId = (p.get("trace_id") ?? "").toLowerCase();
    // attr.<key>=<value>: allowlisted exact-match attribute filters (api.md).
    const attrFilters: [string, string][] = [];
    for (const [name, value] of p.entries()) {
      if (!name.startsWith("attr.")) continue;
      const key = name.slice("attr.".length);
      if (!LOG_ATTR_FILTERS.includes(key)) return apiError("invalid_argument", `attr.${key}: unsupported attribute filter`);
      if (!value || p.getAll(name).length !== 1) return apiError("invalid_argument", `attr.${key}: exactly one non-empty value is required`);
      attrFilters.push([key, value]);
    }
    const containerId = (p.get("container_id") ?? "").toLowerCase();
    const spanId = (p.get("span_id") ?? "").toLowerCase();
    if (spanId && !/^[0-9a-f]{16}$/.test(spanId)) return apiError("invalid_argument", "span_id must be 16 hex characters");
    const txn = p.get("transaction") ?? "";
    const txnService = p.get("transaction_service") ?? "";
    if (!!txn !== !!txnService) return apiError("invalid_argument", "transaction and transaction_service must be set together");
    const txnTraces = txn ? new Set(transactionTraceIds(txnService, txn)) : null;
    // Opaque cursor (the real API encodes the last row); here the offset into the filtered listing.
    let offset = 0;
    const cursor = p.get("cursor");
    if (cursor) {
      try {
        const o = (JSON.parse(atob(cursor)) as { o?: unknown }).o;
        if (typeof o !== "number" || !Number.isInteger(o) || o < 0) throw new Error("bad cursor");
        offset = o;
      } catch {
        return apiError("invalid_argument", "cursor: invalid");
      }
    }
    const now = Date.now();
    // APM log records with trace context (the mock trace's spans), for log ↔ trace navigation.
    const traceLogs = fx.trace(now).slice(0, 4).map((sp, i) => ({
      timestamp: sp.start,
      severity_text: i === 3 ? "ERROR" : "INFO",
      severity_number: i === 3 ? 17 : 9,
      body: `checkout flow: ${sp.name}`,
      host_id: "",
      service_name: sp.service_name,
      trace_id: fx.TRACE_ID,
      span_id: sp.span_id,
      attributes: {} as Record<string, string>,
      resource_attributes: { "service.name": sp.service_name } as Record<string, string>,
    }));
    const list = [...fx.logs(now), ...containerLogs(now), ...kubernetesLogs(now), ...traceLogs].sort((a, b) => Date.parse(b.timestamp) - Date.parse(a.timestamp)).filter((l) => {
      const ts = Date.parse(l.timestamp);
      if (ts < r.from || ts > r.to) return false;
      if (p.get("host_id") && l.host_id !== p.get("host_id")) return false;
      if (p.get("service") && l.service_name !== p.get("service")) return false;
      if (q && !l.body.toLowerCase().includes(q)) return false;
      if (traceId && l.trace_id !== traceId) return false;
      if (spanId && l.span_id !== spanId) return false;
      if (txnTraces && !txnTraces.has(l.trace_id)) return false;
      if (containerId && l.resource_attributes["container.id"] !== containerId) return false;
      if (p.get("k8s_pod_uid") && l.resource_attributes["k8s.pod.uid"] !== p.get("k8s_pod_uid")) return false;
      if (p.get("compose_service") && l.resource_attributes["docker.compose.service"] !== p.get("compose_service")) return false;
      if (sevMin !== null && l.severity_number < sevMin) return false;
      if (attrFilters.some(([k, v]) => l.attributes[k] !== v)) return false;
      return true;
    });
    const end = offset + lim;
    return HttpResponse.json({ logs: list.slice(offset, end), next_cursor: end < list.length ? btoa(JSON.stringify({ o: end })) : null });
  })),

  http.get(`${API}/traces/:traceId`, authed(({ params }) => {
    const id = String(params.traceId).toLowerCase();
    if (!/^[0-9a-f]{32}$/.test(id)) return apiError("invalid_argument", "trace_id must be 32 hex characters");
    if (id === fx.BIG_TRACE_ID) return HttpResponse.json({ trace_id: id, spans: fx.bigTrace(Date.now()) });
    if (id !== fx.TRACE_ID) return apiError("not_found", "trace not found");
    return HttpResponse.json({ trace_id: id, spans: fx.trace(Date.now()) });
  })),

  ...accountHandlers,
  ...fleetHandlers,
  ...integrationSettingsHandlers,
  ...containerHandlers,
  ...costHandlers, // mocks/costs.ts: infrastructure cost estimates (D-134)
  ...kubernetesHandlers,
  ...apmHandlers,
  ...alertHandlers,
  ...sloHandlers, // mocks/slos.ts: service level objectives, error budgets and burn rates (D-125)
  ...syntheticHandlers, // mocks/synthetics.ts: scheduled outside-in checks (D-132)
  ...jobHandlers, // mocks/jobs.ts: cron and heartbeat monitoring (D-141)
  ...vulnerabilityHandlers, // mocks/vulnerabilities.ts: vulnerable packages per host (D-142)
  ...rumHandlers, // mocks/rum.ts: browser applications, vitals, pages and sessions (D-136)
  ...profileHandlers, // mocks/profiles.ts: continuous profiling flame graphs (profiles.md)
  ...dbHandlers, // mocks/db.ts: database query performance (D-138)
  ...browserKeyHandlers, // mocks/browserKeys.ts: the public keys of the RUM SDK (D-136)
  ...cloudHandlers, // mocks/cloud.ts: managed cloud service metrics (D-135)
  ...oqlHandlers,
  ...dashboardSharingHandlers, // before dashboardHandlers: /dashboards/settings is not /dashboards/:id
  ...dashboardHandlers,
  ...usageHandlers,
  ...operatorHandlers, // mocks/operator.ts: operator console, SaaS state, support access
  ...ssoHandlers,
  ...onboardingHandlers,
  ...privacyHandlers, // mocks/privacy.ts: data exports, deletion, status page (D-107, D-108)
  ...explorerHandlers, // mocks/explorer.ts: field keys/values, logs and metrics explorer, saved views (D-118, D-119)

  http.all(`${API}/*`, () => apiError("not_found", "no such endpoint")),
];
