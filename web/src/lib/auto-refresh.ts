// Global auto-refresh (AppShell "refresh" control). The interval is the `refresh` URL search param (`?refresh=30s`,
// absent = the browser's remembered choice, default off) and is retained across navigations by the root route.
//
// There is no "now" in query keys: every time-range query factory (api/queries.ts, explorer.ts, oql.ts, kubernetes.ts)
// keys on the *range spec* and resolves `resolveRange(spec, Date.now())` inside its queryFn. A tick therefore only has to
// refetch the mounted queries: each refetch resolves a relative range against the tick's "now", so the window moves
// forward, while previous data stays on screen (same key; `keepPreviousData` where keys change). Absolute ranges never
// tick (the control is disabled); manual "refresh now" still refetches them.
import type { Query, QueryClient } from "@tanstack/react-query";
import { isCustomRange, parseDuration, type RangeSpec } from "@/lib/time";

export const REFRESH_INTERVALS = ["5s", "10s", "30s", "1m", "5m", "15m"] as const;
export type RefreshInterval = (typeof REFRESH_INTERVALS)[number];

export const REFRESH_STORAGE = "openlog.autoRefresh";
const OFF = "off";

/** A valid interval from an untrusted value (URL, storage), else undefined. */
export function parseRefreshInterval(v: unknown): RefreshInterval | undefined {
  return typeof v === "string" && (REFRESH_INTERVALS as readonly string[]).includes(v) ? (v as RefreshInterval) : undefined;
}

export function refreshIntervalMs(v: RefreshInterval): number {
  return parseDuration(v)!;
}

/** The remembered interval of this browser (undefined = off or nothing stored). */
export function readStoredRefresh(): RefreshInterval | undefined {
  try {
    return parseRefreshInterval(localStorage.getItem(REFRESH_STORAGE));
  } catch {
    return undefined;
  }
}

export function storeRefresh(v: RefreshInterval | undefined): void {
  try {
    localStorage.setItem(REFRESH_STORAGE, v ?? OFF);
  } catch {
    // storage unavailable (private mode, blocked): the URL param still works
  }
}

/** The URL param wins; without one the remembered choice applies. */
export function effectiveRefresh(url: RefreshInterval | undefined, stored: RefreshInterval | undefined): RefreshInterval | undefined {
  return url ?? stored;
}

/** Tick interval for a range: null when off or when the range is absolute (it would refetch the same window). */
export function autoRefreshMs(range: RangeSpec, interval: RefreshInterval | undefined): number | null {
  if (!interval || isCustomRange(range)) return null;
  return refreshIntervalMs(interval);
}

/** Shell-level queries (session, version, banners) opt out with `meta: { autoRefresh: false }`. */
const refreshable = (q: Query) => q.meta?.autoRefresh !== false;

/** Invalidates and refetches only mounted (active) queries; in-flight requests are joined, not cancelled. */
export function refreshActiveQueries(client: QueryClient): Promise<void> {
  return client.invalidateQueries({ type: "active", predicate: refreshable, refetchType: "active" }, { cancelRefetch: false });
}

/** Whether a mounted, refreshable query is fetching (a tick is skipped meanwhile). */
export function isRefreshing(client: QueryClient): boolean {
  return client.isFetching({ type: "active", predicate: refreshable }) > 0;
}

export const refreshingFilters = { type: "active", predicate: refreshable } as const;

export interface AutoRefreshTimerOptions {
  intervalMs: number;
  refresh: () => void;
  /** a tick is skipped (and the next one scheduled) while this is true */
  isBusy: () => boolean;
  doc?: Pick<Document, "hidden" | "addEventListener" | "removeEventListener">;
}

/**
 * Calls `refresh` every `intervalMs`. Paused while the document is hidden; when it becomes visible again and at least
 * one interval has elapsed since the last refresh, refreshes at once. Returns the stop function.
 */
export function startAutoRefresh({ intervalMs, refresh, isBusy, doc = document }: AutoRefreshTimerOptions): () => void {
  let last = Date.now();
  let timer: ReturnType<typeof setTimeout> | undefined;

  const schedule = () => {
    clearTimeout(timer);
    timer = undefined;
    if (doc.hidden) return;
    timer = setTimeout(tick, Math.max(0, last + intervalMs - Date.now()));
  };
  const tick = () => {
    timer = undefined;
    if (doc.hidden) return;
    last = Date.now();
    if (!isBusy()) refresh();
    schedule();
  };
  const onVisibility = () => schedule();

  doc.addEventListener("visibilitychange", onVisibility);
  schedule();
  return () => {
    clearTimeout(timer);
    doc.removeEventListener("visibilitychange", onVisibility);
  };
}
