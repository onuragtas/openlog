import assert from 'node:assert/strict';
import { test } from 'node:test';
import { ConfigError, LICENSE_KEY_HEADER, loadConfig, parseGoDuration, parseKV } from '../../src/config';

test('defaults', () => {
  const { config: c, warnings } = loadConfig({}, {});
  assert.equal(c.enabled, true);
  assert.equal(c.endpoint, 'http://localhost:4318');
  assert.equal(c.protocol, 'http/protobuf');
  assert.equal(c.compression, 'gzip');
  assert.equal(c.samplingRatio, 1);
  assert.equal(c.samplingRV, false);
  assert.equal(c.logLevel, 'warn');
  assert.equal(c.metricIntervalMs, 60_000);
  assert.equal(c.shutdownTimeoutMs, 5_000);
  assert.equal(c.dbQueryText, 'sanitized');
  assert.equal(c.infraRuntimeDir, '/run/openlog-infra-agent');
  assert.equal(c.infraStateDir, '/var/lib/openlog-infra-agent');
  assert.match(c.serviceName, /^unknown_service:/);
  assert.ok(warnings.some((w) => w.includes('OPENLOG_SERVICE_NAME')));
  assert.ok(warnings.some((w) => w.includes('OPENLOG_LICENSE_KEY')));
});

test('precedence: options > OPENLOG_* > OTEL_*', () => {
  const env = {
    OTEL_EXPORTER_OTLP_ENDPOINT: 'http://otel:4318',
    OTEL_SERVICE_NAME: 'otel-name',
    OTEL_EXPORTER_OTLP_HEADERS: 'x-a=1,x-b=hello%20world',
    OTEL_RESOURCE_ATTRIBUTES: 'team=a,service.namespace=ns',
    OTEL_TRACES_SAMPLER: 'parentbased_traceidratio',
    OTEL_TRACES_SAMPLER_ARG: '0.5',
    OTEL_METRIC_EXPORT_INTERVAL: '15000',
    OPENLOG_ENDPOINT: 'https://ingest.example.com/',
    OPENLOG_SERVICE_NAME: 'checkout',
    OPENLOG_LICENSE_KEY: 'k1',
    OPENLOG_SAMPLING_RATIO: '0.25',
    OPENLOG_SAMPLING_RV: 'true',
    OPENLOG_RESOURCE_ATTRIBUTES: 'team=b',
    OPENLOG_METRIC_EXPORT_INTERVAL: '1m30s',
    OPENLOG_SHUTDOWN_TIMEOUT: '750ms',
    OPENLOG_HOST_ID: 'host-12345678',
    OPENLOG_DB_QUERY_TEXT: 'raw',
    OPENLOG_INSTRUMENTATIONS_DISABLED: 'aws-sdk, graphql',
  };
  const { config: c } = loadConfig(env, { serviceVersion: '1.2.3', headers: { [LICENSE_KEY_HEADER]: 'ignored' } });
  assert.equal(c.endpoint, 'https://ingest.example.com');
  assert.equal(c.serviceName, 'checkout');
  assert.equal(c.serviceVersion, '1.2.3');
  assert.equal(c.headers['x-a'], '1');
  assert.equal(c.headers['x-b'], 'hello world');
  assert.equal(c.headers[LICENSE_KEY_HEADER], 'k1'); // license key wins over headers
  assert.equal(c.resourceAttributes.team, 'b');
  assert.equal(c.resourceAttributes['service.namespace'], 'ns');
  assert.equal(c.samplingRatio, 0.25);
  assert.equal(c.samplingRV, true);
  assert.equal(c.metricIntervalMs, 90_000);
  assert.equal(c.shutdownTimeoutMs, 750);
  assert.equal(c.hostId, 'host-12345678');
  assert.equal(c.dbQueryText, 'raw');
  assert.deepEqual(c.disabledInstrumentations, ['aws-sdk', 'graphql']);

  const o = loadConfig(env, { samplingRatio: 0.1, endpoint: 'http://x:1' }).config;
  assert.equal(o.samplingRatio, 0.1);
  assert.equal(o.endpoint, 'http://x:1');
  assert.equal(loadConfig({ OTEL_TRACES_SAMPLER_ARG: '0.5' }).config.samplingRatio, 0.5);
  assert.equal(loadConfig({ OTEL_TRACES_SAMPLER: 'always_on', OTEL_TRACES_SAMPLER_ARG: '0.5' }).config.samplingRatio, 1);
  assert.equal(loadConfig({ OTEL_METRIC_EXPORT_INTERVAL: '15000' }).config.metricIntervalMs, 15_000);
});

