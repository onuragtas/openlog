// Uncaught errors and unhandled promise rejections (docs/contracts/rum.md §2.3).
//
// The stack trace is what makes a JS error actionable, so it is always sent (bounded server-side). The
// backend groups these with the same fingerprint it uses for backend exceptions (apm.md §3.1), whose frame
// normalization already strips content hashes from bundle file names — `main.3f2a1b9c.js` and
// `main.8c1d2e4f.js` are the same frame — so a deploy does not reopen every error group.

export interface CapturedError {
  type: string;
  message: string;
  stacktrace: string;
  source: 'error' | 'unhandledrejection' | 'console';
}

function describe(value: unknown): { type: string; message: string; stacktrace: string } {
  if (value instanceof Error) {
    return { type: value.name || 'Error', message: value.message || String(value), stacktrace: value.stack ?? '' };
  }
  if (typeof value === 'object' && value !== null) {
    // A rejected non-Error (a fetch Response, a plain object) still deserves to be reported.
    const o = value as { name?: unknown; message?: unknown; stack?: unknown };
    const message = typeof o.message === 'string' ? o.message : safeJSON(value);
    return {
      type: typeof o.name === 'string' && o.name ? o.name : 'Object',
      message,
      stacktrace: typeof o.stack === 'string' ? o.stack : '',
    };
  }
  return { type: typeof value === 'string' ? 'Error' : typeof value, message: String(value), stacktrace: '' };
}

function safeJSON(v: unknown): string {
  try {
    return JSON.stringify(v) ?? String(v);
  } catch {
    return String(v);
  }
}

/**
 * Registers the error listeners and returns a function that removes them.
 *
 * `window.onerror` is not assigned; an `error` event listener is added instead, so an application that sets
 * its own onerror handler is not overwritten.
 */
export function captureErrors(report: (e: CapturedError) => void, includeConsole: boolean): () => void {
  const onError = (ev: ErrorEvent) => {
    const d = ev.error !== undefined && ev.error !== null ? describe(ev.error) : { type: 'Error', message: ev.message, stacktrace: '' };
    if (!d.stacktrace && ev.filename) {
      // No Error object (a cross-origin script, an old browser): synthesise the one frame we do know.
      d.stacktrace = `    at ${ev.filename}:${ev.lineno ?? 0}:${ev.colno ?? 0}`;
    }
    report({ ...d, source: 'error' });
  };
  const onRejection = (ev: PromiseRejectionEvent) => {
    report({ ...describe(ev.reason), source: 'unhandledrejection' });
  };
  window.addEventListener('error', onError, true);
  window.addEventListener('unhandledrejection', onRejection, true);

  let restoreConsole: (() => void) | undefined;
  if (includeConsole) {
    const original = console.error;
    console.error = function patchedError(...args: unknown[]) {
      try {
        const first = args.find((a) => a instanceof Error) ?? args[0];
        const d = describe(first);
        report({ ...d, message: d.message || args.map(String).join(' '), source: 'console' });
      } catch {
        /* never let reporting break console.error */
      }
      original.apply(console, args as []);
    };
    restoreConsole = () => {
      console.error = original;
    };
  }

  return () => {
    window.removeEventListener('error', onError, true);
    window.removeEventListener('unhandledrejection', onRejection, true);
    restoreConsole?.();
  };
}
