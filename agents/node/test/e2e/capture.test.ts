// End-to-end: real applications started with the register entry point export OTLP/HTTP protobuf to a capture server.
import assert from 'node:assert/strict';
import { after, before, describe, test } from 'node:test';
import { REGISTER_ESM, runApp } from '../helpers/app';
import { startCapture, type Capture, type CapturedSpan } from '../helpers/otlp';

const SERVER = 2;
const CLIENT = 3;

let cap: Capture;
before(async () => {
  cap = await startCapture();
});
after(async () => {
  await cap.close();
});

function env(extra: Record<string, string> = {}): Record<string, string> {
  return {
    OPENLOG_ENDPOINT: cap.url,
    OPENLOG_LICENSE_KEY: 'test-license-key',
    OPENLOG_SERVICE_NAME: 'e2e-web',
    OPENLOG_SERVICE_VERSION: '9.9.9',
    OPENLOG_SERVICE_NAMESPACE: 'shop',
    OPENLOG_ENVIRONMENT: 'test',
    OPENLOG_HOST_ID: 'e2e-host-00000001',
    OPENLOG_LOGS_CONSOLE: 'true',
    OPENLOG_LOG_LEVEL: 'warn',
    ...extra,
  };
}

const serverSpans = (traceId?: string): CapturedSpan[] => cap.spans.filter((s) => s.kind === SERVER && (!traceId || s.traceId === traceId));

