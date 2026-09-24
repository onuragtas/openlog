// MSW handlers for source maps (docs/contracts/api.md "Source maps", rum.md §8). The stored documents are
// never served back by the real API either — only the metadata rows the settings screen lists.
import { http, HttpResponse } from "msw";
import type { SourceMap } from "@/api/sourceMaps";
import { authenticate } from "./account";
import { formatTs } from "./fixtures";

const API = "*/api/v1";

type Code = "invalid_argument" | "permission_denied" | "not_found";
const STATUS: Record<Code, number> = { invalid_argument: 400, permission_denied: 403, not_found: 404 };
const fail = (code: Code, message: string) => HttpResponse.json({ error: { code, message } }, { status: STATUS[code] });

const sha = (seed: string) => {
  let h = 2166136261;
  for (const c of seed) h = Math.imul(h ^ c.charCodeAt(0), 16777619) >>> 0;
  return Array.from({ length: 8 }, (_, i) => (h + i * 0x9e3779b9).toString(16).padStart(8, "0")).join("");
};

function seed(): SourceMap[] {
  const now = Date.now();
  const rows: [string, string, number, number][] = [
    ["main.3f2a1b9c.js", "shop-web", 1_842_133, 1],
    ["vendor.8c1d0e2f.js", "shop-web", 3_204_871, 1],
    ["checkout.5a7b9c3d.js", "shop-web", 412_907, 6],
  ];
  return rows.map(([script, app, size, ageDays], i) => ({
    id: `0f7b3c1e-5a2d-4c1b-9e8f-0000000071${String(i).padStart(2, "0")}`,
    app,
    script,
    size_bytes: size,
    sha256: sha(script),
    created_by_email: i === 2 ? "grace@example.com" : "admin@openlog.local",
    created_at: formatTs(now - ageDays * 86_400_000),
    updated_at: formatTs(now - ageDays * 86_400_000),
  }));
}

let maps = seed();

/** Restores the seed rows (tests). */
export function resetMockSourceMaps(): void {
  maps = seed();
}

/** Signed-in admin or owner, like the server (uploading and deleting are not API-key operations). */
function gateWrite(request: Request): Response | null {
  const ctx = authenticate(request);
  if (ctx instanceof Response) return ctx;
  if (ctx.kind !== "session") return fail("permission_denied", "this operation requires a signed-in user; API keys are read-only");
  if (ctx.role !== "admin" && ctx.role !== "owner") return fail("permission_denied", `your role (${ctx.role}) does not allow this operation`);
  return null;
}

export const sourceMapHandlers = [
  http.get(`${API}/source-maps`, ({ request }) => {
    const ctx = authenticate(request);
    if (ctx instanceof Response) return ctx;
    const sorted = [...maps].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
    return HttpResponse.json({ source_maps: sorted });
  }),

  http.post(`${API}/source-maps`, async ({ request }) => {
    const refused = gateWrite(request);
    if (refused) return refused;
    const url = new URL(request.url);
    const app = url.searchParams.get("app") ?? "";
    const script = url.searchParams.get("script") ?? "";
    if (!app) return fail("invalid_argument", "app is required");
    if (!script) return fail("invalid_argument", "script is required");
    if (/[/?#]/.test(script)) return fail("invalid_argument", "script must be a file name, without a directory, query string or fragment");
    const document = await request.arrayBuffer();
    if (document.byteLength === 0) return fail("invalid_argument", "the source map document is empty");
    const now = formatTs(Date.now());
    // Uploading a script again replaces the map held for it and keeps its id (rum.md §8).
    const existing = maps.find((m) => m.app === app && m.script === script);
    const row: SourceMap = {
      id: existing?.id ?? `0f7b3c1e-5a2d-4c1b-9e8f-0000000071${String(maps.length).padStart(2, "0")}`,
      app,
      script,
      size_bytes: document.byteLength,
      sha256: sha(`${script}:${document.byteLength}`),
      created_by_email: "admin@openlog.local",
      created_at: existing?.created_at ?? now,
      updated_at: now,
    };
    maps = existing ? maps.map((m) => (m.id === row.id ? row : m)) : [row, ...maps];
    return HttpResponse.json({ source_map: row }, { status: existing ? 200 : 201 });
  }),

  http.delete(`${API}/source-maps/:id`, ({ request, params }) => {
    const refused = gateWrite(request);
    if (refused) return refused;
    const id = String(params.id);
    if (!maps.some((m) => m.id === id)) return fail("not_found", "source map not found");
    maps = maps.filter((m) => m.id !== id);
    return new HttpResponse(null, { status: 204 });
  }),
];
