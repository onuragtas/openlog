// A minimal DOM for node:test. jsdom would work too, but it is a heavy dependency for the handful of APIs
// the SDK touches, and a hand-written double makes it obvious which browser behaviours the tests actually
// depend on (and lets a test drive a PerformanceObserver, which jsdom does not implement anyway).

export interface FakeBeacon {
  url: string;
  body: string;
}

export interface FakeRequest {
  url: string;
  init: RequestInit | undefined;
}

export interface DomHarness {
  beacons: FakeBeacon[];
  fetches: FakeRequest[];
  /** Emits entries to every observer registered for `type`. */
  emitPerf(type: string, entries: unknown[]): void;
  /** Fires visibilitychange with visibilityState = hidden, then pagehide. */
  hidePage(): void;
  listeners: Map<string, ((ev: unknown) => void)[]>;
  restore(): void;
}

type Listener = (ev: unknown) => void;

/** Installs the globals the SDK uses and returns handles to drive them. */
export function installDom(opts: { url?: string; fetchStatus?: number } = {}): DomHarness {
  const url = new URL(opts.url ?? 'https://shop.example.com/orders/42');
  const listeners = new Map<string, Listener[]>();
  const observers = new Map<string, ((entries: unknown[]) => void)[]>();
  const beacons: FakeBeacon[] = [];
  const fetches: FakeRequest[] = [];
  const storage = new Map<string, string>();

  const addListener = (target: string) => (type: string, fn: Listener) => {
    const key = `${target}:${type}`;
    listeners.set(key, [...(listeners.get(key) ?? []), fn]);
  };
  const removeListener = (target: string) => (type: string, fn: Listener) => {
    const key = `${target}:${type}`;
    listeners.set(
      key,
      (listeners.get(key) ?? []).filter((f) => f !== fn),
    );
  };
  const fire = (key: string, ev: unknown) => {
    for (const fn of [...(listeners.get(key) ?? [])]) fn(ev);
  };

  const location = { href: url.href, pathname: url.pathname, origin: url.origin, hash: url.hash };

  function applyURL(u: URL) {
    location.href = u.href;
    location.pathname = u.pathname;
    location.origin = u.origin;
    location.hash = u.hash;
  }

  const history = {
    pushState(_s: unknown, _t: string, next?: string) {
      if (next) applyURL(new URL(next, location.href));
    },
    replaceState(_s: unknown, _t: string, next?: string) {
      if (next) applyURL(new URL(next, location.href));
    },
  };

  class FakePerformanceObserver {
    private cb: (list: { getEntries: () => unknown[] }) => void;
    constructor(cb: (list: { getEntries: () => unknown[] }) => void) {
      this.cb = cb;
    }
    observe(init: { type: string }) {
      // Unsupported types throw in real browsers; the SDK relies on that, so keep the same contract here.
      if (init.type === 'unsupported') throw new Error('unsupported');
      observers.set(init.type, [...(observers.get(init.type) ?? []), (entries) => this.cb({ getEntries: () => entries })]);
    }
    disconnect() {}
  }

  const doc = {
    visibilityState: 'visible',
    readyState: 'complete',
    addEventListener: addListener('document'),
    removeEventListener: removeListener('document'),
  };

  const fetchImpl = (input: unknown, init?: RequestInit) => {
    const u = typeof input === 'string' ? input : String(input);
    fetches.push({ url: u, init });
    return Promise.resolve({ ok: true, status: opts.fetchStatus ?? 200, json: () => Promise.resolve({}) });
  };

  const win = {
    addEventListener: addListener('window'),
    removeEventListener: removeListener('window'),
    history,
    fetch: fetchImpl,
  };

  const globals: Record<string, unknown> = {
    window: win,
    document: doc,
    location,
    history,
    navigator: {
      userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/120 Safari/537.36',
      language: 'en-GB',
      sendBeacon: (u: string, blob: { __body?: string }) => {
        beacons.push({ url: u, body: blob.__body ?? '' });
        return true;
      },
    },
    sessionStorage: {
      getItem: (k: string) => storage.get(k) ?? null,
      setItem: (k: string, v: string) => void storage.set(k, v),
      removeItem: (k: string) => void storage.delete(k),
    },
    performance: {
      now: () => 123,
      getEntriesByType: (type: string) =>
        type === 'navigation'
          ? [
              {
                responseStart: 80,
                domainLookupStart: 5,
                domainLookupEnd: 15,
                connectStart: 15,
                connectEnd: 40,
                secureConnectionStart: 25,
                responseEnd: 120,
                domInteractive: 300,
                domContentLoadedEventEnd: 350,
                loadEventEnd: 500,
              },
            ]
          : [],
    },
    PerformanceObserver: FakePerformanceObserver,
    // A Blob double that keeps the body readable, so a beacon's payload can be asserted.
    Blob: class {
      __body: string;
      type: string;
      constructor(parts: string[], options?: { type?: string }) {
        this.__body = parts.join('');
        this.type = options?.type ?? '';
      }
    },
    Headers: class {
      private m = new Map<string, string>();
      constructor(init?: Record<string, string> | Headers) {
        if (init && typeof (init as Headers).forEach === 'function') (init as Headers).forEach((v, k) => this.m.set(k.toLowerCase(), v));
        else if (init) for (const [k, v] of Object.entries(init as Record<string, string>)) this.m.set(k.toLowerCase(), v);
      }
      set(k: string, v: string) {
        this.m.set(k.toLowerCase(), v);
      }
      get(k: string) {
        return this.m.get(k.toLowerCase()) ?? null;
      }
      forEach(fn: (v: string, k: string) => void) {
        this.m.forEach(fn);
      }
    },
    fetch: fetchImpl,
  };

  // Node 22 defines `navigator`, `performance` and `fetch` as getter-only accessors on globalThis, so a
  // plain assignment throws ("Cannot set property navigator of #<Object> which has only a getter").
  // defineProperty replaces them, and restore() puts the original descriptors back.
  const originals = new Map<string, PropertyDescriptor | undefined>();
  for (const [key, value] of Object.entries(globals)) {
    originals.set(key, Object.getOwnPropertyDescriptor(globalThis, key));
    Object.defineProperty(globalThis, key, { value, configurable: true, writable: true });
  }

  return {
    beacons,
    fetches,
    listeners,
    emitPerf(type, entries) {
      for (const fn of observers.get(type) ?? []) fn(entries);
    },
    hidePage() {
      doc.visibilityState = 'hidden';
      fire('document:visibilitychange', {});
      fire('window:pagehide', {});
    },
    restore() {
      for (const [key, descriptor] of originals) {
        if (descriptor) Object.defineProperty(globalThis, key, descriptor);
        else delete (globalThis as Record<string, unknown>)[key];
      }
      originals.clear();
    },
  };
}
