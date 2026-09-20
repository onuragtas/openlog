// MSW handlers for the job monitoring endpoints (internal/api/jobs.go, docs/contracts/api.md "Job
// monitoring"): a nightly backup that reports on a cron schedule and a heartbeat that is currently late,
// with in-memory CRUD and a ping endpoint that behaves like the real one.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type { JobMonitor, JobMonitorInput, JobRun } from "@/api/jobs";
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

/** Writes need a signed-in member or higher (api.md "Job monitoring"). */
function writer(request: Request): Response | null {
  const ctx = authenticate(request);
  if (ctx instanceof Response) return ctx;
  if (ctx.kind !== "session") return fail("permission_denied", "this operation requires a signed-in user; API keys are read-only");
  if (ctx.role === "viewer") return fail("permission_denied", `your role (${ctx.role}) does not allow this operation`);
  return null;
}

const MONITOR_IDS = {
  ok: "53000000-0000-4000-8000-000000000001",
  late: "53000000-0000-4000-8000-000000000002",
};

/** Test helper: the seeded monitors (one healthy cron job, one late heartbeat). */
export const MOCK_JOB_IDS = MONITOR_IDS;

let nextId = 3;
let monitors: JobMonitor[] = [];

function seed(): JobMonitor[] {
  const now = Date.now();
  const base = {
    description: "",
    created_by_email: "admin@openlog.local",
    updated_by_email: "admin@openlog.local",
    created_at: formatTs(now - 30 * 86_400_000),
    updated_at: formatTs(now - 86_400_000),
  };
  return [
    {
      ...base,
      id: MONITOR_IDS.ok,
      name: "Nightly backup",
      description: "pg_dump of the shop database to object storage",
      kind: "cron",
      cron: "0 3 * * *",
      time_zone: "Europe/Istanbul",
      interval_seconds: 0,
      grace_seconds: 900,
      enabled: true,
      tags: ["backup"],
      ping_url: "https://openlog.example.com/api/v1/jobs/ping/olj_a2b3c4d5e6f7g2h3i4j5k6l7m2n3o4p5",
      state: {
        status: "success",
        last_ping_at: formatTs(now - 6 * 3_600_000),
        last_started_at: formatTs(now - 6 * 3_600_000 - 148_000),
        last_finished_at: formatTs(now - 6 * 3_600_000),
        last_duration_ms: 148_000,
        last_exit_code: 0,
        last_message: "42 GB written",
        expected_at: formatTs(now + 18 * 3_600_000),
        consecutive_failures: 0,
        late: false,
      },
      summary: { runs: 7, failures: 0, missed: 0, avg_ms: 151_000, max_ms: 162_000, last_at: formatTs(now - 6 * 3_600_000) },
    },
    {
      ...base,
      id: MONITOR_IDS.late,
      name: "Invoice sync",
      kind: "interval",
      cron: "",
      time_zone: "",
      interval_seconds: 3600,
      grace_seconds: 300,
      enabled: true,
      tags: ["billing"],
      ping_url: "https://openlog.example.com/api/v1/jobs/ping/olj_z2y3x4w5v6u7t2s3r4q5p6o7n2m3l4k5",
      state: {
        status: "missed",
        last_ping_at: formatTs(now - 3 * 3_600_000),
        last_started_at: null,
        last_finished_at: formatTs(now - 3 * 3_600_000),
        last_duration_ms: 0,
        last_exit_code: 0,
        last_message: "",
        expected_at: formatTs(now - 2 * 3_600_000),
        consecutive_failures: 2,
        late: true,
      },
      summary: { runs: 20, failures: 0, missed: 2, avg_ms: 4200, max_ms: 9100, last_at: formatTs(now - 3 * 3_600_000) },
    },
  ];
}

monitors = seed();

/** Test helper: back to the seeded monitors. */
export function resetMockJobs(): void {
  monitors = seed();
  nextId = 3;
}

function runs(monitor: JobMonitor): JobRun[] {
  const now = Date.now();
  const step = monitor.kind === "cron" ? 86_400_000 : 3_600_000;
  const out: JobRun[] = [];
  for (let i = 0; i < 8; i++) {
    const at = now - (i + 1) * step;
    const missed = monitor.id === MONITOR_IDS.late && i === 0;
    out.push({
      timestamp: formatTs(at),
      status: missed ? "missed" : "success",
      started_at: missed ? null : formatTs(at - 148_000),
      duration_ms: missed ? 0 : 148_000 - i * 1200,
      exit_code: 0,
      late_seconds: missed ? 7200 : 12,
      message: missed ? "" : "42 GB written",
      source: "203.0.113.10",
    });
  }
  return out;
}

function summaryOf(monitor: JobMonitor) {
  return monitor.summary ?? { runs: 0, failures: 0, missed: 0, avg_ms: null, max_ms: null, last_at: null };
}

