// MSW handlers for the cloud connection endpoints (internal/api/cloudconnect.go, docs/contracts/api.md
// "Cloud connections"): a healthy AWS account over two regions and an Azure subscription whose last poll
// failed, with in-memory CRUD. Credentials are accepted but never returned, like the real API.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type { CloudConnection, CloudConnectionInput, CloudRun, CloudScopeStatus } from "@/api/cloud";
import type { Role } from "@/api/roles";
import { authenticate } from "./account";
import { formatTs } from "./fixtures";

const API = "*/api/v1/cloud";
const RANK: Record<Role, number> = { viewer: 1, member: 2, admin: 3, owner: 4 };

type Code = "invalid_argument" | "permission_denied" | "not_found" | "failed_precondition";
const STATUS: Record<Code, number> = { invalid_argument: 400, permission_denied: 403, not_found: 404, failed_precondition: 409 };
const fail = (code: Code, message: string) => HttpResponse.json({ error: { code, message } }, { status: STATUS[code] });

function authed(resolver: HttpResponseResolver): HttpResponseResolver {
  return (info) => {
    const ctx = authenticate(info.request);
    if (ctx instanceof Response) return ctx;
    return resolver(info);
  };
}

/** Changes need an admin or owner (a connection stores cloud credentials). */
function manager(request: Request): Response | null {
  const ctx = authenticate(request);
  if (ctx instanceof Response) return ctx;
  if (RANK[ctx.role] < RANK.admin) return fail("permission_denied", `your role (${ctx.role}) does not allow this operation`);
  return null;
}

const CONNECTION_IDS = {
  aws: "53000000-0000-4000-8000-000000000001",
  azure: "53000000-0000-4000-8000-000000000002",
};

/** Test helper: the seeded connections (one healthy, one whose last poll failed). */
export const MOCK_CLOUD_IDS = CONNECTION_IDS;

/** The provider catalog, mirroring internal/cloudconnect's built-in services. */
const CATALOG = {
  providers: [
    {
      id: "aws" as const,
      scope_label: "region",
      credentials: [
        { key: "access_key_id", required: true, secret: false },
        { key: "secret_access_key", required: true, secret: true },
        { key: "session_token", required: false, secret: true },
      ],
      services: [
        { id: "rds", metrics: 8 },
        { id: "s3", metrics: 2 },
        { id: "lambda", metrics: 5 },
        { id: "sqs", metrics: 4 },
        { id: "dynamodb", metrics: 4 },
        { id: "elb", metrics: 4 },
        { id: "elasticache", metrics: 5 },
      ],
    },
    {
      id: "azure" as const,
      scope_label: "subscription",
      credentials: [
        { key: "tenant_id", required: true, secret: false },
        { key: "client_id", required: true, secret: false },
        { key: "client_secret", required: true, secret: true },
      ],
      services: [
        { id: "azure_sql", metrics: 6 },
        { id: "azure_storage", metrics: 4 },
        { id: "azure_vm", metrics: 6 },
        { id: "azure_functions", metrics: 4 },
        { id: "azure_cosmos", metrics: 3 },
      ],
    },
    {
      id: "gcp" as const,
      scope_label: "project",
      credentials: [
        { key: "client_email", required: true, secret: false },
        { key: "private_key", required: true, secret: true },
        { key: "token_uri", required: false, secret: false },
      ],
      services: [
        { id: "cloud_sql", metrics: 4 },
        { id: "gcs", metrics: 3 },
        { id: "cloud_functions", metrics: 3 },
        { id: "pubsub", metrics: 2 },
        { id: "gce", metrics: 3 },
      ],
    },
  ],
  secrets_configured: true,
  test_supported: true,
};

function scope(name: string, status: "ok" | "partial" | "error", metrics: number, error = ""): CloudScopeStatus {
  const now = Date.now();
  return {
    scope: name,
    next_run_at: formatTs(now + 120_000),
    last_run_at: formatTs(now - 180_000),
    last_status: status,
    last_error: error,
    last_metrics: metrics,
    last_api_calls: 6,
    last_duration_ms: 1830.4,
    consecutive_errors: status === "error" ? 3 : 0,
  };
}

