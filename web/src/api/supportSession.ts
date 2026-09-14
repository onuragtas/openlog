// Operator support view (docs/contracts/api.md "SaaS operations", D-105). While a support session is active the API
// client sends its id in X-Openlog-Support-Session (not the organization header); the server verifies it belongs to
// the signed-in operator's own session. Kept in sessionStorage: it never outlives the browser tab.

export const SUPPORT_HEADER = "X-Openlog-Support-Session";
const STORAGE_KEY = "openlog.supportSession";

export interface ActiveSupportSession {
  id: string;
  org_id: string;
  org_name: string;
  expires_at: string;
}

type Listener = () => void;
const listeners = new Set<Listener>();

export function getSupportSession(now = Date.now()): ActiveSupportSession | null {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const s = JSON.parse(raw) as ActiveSupportSession;
    if (!s?.id || Date.parse(s.expires_at) <= now) {
      sessionStorage.removeItem(STORAGE_KEY);
      return null;
    }
    return s;
  } catch {
    return null;
  }
}

export function setSupportSession(s: ActiveSupportSession | null): void {
  try {
    if (s) sessionStorage.setItem(STORAGE_KEY, JSON.stringify(s));
    else sessionStorage.removeItem(STORAGE_KEY);
  } catch {
    // storage unavailable: support views need sessionStorage
  }
  listeners.forEach((l) => l());
}

/** The active support session id (a stable useSyncExternalStore snapshot). */
export function supportSessionId(): string | null {
  return getSupportSession()?.id ?? null;
}

/** Subscribes to support session changes (useSyncExternalStore). */
export function subscribeSupportSession(l: Listener): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}
