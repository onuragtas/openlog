import {
  context as apiContext,
  isSpanContextValid,
  trace,
  type Attributes,
  type Context,
  type Link,
  type Span,
  type SpanKind,
  type SpanOptions,
  type TraceState as ApiTraceState,
  type Tracer,
  type TracerOptions,
  type TracerProvider,
} from '@opentelemetry/api';
import { TraceState } from '@opentelemetry/core';
import {
  AlwaysOffSampler,
  AlwaysOnSampler,
  ParentBasedSampler,
  SamplingDecision,
  type Sampler,
  type SamplingResult,
} from '@opentelemetry/sdk-trace-base';

// Consistent probability sampling with the OpenTelemetry tracestate `ot` entry
// (https://opentelemetry.io/docs/specs/otel/trace/tracestate-probability-sampling/), a port of the Go agent's
// sampler.go. Cross-language fixtures produced by the Go agent: test/interop/go-sampler-fixtures.json.

/**
 * Span attribute carrying the head sampling probability of a trace root sampled by this agent, or of a local entry
 * span whose remote parent was sampled with a probability below 1 (tracestate ot=th). Only set when p < 1. The APM
 * backend weights RED metrics by 1/p (apm.md §4).
 */
export const SAMPLING_RATIO_KEY = 'sampling.ratio';

const OT_KEY = 'ot';
const MAX_OT_VALUE_LEN = 256;
const MAX_MEMBERS = 32;
export const MAX_THRESHOLD = 1n << 56n; // exclusive; T = 2^56 would mean p = 0
const RANDOMNESS_MASK = MAX_THRESHOLD - 1n;
const TRACE_FLAG_RANDOM = 0x02;

/** Returns 56 random bits and their ot=rv encoding (14 hex digits). */
export type RandomnessSource = () => [bigint, string];

export const defaultRandomness: RandomnessSource = () => {
  // crypto-grade randomness is not required; two 28-bit halves avoid Number precision limits
  const hi = BigInt(Math.floor(Math.random() * 0x10000000));
  const lo = BigInt(Math.floor(Math.random() * 0x10000000));
  const r = (hi << 28n) | lo;
  return [r, r.toString(16).padStart(14, '0')];
};

export type OTField = [key: string, value: string];

/** Parses the `ot` value ("k1:v1;k2:v2"). */
export function parseOT(s: string | undefined): OTField[] {
  if (!s) return [];
  const out: OTField[] = [];
  for (const part of s.split(';')) {
    const i = part.indexOf(':');
    if (i > 0) out.push([part.slice(0, i), part.slice(i + 1)]);
  }
  return out;
}

const otGet = (o: OTField[], k: string): string | undefined => o.find((f) => f[0] === k)?.[1];
const otWithout = (o: OTField[], k: string): OTField[] => o.filter((f) => f[0] !== k);
const otWith = (o: OTField[], k: string, v: string): OTField[] => [[k, v], ...otWithout(o, k)];
export const otString = (o: OTField[]): string => o.map((f) => `${f[0]}:${f[1]}`).join(';');

/** Explicit 56-bit randomness ot=rv:<14 hex digits>. */
export function otRandomness(o: OTField[]): bigint | undefined {
  const v = otGet(o, 'rv');
  if (v === undefined || !/^[0-9a-fA-F]{14}$/.test(v)) return undefined;
  return BigInt('0x' + v);
}

/** Encodes T as up to 14 hex digits without trailing zeros ("0" for T=0). */
export function encodeThreshold(t: bigint): string {
  return t.toString(16).padStart(14, '0').replace(/0+$/, '') || '0';
}

/** Decodes th:<hex> (1..14 hex digits, right-padded with zeros). */
export function parseThreshold(s: string): bigint | undefined {
  if (!/^[0-9a-fA-F]{1,14}$/.test(s)) return undefined;
  return BigInt('0x' + s.padEnd(14, '0'));
}

/** p from th:<hex> (p = 1 − T/2^56) or legacy p:<n> (p = 2^−n). */
export function otProbability(o: OTField[]): number | undefined {
  const th = otGet(o, 'th');
  if (th !== undefined) {
    const t = parseThreshold(th);
    if (t !== undefined) return 1 - Number(t) / Number(MAX_THRESHOLD);
  }
  const p = otGet(o, 'p');
  if (p !== undefined && /^[+-]?\d+$/.test(p)) {
    const n = parseInt(p, 10);
    if (n >= 0 && n <= 63) return n === 63 ? 0 : Math.pow(2, -n);
  }
  return undefined;
}

