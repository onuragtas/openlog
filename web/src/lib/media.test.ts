import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { isCompact, matchesMedia, MOBILE_QUERY, useMediaQuery } from "./media";

type Listener = () => void;

/** Minimal matchMedia stub whose width can be changed. */
function stubMatchMedia(initialWidth: number) {
  let width = initialWidth;
  const listeners = new Set<Listener>();
  const evaluate = (query: string) => {
    const max = /max-width:\s*(\d+)px/.exec(query);
    const min = /min-width:\s*(\d+)px/.exec(query);
    return (!max || width <= Number(max[1])) && (!min || width >= Number(min[1]));
  };
  vi.stubGlobal(
    "matchMedia",
    vi.fn((query: string) => ({
      get matches() {
        return evaluate(query);
      },
      media: query,
      addEventListener: (_: string, l: Listener) => listeners.add(l),
      removeEventListener: (_: string, l: Listener) => listeners.delete(l),
    })),
  );
  return {
    resize(w: number) {
      width = w;
      listeners.forEach((l) => l());
    },
    listenerCount: () => listeners.size,
  };
}

afterEach(() => vi.unstubAllGlobals());

describe("media queries", () => {
  it("is false without matchMedia (desktop layout in tests)", () => {
    vi.stubGlobal("matchMedia", undefined);
    expect(matchesMedia(MOBILE_QUERY)).toBe(false);
    const { result } = renderHook(() => useMediaQuery(MOBILE_QUERY));
    expect(result.current).toBe(false);
  });

  it("follows viewport changes and unsubscribes on unmount", () => {
    const mm = stubMatchMedia(390);
    const { result, unmount } = renderHook(() => useMediaQuery(MOBILE_QUERY));
    expect(result.current).toBe(true);
    act(() => mm.resize(1024));
    expect(result.current).toBe(false);
    act(() => mm.resize(767));
    expect(result.current).toBe(true);
    unmount();
    expect(mm.listenerCount()).toBe(0);
  });

  it("treats unmeasured containers as wide", () => {
    expect(isCompact(0, 640)).toBe(false);
    expect(isCompact(390, 640)).toBe(true);
    expect(isCompact(640, 640)).toBe(false);
  });
});
