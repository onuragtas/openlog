// fetch and XMLHttpRequest instrumentation with W3C trace propagation (docs/contracts/rum.md §6).
//
// This is what makes RUM full-stack rather than a second, disconnected product: the browser span is the
// parent of the backend's server span, so one trace runs from the click to the database and both directions
// of "why was this slow" are answerable.
//
// Propagation is opt-in per target and defaults to same-origin, for two reasons that are easy to get wrong:
// adding a header to a cross-origin request turns it into a preflighted request (a second round trip, and a
// hard failure if the other server does not allow the header), and it hands your trace ids to whoever runs
// that server.

import type { TracePropagationTarget } from './config.js';
import { spanId, traceparent } from './ids.js';

export interface RequestSpan {
  method: string;
  url: string;
  status: number;
  startMs: number;
  durationMs: number;
  spanId: string;
  errorType?: string;
}

function sameOrigin(url: string): boolean {
  try {
    return new URL(url, location.href).origin === location.origin;
  } catch {
    return false;
  }
}

export function shouldPropagate(url: string, targets: 'same-origin' | TracePropagationTarget[]): boolean {
  if (targets === 'same-origin') return sameOrigin(url);
  let absolute: string;
  try {
    absolute = new URL(url, location.href).href;
  } catch {
    return false;
  }
  return targets.some((t) => (typeof t === 'string' ? absolute.startsWith(t) : t.test(absolute)));
}

/** The URL as stored: no query string and no fragment, because that is where tokens and personal data live. */
export function cleanURL(url: string): { full: string; host: string } {
  try {
    const u = new URL(url, location.href);
    return { full: `${u.origin}${u.pathname}`, host: u.host };
  } catch {
    return { full: url, host: '' };
  }
}

interface Hooks {
  /** The current page view's trace and span, which every request becomes a child of. */
  context: () => { traceId: string; parentSpanId: string } | null;
  report: (s: RequestSpan) => void;
  targets: 'same-origin' | TracePropagationTarget[];
}

/** Patches fetch and XMLHttpRequest; returns a function that restores them. */
export function instrumentRequests(hooks: Hooks): () => void {
  const restore: (() => void)[] = [];

  // Patched on globalThis, not on `window`: they are the same object in a document, but a worker has no
  // `window`, and patching the wrong one fails silently — the call is simply never traced.
  const scope = globalThis as { fetch?: typeof fetch };
  if (typeof scope.fetch === 'function') {
    const original = scope.fetch;
    scope.fetch = function patchedFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
      const ctx = hooks.context();
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
      const method = (init?.method ?? (typeof input === 'object' && 'method' in input ? input.method : 'GET') ?? 'GET').toUpperCase();
      if (!ctx) return original.call(globalThis, input as RequestInfo, init);

      const id = spanId();
      const startMs = Date.now();
      const started = performance.now();
      let nextInit = init;
      if (shouldPropagate(url, hooks.targets)) {
        // Headers are copied, never mutated in place: the caller may reuse the object.
        const headers = new Headers(init?.headers ?? (typeof input === 'object' && 'headers' in input ? input.headers : undefined));
        headers.set('traceparent', traceparent(ctx.traceId, id));
        nextInit = { ...init, headers };
      }
      const finish = (status: number, errorType?: string) => {
        hooks.report({ method, url, status, startMs, durationMs: performance.now() - started, spanId: id, errorType });
      };
      return original.call(globalThis, input as RequestInfo, nextInit).then(
        (res) => {
          finish(res.status);
          return res;
        },
        (err: unknown) => {
          // A network failure has no status; 0 is what the browser itself reports for one.
          finish(0, err instanceof Error ? err.name : 'Error');
          throw err;
        },
      );
    };
    restore.push(() => {
      scope.fetch = original;
    });
  }

  if (typeof XMLHttpRequest === 'function') {
    const proto = XMLHttpRequest.prototype;
    const origOpen = proto.open;
    const origSend = proto.send;
    interface Tracked extends XMLHttpRequest {
      __openlog?: { method: string; url: string; id: string; startMs: number; started: number };
    }
    proto.open = function patchedOpen(this: Tracked, method: string, url: string | URL, ...rest: unknown[]) {
      this.__openlog = { method: String(method).toUpperCase(), url: String(url), id: spanId(), startMs: 0, started: 0 };
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      return (origOpen as any).call(this, method, url, ...rest);
    } as typeof proto.open;
    proto.send = function patchedSend(this: Tracked, body?: Document | XMLHttpRequestBodyInit | null) {
      const t = this.__openlog;
      const ctx = hooks.context();
      if (t && ctx) {
        t.startMs = Date.now();
        t.started = performance.now();
        if (shouldPropagate(t.url, hooks.targets)) {
          try {
            this.setRequestHeader('traceparent', traceparent(ctx.traceId, t.id));
          } catch {
            /* header already sent, or a forbidden header name */
          }
        }
        this.addEventListener('loadend', () => {
          hooks.report({
            method: t.method,
            url: t.url,
            status: this.status,
            startMs: t.startMs,
            durationMs: performance.now() - t.started,
            spanId: t.id,
            errorType: this.status === 0 ? 'NetworkError' : undefined,
          });
        });
      }
      return origSend.call(this, body ?? null);
    } as typeof proto.send;
    restore.push(() => {
      proto.open = origOpen;
      proto.send = origSend;
    });
  }

  return () => {
    for (const fn of restore) fn();
  };
}
