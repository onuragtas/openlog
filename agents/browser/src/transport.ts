// Batching and delivery (docs/contracts/rum.md §1.2).
//
// Two things make browser telemetry different from an agent's: the process can disappear at any moment, and
// nothing may ever throw into the page. So the queue flushes on every signal that the page is going away,
// and every failure path here ends in "give up quietly".

import type { ResolvedConfig } from './config.js';
import { buildPayload, type OtlpSpan } from './otlp.js';

/** How many spans the queue holds before the oldest are dropped (a page that never unloads, offline). */
const MAX_QUEUE = 256;

export class Transport {
  private queue: OtlpSpan[] = [];
  private timer: ReturnType<typeof setTimeout> | null = null;
  private stopped = false;
  private resource: { serviceName: string; environment: string };

  constructor(private cfg: ResolvedConfig) {
    this.resource = { serviceName: cfg.serviceName, environment: cfg.environment };
  }

  /** The server's view of the application, once GET /v1/rum/config has answered. */
  setResource(serviceName: string, environment: string): void {
    this.resource = { serviceName, environment };
  }

  add(span: OtlpSpan): void {
    if (this.stopped) return;
    if (this.queue.length >= MAX_QUEUE) {
      // Drop the oldest: the newest events are the ones still worth having, and an unbounded queue in a
      // long-lived tab is a memory leak the page would blame on itself.
      this.queue.shift();
    }
    this.queue.push(span);
    if (this.queue.length >= this.cfg.maxBatchSize) {
      this.flush();
      return;
    }
    this.schedule();
  }

  private schedule(): void {
    if (this.timer !== null) return;
    this.timer = setTimeout(() => {
      this.timer = null;
      this.flush();
    }, this.cfg.flushIntervalMs);
  }

  /**
   * Sends everything queued. `beacon` asks for the delivery that survives the page going away.
   *
   * navigator.sendBeacon is the only send the browser guarantees to finish after unload, but it can only
   * carry a CORS-simple content type — `application/json` would make it preflight, and a preflight cannot
   * complete during unload, so the request would be dropped and the last batch of every visit lost. It
   * therefore posts `text/plain` with the key in the query string (the key is public; rum.md §3.1), and the
   * ingest parses the body as OTLP/JSON either way.
   */
  flush(beacon = false): void {
    if (this.timer !== null) {
      clearTimeout(this.timer);
      this.timer = null;
    }
    const spans = this.queue;
    if (spans.length === 0) return;
    this.queue = [];
    const body = buildPayload(spans, this.resource);

    if (beacon && typeof navigator !== 'undefined' && typeof navigator.sendBeacon === 'function') {
      try {
        const url = `${this.cfg.rumUrl}?k=${encodeURIComponent(this.cfg.key)}`;
        if (navigator.sendBeacon(url, new Blob([body], { type: 'text/plain;charset=UTF-8' }))) return;
      } catch {
        // fall through to fetch
      }
    }

    try {
      void fetch(this.cfg.rumUrl, {
        method: 'POST',
        // keepalive lets the request outlive the document too, but is capped at 64 KiB and is not honoured
        // everywhere, which is why sendBeacon is tried first on unload.
        keepalive: true,
        mode: 'cors',
        credentials: 'omit',
        headers: { 'Content-Type': 'application/json', 'openlog-browser-key': this.cfg.key },
        body,
      }).catch((err: unknown) => this.debug('send failed', err));
    } catch (err) {
      this.debug('send failed', err);
    }
  }

  stop(): void {
    this.flush(true);
    this.stopped = true;
  }

  private debug(...args: unknown[]): void {
    if (this.cfg.debug) console.warn('[openlog]', ...args);
  }
}

/**
 * Registers the page-lifecycle flushes. `visibilitychange`→hidden is the one that actually fires on mobile,
 * where a tab is frozen rather than unloaded; `pagehide` covers the back/forward cache. `beforeunload` is
 * deliberately not used: it breaks the back/forward cache and fires unreliably.
 */
export function onPageHidden(fn: () => void): () => void {
  const onVisibility = () => {
    if (document.visibilityState === 'hidden') fn();
  };
  document.addEventListener('visibilitychange', onVisibility, true);
  window.addEventListener('pagehide', fn, true);
  return () => {
    document.removeEventListener('visibilitychange', onVisibility, true);
    window.removeEventListener('pagehide', fn, true);
  };
}