// W3C tracestate value: printable ASCII except ',' and '=', not ending with a space, at most 256 characters.
const VALID_VALUE = /^[\x20-\x2b\x2d-\x3c\x3e-\x7e]{0,255}[\x21-\x2b\x2d-\x3c\x3e-\x7e]$/;

/**
 * Stores `o` as the `ot` member of `ts` (moved to the front), keeping W3C limits: the value is at most 256 characters
 * (sub-keys other than th and rv are dropped, last first) and the list keeps at most 32 members (the right-most
 * member is dropped). An empty `o` removes the member. On an invalid value `ts` is returned unchanged.
 */
export function withOT(ts: ApiTraceState | undefined, o: OTField[]): ApiTraceState | undefined {
  let fields = o;
  while (otString(fields).length > MAX_OT_VALUE_LEN) {
    let i = fields.length - 1;
    while (i >= 0 && (fields[i][0] === 'th' || fields[i][0] === 'rv')) i--;
    if (i < 0) return ts;
    fields = [...fields.slice(0, i), ...fields.slice(i + 1)];
  }
  if (fields.length === 0) {
    if (!ts || ts.get(OT_KEY) === undefined) return ts;
    return ts.unset(OT_KEY);
  }
  const value = otString(fields);
  if (!VALID_VALUE.test(value)) return ts;
  const others = ts
    ? ts
        .serialize()
        .split(',')
        .map((m) => m.trim())
        .filter((m) => m && !m.startsWith(OT_KEY + '='))
    : [];
  return new TraceState([`${OT_KEY}=${value}`, ...others].slice(0, MAX_MEMBERS).join(','));
}

function parentTraceState(ctx: Context): ApiTraceState | undefined {
  return trace.getSpanContext(ctx)?.traceState;
}

/** Root sampler for 0 < ratio < 1 (threshold comparison against trace id or ot=rv randomness). */
export class ConsistentRatioRootSampler implements Sampler {
  readonly threshold: bigint;
  readonly th: string;

  constructor(
    readonly ratio: number,
    private readonly writeRV: boolean,
    private readonly randomness: RandomnessSource,
  ) {
    // ratio·2^56 is exact in float64 (power-of-two scaling); 1-ratio would not be.
    let keep = BigInt(Math.round(ratio * Number(MAX_THRESHOLD)));
    if (keep === 0n) keep = 1n;
    this.threshold = MAX_THRESHOLD - keep;
    this.th = encodeThreshold(this.threshold);
  }

  shouldSample(ctx: Context, traceId: string): SamplingResult {
    const ts = parentTraceState(ctx);
    let ot = parseOT(ts?.get(OT_KEY));
    let r = otRandomness(ot);
    if (r === undefined) {
      if (this.writeRV) {
        const [rnd, rv] = this.randomness();
        r = rnd;
        ot = [...otWithout(ot, 'rv'), ['rv', rv]];
      } else {
        r = BigInt('0x' + traceId.slice(18, 32)) & RANDOMNESS_MASK;
      }
    }
    if (r < this.threshold) {
      return { decision: SamplingDecision.NOT_RECORD, traceState: withOT(ts, otWithout(ot, 'th')) };
    }
    return {
      decision: SamplingDecision.RECORD_AND_SAMPLED,
      attributes: { [SAMPLING_RATIO_KEY]: this.ratio },
      traceState: withOT(ts, otWith(ot, 'th', this.th)),
    };
  }

  toString(): string {
    return `OpenlogConsistentRatio{${this.ratio}}`;
  }
}

/** Adds ot=rv to roots decided by another sampler (ratio 0 or 1). */
class RVRootSampler implements Sampler {
  constructor(
    private readonly inner: Sampler,
    private readonly randomness: RandomnessSource,
  ) {}

  shouldSample(ctx: Context, traceId: string, name: string, kind: SpanKind, attrs: Attributes, links: Link[]): SamplingResult {
    const res = this.inner.shouldSample(ctx, traceId, name, kind, attrs, links);
    const ts = res.traceState ?? parentTraceState(ctx);
    const ot = parseOT(ts?.get(OT_KEY));
    if (otRandomness(ot) !== undefined) return res;
    const [, rv] = this.randomness();
    return { ...res, traceState: withOT(ts, [...otWithout(ot, 'rv'), ['rv', rv]]) };
  }

  toString(): string {
    return `OpenlogRV{${this.inner.toString()}}`;
  }
}

/** Samples every span whose remote parent is sampled and records the upstream sampling probability on it. */
export class RemoteParentSampledSampler implements Sampler {
  shouldSample(ctx: Context): SamplingResult {
    const ts = parentTraceState(ctx);
    const res: SamplingResult = { decision: SamplingDecision.RECORD_AND_SAMPLED, traceState: ts };
    const p = otProbability(parseOT(ts?.get(OT_KEY)));
    if (p !== undefined && p > 0 && p < 1) return { ...res, attributes: { [SAMPLING_RATIO_KEY]: p } };
    return res;
  }