function validate(body: Partial<JobMonitorInput>): string | null {
  if (!body.name || typeof body.name !== "string" || body.name.trim() === "") return "name: must be 1-200 characters";
  const kind = body.kind ?? "cron";
  if (kind === "cron") {
    const cron = (body.cron ?? "").trim();
    if (!cron) return "cron: required";
    if (!cron.startsWith("@") && cron.split(/\s+/).length !== 5) return "cron: must have 5 fields (minute hour day month weekday)";
  } else {
    const seconds = body.interval_seconds ?? 0;
    if (seconds < 60 || seconds > 7_776_000) return "interval_seconds: must be between 60 and 7776000 seconds";
  }
  const grace = body.grace_seconds ?? 300;
  if (grace < 0 || grace > 86_400) return "grace_seconds: must be between 0 and 86400 seconds";
  return null;
}

function stored(body: JobMonitorInput, existing?: JobMonitor): JobMonitor {
  const now = Date.now();
  const id = existing?.id ?? `53000000-0000-4000-8000-${String(nextId++).padStart(12, "0")}`;
  const kind = body.kind ?? "cron";
  return {
    id,
    name: body.name.trim(),
    description: body.description ?? "",
    kind,
    cron: kind === "cron" ? (body.cron ?? "").trim() : "",
    time_zone: kind === "cron" ? (body.time_zone ?? "") : "",
    interval_seconds: kind === "interval" ? (body.interval_seconds ?? 3600) : 0,
    grace_seconds: body.grace_seconds ?? 300,
    enabled: body.enabled ?? true,
    tags: body.tags ?? [],
    ping_url: existing?.ping_url ?? `https://openlog.example.com/api/v1/jobs/ping/olj_${id.replace(/-/g, "").slice(0, 32)}`,
    created_by_email: existing?.created_by_email ?? "admin@openlog.local",
    updated_by_email: "admin@openlog.local",
    created_at: existing?.created_at ?? formatTs(now),
    updated_at: formatTs(now),
    state: existing?.state ?? {
      status: "",
      last_ping_at: null,
      last_started_at: null,
      last_finished_at: null,
      last_duration_ms: 0,
      last_exit_code: 0,
      last_message: "",
      expected_at: formatTs(now + 3_600_000),
      consecutive_failures: 0,
      late: false,
    },
  };
}

export const jobHandlers = [
  http.get(`${API}/jobs/monitors`, authed(({ request }) => {
    const withSummary = new URL(request.url).searchParams.get("summary") !== "false";
    return HttpResponse.json({ monitors: monitors.map((m) => (withSummary ? m : { ...m, summary: undefined })) });
  })),
  http.post(`${API}/jobs/monitors`, async ({ request }) => {
    const denied = writer(request);
    if (denied) return denied;
    const body = (await request.json()) as JobMonitorInput;
    const invalid = validate(body);
    if (invalid) return fail("invalid_argument", invalid);
    const monitor = stored(body);
    monitors = [...monitors, monitor];
    return HttpResponse.json(monitor, { status: 201 });
  }),
  http.get(`${API}/jobs/monitors/:id`, authed(({ params }) => {
    const monitor = monitors.find((m) => m.id === params.id);
    return monitor ? HttpResponse.json(monitor) : fail("not_found", "job monitor not found");
  })),
  http.put(`${API}/jobs/monitors/:id`, async ({ request, params }) => {
    const denied = writer(request);
    if (denied) return denied;
    const existing = monitors.find((m) => m.id === params.id);
    if (!existing) return fail("not_found", "job monitor not found");
    const body = (await request.json()) as JobMonitorInput;
    const invalid = validate(body);
    if (invalid) return fail("invalid_argument", invalid);
    const monitor = stored(body, existing);
    monitors = monitors.map((m) => (m.id === monitor.id ? monitor : m));
    return HttpResponse.json(monitor);
  }),
  http.delete(`${API}/jobs/monitors/:id`, ({ request, params }) => {
    const denied = writer(request);
    if (denied) return denied;
    if (!monitors.some((m) => m.id === params.id)) return fail("not_found", "job monitor not found");
    monitors = monitors.filter((m) => m.id !== params.id);
    return new HttpResponse(null, { status: 204 });
  }),
  http.post(`${API}/jobs/monitors/:id/rotate`, ({ request, params }) => {
    const denied = writer(request);
    if (denied) return denied;
    const existing = monitors.find((m) => m.id === params.id);
    if (!existing) return fail("not_found", "job monitor not found");
    const monitor = { ...existing, ping_url: `${existing.ping_url.slice(0, existing.ping_url.lastIndexOf("/") + 1)}olj_${"r".repeat(32)}` };
    monitors = monitors.map((m) => (m.id === monitor.id ? monitor : m));
    return HttpResponse.json(monitor);
  }),
  http.get(`${API}/jobs/monitors/:id/runs`, authed(({ params }) => {
    const monitor = monitors.find((m) => m.id === params.id);
    if (!monitor) return fail("not_found", "job monitor not found");
    return HttpResponse.json({ monitor, runs: runs(monitor), summary: summaryOf(monitor) });
  })),
];
