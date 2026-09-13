'use strict';

// OpenTelemetry bootstrap, loaded with `node --require ./instrumentation.js server.js`.
// Exporter endpoint, headers, sampler and resource attributes come from the standard OTEL_* env.
const { NodeSDK } = require('@opentelemetry/sdk-node');
const { getNodeAutoInstrumentations } = require('@opentelemetry/auto-instrumentations-node');
const { OTLPTraceExporter } = require('@opentelemetry/exporter-trace-otlp-proto');
const { OTLPLogExporter } = require('@opentelemetry/exporter-logs-otlp-proto');
const { BatchSpanProcessor, ParentBasedSampler, AlwaysOnSampler } = require('@opentelemetry/sdk-trace-base');
const { BatchLogRecordProcessor } = require('@opentelemetry/sdk-logs');
const { envDetector, processDetector } = require('@opentelemetry/resources');
const { W3CTraceContextPropagator, W3CBaggagePropagator, CompositePropagator } = require('@opentelemetry/core');

if (!process.env.OTEL_EXPORTER_OTLP_HEADERS && process.env.OPENLOG_LICENSE_KEY) {
  process.env.OTEL_EXPORTER_OTLP_HEADERS = `openlog-license-key=${process.env.OPENLOG_LICENSE_KEY}`;
}
process.env.OTEL_EXPORTER_OTLP_ENDPOINT ||= 'http://openlog:4318';

const sdk = new NodeSDK({
  // env (OTEL_RESOURCE_ATTRIBUTES / OTEL_SERVICE_NAME) + process; no host detector, so the
  // container hostname does not override host.name / host.id from the environment.
  resourceDetectors: [envDetector, processDetector],
  sampler: new ParentBasedSampler({ root: new AlwaysOnSampler() }),
  textMapPropagator: new CompositePropagator({
    propagators: [new W3CTraceContextPropagator(), new W3CBaggagePropagator()],
  }),
  spanProcessors: [new BatchSpanProcessor(new OTLPTraceExporter())],
  logRecordProcessors: [new BatchLogRecordProcessor(new OTLPLogExporter())],
  instrumentations: [
    getNodeAutoInstrumentations({
      '@opentelemetry/instrumentation-fs': { enabled: false },
      '@opentelemetry/instrumentation-dns': { enabled: false },
      '@opentelemetry/instrumentation-net': { enabled: false },
      '@opentelemetry/instrumentation-http': {
        ignoreIncomingRequestHook: (req) => req.url === '/healthz',
      },
    }),
  ],
});

sdk.start();

const shutdown = () => {
  sdk.shutdown().finally(() => process.exit(0));
};
process.on('SIGTERM', shutdown);
process.on('SIGINT', shutdown);
