// openlog browser SDK (docs/contracts/rum.md).
//
//   import { init } from 'openlog-browser';
//   init({ key: 'olb_…', endpoint: 'https://ingest.example.com:4318' });
//
// Nothing here may ever throw into the page: init() catches its own configuration errors and every callback
// is wrapped, because a monitoring library that breaks the site it monitors is worse than no monitoring.

import { ConfigError, resolveConfig, type OpenLogBrowserOptions, type ResolvedConfig } from './config.js';
import { captureErrors } from './errors.js';
import { cleanURL, instrumentRequests, type RequestSpan } from './fetch.js';
import { afterLoad, navigationTimings, onRouteChange } from './navigation.js';
import { buildSpan, type AnyAttr, type OtlpSpan, type RumEvent } from './otlp.js';
import { SessionState } from './session.js';
import { onPageHidden, Transport } from './transport.js';
import { collectVitals, type VitalSample } from './vitals.js';
import { VERSION } from './version.js';

export type { OpenLogBrowserOptions, TracePropagationTarget } from './config.js';
export { VERSION };

export interface OpenLogBrowser {
  /** Sends everything buffered now. */
  flush(): void;
  /** Removes every listener and patch, and flushes. */
  shutdown(): void;
  /** The current session id, or "" when the session was not sampled. */
  sessionId(): string;
  /** Reports an error the application caught itself. */
  recordError(error: unknown): void;
  /**
   * Records an application-defined event, e.g. `recordEvent('checkout_started', { plan: 'pro' })`.
   *
   * Parameters are stored under `openlog.rum.custom.param.<key>` — a namespace of their own, so they can
   * never collide with a field openlog later learns to interpret. At most 16 are kept per event; keys are
   * `[a-z0-9_.-]`, values are stored as text. Query with OQL:
   * `FROM Span WHERE openlog.rum.custom.name = 'checkout_started' FACET openlog.rum.custom.param.plan`.
   */
  recordEvent(name: string, params?: CustomParams): void;
  /** Records an application-defined timing in milliseconds, e.g. `recordTiming('cart_priced', 42)`. */
  recordTiming(name: string, milliseconds: number, params?: CustomParams): void;
}

const NOOP: OpenLogBrowser = {
  flush() {},
  shutdown() {},
  sessionId: () => '',
  recordError() {},
  recordEvent() {},
  recordTiming() {},
};

let active: OpenLogBrowser | null = null;

/**
 * Starts the SDK. Calling it twice returns the first instance: a second agent would double-count every page
 * view, and the usual cause is a framework mounting twice rather than a deliberate second install.
 */
export function init(options: OpenLogBrowserOptions): OpenLogBrowser {
  if (active) return active;
  if (typeof window === 'undefined' || typeof document === 'undefined') return NOOP; // SSR
  let cfg: ResolvedConfig;
  try {
    cfg = resolveConfig(options);
  } catch (err) {
    if (err instanceof ConfigError) console.warn(err.message);
    else console.warn('[openlog] not started:', err);
    return NOOP;
  }
  try {
    active = start(cfg);
  } catch (err) {
    if (cfg.debug) console.warn('[openlog] not started:', err);
    return NOOP;
  }
  return active;
}

