// MSW handlers for the SLO endpoints (internal/api/slos.go, docs/contracts/slo.md): two objectives of the
// mock shop — checkout availability (28 days, budget nearly gone, fast window burning) and catalog latency
// (7 days, healthy) — with in-memory CRUD.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type { Slo, SloBudget, SloInput, SloPoint } from "@/api/slos";
import { authenticate } from "./account";
import { formatTs } from "./fixtures";

const API = "*/api/v1";

type Code = "invalid_argument" | "not_found" | "permission_denied" | "failed_precondition";
const STATUS: Record<Code, number> = { invalid_argument: 400, not_found: 404, permission_denied: 403, failed_precondition: 409 };
const fail = (code: Code, message: string) => HttpResponse.json({ error: { code, message } }, { status: STATUS[code] });

function authed(resolver: HttpResponseResolver): HttpResponseResolver {
  return (info) => {
    const ctx = authenticate(info.request);
    if (ctx instanceof Response) return ctx;
    return resolver(info);
  };
}

/** Writes need a signed-in member or higher (slo.md §1). */
function writer(request: Request): Response | null {
  const ctx = authenticate(request);
  if (ctx instanceof Response) return ctx;
  if (ctx.kind !== "session") return fail("permission_denied", "this operation requires a signed-in user; API keys are read-only");
  if (ctx.role === "viewer") return fail("permission_denied", `your role (${ctx.role}) does not allow this operation`);
  return null;
}

const SLO_IDS = {
  availability: "51000000-0000-4000-8000-000000000001",
  latency: "51000000-0000-4000-8000-000000000002",
};

/** Test helper: the seeded objectives (availability burning, latency healthy). */
export const MOCK_SLO_IDS = SLO_IDS;

/** Bad request share per SLO, so the mock budgets are deterministic. */
const BAD_RATIO: Record<string, number> = { [SLO_IDS.availability]: 0.0012, [SLO_IDS.latency]: 0.004 };

function seed(): Slo[] {
  const now = Date.now();
  const base = { created_by_email: "admin@openlog.local", updated_by_email: "admin@openlog.local", created_at: formatTs(now - 30 * 86_400_000), updated_at: formatTs(now - 86_400_000) };
  return [
    {
      id: SLO_IDS.availability, name: "Checkout availability", description: "Successful checkout requests", service_name: "frontend",
      service_namespace: "shop", environment: "prod", sli_type: "availability", latency_threshold_ms: null, objective: 99.9,
      window_days: 28, ...base,
    },
    {
      id: SLO_IDS.latency, name: "Catalog latency", description: "", service_name: "catalog", service_namespace: null,
      environment: "prod", sli_type: "latency", latency_threshold_ms: 300, objective: 99, window_days: 7, ...base,
    },
  ];
}

let slos: Slo[] = seed();
let nextId = 3;

/** Test helper: forget created SLOs. */
export function resetMockSlos(): void {
  slos = seed();
  nextId = 3;
}

function budget(requests: number, badRatio: number, objective: number): SloBudget {
  const bad = Math.round(requests * badRatio);
  const good = requests - bad;
  const allowed = 1 - objective / 100;
  const budgetRequests = allowed * requests;
  return {
    requests, good, bad, sli: requests ? good / requests : null,
    budget_requests: budgetRequests, budget_consumed: bad, budget_remaining: budgetRequests - bad,
    remaining_ratio: requests ? 1 - bad / budgetRequests : null,
    burn_rate: requests ? bad / requests / allowed : null,
    met: requests === 0 || good / requests >= objective / 100,
  };
}

function status(slo: Slo) {
  const now = Date.now();
  const minutes = slo.window_days * 24 * 60;
  const requests = minutes * 200;
  return {
    from: formatTs(now - slo.window_days * 86_400_000),
    to: formatTs(now),
    window_days: slo.window_days,
    budget: budget(requests, BAD_RATIO[slo.id] ?? 0.0005, slo.objective),
  };
}

function series(slo: Slo, points = 60): SloPoint[] {
  const now = Date.now();
  const stepMs = Math.round((slo.window_days * 86_400_000) / points);
  const total = status(slo).budget;
  const out: SloPoint[] = [];
  let consumed = 0;
  for (let i = 0; i < points; i++) {
    const requests = total.requests / points;
    // The budget burns faster in the last fifth of the window (a recent incident).
    const share = i > points * 0.8 ? 3 : 0.5;
    const bad = Math.min(total.bad - consumed, (total.bad / points) * share);
    consumed += Math.max(0, bad);
    out.push({
      t: now - (points - i) * stepMs,
      requests,
      good: requests - Math.max(0, bad),
      bad: Math.max(0, bad),
      sli: requests ? (requests - Math.max(0, bad)) / requests : null,
      burn_rate: requests ? Math.max(0, bad) / requests / (1 - slo.objective / 100) : null,
      remaining_ratio: total.budget_requests ? 1 - consumed / total.budget_requests : null,
    });
  }
  return out;
}

