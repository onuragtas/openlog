import { AggregationTemporality, DataPointType, InMemoryMetricExporter, MeterProvider, PeriodicExportingMetricReader } from '@opentelemetry/sdk-metrics';
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { RUNTIME_LATENCY_BOUNDS, RUNTIME_SCOPE, startRuntimeMetrics } from '../../src/runtime-metrics';

test('runtime metrics use semantic-convention names and report sane values', async () => {
  const exporter = new InMemoryMetricExporter(AggregationTemporality.CUMULATIVE);
  const reader = new PeriodicExportingMetricReader({ exporter, exportIntervalMillis: 60_000 });
  const provider = new MeterProvider({ readers: [reader] });
  const rt = startRuntimeMetrics(provider);
  // some event loop activity and a forced GC when available
  await new Promise((r) => setTimeout(r, 50));
  const junk: unknown[] = [];
  for (let i = 0; i < 200_000; i++) junk.push({ i });
  junk.length = 0;
  (globalThis as { gc?: () => void }).gc?.();
  await new Promise((r) => setTimeout(r, 50));
  await reader.forceFlush();
  const scope = exporter.getMetrics().flatMap((rm) => rm.scopeMetrics).find((s) => s.scope.name === RUNTIME_SCOPE);
  assert.ok(scope, 'runtime scope exported');
  const byName = new Map(scope.metrics.map((m) => [m.descriptor.name, m]));
  for (const name of [
    'nodejs.eventloop.delay.min',
    'nodejs.eventloop.delay.max',
    'nodejs.eventloop.delay.mean',
    'nodejs.eventloop.delay.stddev',
    'nodejs.eventloop.delay.p50',
    'nodejs.eventloop.delay.p90',
    'nodejs.eventloop.delay.p99',
    'nodejs.eventloop.utilization',
    'nodejs.eventloop.time',
    'v8js.memory.heap.used',
    'v8js.memory.heap.space.size',
    'v8js.memory.heap.space.available_size',
    'v8js.memory.heap.space.physical_size',
    'v8js.memory.heap.limit',
    'v8js.resource.active',
    'process.cpu.time',
    'process.memory.usage',
  ]) {
    assert.ok(byName.has(name), `missing ${name}; have ${[...byName.keys()].join(', ')}`);
  }
  const util = byName.get('nodejs.eventloop.utilization')!;
  const u = util.dataPoints[0].value as number;
  assert.ok(u >= 0 && u <= 1, `utilization ${u}`);
  const p99 = byName.get('nodejs.eventloop.delay.p99')!.dataPoints[0].value as number;
  assert.ok(p99 > 0 && p99 < 10, `p99 ${p99} s`);
  const heap = byName.get('v8js.memory.heap.used')!;
  assert.ok(heap.dataPoints.some((p) => p.attributes['v8js.heap.space.name'] === 'old_space' && (p.value as number) > 0));
  const loop = byName.get('nodejs.eventloop.time')!;
  assert.deepEqual(new Set(loop.dataPoints.map((p) => p.attributes['nodejs.eventloop.state'])), new Set(['active', 'idle']));
  const cpu = byName.get('process.cpu.time')!;
  assert.ok(cpu.dataPoints.some((p) => p.attributes['cpu.mode'] === 'user' && (p.value as number) > 0));
  const active = byName.get('v8js.resource.active')!;
  assert.ok(active.dataPoints.every((p) => typeof p.attributes['v8js.resource.type'] === 'string'));
  const gc = byName.get('v8js.gc.duration');
  if (gc) {
    assert.equal(gc.dataPointType, DataPointType.HISTOGRAM);
    const point = gc.dataPoints[0].value as { buckets: { boundaries: number[] } };
    assert.deepEqual(point.buckets.boundaries, RUNTIME_LATENCY_BOUNDS);
    assert.ok(gc.dataPoints.every((p) => ['major', 'minor', 'incremental', 'weakcb', 'other'].includes(String(p.attributes['v8js.gc.type']))));
  }
  rt.stop();
  await provider.shutdown();
});
