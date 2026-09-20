// MSW handlers for the synthetic monitoring endpoints (internal/api/synthetics.go, docs/contracts/api.md
// "Synthetic monitoring"): two checks of the mock shop — a healthy checkout check and a catalog check that
// is currently failing with 503 — with in-memory CRUD.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type { SyntheticCheck, SyntheticCheckInput, SyntheticFailure, SyntheticPoint, SyntheticSummary } from "@/api/synthetics";
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

/** Writes need a signed-in member or higher (api.md "Synthetic monitoring"). */
function writer(request: Request): Response | null {
  const ctx = authenticate(request);
  if (ctx instanceof Response) return ctx;
  if (ctx.kind !== "session") return fail("permission_denied", "this operation requires a signed-in user; API keys are read-only");
  if (ctx.role === "viewer") return fail("permission_denied", `your role (${ctx.role}) does not allow this operation`);
  return null;
}

const CHECK_IDS = {
  up: "52000000-0000-4000-8000-000000000001",
  down: "52000000-0000-4000-8000-000000000002",
};

/** Test helper: the seeded checks (one healthy, one failing). */
export const MOCK_SYNTHETIC_IDS = CHECK_IDS;

/** Failure share per check, so the mock summaries are deterministic. */
const FAILURE_RATIO: Record<string, number> = { [CHECK_IDS.up]: 0.002, [CHECK_IDS.down]: 0.12 };

function seed(): SyntheticCheck[] {
  const now = Date.now();
  const base = {
    // The fields of the other check kinds; an http check stores them empty (see D-140).
    target: "",
    dns_record_type: "",
    dns_expected: [],
    tls_warning_days: 0,
    created_by_email: "admin@openlog.local",
    updated_by_email: "admin@openlog.local",
    created_at: formatTs(now - 20 * 86_400_000),
    updated_at: formatTs(now - 86_400_000),
  };
  return [
    {
      id: CHECK_IDS.up,
      name: "Checkout health",
      type: "http",
      enabled: true,
      url: "https://shop.example.com/health",
      method: "GET",
      headers: {},
      body: "",
      expected_status: [200],
      assertion_type: "contains",
      assertion_path: "",
      assertion_value: "ok",
      timeout_ms: 10000,
      interval_seconds: 300,
      locations: ["local"],
      ...base,
      status: [
        {
          location: "local",
          next_run_at: formatTs(now + 120_000),
          last_run_at: formatTs(now - 180_000),
          last_success: true,
          last_status_code: 200,
          last_duration_ms: 118.4,
          last_error_kind: "",
          last_error: "",
        },
      ],
    },
    {
      id: CHECK_IDS.down,
      name: "Catalog API",
      type: "http",
      enabled: true,
      url: "https://shop.example.com/api/catalog",
      method: "GET",
      headers: { "X-Api-Version": "2" },
      body: "",
      expected_status: [200],
      assertion_type: "json_path",
      assertion_path: "status",
      assertion_value: "ok",
      timeout_ms: 5000,
      interval_seconds: 60,
      locations: ["local"],
      ...base,
      status: [
        {
          location: "local",
          next_run_at: formatTs(now + 30_000),
          last_run_at: formatTs(now - 30_000),
          last_success: false,
          last_status_code: 503,
          last_duration_ms: 87.2,
          last_error_kind: "status",
          last_error: "HTTP 503, expected 200",
        },
      ],
    },
  ];
}

let checks: SyntheticCheck[] = seed();
let nextId = 3;

/** Test helper: forget created checks. */
export function resetMockSynthetics(): void {
  checks = seed();
  nextId = 3;
}

function points(check: SyntheticCheck, count = 24): SyntheticPoint[] {
  const now = Date.now();
  const stepMs = Math.round((24 * 3_600_000) / count);
  const perBucket = Math.max(1, Math.round(stepMs / 1000 / check.interval_seconds));
  const ratio = FAILURE_RATIO[check.id] ?? 0;
  const out: SyntheticPoint[] = [];
  for (let i = 0; i < count; i++) {
    // The failing check only started failing in the last quarter of the window.
    const failing = ratio > 0.05 ? i > count * 0.75 : ratio > 0;
    const failures = failing ? Math.max(1, Math.round(perBucket * ratio)) : 0;
    out.push({
      t: now - (count - i) * stepMs,
      runs: perBucket,
      failures,
      uptime: ((perBucket - failures) / perBucket) * 100,
      p95_ms: 110 + Math.round(40 * Math.sin(i / 3)) + (failing ? 60 : 0),
    });
  }
  return out;
}

function summary(check: SyntheticCheck): SyntheticSummary {
  const series = points(check);
  const runs = series.reduce((n, p) => n + p.runs, 0);
  const failures = series.reduce((n, p) => n + p.failures, 0);
  return {
    from: formatTs(Date.now() - 24 * 3_600_000),
    to: formatTs(Date.now()),
    step: "3600s",
    runs,
    failures,
    uptime: runs ? ((runs - failures) / runs) * 100 : null,
    avg_ms: 124.6,
    p50_ms: 112,
    p95_ms: 238,
    p99_ms: 486,
    points: series,
  };
}

function failures(check: SyntheticCheck): SyntheticFailure[] {
  if ((FAILURE_RATIO[check.id] ?? 0) < 0.05) return [];
  const now = Date.now();
  return [0, 1, 2].map((i) => ({
    timestamp: formatTs(now - (i + 1) * 60_000),
    location: "local",
    status_code: 503,
    error_kind: "status",
    error: "HTTP 503, expected 200",
    duration_ms: 87.2 + i,
  }));
}

