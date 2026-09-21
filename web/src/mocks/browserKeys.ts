// MSW handlers for the browser keys of the RUM SDK (internal/api/browserkeys.go, docs/contracts/rum.md §3):
// one key of the mock shop, with in-memory create, update and revoke. Managing a key needs a signed-in admin
// (ActManageBrowserKeys is UserOnly), so an API key is refused here the way the server refuses it.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type { BrowserKey, BrowserKeyInput } from "@/api/browserKeys";
import { authenticate } from "./account";
import { formatTs } from "./fixtures";

const API = "*/api/v1";

const fail = (code: string, message: string, status: number) => HttpResponse.json({ error: { code, message } }, { status });

function authed(resolver: HttpResponseResolver): HttpResponseResolver {
  return (info) => {
    const ctx = authenticate(info.request);
    if (ctx instanceof Response) return ctx;
    return resolver(info);
  };
}

/** Writes need a signed-in admin or owner; a browser key decides what a public page may write under. */
function manager(request: Request): Response | null {
  const ctx = authenticate(request);
  if (ctx instanceof Response) return ctx;
  if (ctx.kind !== "session") return fail("permission_denied", "this operation requires a signed-in user; API keys cannot manage browser keys", 403);
  if (ctx.role !== "admin" && ctx.role !== "owner") return fail("permission_denied", `your role (${ctx.role}) does not allow this operation`, 403);
  return null;
}

export const MOCK_BROWSER_KEY_ID = "6a000000-0000-4000-8000-000000000001";

function seed(): BrowserKey[] {
  const now = Date.now();
  return [
    {
      id: MOCK_BROWSER_KEY_ID,
      name: "Shop web",
      prefix: "olb_1a2b3c4d",
      key: "olb_1a2b3c4d5e6f708192a3b4c5d6e7f809",
      service_name: "shop-web",
      environment: "production",
      kind: "browser",
      origins: ["https://shop.example.com", "https://*.example.com"],
      app_ids: [],
      rate_limit_per_minute: 6000,
      sample_rate: 1,
      created_by_email: "owner@example.com",
      created_at: formatTs(now - 30 * 86_400_000),
      updated_at: formatTs(now - 30 * 86_400_000),
      last_used_at: formatTs(now - 45_000),
      revoked_at: "",
    },
  ];
}

let keys = seed();

/** Test helper: back to the seeded key. */
export function resetMockBrowserKeys() {
  keys = seed();
}

function validate(input: BrowserKeyInput): Response | null {
  if (input.name.trim() === "") return fail("invalid_argument", "name is required", 400);
  if (input.service_name.trim() === "") return fail("invalid_argument", "service_name is required", 400);
  // Exactly the allowlist of the kind, and only that one: a key carrying both would have a scope that
  // depends on which check ran first, so the server refuses it and so does this (§3.6).
  const kind = input.kind ?? "browser";
  if (kind === "mobile") {
    if (!input.app_ids?.length) return fail("invalid_argument", "at least one application id is required", 400);
    if (input.app_ids.includes("*")) return fail("invalid_argument", `"*" is not a valid application id`, 400);
    if (input.origins?.length) return fail("invalid_argument", "origins belong to a browser key", 400);
  } else {
    // An empty allowlist accepts data from any page on the internet, so it is refused rather than defaulted (§3.2).
    if (!input.origins?.length) return fail("invalid_argument", "at least one origin is required", 400);
    if (input.origins.includes("*")) return fail("invalid_argument", `"*" is not an allowed origin`, 400);
    if (input.app_ids?.length) return fail("invalid_argument", "app_ids belong to a mobile key", 400);
  }
  if (input.sample_rate <= 0 || input.sample_rate > 1) return fail("invalid_argument", "sample_rate must be between 0 and 1", 400);
  return null;
}

export const browserKeyHandlers = [
  http.get(`${API}/browser-keys`, authed(() => HttpResponse.json({ browser_keys: keys }))),

  http.post(`${API}/browser-keys`, async ({ request }) => {
    const denied = manager(request);
    if (denied) return denied;
    const input = (await request.json()) as BrowserKeyInput;
    const bad = validate(input);
    if (bad) return bad;
    const now = Date.now();
    const key: BrowserKey = {
      id: `6a000000-0000-4000-8000-${String(keys.length + 2).padStart(12, "0")}`,
      name: input.name,
      prefix: "olb_9f8e7d6c",
      key: "olb_9f8e7d6c5b4a39281706f5e4d3c2b1a0",
      service_name: input.service_name,
      environment: input.environment ?? "",
      kind: input.kind ?? "browser",
      origins: input.origins ?? [],
      app_ids: input.app_ids ?? [],
      rate_limit_per_minute: input.rate_limit_per_minute,
      sample_rate: input.sample_rate,
      created_by_email: "owner@example.com",
      created_at: formatTs(now),
      updated_at: formatTs(now),
      last_used_at: "",
      revoked_at: "",
    };
    keys = [...keys, key];
    return HttpResponse.json({ browser_key: key, key: "olb_9f8e7d6c5b4a39281706f5e4d3c2b1a0" }, { status: 201 });
  }),

  http.put(`${API}/browser-keys/:id`, async ({ request, params }) => {
    const denied = manager(request);
    if (denied) return denied;
    const input = (await request.json()) as BrowserKeyInput;
    const bad = validate(input);
    if (bad) return bad;
    const key = keys.find((k) => k.id === params.id);
    if (!key) return fail("not_found", "browser key not found", 404);
    const updated: BrowserKey = {
      ...key,
      name: input.name,
      service_name: input.service_name,
      environment: input.environment ?? "",
      kind: input.kind ?? "browser",
      origins: input.origins ?? [],
      app_ids: input.app_ids ?? [],
      rate_limit_per_minute: input.rate_limit_per_minute,
      sample_rate: input.sample_rate,
      updated_at: formatTs(Date.now()),
    };
    keys = keys.map((k) => (k.id === updated.id ? updated : k));
    return HttpResponse.json(updated);
  }),

  http.delete(`${API}/browser-keys/:id`, ({ request, params }) => {
    const denied = manager(request);
    if (denied) return denied;
    const key = keys.find((k) => k.id === params.id);
    if (!key) return fail("not_found", "browser key not found", 404);
    // Revocation is a soft delete: the value stays permanently unusable (§3.5).
    keys = keys.map((k) => (k.id === key.id ? { ...k, revoked_at: formatTs(Date.now()) } : k));
    return new HttpResponse(null, { status: 204 });
  }),
];
