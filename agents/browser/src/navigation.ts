// Page load timing and SPA route changes (docs/contracts/rum.md §2.2, §1.3).

/** Navigation Timing phases, in milliseconds, as the backend's `openlog.rum.timing.*` attributes. */
export interface NavigationTimings {
  'openlog.rum.timing.ttfb_ms'?: number;
  'openlog.rum.timing.dns_ms'?: number;
  'openlog.rum.timing.connect_ms'?: number;
  'openlog.rum.timing.tls_ms'?: number;
  'openlog.rum.timing.response_ms'?: number;
  'openlog.rum.timing.dom_interactive_ms'?: number;
  'openlog.rum.timing.dom_content_loaded_ms'?: number;
  'openlog.rum.timing.load_event_ms'?: number;
}

const round = (v: number): number | undefined => (Number.isFinite(v) && v > 0 ? Math.round(v * 100) / 100 : undefined);

/**
 * Reads the Navigation Timing entry. A phase the browser skipped (a warm DNS cache, a plain-http origin,
 * a back/forward-cache restore) is left out rather than reported as 0, so an average of the phase is an
 * average over the loads that actually performed it.
 */
export function navigationTimings(): { timings: NavigationTimings; durationMs: number } {
  const empty = { timings: {}, durationMs: 0 };
  let nav: PerformanceNavigationTiming | undefined;
  try {
    nav = performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming | undefined;
  } catch {
    return empty;
  }
  if (!nav) return empty;
  const timings: NavigationTimings = {
    'openlog.rum.timing.ttfb_ms': round(nav.responseStart),
    'openlog.rum.timing.dns_ms': round(nav.domainLookupEnd - nav.domainLookupStart),
    'openlog.rum.timing.connect_ms': round(nav.connectEnd - nav.connectStart),
    'openlog.rum.timing.tls_ms': nav.secureConnectionStart > 0 ? round(nav.connectEnd - nav.secureConnectionStart) : undefined,
    'openlog.rum.timing.response_ms': round(nav.responseEnd - nav.responseStart),
    'openlog.rum.timing.dom_interactive_ms': round(nav.domInteractive),
    'openlog.rum.timing.dom_content_loaded_ms': round(nav.domContentLoadedEventEnd),
    'openlog.rum.timing.load_event_ms': round(nav.loadEventEnd),
  };
  // The page load duration is loadEventEnd when the load has finished, else how far it has got. A page
  // measured before `load` (the SDK started early, the visitor left early) still reports something real.
  const duration = nav.loadEventEnd > 0 ? nav.loadEventEnd : nav.domContentLoadedEventEnd > 0 ? nav.domContentLoadedEventEnd : performance.now();
  return { timings, durationMs: Math.max(0, Math.round(duration * 100) / 100) };
}

/** Runs fn once the load event has fired (immediately when it already has). */
export function afterLoad(fn: () => void): void {
  if (document.readyState === 'complete') {
    // A task boundary, so loadEventEnd is populated before the timings are read.
    setTimeout(fn, 0);
    return;
  }
  window.addEventListener('load', () => setTimeout(fn, 0), { once: true });
}

/**
 * Calls `onChange` when a single-page application navigates. history.pushState and replaceState are patched
 * because they fire no event of their own; popstate and hashchange cover the back/forward buttons.
 *
 * The patch preserves the original functions and forwards the return value, so an application that wraps
 * history itself (most routers do) keeps working. The returned function restores them.
 */
export function onRouteChange(onChange: (url: string) => void): () => void {
  let last = location.href;
  const fire = () => {
    const href = location.href;
    // Compare without the hash unless the hash is the route: a `#section` jump is not a page view.
    if (href === last) return;
    const changedPath = new URL(href).pathname !== new URL(last).pathname;
    const changedHash = new URL(href).hash !== new URL(last).hash;
    last = href;
    if (changedPath || changedHash) onChange(href);
  };
  const schedule = () => setTimeout(fire, 0); // let the router update the DOM first

  const history = window.history;
  const origPush = history.pushState;
  const origReplace = history.replaceState;
  history.pushState = function pushState(this: History, ...args: Parameters<History['pushState']>) {
    const r = origPush.apply(this, args);
    schedule();
    return r;
  };
  history.replaceState = function replaceState(this: History, ...args: Parameters<History['replaceState']>) {
    const r = origReplace.apply(this, args);
    schedule();
    return r;
  };
  window.addEventListener('popstate', schedule);
  window.addEventListener('hashchange', schedule);

  return () => {
    history.pushState = origPush;
    history.replaceState = origReplace;
    window.removeEventListener('popstate', schedule);
    window.removeEventListener('hashchange', schedule);
  };
}