function validate(body: Partial<SyntheticCheckInput>): string | null {
  if (!body.name || typeof body.name !== "string" || body.name.trim() === "") return "name: must be 1-200 characters";
  const type = body.type ?? "http";
  const target = (body.target ?? "").trim();
  if (type === "http") {
    if (!body.url || typeof body.url !== "string") return "url: required";
    try {
      const u = new URL(body.url);
      if (u.protocol !== "http:" && u.protocol !== "https:") return "url: must be an http or https URL";
    } catch {
      return "url: invalid URL";
    }
  } else if (type === "dns") {
    if (!target) return "target: required: the name to resolve";
  } else {
    if (!target) return "target: required: host:port";
    const port = Number(target.slice(target.lastIndexOf(":") + 1));
    if (!target.includes(":") || !Number.isInteger(port) || port < 1 || port > 65535) {
      return "target: must be host:port (e.g. example.com:443)";
    }
  }
  const timeout = body.timeout_ms ?? 10000;
  const interval = body.interval_seconds ?? 300;
  if (timeout < 500 || timeout > 60000) return "timeout_ms: must be between 500 and 60000 milliseconds";
  if (interval < 30 || interval > 86400) return "interval_seconds: must be between 30 and 86400 seconds";
  if (timeout > interval * 1000) return "timeout_ms: must not be longer than interval_seconds";
  return null;
}

function stored(body: SyntheticCheckInput, existing?: SyntheticCheck): SyntheticCheck {
  const now = Date.now();
  const locations = body.locations?.length ? body.locations : ["local"];
  return {
    id: existing?.id ?? `52000000-0000-4000-8000-${String(nextId++).padStart(12, "0")}`,
    name: body.name.trim(),
    type: body.type ?? "http",
    enabled: body.enabled ?? true,
    url: (body.url ?? "").trim(),
    method: body.method ?? "GET",
    target: (body.target ?? "").trim(),
    dns_record_type: body.dns_record_type ?? (body.type === "dns" ? "A" : ""),
    dns_expected: body.dns_expected ?? [],
    tls_warning_days: body.tls_warning_days ?? (body.type === "tls" ? 14 : 0),
    headers: body.headers ?? {},
    body: body.body ?? "",
    expected_status: body.expected_status?.length ? body.expected_status : [200],
    assertion_type: body.assertion_type ?? "none",
    assertion_path: body.assertion_path ?? "",
    assertion_value: body.assertion_value ?? "",
    timeout_ms: body.timeout_ms ?? 10000,
    interval_seconds: body.interval_seconds ?? 300,
    locations,
    created_by_email: existing?.created_by_email ?? "admin@openlog.local",
    updated_by_email: "admin@openlog.local",
    created_at: existing?.created_at ?? formatTs(now),
    updated_at: formatTs(now),
    status:
      existing?.status ??
      locations.map((location) => ({
        location,
        next_run_at: formatTs(now),
        last_run_at: null,
        last_success: null,
        last_status_code: 0,
        last_duration_ms: 0,
        last_error_kind: "",
        last_error: "",
      })),
  };
}

export const syntheticHandlers = [
  http.get(`${API}/synthetics/checks`, authed(({ request }) => {
    const withSummary = new URL(request.url).searchParams.get("summary") !== "false";
    return HttpResponse.json({
      checks: checks.map((c) => ({ ...c, summary: withSummary ? summary(c) : null })),
      locations: ["local"],
    });
  })),

  http.post(`${API}/synthetics/checks`, async ({ request }) => {
    const denied = writer(request);
    if (denied) return denied;
    const body = (await request.json().catch(() => ({}))) as Partial<SyntheticCheckInput>;
    const err = validate(body);
    if (err) return fail("invalid_argument", err);
    const c = stored(body as SyntheticCheckInput);
    checks = [...checks, c];
    return HttpResponse.json(c, { status: 201 });
  }),

  http.get(`${API}/synthetics/checks/:id`, authed(({ params }) => {
    const c = checks.find((x) => x.id === params.id);
    return c ? HttpResponse.json(c) : fail("not_found", "synthetic check not found");
  })),

  http.put(`${API}/synthetics/checks/:id`, async ({ request, params }) => {
    const denied = writer(request);
    if (denied) return denied;
    const existing = checks.find((x) => x.id === params.id);
    if (!existing) return fail("not_found", "synthetic check not found");
    const body = (await request.json().catch(() => ({}))) as Partial<SyntheticCheckInput>;
    const err = validate(body);
    if (err) return fail("invalid_argument", err);
    const c = stored(body as SyntheticCheckInput, existing);
    checks = checks.map((x) => (x.id === c.id ? c : x));
    return HttpResponse.json(c);
  }),

  http.delete(`${API}/synthetics/checks/:id`, ({ request, params }) => {
    const denied = writer(request);
    if (denied) return denied;
    if (!checks.some((x) => x.id === params.id)) return fail("not_found", "synthetic check not found");
    checks = checks.filter((x) => x.id !== params.id);
    return new HttpResponse(null, { status: 204 });
  }),

  http.get(`${API}/synthetics/checks/:id/results`, authed(({ params }) => {
    const c = checks.find((x) => x.id === params.id);
    if (!c) return fail("not_found", "synthetic check not found");
    return HttpResponse.json({ check: c, summary: summary(c), failures: failures(c) });
  })),
];
