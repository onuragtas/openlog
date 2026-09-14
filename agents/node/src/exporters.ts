import { OTLPLogExporter } from '@opentelemetry/exporter-logs-otlp-proto';
import { OTLPMetricExporter } from '@opentelemetry/exporter-metrics-otlp-proto';
import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-proto';
import type { LogRecordExporter } from '@opentelemetry/sdk-logs';
import type { PushMetricExporter } from '@opentelemetry/sdk-metrics';
import type { SpanExporter } from '@opentelemetry/sdk-trace-base';
import { createRequire } from 'node:module';
import * as path from 'node:path';
import { ConfigError, PROTOCOL_GRPC, type Config } from './config';

export interface Exporters {
  trace: SpanExporter;
  metric: PushMetricExporter;
  log: LogRecordExporter;
}

type Ctor<T> = new (config: Record<string, unknown>) => T;

function loadGrpc(pkg: string): Record<string, unknown> {
  // grpc exporters are optional peer dependencies: resolved from this package, then from the application.
  const bases = [__filename, path.join(process.cwd(), 'index.js')];
  let lastErr: unknown;
  for (const base of bases) {
    try {
      return createRequire(base)(pkg) as Record<string, unknown>;
    } catch (err) {
      lastErr = err;
    }
  }
  throw new ConfigError(
    `openlog: OPENLOG_PROTOCOL=grpc needs ${pkg} (npm install @opentelemetry/exporter-trace-otlp-grpc@0.222.0 ` +
      `@opentelemetry/exporter-metrics-otlp-grpc@0.222.0 @opentelemetry/exporter-logs-otlp-grpc@0.222.0): ${String(lastErr)}`,
  );
}

/**
 * OTLP exporters. http/protobuf posts to <endpoint>/v1/{traces,metrics,logs}; grpc uses the endpoint as target
 * (http:// = plaintext). The OTel exporters retry 429/502/503/504 (and gRPC UNAVAILABLE/RESOURCE_EXHAUSTED) with
 * exponential backoff and honor Retry-After (D-014); failures reach the rate-limited diagnostics logger.
 */
export function createExporters(cfg: Config): Exporters {
  const common = { headers: { ...cfg.headers }, compression: cfg.compression, timeoutMillis: cfg.exportTimeoutMs };
  if (cfg.protocol === PROTOCOL_GRPC) {
    const traceMod = loadGrpc('@opentelemetry/exporter-trace-otlp-grpc');
    const metricMod = loadGrpc('@opentelemetry/exporter-metrics-otlp-grpc');
    const logMod = loadGrpc('@opentelemetry/exporter-logs-otlp-grpc');
    const grpc = createRequire(require.resolve('@opentelemetry/exporter-trace-otlp-grpc', { paths: [__dirname, process.cwd()] }))(
      '@grpc/grpc-js',
    ) as { Metadata: new () => { set(k: string, v: string): void } };
    const metadata = (): unknown => {
      const md = new grpc.Metadata();
      for (const [k, v] of Object.entries(cfg.headers)) md.set(k, v);
      return md;
    };
    const g = { url: cfg.endpoint, compression: cfg.compression, timeoutMillis: cfg.exportTimeoutMs };
    return {
      trace: new (traceMod.OTLPTraceExporter as Ctor<SpanExporter>)({ ...g, metadata: metadata() }),
      metric: new (metricMod.OTLPMetricExporter as Ctor<PushMetricExporter>)({ ...g, metadata: metadata() }),
      log: new (logMod.OTLPLogExporter as Ctor<LogRecordExporter>)({ ...g, metadata: metadata() }),
    };
  }
  return {
    trace: new OTLPTraceExporter({ ...common, url: `${cfg.endpoint}/v1/traces` } as ConstructorParameters<typeof OTLPTraceExporter>[0]),
    metric: new OTLPMetricExporter({ ...common, url: `${cfg.endpoint}/v1/metrics` } as ConstructorParameters<typeof OTLPMetricExporter>[0]),
    log: new OTLPLogExporter({ ...common, url: `${cfg.endpoint}/v1/logs` } as ConstructorParameters<typeof OTLPLogExporter>[0]),
  };
}