function start(cfg: ResolvedConfig): OpenLogBrowser {
  const session = new SessionState(cfg.sampleRate);
  const transport = new Transport(cfg);
  const teardown: (() => void)[] = [];

  // The server's sample rate (from the key) wins, so an operator can turn the volume down without a deploy.
  // The page reports with the local rate until this answers; failure is silent by design.
  void fetch(cfg.configUrl, { headers: { 'openlog-browser-key': cfg.key }, credentials: 'omit', mode: 'cors' })
    .then((r) => (r.ok ? (r.json() as Promise<{ service_name?: string; environment?: string; sample_rate?: number }>) : null))
    .then((conf) => {
      if (!conf) return;
      transport.setResource(conf.service_name ?? cfg.serviceName, conf.environment ?? cfg.environment);
      if (typeof conf.sample_rate === 'number') session.applyServerSampleRate(conf.sample_rate);
    })
    .catch(() => undefined);

  /** Common attributes of every span of the current page view. */
  const common = (): AnyAttr => {
    const { full, host } = cleanURL(location.href);
    return {
      'session.id': session.id,
      'openlog.rum.page_view.id': session.pageViewId,
      'url.path': location.pathname,
      'url.full': full,
      'url.domain': host,
      'device.type': deviceType(),
    };
  };

  const emit = (event: RumEvent, name: string, opts: Partial<Parameters<typeof buildSpan>[0]> = {}) => {
    if (!session.sampled) return;
    try {
      session.touch();
      const span: OtlpSpan = buildSpan({
        name,
        event,
        startMs: opts.startMs ?? Date.now(),
        durationMs: opts.durationMs ?? 0,
        traceId: opts.traceId ?? session.traceId,
        spanId: opts.spanId,
        // Vitals, errors and requests hang off the page view, so one trace holds the whole page.
        parentSpanId: opts.parentSpanId ?? (event === 'page_view' ? undefined : session.pageViewSpanId),
        attributes: { 'openlog.rum.event': event, ...common(), ...opts.attributes },
        exception: opts.exception,
        error: opts.error,
      });
      transport.add(span);
    } catch (err) {
      if (cfg.debug) console.warn('[openlog] dropped an event:', err);
    }
  };

  // ---- web vitals ----
  // Declared before the page-view wiring below, which finalizes them on an SPA route change.
  const vitals = cfg.captureVitals
    ? collectVitals((v: VitalSample) =>
        emit('vital', `vital ${v.name}`, { attributes: { 'openlog.rum.vital.name': v.name, 'openlog.rum.vital.value': v.value } }),
      )
    : { finalize: () => {}, stop: () => {} };
  teardown.push(vitals.stop);

  // ---- page views ----
  const sendPageView = (kind: 'load' | 'route_change', durationMs: number, attributes: AnyAttr = {}) => {
    emit('page_view', `pageview ${location.pathname}`, {
      durationMs,
      spanId: session.pageViewSpanId,
      attributes: { 'openlog.rum.page_view.kind': kind, ...attributes },
    });
  };

  if (cfg.capturePageViews) {
    afterLoad(() => {
      const { timings, durationMs } = navigationTimings();
      sendPageView('load', durationMs, timings as AnyAttr);
    });
    teardown.push(
      onRouteChange(() => {
        // A route change is a new page view, and therefore a new trace: the requests it causes belong to it,
        // not to the document load that happened minutes ago.
        vitals.finalize();
        session.newPageView();
        sendPageView('route_change', 0);
      }),
    );
  }

  // ---- errors ----
  if (cfg.captureErrors) {
    teardown.push(
      captureErrors((e) => {
        emit('error', `error ${e.type}`, {
          error: true,
          exception: { type: e.type, message: e.message, stacktrace: e.stacktrace },
          attributes: { 'openlog.rum.error.source': e.source },
        });
      }, cfg.captureConsoleErrors),
    );
  }

  // ---- fetch / XHR ----
  if (cfg.captureRequests) {
    teardown.push(
      instrumentRequests({
        targets: cfg.propagateTraceHeaders,
        context: () => (session.sampled ? { traceId: session.traceId, parentSpanId: session.pageViewSpanId } : null),
        report: (s: RequestSpan) => {
          const { full, host } = cleanURL(s.url);
          emit('resource', `${s.method} ${new URL(full, location.href).pathname}`, {
            startMs: s.startMs,
            durationMs: s.durationMs,
            spanId: s.spanId,
            error: s.status === 0 || s.status >= 500,
            attributes: {
              'http.request.method': s.method,
              'http.response.status_code': s.status || undefined,
              'server.address': host,
              'url.full': full,
              'error.type': s.errorType,
            },
          });
        },
      }),
    );
  }

  // The page going away is the only moment LCP, CLS and INP are final, and the last chance to send.
  teardown.push(
    onPageHidden(() => {
      vitals.finalize();
      transport.flush(true);
    }),
  );

  return {
    flush: () => transport.flush(),
    shutdown: () => {
      for (const fn of teardown.reverse()) {
        try {
          fn();
        } catch {
          /* ignore */
        }
      }
      transport.stop();
      active = null;
    },
    sessionId: () => (session.sampled ? session.id : ''),
    recordError: (error: unknown) => {
      const e = error instanceof Error ? error : new Error(String(error));
      emit('error', `error ${e.name}`, {
        error: true,
        exception: { type: e.name, message: e.message, stacktrace: e.stack ?? '' },
        attributes: { 'openlog.rum.error.source': 'error' },
      });
    },
    recordEvent: (name: string, params?: CustomParams) => {
      const n = String(name ?? '').trim();
      if (!n) return; // an unnamed event is an unqueryable row, and the server refuses it anyway
      emit('custom', n, { attributes: { 'openlog.rum.custom.name': n, ...customParams(params) } });
    },
    recordTiming: (name: string, milliseconds: number, params?: CustomParams) => {
      const n = String(name ?? '').trim();
      if (!n || !Number.isFinite(milliseconds)) return;
      emit('custom', n, {
        durationMs: milliseconds,
        attributes: {
          'openlog.rum.custom.name': n,
          'openlog.rum.custom.value': milliseconds,
          'openlog.rum.custom.unit': 'ms',
          ...customParams(params),
        },
      });
    },
  };
}

/** Parameters of a custom event: the application's own vocabulary, stored in a namespace of its own. */
export type CustomParams = Record<string, string | number | boolean>;

const CUSTOM_PARAM_PREFIX = 'openlog.rum.custom.param.';
const MAX_CUSTOM_PARAMS = 16;

/**
 * customParams prefixes and bounds the caller's parameters. Keys are lower-cased and limited to
 * [a-z0-9_.-]; anything else is dropped here because the server drops it there — sending it would only cost
 * bandwidth to produce the same result.
 */
function customParams(params?: CustomParams): Record<string, string> {
  if (!params) return {};
  const out: Record<string, string> = {};
  let n = 0;
  for (const [rawKey, value] of Object.entries(params)) {
    if (n >= MAX_CUSTOM_PARAMS) break;
    if (value === undefined || value === null) continue;
    const key = rawKey.trim().toLowerCase();
    if (!/^[a-z0-9_.-]{1,64}$/.test(key)) continue;
    out[CUSTOM_PARAM_PREFIX + key] = String(value);
    n++;
  }
  return out;
}

/** Coarse device class, from the user agent hints when present and the user agent string otherwise. */
function deviceType(): 'mobile' | 'tablet' | 'desktop' {
  const ua = typeof navigator !== 'undefined' ? navigator.userAgent : '';
  if (/\bipad\b|\btablet\b|\b(android)(?!.*\bmobile\b)/i.test(ua)) return 'tablet';
  const data = (navigator as Navigator & { userAgentData?: { mobile?: boolean } }).userAgentData;
  if (data && typeof data.mobile === 'boolean') return data.mobile ? 'mobile' : 'desktop';
  if (/\bmobile\b|\biphone\b|\bipod\b|\bandroid\b/i.test(ua)) return 'mobile';
  return 'desktop';
}
