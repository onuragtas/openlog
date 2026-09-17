// Trace, span and session identifiers (W3C trace context). Random ids only: the SDK never derives an id
// from anything about the visitor, so nothing here can become a tracking identifier.

const HEX = '0123456789abcdef';

/** Cryptographically strong random bytes when the platform has them, else Math.random. */
function randomBytes(n: number): Uint8Array {
  const out = new Uint8Array(n);
  const c = typeof crypto !== 'undefined' ? crypto : undefined;
  if (c && typeof c.getRandomValues === 'function') {
    c.getRandomValues(out);
    return out;
  }
  for (let i = 0; i < n; i++) out[i] = Math.floor(Math.random() * 256);
  return out;
}

function hex(n: number): string {
  const b = randomBytes(n);
  let s = '';
  for (let i = 0; i < n; i++) {
    s += HEX[(b[i] >> 4) & 0xf] + HEX[b[i] & 0xf];
  }
  return s;
}

/** 32 hex characters. */
export function traceId(): string {
  return hex(16);
}

/** 16 hex characters. */
export function spanId(): string {
  return hex(8);
}

/** 32 hex characters, the shape the backend requires for `session.id`. */
export function sessionId(): string {
  return hex(16);
}

/**
 * The W3C `traceparent` value of a sampled span: version 00, the trace id, the span id and the sampled flag.
 * The SDK only propagates for spans it is actually sending, so the flag is always 01 — claiming `01` for a
 * span that is never exported would make the backend wait for a child that never arrives.
 */
export function traceparent(trace: string, span: string): string {
  return `00-${trace}-${span}-01`;
}
