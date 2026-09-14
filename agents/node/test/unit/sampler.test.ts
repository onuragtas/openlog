import { context, propagation, ROOT_CONTEXT, SpanKind, trace, type Span } from '@opentelemetry/api';
import { W3CTraceContextPropagator } from '@opentelemetry/core';
import { BasicTracerProvider, InMemorySpanExporter, SimpleSpanProcessor, type IdGenerator, type ReadableSpan } from '@opentelemetry/sdk-trace-base';
import { AsyncLocalStorageContextManager } from '@opentelemetry/context-async-hooks';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import * as path from 'node:path';
import { after, before, test } from 'node:test';
import {
  createSampler,
  encodeThreshold,
  otProbability,
  parseOT,
  parseThreshold,
  RandomFlagTracerProvider,
  SAMPLING_RATIO_KEY,
  withOT,
  type RandomnessSource,
} from '../../src/sampler';
import { TraceState } from '@opentelemetry/core';

interface FixtureSpan {
  sampled: boolean;
  traceparent: string;
  tracestate: string;
  samplingRatio: number | null;
}

interface Fixture {
  name: string;
  ratio: number;
  writeRV: boolean;
  traceId: string;
  spanId: string;
  childSpanId: string;
  randomness?: string;
  incoming: { traceparent: string; tracestate: string } | null;
  span: FixtureSpan;
  child: FixtureSpan;
}

const FIXTURES: { cases: Fixture[] } = JSON.parse(
  readFileSync(path.resolve(__dirname, '../../../test/interop/go-sampler-fixtures.json'), 'utf8'),
);

class FixedIds implements IdGenerator {
  private n = 0;
  constructor(private readonly f: Fixture) {}
  generateTraceId(): string {
    return this.f.traceId;
  }
  generateSpanId(): string {
    return this.n++ === 0 ? this.f.spanId : this.f.childSpanId;
  }
}

const propagator = new W3CTraceContextPropagator();

function headers(ctx: ReturnType<typeof context.active>): { traceparent: string; tracestate: string } {
  const carrier: Record<string, string> = {};
  propagator.inject(ctx, carrier, { set: (c, k, v) => ((c as Record<string, string>)[k] = v) });
  return { traceparent: carrier.traceparent ?? '', tracestate: carrier.tracestate ?? '' };
}

before(() => {
  context.setGlobalContextManager(new AsyncLocalStorageContextManager().enable());
});
after(() => {
  context.disable();
});

test('threshold encoding (Go TestThresholdEncoding)', () => {
  const cases: [number, string][] = [
    [0.5, '8'],
    [0.25, 'c'],
    [0.125, 'e'],
    [0.75, '4'],
    [0.1, 'e6666666666666'],
    [0.001, 'ffbe76c8b43958'],
  ];
  for (const [ratio, th] of cases) {
    const keep = BigInt(Math.round(ratio * 2 ** 56));
    assert.equal(encodeThreshold((1n << 56n) - keep), th, `ratio ${ratio}`);
    const p = otProbability([['th', th]]);
    assert.ok(p !== undefined && Math.abs(p - ratio) < 1e-12, `decoded ${p}`);
  }
  assert.equal(encodeThreshold(0n), '0');
  assert.equal(otProbability([['p', '2']]), 0.25);
  assert.equal(otProbability([['p', '63']]), 0);
  assert.equal(otProbability([['p', '64']]), undefined);
  assert.equal(parseThreshold('xyz'), undefined);
  assert.equal(parseThreshold('123456789abcdef'), undefined);
  assert.deepEqual(parseOT('th:c;rv:00000000000001;bad;:x'), [
    ['th', 'c'],
    ['rv', '00000000000001'],
  ]);
});

test('withOT keeps W3C limits', () => {
  const members = Array.from({ length: 32 }, (_, i) => `v${i}=x`).join(',');
  const ts = withOT(new TraceState(members), [['th', 'c']]);
  const out = ts!.serialize().split(',');
  assert.equal(out.length, 32);
  assert.equal(out[0], 'ot=th:c');
  assert.equal(out[31], 'v30=x'); // right-most member dropped
  const long: [string, string][] = [
    ['th', 'c'],
    ['x', 'a'.repeat(200)],
    ['y', 'b'.repeat(100)],
  ];
  assert.equal(withOT(undefined, long)!.get('ot'), 'th:c;x:' + 'a'.repeat(200));
  assert.equal(withOT(new TraceState('ot=th:c,k=v'), [])!.serialize(), 'k=v');
  const bad = new TraceState('k=v');
  assert.equal(withOT(bad, [['x', 'a,b']]), bad);
});

