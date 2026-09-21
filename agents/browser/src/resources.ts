// Static asset timing (docs/contracts/rum.md §2.6).
//
// A page loads an order of magnitude more assets than it makes requests, so sending one span per asset
// would make resource timing the largest thing openlog stores about a browser application — larger than the
// page views it exists to explain. **Only the slowest few of each page view are kept**, which bounds the
// volume deterministically rather than statistically: the same page always costs the same, and the assets
// that survive are the ones a person opens this screen to find.
//
// fetch and XHR are skipped here even though the browser reports them as resources: they are already
// instrumented in fetch.ts, with a trace id and a status code this API cannot see. Counting them twice
// would double every request in the timeline.

import { cleanURL } from './fetch.js';
import { observe } from './vitals.js';

export interface ResourceSample {
  url: string;
  initiator: string;
  startMs: number;
  durationMs: number;
  transferBytes: number;
  encodedBytes: number;
  cached: boolean;
}

/** Reported by fetch.ts already; the browser lists them as resources too. */
const INSTRUMENTED = new Set(['fetch', 'xmlhttprequest']);

interface ResourceEntry extends PerformanceEntry {
  initiatorType?: string;
  transferSize?: number;
  encodedBodySize?: number;
  decodedBodySize?: number;
}

/** Absolute wall-clock start of an entry whose startTime is relative to the page. */
function absoluteStart(startTime: number): number {
  const origin = typeof performance.timeOrigin === 'number' ? performance.timeOrigin : Date.now() - performance.now();
  return Math.round(origin + startTime);
}

/**
 * Collects resource timing until flush() is called, keeping at most `limit` entries — the slowest ones.
 *
 * flush() is meant to run when a page view ends, so each page view accounts for its own assets and a
 * single-page application does not attribute the whole session's loading to its first screen.
 */
export function collectResources(limit: number, report: (s: ResourceSample) => void): { flush: () => void; stop: () => void } {
  let kept: ResourceSample[] = [];

  const add = (s: ResourceSample) => {
    // Slowest-first, truncated. An insertion sort over a list this short is cheaper than sorting the
    // hundreds of entries a page can produce, and it never holds more than `limit` of them in memory.
    let i = kept.findIndex((k) => s.durationMs > k.durationMs);
    if (i < 0) i = kept.length;
    if (i >= limit) return;
    kept.splice(i, 0, s);
    if (kept.length > limit) kept.length = limit;
  };

  // buffered: entries that were already recorded before the SDK started still count — a page's slowest
  // asset is usually one that loaded before any script ran.
  const po = observe('resource', true, (entries) => {
    for (const e of entries as ResourceEntry[]) {
      const initiator = e.initiatorType ?? '';
      if (INSTRUMENTED.has(initiator)) continue;
      const transfer = e.transferSize ?? 0;
      const encoded = e.encodedBodySize ?? 0;
      const decoded = e.decodedBodySize ?? 0;
      add({
        url: cleanURL(e.name).full,
        initiator,
        startMs: absoluteStart(e.startTime),
        durationMs: Math.round(e.duration),
        transferBytes: transfer,
        encodedBytes: encoded,
        // Nothing crossed the network but the body was there: the cache answered. Without this a 0 ms
        // asset reads as an impossibly fast one.
        cached: transfer === 0 && decoded > 0,
      });
    }
  });

  return {
    flush() {
      const out = kept;
      kept = [];
      for (const s of out) report(s);
    },
    stop() {
      po?.disconnect();
      kept = [];
    },
  };
}
