// Session authentication (M1). The session is an HttpOnly cookie set by
// POST /api/v1/auth/login and sent by the browser automatically; scripts never
// see it. This module keeps the per-session CSRF token (in memory only;
// GET /api/v1/auth/me returns it again after a reload) and the organization the
// user selected (sent as X-Openlog-Org-Id).

export const CSRF_HEADER = "X-CSRF-Token";
export const ORG_HEADER = "X-Openlog-Org-Id";
export const ORG_STORAGE = "openlog.orgId";

let csrfToken: string | null = null;

export function getCsrfToken(): string | null {
  return csrfToken;
}

export function setCsrfToken(token: string | null | undefined): void {
  csrfToken = token ? token : null;
}

export function getSelectedOrg(): string | null {
  try {
    const v = localStorage.getItem(ORG_STORAGE);
    return v && v.trim() !== "" ? v : null;
  } catch {
    return null;
  }
}

export function setSelectedOrg(id: string | null): void {
  try {
    if (id) localStorage.setItem(ORG_STORAGE, id);
    else localStorage.removeItem(ORG_STORAGE);
  } catch {
    // storage unavailable (private mode): the default organization is used
  }
}

/** Forgets client-side session state; the server clears the cookie. */
export function clearAuthState(): void {
  csrfToken = null;
}
