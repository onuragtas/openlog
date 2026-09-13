// Breakpoint and element-size hooks for responsive layouts. The breakpoints match Tailwind's
// defaults (md = 768px, lg = 1024px), so JS layout switches line up with `md:` / `lg:` classes.
import { useCallback, useEffect, useState, useSyncExternalStore } from "react";

/** Phones: below Tailwind `md`. */
export const MOBILE_QUERY = "(max-width: 767px)";
/** Phones and tablets in portrait: below Tailwind `lg` (the sidebar becomes a drawer). */
export const BELOW_LG_QUERY = "(max-width: 1023px)";

/** Current match of a media query; false where matchMedia is unavailable (SSR, jsdom). */
export function matchesMedia(query: string): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function" && window.matchMedia(query).matches;
}

/** Subscribes to a media query (re-renders on change, e.g. rotation or window resize). */
export function useMediaQuery(query: string): boolean {
  const subscribe = useCallback(
    (onChange: () => void) => {
      if (typeof window === "undefined" || typeof window.matchMedia !== "function") return () => undefined;
      const mq = window.matchMedia(query);
      mq.addEventListener("change", onChange);
      return () => mq.removeEventListener("change", onChange);
    },
    [query],
  );
  return useSyncExternalStore(
    subscribe,
    () => matchesMedia(query),
    () => false,
  );
}

export const useIsMobile = () => useMediaQuery(MOBILE_QUERY);
export const useIsBelowLg = () => useMediaQuery(BELOW_LG_QUERY);

/**
 * Whether a measured container is narrower than `threshold` px. An unmeasured (0) width counts as
 * wide, so the first render and jsdom tests use the desktop layout.
 */
export function isCompact(width: number, threshold: number): boolean {
  return width > 0 && width < threshold;
}

/** Content-box width of an element (ResizeObserver). Returns a callback ref and the width in px (0 until measured). */
export function useElementWidth<T extends HTMLElement>(): [(el: T | null) => void, number] {
  const [el, setEl] = useState<T | null>(null);
  const [width, setWidth] = useState(0);
  useEffect(() => {
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver((entries) => {
      const w = Math.round(entries[0]?.contentRect.width ?? 0);
      setWidth((prev) => (prev === w ? prev : w));
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [el]);
  return [setEl, width];
}