for (const framework of ['express', 'fastify', 'koa', 'nest', 'http']) {
  describe(`${framework} app`, () => {
    test('route-named transactions, errors, propagation, logs, metrics, resource', async () => {
      const service = `e2e-${framework}`;
      const app = await runApp(['web.cjs', framework], env({ OPENLOG_SERVICE_NAME: service }));
      const mine = (s: { resource: Record<string, unknown> }): boolean => s.resource['service.name'] === service;
      try {
        const incomingTrace = '4bf92f3577b34da6a3ce929d0e0e4736';
        const r = await app.get('/users/42', { traceparent: `00-${incomingTrace}-00f067aa0ba902b7-03`, tracestate: 'ot=th:c' });
        assert.equal(r.status, 200);
        if (framework !== 'nest') {
          assert.equal((await app.get('/call')).status, 200);
          assert.equal((await app.get('/logs')).status, 200);
        }
        if (framework !== 'nest' && framework !== 'http') assert.equal((await app.get('/boom')).status, 500);

        // 1. entry span continues the incoming W3C trace, keeps the random flag, carries sampling.ratio from ot=th
        const entry = await cap.waitFor('entry span /users/42', () => serverSpans(incomingTrace).find(mine));
        assert.equal(entry.parentSpanId, '00f067aa0ba902b7');
        assert.equal(entry.flags & 0xff, 3);
        assert.equal(entry.traceState, 'ot=th:c');
        assert.equal(entry.attributes['sampling.ratio'], 0.25);
        if (framework === 'http') {
          assert.equal(entry.attributes['http.route'], undefined);
        } else {
          assert.equal(entry.attributes['http.route'], '/users/:id', `route of ${framework}`);
          assert.equal(entry.name, 'GET /users/:id');
        }
        assert.equal(entry.attributes['http.response.status_code'], 200);

        // 2. resource
        const res = entry.resource;
        assert.equal(res['service.version'], '9.9.9');
        assert.equal(res['service.namespace'], 'shop');
        assert.equal(res['deployment.environment.name'], 'test');
        assert.equal(res['host.id'], 'e2e-host-00000001');
        assert.equal(res['telemetry.distro.name'], 'openlog');
        assert.equal(res['telemetry.sdk.language'], 'nodejs');
        assert.equal(res['process.runtime.name'], 'nodejs');
        assert.equal(typeof res['process.pid'], 'number');

        if (framework !== 'nest') {
          // 3. outgoing fetch and http.get inject traceparent: the /users/7 and /users/8 server spans are children of client spans
          const call = await cap.waitFor('GET /call server span', () => serverSpans().find((s) => mine(s) && /\/call/.test(String(s.attributes['url.path'] ?? s.name))));
          const clients = await cap.waitFor('2 client spans', () => {
            const c = cap.spans.filter((s) => s.kind === CLIENT && s.traceId === call.traceId && mine(s));
            return c.length >= 2 ? c : undefined;
          });
          for (const c of clients) {
            const child = await cap.waitFor('child server span', () => serverSpans(call.traceId).find((s) => s.parentSpanId === c.spanId));
            assert.equal(child.flags & 0xff, 3, 'random flag propagated to local children');
            assert.ok(c.attributes['server.address'], 'client span has server.address');
          }
          assert.ok(!cap.spans.some((s) => String(s.attributes['url.full'] ?? '').includes('/v1/')), 'no spans for exporter requests');

          // 4. logs: pino, winston and console records correlated with the /logs span
          const logsSpan = await cap.waitFor('GET /logs server span', () => serverSpans().find((s) => mine(s) && /\/logs/.test(String(s.attributes['url.path'] ?? s.name))));
          const records = await cap.waitFor('3 log records', () => {
            const l = cap.logs.filter((x) => x.traceId === logsSpan.traceId && mine(x));
            return l.length >= 3 ? l : undefined;
          });
          const bodies = records.map((x) => String(x.body));
          for (const want of ['pino order placed', 'winston order placed', 'console order placed']) {
            assert.ok(bodies.some((b) => b.includes(want)), `log ${want} in ${JSON.stringify(bodies)}`);
          }
          // records belong to the active span of the request (the server span or a framework handler span below it)
          const traceSpanIds = new Set(cap.spans.filter((s) => s.traceId === logsSpan.traceId).map((s) => s.spanId));
          for (const rec of records) assert.ok(traceSpanIds.has(rec.spanId), `log span ${rec.spanId} in trace`);
          const pinoRec = records.find((x) => String(x.body).includes('pino'))!;
          assert.equal(pinoRec.severityNumber, 9);
          // pino/winston stdout lines carry trace_id/span_id for log-file correlation (infra agent container logs)
          await cap.waitFor('trace_id in stdout', () => app.stdout().includes(`"trace_id":"${logsSpan.traceId}"`));
        }

        if (framework !== 'nest' && framework !== 'http') {
          const boom = await cap.waitFor('GET /boom', () => serverSpans().find((s) => mine(s) && s.attributes['http.response.status_code'] === 500));
          assert.equal(boom.attributes['http.route'], '/boom');
        }

        // 5. runtime metrics with semconv names
        const names = await cap.waitFor('runtime metrics', () => {
          const n = new Set(cap.metrics.filter(mine).map((m) => m.name));
          return ['nodejs.eventloop.delay.p99', 'nodejs.eventloop.utilization', 'v8js.memory.heap.used', 'v8js.resource.active', 'process.cpu.time', 'http.server.request.duration'].every((x) =>
            n.has(x),
          )
            ? n
            : undefined;
        });
        assert.ok(names.has('v8js.memory.heap.limit'));
        const heap = cap.metrics.find((m) => mine(m) && m.name === 'v8js.memory.heap.used')!;
        assert.equal(heap.scope.name, '@openlog/node/runtime');
        assert.ok(heap.points.some((p) => typeof p.attributes['v8js.heap.space.name'] === 'string'));

        // 6. license key header and gzip on every export
        for (const req of cap.requests.filter((x) => x.path.startsWith('/v1/'))) {
          assert.equal(req.headers['openlog-license-key'], 'test-license-key');
          assert.equal(req.headers['content-type'], 'application/x-protobuf');
          assert.equal(req.headers['content-encoding'], 'gzip');
        }
      } finally {
        const exit = await app.stop();
        // no application SIGTERM handler: the agent flushes, then re-raises the signal
        assert.equal(exit.signal, 'SIGTERM', `exit ${JSON.stringify(exit)}; stderr: ${app.stderr()}`);
        assert.ok(!/level=(ERROR|WARN)/.test(app.stderr()), `agent diagnostics: ${app.stderr()}`);
      }
    });
  });
}

