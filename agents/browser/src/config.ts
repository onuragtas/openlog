// Configuration of the openlog browser SDK (docs/contracts/rum.md §1).

/** What a fetch/XHR call must match for the SDK to add W3C trace headers to it. */
export type TracePropagationTarget = string | RegExp;

export interface OpenLogBrowserOptions {
  /**
   * The browser key (`olb_…`). It is public by design: it ships in the page, anyone can read it, and the
   * server bounds what it can do (rum.md §3). Never put an ingest license key here — it would not work, and
   * it would be a real secret in a public place.
   */
  key: string;
  /** OTLP/HTTP base URL of openlog ingest, e.g. `https://ingest.example.com:4318`. */
  endpoint: string;
  /**
   * Informational only. The application a payload is stored under is taken from the key server-side, so
   * setting this wrong changes nothing; it exists so logs and debugging show what the page intended.
   */
  serviceName?: string;
  environment?: string;
  /**
   * Share of sessions to keep, 0–1. The server's value (from the key) wins once `GET /v1/rum/config`
   * answers; this is the value used until then, and the fallback when the call fails.
   */
  sampleRate?: number;
  /**
   * Which requests get the W3C `traceparent` header. `'same-origin'` (the default) is the only setting that
   * is safe without thinking: adding trace headers to a cross-origin request makes the browser preflight it,
   * and sends your trace ids to a third party. List origins explicitly to extend it to your own APIs.
   */
  propagateTraceHeaders?: 'same-origin' | TracePropagationTarget[];
  /** Events buffered before an early flush. */
  maxBatchSize?: number;
  /** How often a non-empty queue is flushed. */
  flushIntervalMs?: number;
  captureVitals?: boolean;
  capturePageViews?: boolean;
  captureErrors?: boolean;
  /** Also report `console.error(...)` calls. Off by default: it is noisy and often intentional. */
  captureConsoleErrors?: boolean;
  captureRequests?: boolean;
  /**
   * Also time the static assets the page loaded — scripts, stylesheets, images, fonts — not only fetch and
   * XHR. **Off by default**, and that is about volume rather than value: a page loads an order of magnitude
   * more assets than it makes requests, so turning this on without meaning to would multiply what an
   * installation stores and is billed for. Only the slowest few of each page view are sent when it is on.
   */
  captureResources?: boolean;
  /** Log what the SDK does to the console. Never on by default. */
  debug?: boolean;
}

export interface ResolvedConfig extends Required<Omit<OpenLogBrowserOptions, 'propagateTraceHeaders'>> {
  propagateTraceHeaders: 'same-origin' | TracePropagationTarget[];
  /** `${endpoint}/v1/rum` */
  rumUrl: string;
  /** `${endpoint}/v1/rum/config` */
  configUrl: string;
}

const DEFAULTS = {
  serviceName: '',
  environment: '',
  sampleRate: 1,
  maxBatchSize: 32,
  flushIntervalMs: 5000,
  captureVitals: true,
  capturePageViews: true,
  captureErrors: true,
  captureConsoleErrors: false,
  captureRequests: true,
  captureResources: false,
  debug: false,
};

export class ConfigError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'ConfigError';
  }
}

function trimSlash(v: string): string {
  return v.replace(/\/+$/, '');
}

/** Validates and fills in the options. Throws ConfigError, which init() catches so a page never breaks. */
export function resolveConfig(options: OpenLogBrowserOptions): ResolvedConfig {
  const key = (options.key ?? '').trim();
  if (!key) throw new ConfigError('openlog: `key` is required (the browser key, olb_…)');
  if (key.startsWith('olk_')) {
    throw new ConfigError('openlog: `key` looks like an ingest license key (olk_…). Create a browser key instead: it is meant to be public.');
  }
  const endpoint = trimSlash((options.endpoint ?? '').trim());
  if (!endpoint) throw new ConfigError('openlog: `endpoint` is required, e.g. https://ingest.example.com:4318');
  if (!/^https?:\/\//.test(endpoint)) throw new ConfigError(`openlog: \`endpoint\` must start with http:// or https://, got ${endpoint}`);

  const sampleRate = options.sampleRate ?? DEFAULTS.sampleRate;
  if (!(sampleRate > 0 && sampleRate <= 1)) throw new ConfigError('openlog: `sampleRate` must be greater than 0 and at most 1');

  const maxBatchSize = options.maxBatchSize ?? DEFAULTS.maxBatchSize;
  if (!(maxBatchSize >= 1 && maxBatchSize <= 1000)) throw new ConfigError('openlog: `maxBatchSize` must be between 1 and 1000');

  const flushIntervalMs = options.flushIntervalMs ?? DEFAULTS.flushIntervalMs;
  if (!(flushIntervalMs >= 500 && flushIntervalMs <= 60000)) throw new ConfigError('openlog: `flushIntervalMs` must be between 500 and 60000');

  return {
    ...DEFAULTS,
    ...options,
    key,
    endpoint,
    sampleRate,
    maxBatchSize,
    flushIntervalMs,
    propagateTraceHeaders: options.propagateTraceHeaders ?? 'same-origin',
    rumUrl: `${endpoint}/v1/rum`,
    configUrl: `${endpoint}/v1/rum/config`,
  };
}