function seed(): CloudConnection[] {
  const now = Date.now();
  const base = {
    created_by_email: "admin@openlog.local",
    updated_by_email: "admin@openlog.local",
    created_at: formatTs(now - 20 * 86_400_000),
    updated_at: formatTs(now - 86_400_000),
    credentials_set: true,
    credentials_key_id: "a1b2c3d4",
    ingest_mode: "poll" as const,
    poll_interval_seconds: 300,
    max_metrics_per_poll: 5000,
    max_api_calls_per_poll: 200,
  };
  return [
    {
      id: CONNECTION_IDS.aws,
      name: "Production AWS",
      provider: "aws",
      enabled: true,
      scopes: ["eu-central-1", "us-east-1"],
      services: ["rds", "lambda"],
      ...base,
      status: [scope("eu-central-1", "ok", 412), scope("us-east-1", "ok", 88)],
    },
    {
      id: CONNECTION_IDS.azure,
      name: "Azure production",
      provider: "azure",
      enabled: true,
      scopes: ["00000000-1111-2222-3333-444444444444"],
      services: ["azure_sql"],
      ...base,
      status: [
        scope("00000000-1111-2222-3333-444444444444", "error", 0,
          "azure: the credentials are not allowed to read these metrics"),
      ],
    },
  ];
}

function seedRuns(): Record<string, CloudRun[]> {
  const now = Date.now();
  return {
    [CONNECTION_IDS.aws]: [
      {
        id: 3, scope: "eu-central-1", started_at: formatTs(now - 180_000), duration_ms: 1830.4,
        status: "ok", metrics: 412, api_calls: 6, throttled: 0, error: "",
        services: [
          { service: "rds", metrics: 330, error: "" },
          { service: "lambda", metrics: 82, error: "" },
        ],
      },
      {
        id: 2, scope: "us-east-1", started_at: formatTs(now - 200_000), duration_ms: 940.2,
        status: "partial", metrics: 88, api_calls: 4, throttled: 1,
        error: "the metric cap of this poll was reached; raise max_metrics_per_poll or collect fewer services",
        services: [
          { service: "rds", metrics: 88, error: "" },
          { service: "lambda", metrics: 0, error: "the metric cap of this poll is reached" },
        ],
      },
    ],
    [CONNECTION_IDS.azure]: [
      {
        id: 1, scope: "00000000-1111-2222-3333-444444444444", started_at: formatTs(now - 180_000),
        duration_ms: 320.1, status: "error", metrics: 0, api_calls: 1, throttled: 0,
        error: "azure: the credentials are not allowed to read these metrics",
        services: [],
      },
    ],
  };
}

let connections: CloudConnection[] = seed();
let runs: Record<string, CloudRun[]> = seedRuns();
let nextId = 3;
/** Test helper: the credentials the last write stored (the API never returns them). */
let storedCredentials: Record<string, Record<string, string>> = {};

/** Test helper: forget created connections. */
export function resetMockCloud(): void {
  connections = seed();
  runs = seedRuns();
  storedCredentials = {};
  nextId = 3;
}

/** Test helper: what was stored for a connection, to assert a secret was sent but never returned. */
export function mockCloudCredentials(id: string): Record<string, string> | undefined {
  return storedCredentials[id];
}

const SERVICE_IDS = new Set(CATALOG.providers.flatMap((p) => p.services.map((s) => s.id)));

function validate(body: Partial<CloudConnectionInput>): string | null {
  if (!body.name || typeof body.name !== "string" || body.name.trim() === "") return "name: must be 1-200 characters";
  const provider = CATALOG.providers.find((p) => p.id === body.provider);
  if (!provider) return "provider: must be one of aws, azure, gcp";
  if (!body.scopes || body.scopes.length === 0) return `scopes: at least one ${provider.scope_label} is required`;
  for (const s of body.scopes) {
    if (!/^[A-Za-z0-9._-]+$/.test(s)) return "scopes: must contain only letters, digits, '-', '_' and '.'";
  }
  if (!body.services || body.services.length === 0) return "services: at least one service is required";
  for (const s of body.services) {
    if (!SERVICE_IDS.has(s)) return `services: unknown service "${s}"`;
    if (!provider.services.some((p) => p.id === s)) return `services: "${s}" is not a ${provider.id} service`;
  }
  const interval = body.poll_interval_seconds ?? 300;
  if (interval < 60 || interval > 86400) return "poll_interval_seconds: must be between 60 and 86400 seconds";
  const maxMetrics = body.max_metrics_per_poll ?? 5000;
  if (maxMetrics < 100 || maxMetrics > 200000) return "max_metrics_per_poll: must be between 100 and 200000";
  const maxCalls = body.max_api_calls_per_poll ?? 200;
  if (maxCalls < 1 || maxCalls > 5000) return "max_api_calls_per_poll: must be between 1 and 5000";
  if (body.credentials) {
    const allowed = new Set(provider.credentials.map((c) => c.key));
    for (const key of Object.keys(body.credentials)) {
      if (!allowed.has(key)) return `credentials.${key}: is not used by a ${provider.id} connection`;
    }
    for (const c of provider.credentials) {
      if (c.required && !(body.credentials as Record<string, string>)[c.key]) {
        return `credentials.${c.key}: required for a ${provider.id} connection`;
      }
    }
  }
  return null;
}