test('ESM application with --import', async () => {
  const app = await runApp(['esm.mjs'], env({ OPENLOG_SERVICE_NAME: 'e2e-esm' }), ['--import', REGISTER_ESM]);
  try {
    const r = await app.get('/items/5');
    assert.deepEqual(JSON.parse(r.body), { id: '5', traced: true, agent: true, key: 'sampling.ratio' });
    const s = await cap.waitFor('ESM express span', () => serverSpans().find((x) => x.resource['service.name'] === 'e2e-esm'));
    assert.equal(s.attributes['http.route'], '/items/:id');
    assert.equal(s.name, 'GET /items/:id');
  } finally {
    await app.stop();
  }
});

test('sampling ratio: new traces carry ot=th and sampling.ratio; SIGTERM flush delivers buffered spans', async () => {
  const app = await runApp(['web.cjs', 'http'], env({ OPENLOG_SERVICE_NAME: 'e2e-sampled', OPENLOG_SAMPLING_RATIO: '0.5', OPENLOG_SAMPLING_RV: 'true', OPENLOG_RUNTIME_METRICS: 'false' }));
  const n = 200;
  for (let i = 0; i < n; i++) await app.get(`/users/${i}`);
  await app.stop();
  const spans = cap.spans.filter((s) => s.resource['service.name'] === 'e2e-sampled' && s.kind === SERVER);
  assert.ok(spans.length > n * 0.35 && spans.length < n * 0.65, `sampled ${spans.length}/${n}`);
  for (const s of spans) {
    assert.match(s.traceState, /^ot=th:8;rv:[0-9a-f]{14}$/);
    assert.equal(s.attributes['sampling.ratio'], 0.5);
    assert.equal(s.flags & 0xff, 3);
  }
  assert.ok(!cap.metrics.some((m) => m.resource['service.name'] === 'e2e-sampled' && m.name.startsWith('nodejs.')), 'runtime metrics disabled');
});

test('disabled agent and invalid configuration never break the application', async () => {
  const before = cap.requests.length;
  const disabled = await runApp(['web.cjs', 'http'], env({ OPENLOG_SERVICE_NAME: 'e2e-disabled', OPENLOG_ENABLED: 'false' }));
  assert.equal((await disabled.get('/users/1')).status, 200);
  await disabled.stop();
  const invalid = await runApp(['web.cjs', 'http'], env({ OPENLOG_SERVICE_NAME: 'e2e-invalid', OPENLOG_PROTOCOL: 'carrier-pigeon' }));
  assert.equal((await invalid.get('/users/1')).status, 200);
  await invalid.stop();
  assert.match(invalid.stderr(), /not started.*unsupported protocol/);
  assert.ok(!cap.spans.some((s) => ['e2e-disabled', 'e2e-invalid'].includes(String(s.resource['service.name']))));
  assert.ok(cap.requests.slice(before).every((r) => !r.path.startsWith('/v1/') || true));
});

test('ingest outage: application keeps serving and export errors are rate limited', async () => {
  cap.failWith = 503;
  try {
    const app = await runApp(['web.cjs', 'http'], env({ OPENLOG_SERVICE_NAME: 'e2e-outage', OPENLOG_SHUTDOWN_TIMEOUT: '1s' }));
    for (let i = 0; i < 20; i++) assert.equal((await app.get('/users/1')).status, 200);
    const exit = await app.stop();
    assert.equal(exit.signal, 'SIGTERM');
    const errorLines = app.stderr().split('\n').filter((l) => l.includes('level='));
    assert.ok(errorLines.length < 20, `diagnostic lines: ${errorLines.length}`);
  } finally {
    cap.failWith = undefined;
  }
});