  toString(): string {
    return 'OpenlogRemoteParentSampled';
  }
}

/**
 * Parent-based sampler with the Go agent's semantics:
 *  - new traces are sampled with probability `ratio` by comparing the 56-bit randomness (tracestate ot=rv, else the
 *    lower 56 bits of the trace id) against T = (1 − ratio)·2^56; sampled roots carry sampling.ratio and ot=th:<T>;
 *  - children of a sampled remote parent are sampled and get sampling.ratio = p when tracestate carries p < 1;
 *  - other children follow their parent. With `writeRV`, roots without ot=rv write explicit randomness.
 */
export function createSampler(ratio: number, writeRV = false, randomness: RandomnessSource = defaultRandomness): Sampler {
  let root: Sampler;
  let keepAll = ratio >= 1 || Number.isNaN(ratio);
  if (!keepAll && ratio > 0 && BigInt(Math.round(ratio * Number(MAX_THRESHOLD))) >= MAX_THRESHOLD) keepAll = true;
  if (keepAll) root = new AlwaysOnSampler();
  else if (ratio <= 0) root = new AlwaysOffSampler();
  else root = new ConsistentRatioRootSampler(ratio, writeRV, randomness);
  if (writeRV && !(root instanceof ConsistentRatioRootSampler)) root = new RVRootSampler(root, randomness);
  return new ParentBasedSampler({ root, remoteParentSampled: new RemoteParentSampledSampler() });
}

/**
 * Sets the W3C Trace Context Level 2 random flag (traceparent flags 0x02) like the Go agent's randomTracerProvider:
 * root spans get it (the SDK's trace ids are random), children inherit it from their parent, so a remote W3C Level 1
 * parent without it is continued unchanged. The JS SDK derives trace flags from the sampling decision only, so the
 * flag is added to the new span context right after the SDK created it (before it is propagated or exported).
 */
export class RandomFlagTracerProvider implements TracerProvider {
  private readonly tracers = new Map<string, Tracer>();

  constructor(readonly delegate: TracerProvider) {}

  getTracer(name: string, version?: string, options?: TracerOptions): Tracer {
    const key = `${name}@${version ?? ''}:${options?.schemaUrl ?? ''}`;
    let t = this.tracers.get(key);
    if (!t) {
      t = new RandomFlagTracer(this.delegate.getTracer(name, version, options));
      this.tracers.set(key, t);
    }
    return t;
  }
}

export class RandomFlagTracer implements Tracer {
  constructor(private readonly delegate: Tracer) {}

  startSpan(name: string, options?: SpanOptions, ctx: Context = apiContext.active()): Span {
    const parent = options?.root ? undefined : trace.getSpanContext(ctx);
    const span = this.delegate.startSpan(name, options, ctx);
    const sc = span.spanContext();
    if (isSpanContextValid(sc) && (sc.traceFlags & TRACE_FLAG_RANDOM) === 0) {
      const random = parent && isSpanContextValid(parent) ? (parent.traceFlags & TRACE_FLAG_RANDOM) !== 0 : true;
      if (random) (sc as { traceFlags: number }).traceFlags = sc.traceFlags | TRACE_FLAG_RANDOM;
    }
    return span;
  }

  startActiveSpan<F extends (span: Span) => unknown>(name: string, fn: F): ReturnType<F>;
  startActiveSpan<F extends (span: Span) => unknown>(name: string, options: SpanOptions, fn: F): ReturnType<F>;
  startActiveSpan<F extends (span: Span) => unknown>(name: string, options: SpanOptions, ctx: Context, fn: F): ReturnType<F>;
  startActiveSpan<F extends (span: Span) => unknown>(name: string, a2?: SpanOptions | F, a3?: Context | F, a4?: F): ReturnType<F> {
    let opts: SpanOptions | undefined;
    let ctx: Context | undefined;
    let fn: F;
    if (arguments.length < 2) return undefined as ReturnType<F>;
    if (arguments.length === 2) fn = a2 as F;
    else if (arguments.length === 3) {
      opts = a2 as SpanOptions;
      fn = a3 as F;
    } else {
      opts = a2 as SpanOptions;
      ctx = a3 as Context;
      fn = a4 as F;
    }
    const parentCtx = ctx ?? apiContext.active();
    const span = this.startSpan(name, opts, parentCtx);
    return apiContext.with(trace.setSpan(parentCtx, span), fn as unknown as (s: Span) => ReturnType<F>, undefined, span);
  }
}