test('cross-language fixtures produced by the Go agent', async (t) => {
  for (const f of FIXTURES.cases) {
    await t.test(f.name, () => {
      const exporter = new InMemorySpanExporter();
      const randomness: RandomnessSource = () => [BigInt('0x' + (f.randomness ?? '00000000000000')), f.randomness ?? '00000000000000'];
      const provider = new BasicTracerProvider({
        sampler: createSampler(f.ratio, f.writeRV, randomness),
        idGenerator: new FixedIds(f),
        spanProcessors: [new SimpleSpanProcessor(exporter)],
      });
      const tracer = new RandomFlagTracerProvider(provider).getTracer('fixtures');
      let ctx = ROOT_CONTEXT;
      if (f.incoming) {
        ctx = propagator.extract(ctx, f.incoming, { get: (c, k) => (c as Record<string, string>)[k], keys: (c) => Object.keys(c) });
      }
      const span = tracer.startSpan('span', { kind: SpanKind.SERVER }, ctx);
      const sctx = trace.setSpan(ctx, span);
      const child = tracer.startSpan('child', { kind: SpanKind.CLIENT }, sctx);
      const cctx = trace.setSpan(sctx, child);
      const check = (s: Span, c: typeof ctx, want: FixtureSpan, which: string): void => {
        const h = headers(c);
        assert.equal(h.traceparent, want.traceparent, `${which} traceparent`);
        assert.equal(h.tracestate, want.tracestate, `${which} tracestate`);
        assert.equal((s.spanContext().traceFlags & 1) === 1, want.sampled, `${which} sampled`);
        const recorded = exporter.getFinishedSpans().find((r: ReadableSpan) => r.spanContext().spanId === s.spanContext().spanId);
        const ratio = recorded?.attributes[SAMPLING_RATIO_KEY];
        assert.equal(ratio ?? null, want.samplingRatio, `${which} sampling.ratio`);
      };
      child.end();
      span.end();
      check(span, sctx, f.span, 'span');
      check(child, cctx, f.child, 'child');
    });
  }
});

test('sampling ratio 0.25 keeps about a quarter and weights to the total', () => {
  const exporter = new InMemorySpanExporter();
  const provider = new BasicTracerProvider({ sampler: createSampler(0.25), spanProcessors: [new SimpleSpanProcessor(exporter)] });
  const tracer = new RandomFlagTracerProvider(provider).getTracer('t');
  const n = 20_000;
  for (let i = 0; i < n; i++) tracer.startSpan('root').end();
  const spans = exporter.getFinishedSpans();
  const weighted = spans.reduce((s, sp) => s + 1 / (sp.attributes[SAMPLING_RATIO_KEY] as number), 0);
  assert.ok(Math.abs(spans.length / n - 0.25) < 0.02, `kept ${spans.length}`);
  assert.ok(Math.abs(weighted - n) / n < 0.08, `weighted ${weighted}`);
  for (const s of spans.slice(0, 10)) {
    assert.equal(s.spanContext().traceState?.get('ot'), 'th:c');
    assert.equal(s.spanContext().traceFlags, 3);
  }
});

test('startActiveSpan sets the random flag and activates the span', () => {
  const provider = new BasicTracerProvider({ sampler: createSampler(1) });
  const tracer = new RandomFlagTracerProvider(provider).getTracer('t');
  tracer.startActiveSpan('outer', (outer) => {
    assert.equal(trace.getActiveSpan(), outer);
    assert.equal(outer.spanContext().traceFlags, 3);
    tracer.startActiveSpan('inner', {}, (inner) => {
      assert.equal(inner.spanContext().traceId, outer.spanContext().traceId);
      assert.equal(inner.spanContext().traceFlags, 3);
      inner.end();
    });
    outer.end();
  });
  // a suppressed/invalid span context is never mutated
  propagation.disable();
});