test('enabled flags', () => {
  assert.equal(loadConfig({ OTEL_SDK_DISABLED: 'true' }).config.enabled, false);
  assert.equal(loadConfig({ OTEL_SDK_DISABLED: 'true', OPENLOG_ENABLED: 'true' }).config.enabled, true);
  assert.equal(loadConfig({ OPENLOG_ENABLED: '0' }).config.enabled, false);
  const { config, warnings } = loadConfig({ OPENLOG_ENABLED: 'nope' });
  assert.equal(config.enabled, true);
  assert.ok(warnings.some((w) => w.includes('OPENLOG_ENABLED')));
});

test('validation and normalization', () => {
  assert.throws(() => loadConfig({ OPENLOG_PROTOCOL: 'http/json' }), ConfigError);
  assert.throws(() => loadConfig({ OPENLOG_COMPRESSION: 'zstd' }), ConfigError);
  assert.throws(() => loadConfig({ OPENLOG_ENDPOINT: 'ftp://x' }), ConfigError);
  assert.equal(loadConfig({ OPENLOG_PROTOCOL: 'grpc' }).config.endpoint, 'http://localhost:4317');
  assert.equal(loadConfig({ OPENLOG_ENDPOINT: 'ingest.example.com:4318' }).config.endpoint, 'https://ingest.example.com:4318');
  assert.equal(loadConfig({ OPENLOG_COMPRESSION: 'none' }).config.compression, 'none');
  const clamped = loadConfig({ OPENLOG_SAMPLING_RATIO: '1.5' });
  assert.equal(clamped.config.samplingRatio, 1);
  assert.ok(clamped.warnings.some((w) => w.includes('clamped')));
  const bad = loadConfig({ OPENLOG_SAMPLING_RATIO: 'abc', OPENLOG_METRIC_EXPORT_INTERVAL: '10', OPENLOG_LOG_LEVEL: 'loud' });
  assert.equal(bad.config.samplingRatio, 1);
  assert.equal(bad.config.metricIntervalMs, 60_000);
  assert.equal(bad.warnings.length >= 3, true);
  assert.equal(loadConfig({ OTEL_RESOURCE_ATTRIBUTES: 'service.name=from-attrs' }).config.serviceName, 'from-attrs');
  // Authorization header instead of a license key: no warning
  assert.ok(!loadConfig({ OTEL_EXPORTER_OTLP_HEADERS: 'Authorization=Bearer x', OPENLOG_SERVICE_NAME: 's' }).warnings.length);
});

test('parseKV and parseGoDuration', () => {
  assert.deepEqual(parseKV(' a = 1 ,b=%3D%2C, =x,novalue,c=%zz'), { a: '1', b: '=,', c: '%zz' });
  assert.equal(parseGoDuration('60s'), 60_000);
  assert.equal(parseGoDuration('1h2m3.5s'), 3_723_500);
  assert.equal(parseGoDuration('250ms'), 250);
  assert.equal(parseGoDuration('1500us'), 1.5);
  assert.equal(parseGoDuration('0'), 0);
  assert.equal(parseGoDuration('10'), undefined);
  assert.equal(parseGoDuration('5 s'), undefined);
  assert.equal(parseGoDuration(''), undefined);
});
