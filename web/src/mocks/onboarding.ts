// MSW handlers for GET /api/v1/onboarding ("Add data" page) and hosts "reported" by a mocked install, so the
// verification step can succeed in dev:mock and Playwright (window.__openlogMock.reportHost, see mocks/browser.ts).
import { http, HttpResponse } from "msw";
import type { Host } from "@/api/types";
import { authenticate } from "./account";
import { formatTs } from "./fixtures";

const API = "*/api/v1";

interface Reported {
  id: string;
  name: string;
  at: number;
}

let reported: Reported[] = [];

/** 32 hex characters derived from the name (stable across reloads). */
function hostIdFor(name: string): string {
  let h1 = 0x811c9dc5;
  let h2 = 0x01000193;
  for (let i = 0; i < name.length; i++) {
    h1 = Math.imul(h1 ^ name.charCodeAt(i), 16777619) >>> 0;
    h2 = Math.imul(h2 ^ name.charCodeAt(name.length - 1 - i), 2246822519) >>> 0;
  }
  const hex = (n: number) => (n >>> 0).toString(16).padStart(8, "0");
  return `${hex(h1)}${hex(h2)}${hex(h1 ^ 0xa5a5a5a5)}${hex(h2 ^ 0x5a5a5a5a)}`;
}

/** Emulates a freshly installed infra agent: the host shows up in GET /api/v1/hosts. Returns its host id. */
export function reportMockHost(name: string): string {
  const id = hostIdFor(name);
  reported = [...reported.filter((r) => r.id !== id), { id, name, at: Date.now() }];
  return id;
}

export function resetMockOnboarding(): void {
  reported = [];
}

/** Hosts added by reportMockHost, in the GET /api/v1/hosts shape. */
export function onboardingHosts(now: number): Host[] {
  return reported.map((r) => ({
    host_id: r.id,
    host_name: r.name,
    os_description: "Ubuntu 24.04 LTS",
    arch: "amd64",
    agent_version: "0.9.1",
    last_seen: formatTs(Math.max(r.at, now - 5_000)),
    resource_attributes: { "host.id": r.id, "host.name": r.name, "os.type": "linux", "host.arch": "amd64" },
  }));
}

export const onboardingHandlers = [
  http.get(`${API}/onboarding`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    const url = new URL(request.url);
    const host = url.hostname.includes(":") ? `[${url.hostname}]` : url.hostname;
    const session = ctx.kind === "session";
    const admin = ctx.role === "admin" || ctx.role === "owner";
    return HttpResponse.json({
      ui_url: { url: url.origin, source: "derived_request" },
      otlp_http: { url: `http://${host}:4318`, source: "derived_request" },
      otlp_grpc: { url: `http://${host}:4317`, source: "derived_request" },
      server_version: "0.9.1",
      agent_version: "0.9.1",
      release_channel: "stable",
      cors_enabled: false,
      cors_allowed_origins: [],
      auth_mode: "postgres",
      organization: { id: ctx.org.id, tenant_id: ctx.org.tenant_id, name: ctx.org.name },
      role: ctx.role,
      features: {
        license_keys: true,
        can_create_license_keys: session && admin,
        can_list_license_keys: session && ctx.role !== "viewer",
        fleet_php_install: true,
        tail_sampling: false,
      },
    });
  }),
];