function burn(slo: Slo) {
  const breaching = slo.id === SLO_IDS.availability;
  const window = (name: string, factor: number, long: number, short: number, rate: number | null) => {
    const requests = (long / 60) * 200;
    const badRatio = rate === null ? 0 : rate * (1 - slo.objective / 100);
    return {
      name, factor, long_seconds: long, short_seconds: short,
      long: budget(requests, badRatio, slo.objective),
      short: budget((short / 60) * 200, badRatio, slo.objective),
      rate, ratio: rate === null ? null : rate / factor, breaching: rate !== null && rate >= factor,
    };
  };
  return [
    window("fast", 14.4, 3600, 300, breaching ? 18.2 : 0.4),
    window("slow", 6, 21600, 1800, breaching ? 4.1 : 0.3),
  ];
}

function validate(body: Partial<SloInput>): string | null {
  if (!body.name || typeof body.name !== "string" || body.name.trim() === "") return "name: must be 1-200 characters";
  if (!body.service_name || typeof body.service_name !== "string") return "service_name: required, at most 512 bytes";
  if (body.sli_type !== "availability" && body.sli_type !== "latency") return "sli_type: must be availability or latency";
  if (body.sli_type === "latency" && !(Number.isInteger(body.latency_threshold_ms) && (body.latency_threshold_ms as number) >= 1)) {
    return "latency_threshold_ms: must be a whole number of milliseconds between 1 and 600000";
  }
  if (typeof body.objective !== "number" || body.objective < 50 || body.objective >= 100) {
    return "objective: must be at least 50 and below 100 (percent)";
  }
  if (![7, 28, 30].includes(Number(body.window_days))) return "window_days: must be 7, 28 or 30";
  return null;
}

function stored(body: SloInput, existing?: Slo): Slo {
  const now = Date.now();
  return {
    id: existing?.id ?? `51000000-0000-4000-8000-${String(nextId++).padStart(12, "0")}`,
    name: body.name.trim(),
    description: body.description ?? "",
    service_name: body.service_name.trim(),
    service_namespace: body.service_namespace ?? null,
    environment: body.environment ?? null,
    sli_type: body.sli_type,
    latency_threshold_ms: body.sli_type === "latency" ? (body.latency_threshold_ms ?? null) : null,
    objective: body.objective,
    window_days: body.window_days,
    created_by_email: existing?.created_by_email ?? "admin@openlog.local",
    updated_by_email: "admin@openlog.local",
    created_at: existing?.created_at ?? formatTs(now),
    updated_at: formatTs(now),
  };
}

export const sloHandlers = [
  http.get(`${API}/slos`, authed(({ request }) => {
    const withStatus = new URL(request.url).searchParams.get("status") !== "false";
    return HttpResponse.json({
      slos: slos.map((s) => ({ ...s, status: withStatus ? status(s) : null })),
      status_truncated: false,
    });
  })),

  http.post(`${API}/slos`, async ({ request }) => {
    const denied = writer(request);
    if (denied) return denied;
    const body = (await request.json().catch(() => ({}))) as Partial<SloInput>;
    const err = validate(body);
    if (err) return fail("invalid_argument", err);
    const s = stored(body as SloInput);
    slos = [...slos, s];
    return HttpResponse.json(s, { status: 201 });
  }),

  http.get(`${API}/slos/:id`, authed(({ params }) => {
    const s = slos.find((x) => x.id === params.id);
    return s ? HttpResponse.json(s) : fail("not_found", "slo not found");
  })),

  http.put(`${API}/slos/:id`, async ({ request, params }) => {
    const denied = writer(request);
    if (denied) return denied;
    const existing = slos.find((x) => x.id === params.id);
    if (!existing) return fail("not_found", "slo not found");
    const body = (await request.json().catch(() => ({}))) as Partial<SloInput>;
    const err = validate(body);
    if (err) return fail("invalid_argument", err);
    const s = stored(body as SloInput, existing);
    slos = slos.map((x) => (x.id === s.id ? s : x));
    return HttpResponse.json(s);
  }),

  http.delete(`${API}/slos/:id`, ({ request, params }) => {
    const denied = writer(request);
    if (denied) return denied;
    if (!slos.some((x) => x.id === params.id)) return fail("not_found", "slo not found");
    slos = slos.filter((x) => x.id !== params.id);
    return new HttpResponse(null, { status: 204 });
  }),

  http.get(`${API}/slos/:id/results`, authed(({ params }) => {
    const s = slos.find((x) => x.id === params.id);
    if (!s) return fail("not_found", "slo not found");
    return HttpResponse.json({ slo: s, status: status(s), step: "600s", burn: burn(s), series: series(s) });
  })),
];
