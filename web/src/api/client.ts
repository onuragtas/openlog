import createClient, { type Middleware } from "openapi-fetch";
import { observeServerVersion, VERSION_HEADER } from "@/lib/server-version";
import { clearAuthState, CSRF_HEADER, getCsrfToken, getSelectedOrg, ORG_HEADER } from "./auth";
import type { paths } from "./schema.gen";
import { getSupportSession, setSupportSession, SUPPORT_HEADER } from "./supportSession";
import type { ApiErrorBody } from "./types";

/** Error thrown by query functions for non-2xx API responses. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

let unauthorizedHandler: (() => void) | null = null;

/** Registers what happens when a signed-in session ends (the app navigates to /login). */
export function setUnauthorizedHandler(fn: (() => void) | null): void {
  unauthorizedHandler = fn;
}

/** Endpoints whose 401 means "wrong credentials", not "the session ended". */
const PUBLIC_PATHS = new Set(["/api/v1/auth/login", "/api/v1/auth/signup", "/api/v1/auth/config", "/api/v1/auth/verify-email", "/api/v1/invitations/lookup", "/api/v1/invitations/accept"]);
const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);

function pathOf(url: string): string {
  try {
    return new URL(url).pathname;
  } catch {
    return url;
  }
}

function isPublic(url: string): boolean {
  try {
    return PUBLIC_PATHS.has(new URL(url).pathname);
  } catch {
    return false;
  }
}

export const authMiddleware: Middleware = {
  onRequest({ request }) {
    // The session cookie travels by itself (same origin); mutations add the CSRF token.
    const csrf = getCsrfToken();
    if (csrf && !SAFE_METHODS.has(request.method.toUpperCase()) && !request.headers.has(CSRF_HEADER)) {
      request.headers.set(CSRF_HEADER, csrf);
    }
    // Operator support view (api/supportSession.ts): the support session selects the organization server-side.
    const support = getSupportSession();
    if (support && !pathOf(request.url).startsWith("/api/v1/operator/")) {
      request.headers.set(SUPPORT_HEADER, support.id);
      return request;
    }
    const org = getSelectedOrg();
    if (org && !request.headers.has(ORG_HEADER)) {
      request.headers.set(ORG_HEADER, org);
    }
    return request;
  },
  onResponse({ request, response }) {
    observeServerVersion(response.headers.get(VERSION_HEADER));
    if (response.status === 403 && request.headers.has(SUPPORT_HEADER)) {
      void response
        .clone()
        .json()
        .then((b: Partial<ApiErrorBody>) => {
          // Operator-only error code (api.md "Support sessions"), not part of the generic ApiErrorBody union.
          if ((b?.error?.code as string | undefined) === "support_session_ended") setSupportSession(null);
        })
        .catch(() => undefined);
    }
    if (response.status === 401 && !isPublic(request.url)) {
      // Only a session we knew about "expires"; a signed-out visitor's first
      // /auth/me simply leads to the login page (router beforeLoad).
      const hadSession = getCsrfToken() !== null;
      clearAuthState();
      if (hadSession) unauthorizedHandler?.();
    }
    return response;
  },
};

export function createApiClient(baseUrl = "", fetchImpl?: typeof fetch) {
  const client = createClient<paths>({
    baseUrl,
    // Resolve fetch lazily so test mocks installed after import are used.
    fetch: fetchImpl ?? ((input: Request) => globalThis.fetch(input)),
  });
  client.use(authMiddleware);
  return client;
}

/** Base URL: same origin (the Go binary serves UI and API; Vite proxies /api in dev). */
function defaultBaseUrl(): string {
  return typeof window !== "undefined" && window.location ? window.location.origin : "http://localhost";
}

export const api = createApiClient(defaultBaseUrl());

/** Converts an openapi-fetch result into data or a thrown ApiError. */
export function unwrap<T>(result: { data?: T; error?: unknown; response: Response }): T {
  if (result.data !== undefined && result.response.ok) return result.data;
  const body = result.error as Partial<ApiErrorBody> | undefined;
  throw new ApiError(
    result.response.status,
    body?.error?.code ?? "unknown",
    body?.error?.message ?? result.response.statusText ?? "request failed",
  );
}

/** Like unwrap for responses without a body (204 No Content). */
export function expectOk(result: { error?: unknown; response: Response }): void {
  if (result.response.ok) return;
  unwrap<never>(result);
}
