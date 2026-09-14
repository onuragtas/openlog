// Report print view data for openlog-renderer (docs/operations/reports.md, D-097). The page authenticates with a
// short-lived render token from the URL fragment — never cookies — so it uses plain fetch instead of the session client.
import { ApiError } from "./client";
import type { SharedDashboard } from "./dashboardSharing";
import type { OqlResult } from "./oql";

/** Reads the render token from `#token=…` and removes it from the address bar and history. */
export function takeRenderToken(loc: Location = window.location, hist: History = window.history): string | null {
  const params = new URLSearchParams(loc.hash.replace(/^#/, ""));
  const token = params.get("token");
  if (loc.hash) hist.replaceState(null, "", loc.pathname);
  return token && /^olrt_[A-Za-z0-9_-]+\.[A-Za-z0-9_-]{43}$/.test(token) ? token : null;
}

async function renderGet<T>(token: string, path: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(path, { headers: { Authorization: `Bearer ${token}` }, credentials: "omit", cache: "no-store", signal });
  if (!res.ok) {
    let code = "unknown";
    let message = res.statusText;
    try {
      const body = (await res.json()) as { error?: { code?: string; message?: string } };
      code = body.error?.code ?? code;
      message = body.error?.message ?? message;
    } catch {
      // not JSON
    }
    throw new ApiError(res.status, code, message);
  }
  return (await res.json()) as T;
}

export const renderDashboard = (token: string, signal?: AbortSignal) => renderGet<SharedDashboard>(token, "/api/v1/render/dashboard", signal);

export const renderWidgetResult = (token: string, widgetId: string, signal?: AbortSignal) =>
  renderGet<OqlResult>(token, `/api/v1/render/dashboard/widgets/${encodeURIComponent(widgetId)}/result`, signal);
