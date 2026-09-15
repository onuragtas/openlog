import { QueryClient, QueryObserver } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  autoRefreshMs,
  effectiveRefresh,
  isRefreshing,
  parseRefreshInterval,
  readStoredRefresh,
  REFRESH_STORAGE,
  refreshActiveQueries,
  refreshIntervalMs,
  startAutoRefresh,
  storeRefresh,
} from "@/lib/auto-refresh";
import { resolveRange, type RangeSpec } from "@/lib/time";

function fakeDocument() {
  const listeners = new Set<() => void>();
  return {
    hidden: false,
    addEventListener: (_: string, fn: () => void) => listeners.add(fn),
    removeEventListener: (_: string, fn: () => void) => listeners.delete(fn),
    setHidden(h: boolean) {
      this.hidden = h;
      listeners.forEach((fn) => fn());
    },
    listenerCount: () => listeners.size,
  };
}
type FakeDoc = ReturnType<typeof fakeDocument>;
const asDoc = (d: FakeDoc) => d as unknown as Document;

describe("interval state", () => {
  it("parses only the offered intervals", () => {
    expect(parseRefreshInterval("30s")).toBe("30s");
    expect(parseRefreshInterval("15m")).toBe("15m");
    expect(parseRefreshInterval("off")).toBeUndefined();
    expect(parseRefreshInterval("2s")).toBeUndefined();
    expect(parseRefreshInterval(30)).toBeUndefined();
    expect(parseRefreshInterval(undefined)).toBeUndefined();
    expect(refreshIntervalMs("5s")).toBe(5_000);
    expect(refreshIntervalMs("1m")).toBe(60_000);
  });

  it("remembers the choice in localStorage and uses it when the URL has none", () => {
    expect(readStoredRefresh()).toBeUndefined();
    storeRefresh("1m");
    expect(localStorage.getItem(REFRESH_STORAGE)).toBe("1m");
    expect(readStoredRefresh()).toBe("1m");
    expect(effectiveRefresh(undefined, readStoredRefresh())).toBe("1m");
    expect(effectiveRefresh("10s", readStoredRefresh())).toBe("10s");
    storeRefresh(undefined);
    expect(localStorage.getItem(REFRESH_STORAGE)).toBe("off");
    expect(readStoredRefresh()).toBeUndefined();
    localStorage.setItem(REFRESH_STORAGE, "garbage");
    expect(readStoredRefresh()).toBeUndefined();
  });

  it("survives unavailable storage", () => {
    const get = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    const set = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    expect(readStoredRefresh()).toBeUndefined();
    expect(() => storeRefresh("5s")).not.toThrow();
    get.mockRestore();
    set.mockRestore();
  });

  it("ticks only for relative ranges", () => {
    expect(autoRefreshMs({ range: "15m" }, "30s")).toBe(30_000);
    expect(autoRefreshMs({}, "5s")).toBe(5_000); // default range is relative
    expect(autoRefreshMs({ range: "1h" }, undefined)).toBeNull();
    expect(autoRefreshMs({ from: "1000", to: "2000" }, "30s")).toBeNull();
  });
});

