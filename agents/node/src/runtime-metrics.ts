import type { BatchObservableResult, Meter, MeterProvider, ObservableResult } from '@opentelemetry/api';
import { constants as perfConstants, monitorEventLoopDelay, performance, PerformanceObserver, type EventLoopUtilization, type IntervalHistogram } from 'node:perf_hooks';
import * as v8 from 'node:v8';
import { VERSION } from './version';

/** Instrumentation scope of the Node.js runtime metrics. */
export const RUNTIME_SCOPE = '@openlog/node/runtime';

/**
 * Explicit bucket boundaries (seconds) of v8js.gc.duration: the Go agent's runtime latency bounds (10 µs … 1 s), so
 * GC pauses of both runtimes are comparable (semconv only advises [0.01, 0.1, 1, 10], too coarse for V8 pauses).
 */
export const RUNTIME_LATENCY_BOUNDS = [0.00001, 0.00005, 0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1];

const GC_TYPES: Record<number, string> = {
  [perfConstants.NODE_PERFORMANCE_GC_MAJOR]: 'major',
  [perfConstants.NODE_PERFORMANCE_GC_MINOR]: 'minor',
  [perfConstants.NODE_PERFORMANCE_GC_INCREMENTAL]: 'incremental',
  [perfConstants.NODE_PERFORMANCE_GC_WEAKCB]: 'weakcb',
};

export interface RuntimeMetrics {
  stop(): void;
}

/**
 * Node.js runtime metrics with OpenTelemetry semantic-convention names (the Go agent's policy: semconv names where they
 * exist, `openlog.` prefix otherwise — every metric here has a semconv name):
 *
 *   nodejs.eventloop.delay.{min,max,mean,stddev,p50,p90,p99}  Gauge  s   (since the previous collection)
 *   nodejs.eventloop.utilization                             Gauge  1   (since the previous collection)
 *   nodejs.eventloop.time {nodejs.eventloop.state}           Counter s
 *   v8js.gc.duration {v8js.gc.type}                          Histogram s
 *   v8js.memory.heap.used / .space.size / .space.available_size / .space.physical_size {v8js.heap.space.name}  UpDownCounter By
 *   v8js.memory.heap.limit                                   UpDownCounter By (heap_size_limit of the isolate)
 *   v8js.resource.active {v8js.resource.type}                UpDownCounter {resource}  (active handles/requests)
 *   process.cpu.time {cpu.mode}                              Counter s
 *   process.memory.usage                                     UpDownCounter By (RSS)
 *
 * Values are read on collection (no timers besides the event loop delay monitor and the GC observer).
 */
