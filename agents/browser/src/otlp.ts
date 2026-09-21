// OTLP/JSON construction (docs/contracts/rum.md §2, semantic-conventions.md §10).
//
// The SDK builds OTLP by hand instead of depending on an OpenTelemetry package. That is the difference
// between a few kilobytes and a few hundred: the browser only ever produces spans of five known shapes, and
// the general SDK's exporters, context manager and processors buy nothing here.

import { spanId, traceId } from './ids.js';
import { VERSION } from './version.js';

/** RUM event kinds (`openlog.rum.event`). */
export type RumEvent = 'page_view' | 'vital' | 'error' | 'resource' | 'custom';

export interface AnyAttr {
  [key: string]: string | number | boolean | undefined | null;
}

export interface SpanInput {
  name: string;
  event: RumEvent;
  /** Milliseconds since the epoch. */
  startMs: number;
  /** Duration in milliseconds; 0 for a point-in-time event. */
  durationMs?: number;
  traceId?: string;
  spanId?: string;
  parentSpanId?: string;
  attributes?: AnyAttr;
  exception?: { type: string; message: string; stacktrace: string };
  /** OTLP status code; 2 = ERROR. The server forces this for error events anyway. */
  error?: boolean;
}

interface OtlpKeyValue {
  key: string;
  value: { stringValue: string };
}

interface OtlpEvent {
  timeUnixNano: string;
  name: string;
  attributes: OtlpKeyValue[];
}

export interface OtlpSpan {
  traceId: string;
  spanId: string;
  parentSpanId?: string;
  name: string;
  kind: number;
  startTimeUnixNano: string;
  endTimeUnixNano: string;
  attributes: OtlpKeyValue[];
  events?: OtlpEvent[];
  status?: { code: number };
}

const NANOS = '000000';

function nano(ms: number): string {
  // Milliseconds are integers here; string concatenation avoids BigInt and the 2^53 rounding that
  // `ms * 1e6` would introduce.
  return `${Math.max(0, Math.round(ms))}${NANOS}`;
}

/** Every attribute travels as a string: the backend stores span attributes as Map(String, String). */
function attrs(a: AnyAttr | undefined): OtlpKeyValue[] {
  const out: OtlpKeyValue[] = [];
  if (!a) return out;
  for (const key of Object.keys(a)) {
    const v = a[key];
    if (v === undefined || v === null || v === '') continue;
    out.push({ key, value: { stringValue: String(v) } });
  }
  return out;
}

export function buildSpan(input: SpanInput): OtlpSpan {
  const start = input.startMs;
  const end = start + (input.durationMs ?? 0);
  const span: OtlpSpan = {
    traceId: input.traceId ?? traceId(),
    spanId: input.spanId ?? spanId(),
    name: input.name,
    // INTERNAL for page views, vitals and errors; CLIENT for requests. The server assigns this too, so the
    // value here only matters for readability if it ever changes.
    kind: input.event === 'resource' ? 3 : 1,
    startTimeUnixNano: nano(start),
    endTimeUnixNano: nano(end),
    attributes: attrs(input.attributes),
  };
  if (input.parentSpanId) span.parentSpanId = input.parentSpanId;
  if (input.exception) {
    span.events = [
      {
        timeUnixNano: nano(end),
        name: 'exception',
        attributes: attrs({
          'exception.type': input.exception.type,
          'exception.message': input.exception.message,
          'exception.stacktrace': input.exception.stacktrace,
        }),
      },
    ];
  }
  if (input.error) span.status = { code: 2 };
  return span;
}

export interface ResourceInfo {
  serviceName: string;
  environment: string;
}

/**
 * The OTLP/JSON body of one batch. The resource is sent for completeness and debuggability, but the server
 * rebuilds it from the key (rum.Sanitize): nothing here is trusted.
 */
export function buildPayload(spans: OtlpSpan[], info: ResourceInfo): string {
  const resource: AnyAttr = {
    'service.name': info.serviceName,
    'deployment.environment.name': info.environment,
    'telemetry.sdk.name': 'openlog-browser',
    'telemetry.sdk.language': 'webjs',
    'telemetry.sdk.version': VERSION,
  };
  const nav = typeof navigator !== 'undefined' ? navigator : undefined;
  if (nav) {
    resource['user_agent.original'] = nav.userAgent;
    resource['browser.language'] = nav.language;
    const data = (nav as Navigator & { userAgentData?: { mobile?: boolean; platform?: string } }).userAgentData;
    if (data) {
      if (typeof data.mobile === 'boolean') resource['browser.mobile'] = data.mobile;
      if (data.platform) resource['browser.platform'] = data.platform;
    }
  }
  return JSON.stringify({
    resourceSpans: [
      {
        resource: { attributes: attrs(resource) },
        scopeSpans: [{ scope: { name: 'openlog-browser', version: VERSION }, spans }],
      },
    ],
  });
}