describe("startAutoRefresh", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("refreshes every interval and stops cleanly", () => {
    const doc = fakeDocument();
    const refresh = vi.fn();
    const stop = startAutoRefresh({ intervalMs: 10_000, refresh, isBusy: () => false, doc: asDoc(doc) });
    vi.advanceTimersByTime(9_999);
    expect(refresh).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(refresh).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(20_000);
    expect(refresh).toHaveBeenCalledTimes(3);
    stop();
    expect(doc.listenerCount()).toBe(0);
    vi.advanceTimersByTime(60_000);
    expect(refresh).toHaveBeenCalledTimes(3);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("skips a tick while the previous refresh is still fetching", () => {
    const doc = fakeDocument();
    const refresh = vi.fn();
    let busy = true;
    const stop = startAutoRefresh({ intervalMs: 5_000, refresh, isBusy: () => busy, doc: asDoc(doc) });
    vi.advanceTimersByTime(5_000);
    expect(refresh).not.toHaveBeenCalled();
    busy = false;
    vi.advanceTimersByTime(4_999);
    expect(refresh).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(refresh).toHaveBeenCalledTimes(1);
    stop();
  });

  it("pauses while the tab is hidden and catches up once when it is visible again", () => {
    const doc = fakeDocument();
    const refresh = vi.fn();
    const stop = startAutoRefresh({ intervalMs: 30_000, refresh, isBusy: () => false, doc: asDoc(doc) });
    vi.advanceTimersByTime(10_000);
    doc.setHidden(true);
    expect(vi.getTimerCount()).toBe(0);
    vi.advanceTimersByTime(120_000);
    expect(refresh).not.toHaveBeenCalled();
    doc.setHidden(false);
    vi.advanceTimersByTime(0);
    expect(refresh).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(29_999);
    expect(refresh).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(1);
    expect(refresh).toHaveBeenCalledTimes(2);
    stop();
  });

  it("does not refresh on becoming visible before an interval elapsed", () => {
    const doc = fakeDocument();
    const refresh = vi.fn();
    const stop = startAutoRefresh({ intervalMs: 30_000, refresh, isBusy: () => false, doc: asDoc(doc) });
    vi.advanceTimersByTime(5_000);
    doc.setHidden(true);
    vi.advanceTimersByTime(5_000);
    doc.setHidden(false);
    vi.advanceTimersByTime(19_999);
    expect(refresh).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(refresh).toHaveBeenCalledTimes(1);
    stop();
  });
});

describe("refreshing queries", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  const windowQuery = (client: QueryClient, key: string, range: RangeSpec, calls: { from: number; to: number }[], meta?: Record<string, unknown>) =>
    new QueryObserver(client, {
      queryKey: ["window", key],
      queryFn: async () => {
        const r = resolveRange(range, Date.now());
        calls.push(r);
        return r;
      },
      meta,
    });

  it("advances the window of a relative range on every tick", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const calls: { from: number; to: number }[] = [];
    const unsub = windowQuery(client, "rel", { range: "15m" }, calls).subscribe(() => {});
    await vi.advanceTimersByTimeAsync(0);
    expect(calls).toHaveLength(1);

    const doc = fakeDocument();
    const stop = startAutoRefresh({ intervalMs: autoRefreshMs({ range: "15m" }, "30s")!, refresh: () => void refreshActiveQueries(client), isBusy: () => isRefreshing(client), doc: asDoc(doc) });
    await vi.advanceTimersByTimeAsync(30_000);
    expect(calls).toHaveLength(2);
    expect(calls[1]!.to - calls[0]!.to).toBe(30_000);
    expect(calls[1]!.to - calls[1]!.from).toBe(15 * 60_000);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(calls).toHaveLength(3);
    stop();
    unsub();
  });

  it("manual refresh refetches only mounted queries that did not opt out", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const active: { from: number; to: number }[] = [];
    const shell: { from: number; to: number }[] = [];
    const inactive: { from: number; to: number }[] = [];
    const unsubA = windowQuery(client, "active", { range: "1h" }, active).subscribe(() => {});
    const unsubS = windowQuery(client, "shell", { range: "1h" }, shell, { autoRefresh: false }).subscribe(() => {});
    await client.fetchQuery({ queryKey: ["window", "cached"], queryFn: async () => inactive.push(resolveRange({ range: "1h" }, Date.now())) });
    await vi.advanceTimersByTimeAsync(0);
    expect([active.length, shell.length, inactive.length]).toEqual([1, 1, 1]);

    vi.advanceTimersByTime(1_000);
    const done = refreshActiveQueries(client);
    expect(isRefreshing(client)).toBe(true);
    await done;
    expect([active.length, shell.length, inactive.length]).toEqual([2, 1, 1]);
    expect(active[1]!.to - active[0]!.to).toBeGreaterThanOrEqual(1_000);
    expect(isRefreshing(client)).toBe(false);
    unsubA();
    unsubS();
  });

  it("joins an in-flight fetch instead of starting a duplicate request", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let started = 0;
    const unsub = new QueryObserver(client, {
      queryKey: ["slow"],
      queryFn: () => {
        started++;
        return new Promise((r) => setTimeout(() => r(started), 1_000));
      },
    }).subscribe(() => {});
    await vi.advanceTimersByTimeAsync(10);
    expect(started).toBe(1);
    const done = refreshActiveQueries(client);
    await vi.advanceTimersByTimeAsync(1_000);
    await done;
    expect(started).toBe(1);
    unsub();
  });
});