export function startRuntimeMetrics(meterProvider: MeterProvider): RuntimeMetrics {
  const meter: Meter = meterProvider.getMeter(RUNTIME_SCOPE, VERSION);

  let delay: IntervalHistogram | undefined;
  try {
    delay = monitorEventLoopDelay({ resolution: 10 });
    delay.enable();
  } catch {
    delay = undefined;
  }
  const delayGauges = {
    min: meter.createObservableGauge('nodejs.eventloop.delay.min', { unit: 's', description: 'Event loop minimum delay.' }),
    max: meter.createObservableGauge('nodejs.eventloop.delay.max', { unit: 's', description: 'Event loop maximum delay.' }),
    mean: meter.createObservableGauge('nodejs.eventloop.delay.mean', { unit: 's', description: 'Event loop mean delay.' }),
    stddev: meter.createObservableGauge('nodejs.eventloop.delay.stddev', { unit: 's', description: 'Event loop standard deviation delay.' }),
    p50: meter.createObservableGauge('nodejs.eventloop.delay.p50', { unit: 's', description: 'Event loop 50 percentile delay.' }),
    p90: meter.createObservableGauge('nodejs.eventloop.delay.p90', { unit: 's', description: 'Event loop 90 percentile delay.' }),
    p99: meter.createObservableGauge('nodejs.eventloop.delay.p99', { unit: 's', description: 'Event loop 99 percentile delay.' }),
  };
  const utilization = meter.createObservableGauge('nodejs.eventloop.utilization', { unit: '1', description: 'Event loop utilization.' });
  const loopTime = meter.createObservableCounter('nodejs.eventloop.time', { unit: 's', description: 'Cumulative duration of time the event loop has been in each state.' });
  const heapUsed = meter.createObservableUpDownCounter('v8js.memory.heap.used', { unit: 'By', description: 'Heap Memory size allocated.' });
  const heapSpaceSize = meter.createObservableUpDownCounter('v8js.memory.heap.space.size', { unit: 'By', description: 'Total heap memory size pre-allocated for a heap space.' });
  const heapAvailable = meter.createObservableUpDownCounter('v8js.memory.heap.space.available_size', { unit: 'By', description: 'Heap space available size.' });
  const heapPhysical = meter.createObservableUpDownCounter('v8js.memory.heap.space.physical_size', { unit: 'By', description: 'Committed size of a heap space.' });
  const heapLimit = meter.createObservableUpDownCounter('v8js.memory.heap.limit', { unit: 'By', description: 'Heap size limit of the V8 isolate.' });
  const activeResources = meter.createObservableUpDownCounter('v8js.resource.active', { unit: '{resource}', description: 'Active resources that are currently keeping the event loop alive.' });
  const cpuTime = meter.createObservableCounter('process.cpu.time', { unit: 's', description: 'Total CPU seconds broken down by different CPU modes.' });
  const memoryUsage = meter.createObservableUpDownCounter('process.memory.usage', { unit: 'By', description: 'The amount of physical memory in use.' });
  const gcDuration = meter.createHistogram('v8js.gc.duration', {
    unit: 's',
    description: 'Garbage collection duration.',
    advice: { explicitBucketBoundaries: RUNTIME_LATENCY_BOUNDS },
  });

  let lastELU: EventLoopUtilization | undefined;
  const getActiveResources = (process as unknown as { getActiveResourcesInfo?: () => string[] }).getActiveResourcesInfo;

  const callback = (r: BatchObservableResult): void => {
    if (delay && delay.count > 0) {
      const ns = (v: number): number => (Number.isFinite(v) ? v / 1e9 : 0);
      r.observe(delayGauges.min, ns(delay.min));
      r.observe(delayGauges.max, ns(delay.max));
      r.observe(delayGauges.mean, ns(delay.mean));
      r.observe(delayGauges.stddev, ns(delay.stddev));
      r.observe(delayGauges.p50, ns(delay.percentile(50)));
      r.observe(delayGauges.p90, ns(delay.percentile(90)));
      r.observe(delayGauges.p99, ns(delay.percentile(99)));
      delay.reset();
    }
    const elu = performance.eventLoopUtilization();
    const diff = lastELU ? performance.eventLoopUtilization(elu, lastELU) : elu;
    lastELU = elu;
    r.observe(utilization, diff.utilization);
    r.observe(loopTime, elu.active / 1000, { 'nodejs.eventloop.state': 'active' });
    r.observe(loopTime, elu.idle / 1000, { 'nodejs.eventloop.state': 'idle' });

    for (const s of v8.getHeapSpaceStatistics()) {
      const attrs = { 'v8js.heap.space.name': s.space_name };
      r.observe(heapUsed, s.space_used_size, attrs);
      r.observe(heapSpaceSize, s.space_size, attrs);
      r.observe(heapAvailable, s.space_available_size, attrs);
      r.observe(heapPhysical, s.physical_space_size, attrs);
    }
    r.observe(heapLimit, v8.getHeapStatistics().heap_size_limit);

    if (getActiveResources) {
      const counts = new Map<string, number>();
      for (const t of getActiveResources.call(process)) counts.set(t, (counts.get(t) ?? 0) + 1);
      for (const [t, n] of counts) r.observe(activeResources, n, { 'v8js.resource.type': t });
    }
    const cpu = process.cpuUsage();
    r.observe(cpuTime, cpu.user / 1e6, { 'cpu.mode': 'user' });
    r.observe(cpuTime, cpu.system / 1e6, { 'cpu.mode': 'system' });
    r.observe(memoryUsage, process.memoryUsage.rss ? process.memoryUsage.rss() : process.memoryUsage().rss);
  };
  const observables = [
    ...Object.values(delayGauges),
    utilization,
    loopTime,
    heapUsed,
    heapSpaceSize,
    heapAvailable,
    heapPhysical,
    heapLimit,
    activeResources,
    cpuTime,
    memoryUsage,
  ];
  meter.addBatchObservableCallback(callback, observables);

  let gcObserver: PerformanceObserver | undefined;
  try {
    gcObserver = new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        const detail = (entry as unknown as { detail?: { kind?: number }; kind?: number }).detail;
        const kind = detail?.kind ?? (entry as unknown as { kind?: number }).kind ?? 0;
        gcDuration.record(entry.duration / 1000, { 'v8js.gc.type': GC_TYPES[kind] ?? 'other' });
      }
    });
    gcObserver.observe({ entryTypes: ['gc'] });
  } catch {
    gcObserver = undefined;
  }

  return {
    stop(): void {
      meter.removeBatchObservableCallback(callback, observables);
      delay?.disable();
      gcObserver?.disconnect();
    },
  };
}

// Kept for API symmetry with ObservableResult users in tests.
export type { ObservableResult };
