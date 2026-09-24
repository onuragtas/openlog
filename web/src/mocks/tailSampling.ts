// MSW handlers for the tail sampling policy (docs/contracts/apm.md §4.2, docs/operations/tail-sampling.md).
// One policy is kept in memory: enough for the settings screen to read it, save an edit and preview what a
// policy would keep. The server owns the real validation; the mock only mirrors the rules the UI relies on.
import { http, HttpResponse } from "msw";
import type { TailSamplingPolicy, TailSamplingPolicyState, TailSamplingPreview } from "@/api/tailSampling";
import { authenticate } from "./account";
import { formatTs } from "./fixtures";

const API = "*/api/v1";

type Code = "invalid_argument" | "permission_denied" | "failed_precondition";
const STATUS: Record<Code, number> = { invalid_argument: 400, permission_denied: 403, failed_precondition: 409 };
const fail = (code: Code, message: string) => HttpResponse.json({ error: { code, message } }, { status: STATUS[code] });

function seed(): TailSamplingPolicyState {
  return {
    enabled: true,
    is_default: false,
    version: 3,
    updated_at: formatTs(Date.now() - 2 * 86_400_000),
    updated_by_email: "admin@openlog.local",
    policy: {
      enabled: true,
      baseline_ratio: 0.1,
      max_spans_per_second: 2000,
      rules: [
        { name: "every error", type: "error" },
        { name: "slow orders", type: "latency", threshold_ms: 500, service: "orders" },
        { name: "checkout path", type: "service", services: ["orders", "catalog"] },
        { name: "health checks", type: "route", route: "/health", ratio: 0.01 },
      ],
    },
  };
}

let state = seed();

/** Restores the seed policy (tests). */
export function resetMockTailSampling(): void {
  state = seed();
}

/** Signed-in admin or owner, like the server (API keys may not change the policy). */
function gateWrite(request: Request): Response | null {
  const ctx = authenticate(request);
  if (ctx instanceof Response) return ctx;
  if (ctx.kind !== "session") return fail("permission_denied", "this operation requires a signed-in user; API keys are read-only");
  if (ctx.role !== "admin" && ctx.role !== "owner") return fail("permission_denied", `your role (${ctx.role}) does not allow this operation`);
  return null;
}

async function body<T>(request: Request): Promise<Partial<T>> {
  try {
    return ((await request.json()) as Partial<T>) ?? {};
  } catch {
    return {};
  }
}

/** A rule's share of the traffic, deterministic so the preview does not flicker between runs. */
function ruleShare(name: string): number {
  let h = 0;
  for (const c of name) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return 0.02 + (h % 18) / 100;
}

function preview(policy: TailSamplingPolicy, windowMinutes: number): TailSamplingPreview {
  const rules = (policy.rules ?? []).map((r) => {
    const matched = ruleShare(r.name);
    return { name: r.name, matched_trace_ratio: matched, kept_trace_ratio: matched * (r.ratio ?? 1) };
  });
  const matchedAll = rules.reduce((a, r) => a + r.matched_trace_ratio, 0);
  const baselineMatched = Math.max(0, 1 - matchedAll);
  const keptByRules = rules.reduce((a, r) => a + r.kept_trace_ratio, 0);
  const kept = policy.enabled ? keptByRules + baselineMatched * policy.baseline_ratio : 1;
  return {
    window_minutes: windowMinutes,
    traces_examined: 12_480,
    sampled_fraction: 1,
    estimated_traces: 12_480,
    kept_trace_ratio: Math.min(1, kept),
    // Kept traces skew long: an error or slow trace carries more spans than a healthy one.
    kept_span_ratio: Math.min(1, kept * 1.35),
    rules: [...rules, { name: "baseline", matched_trace_ratio: baselineMatched, kept_trace_ratio: baselineMatched * policy.baseline_ratio }],
  };
}

export const tailSamplingHandlers = [
  http.get(`${API}/apm/sampling`, ({ request }) => {
    const ctx = authenticate(request);
    return ctx instanceof Response ? ctx : HttpResponse.json(state);
  }),

  http.put(`${API}/apm/sampling`, async ({ request }) => {
    const refused = gateWrite(request);
    if (refused) return refused;
    const b = await body<{ policy: TailSamplingPolicy; version: number }>(request);
    if (!b.policy) return fail("invalid_argument", "policy is required");
    // Optimistic concurrency, like the server: a stale version means someone else saved in between.
    if ((b.version ?? 0) !== state.version) return fail("failed_precondition", `the policy was changed by someone else (stored version ${state.version})`);
    const p = b.policy;
    if (!(p.baseline_ratio >= 0 && p.baseline_ratio <= 1)) return fail("invalid_argument", "baseline_ratio must be between 0 and 1");
    if (p.max_spans_per_second < 0) return fail("invalid_argument", "max_spans_per_second must not be negative");
    for (const r of p.rules ?? []) {
      if (!r.name.trim()) return fail("invalid_argument", "every rule needs a name");
      if (r.type === "latency" && !(r.threshold_ms && r.threshold_ms > 0)) return fail("invalid_argument", `rule "${r.name}": threshold_ms must be greater than 0`);
      if (r.type === "service" && (r.services ?? []).length === 0) return fail("invalid_argument", `rule "${r.name}": name at least one service`);
    }
    state = {
      ...state,
      policy: p,
      is_default: false,
      version: state.version + 1,
      updated_at: formatTs(Date.now()),
      updated_by_email: "admin@openlog.local",
    };
    return HttpResponse.json(state);
  }),

  http.post(`${API}/apm/sampling/preview`, async ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    const b = await body<{ policy: TailSamplingPolicy; window_minutes: number }>(request);
    if (!b.policy) return fail("invalid_argument", "policy is required");
    const w = b.window_minutes ?? 60;
    if (!Number.isInteger(w) || w < 1 || w > 1440) return fail("invalid_argument", "window_minutes must be between 1 and 1440");
    return HttpResponse.json(preview(b.policy, w));
  }),
];