function stored(body: CloudConnectionInput, existing?: CloudConnection): CloudConnection {
  const now = Date.now();
  const id = existing?.id ?? `53000000-0000-4000-8000-${String(nextId++).padStart(12, "0")}`;
  const creds = body.credentials as Record<string, string> | null | undefined;
  if (creds && Object.keys(creds).length > 0) storedCredentials[id] = creds;
  return {
    id,
    name: body.name.trim(),
    provider: body.provider,
    ingest_mode: body.ingest_mode ?? "poll",
    enabled: body.enabled ?? true,
    scopes: body.scopes,
    services: body.services,
    poll_interval_seconds: body.poll_interval_seconds ?? 300,
    max_metrics_per_poll: body.max_metrics_per_poll ?? 5000,
    max_api_calls_per_poll: body.max_api_calls_per_poll ?? 200,
    credentials_set: !!storedCredentials[id],
    credentials_key_id: storedCredentials[id] ? "a1b2c3d4" : "",
    created_by_email: existing?.created_by_email ?? "admin@openlog.local",
    updated_by_email: "admin@openlog.local",
    created_at: existing?.created_at ?? formatTs(now),
    updated_at: formatTs(now),
    status:
      existing?.status ??
      body.scopes.map((s) => ({
        scope: s,
        next_run_at: formatTs(now),
        last_run_at: null,
        last_status: "",
        last_error: "",
        last_metrics: 0,
        last_api_calls: 0,
        last_duration_ms: 0,
        consecutive_errors: 0,
      })),
  };
}

export const cloudHandlers = [
  http.get(`${API}/providers`, authed(() => HttpResponse.json(CATALOG))),

  http.get(`${API}/connections`, authed(() =>
    HttpResponse.json({ connections, secrets_configured: CATALOG.secrets_configured, test_supported: CATALOG.test_supported }))),

  // Before /connections/:id, so "test" is not read as an id.
  http.post(`${API}/connections/test`, async ({ request }) => {
    const denied = manager(request);
    if (denied) return denied;
    const body = (await request.json().catch(() => ({}))) as Record<string, unknown>;
    const id = body.connection_id as string | undefined;
    const creds = body.credentials as Record<string, string> | undefined;
    if (id && !connections.some((c) => c.id === id)) return fail("not_found", "cloud connection not found");
    if (!creds && id && !storedCredentials[id]) {
      return fail("failed_precondition", "the connection has no stored credentials; save them before testing or polling");
    }
    // The mock rejects a key that looks like a placeholder, so the form's failure path is reachable.
    const secret = creds?.secret_access_key ?? creds?.client_secret ?? creds?.private_key ?? "stored";
    if (secret === "wrong") {
      return HttpResponse.json({ ok: false, error: "aws: the credentials were rejected" });
    }
    return HttpResponse.json({ ok: true, error: "" });
  }),

  http.post(`${API}/connections`, async ({ request }) => {
    const denied = manager(request);
    if (denied) return denied;
    const body = (await request.json().catch(() => ({}))) as Partial<CloudConnectionInput>;
    const err = validate(body);
    if (err) return fail("invalid_argument", err);
    const c = stored(body as CloudConnectionInput);
    connections = [...connections, c];
    return HttpResponse.json(c, { status: 201 });
  }),

  http.get(`${API}/connections/:id`, authed(({ params }) => {
    const c = connections.find((x) => x.id === params.id);
    return c ? HttpResponse.json(c) : fail("not_found", "cloud connection not found");
  })),

  http.get(`${API}/connections/:id/runs`, authed(({ params, request }) => {
    const c = connections.find((x) => x.id === params.id);
    if (!c) return fail("not_found", "cloud connection not found");
    const scopeFilter = new URL(request.url).searchParams.get("scope");
    const list = (runs[c.id] ?? []).filter((r) => !scopeFilter || r.scope === scopeFilter);
    return HttpResponse.json({ connection: c, runs: list });
  })),

  http.put(`${API}/connections/:id`, async ({ request, params }) => {
    const denied = manager(request);
    if (denied) return denied;
    const existing = connections.find((x) => x.id === params.id);
    if (!existing) return fail("not_found", "cloud connection not found");
    const body = (await request.json().catch(() => ({}))) as Partial<CloudConnectionInput>;
    const err = validate(body);
    if (err) return fail("invalid_argument", err);
    const c = stored(body as CloudConnectionInput, existing);
    connections = connections.map((x) => (x.id === c.id ? c : x));
    return HttpResponse.json(c);
  }),

  http.delete(`${API}/connections/:id`, ({ request, params }) => {
    const denied = manager(request);
    if (denied) return denied;
    if (!connections.some((x) => x.id === params.id)) return fail("not_found", "cloud connection not found");
    connections = connections.filter((x) => x.id !== params.id);
    return new HttpResponse(null, { status: 204 });
  }),
];
