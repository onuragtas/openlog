// Core Web Vitals (docs/contracts/rum.md §2).
//
// Collected with PerformanceObserver directly rather than by depending on the `web-vitals` package: the
// dependency is small but it is a dependency, and the four observers below are the whole of it.
//
// KNOWN SIMPLIFICATION (rum.md §2.1): INP here is the worst interaction latency of the page, not the
// 98th-percentile-with-outlier-correction that the official definition uses for pages with more than 50
// interactions. On such pages this SDK's INP is pessimistic — it reports the worst case where the official
// metric would discard a few outliers. It is never optimistic, and it is the same number for every page,
// so it is honest to compare across routes. Replacing it with the full algorithm is a contained change.

export type VitalName = 'lcp' | 'inp' | 'cls' | 'fcp' | 'ttfb';

export interface VitalSample {
  name: VitalName;
  value: number;
}

type Report = (v: VitalSample) => void;

interface LayoutShift extends PerformanceEntry {
  value: number;
  hadRecentInput: boolean;
}

function observe(type: string, buffered: boolean, cb: (entries: PerformanceEntry[]) => void): PerformanceObserver | null {
  try {
    const po = new PerformanceObserver((list) => cb(list.getEntries()));
    // `durationThreshold` is only meaningful for 'event'; unknown options are ignored by the browser.
    po.observe({ type, buffered } as PerformanceObserverInit);
    return po;
  } catch {
    // An unsupported entry type throws; the vital is simply not collected on that browser.
    return null;
  }
}

/**
 * Starts collecting. `report` is called once per vital, at the moment the value becomes final: LCP, CLS and
 * INP are only knowable when the page is hidden (a larger element, a later shift or a slower interaction can
 * always still happen), so they are held until finalize() runs.
 */
export function collectVitals(report: Report): { finalize: () => void; stop: () => void } {
  const observers: PerformanceObserver[] = [];
  const keep = (po: PerformanceObserver | null) => {
    if (po) observers.push(po);
  };
  const reported = new Set<VitalName>();
  const once = (name: VitalName, value: number) => {
    if (reported.has(name) || !Number.isFinite(value) || value < 0) return;
    reported.add(name);
    report({ name, value });
  };

  // TTFB and FCP are final as soon as they happen.
  try {
    const nav = performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming | undefined;
    if (nav && nav.responseStart > 0) once('ttfb', nav.responseStart);
  } catch {
    /* ignore */
  }
  keep(
    observe('paint', true, (entries) => {
      for (const e of entries) {
        if (e.name === 'first-contentful-paint') once('fcp', e.startTime);
      }
    }),
  );

  let lcp = 0;
  keep(
    observe('largest-contentful-paint', true, (entries) => {
      // The last entry wins: the browser reports progressively larger candidates.
      const last = entries[entries.length - 1];
      if (last) lcp = last.startTime;
    }),
  );

  // CLS is the largest burst of shifts, not their sum: a burst ends after 1s without a shift or 5s total.
  let cls = 0;
  let burst = 0;
  let burstStart = 0;
  let burstEnd = 0;
  keep(
    observe('layout-shift', true, (entries) => {
      for (const entry of entries as LayoutShift[]) {
        // Shifts within 500ms of an interaction are the user's doing, not the page's.
        if (entry.hadRecentInput) continue;
        if (burst !== 0 && entry.startTime - burstEnd < 1000 && entry.startTime - burstStart < 5000) {
          burst += entry.value;
        } else {
          burst = entry.value;
          burstStart = entry.startTime;
        }
        burstEnd = entry.startTime;
        if (burst > cls) cls = burst;
      }
    }),
  );

  let inp = 0;
  keep(
    observe('event', true, (entries) => {
      for (const e of entries) {
        const d = (e as PerformanceEntry & { interactionId?: number }).interactionId ? e.duration : 0;
        if (d > inp) inp = d;
      }
    }),
  );

  const finalize = () => {
    if (lcp > 0) once('lcp', lcp);
    // CLS is reported even at 0: "this page does not shift" is a real, useful measurement, and omitting it
    // would make a perfect page indistinguishable from one the SDK could not measure.
    once('cls', cls);
    if (inp > 0) once('inp', inp);
  };
  const stop = () => {
    for (const po of observers) {
      try {
        po.disconnect();
      } catch {
        /* ignore */
      }
    }
    observers.length = 0;
  };
  return { finalize, stop };
}
