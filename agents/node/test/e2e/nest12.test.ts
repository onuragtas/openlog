// End-to-end: NestJS 12 (ESM-only, outside @opentelemetry/instrumentation-nestjs-core's range) started with
// `--import dist/esm/register.js`, on express and fastify, with and without the openlog-node/nest interceptor.
// Needs `npm ci --prefix test/apps/nest12` and Node.js >= 20; otherwise the suite is skipped.
import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import * as path from 'node:path';
import { after, before, describe, test } from 'node:test';
import { APPS, REGISTER_ESM, runApp } from '../helpers/app';
import { startCapture, type Capture, type CapturedSpan } from '../helpers/otlp';

const SERVER = 2;
const major = Number(process.versions.node.split('.')[0]);
const installed = existsSync(path.join(APPS, 'nest12', 'node_modules', '@nestjs', 'core', 'package.json'));
const skip =
  major < 20 ? `NestJS 12 needs Node.js >= 20 (running ${process.versions.node})` : !installed ? 'NestJS 12 app not installed: run `npm ci --prefix test/apps/nest12`' : false;

const cases: { name: string; args: string[]; env: Record<string, string>; nestAttrs: boolean }[] = [
  { name: 'express platform, no interceptor (route from the express instrumentation)', args: [], env: {}, nestAttrs: false },
  { name: 'express platform, interceptor, express instrumentation disabled', args: ['interceptor'], env: { OPENLOG_INSTRUMENTATIONS_DISABLED: 'express' }, nestAttrs: true },
  { name: 'fastify platform, no interceptor (route from @fastify/otel)', args: ['fastify'], env: {}, nestAttrs: false },
  { name: 'fastify platform, interceptor, fastify instrumentation disabled', args: ['fastify', 'interceptor'], env: { OPENLOG_INSTRUMENTATIONS_DISABLED: 'fastify' }, nestAttrs: true },
];

describe('NestJS 12 (ESM)', { skip }, () => {
  let cap: Capture;
  before(async () => {
    cap = await startCapture();
  });
  after(async () => {
    await cap?.close();
  });

  cases.forEach((c, i) => {
    test(c.name, async () => {
      const service = `e2e-nest12-${i}`;
      const app = await runApp(
        ['nest12/app.mjs', ...c.args],
        {
          OPENLOG_ENDPOINT: cap.url,
          OPENLOG_LICENSE_KEY: 'test-license-key',
          OPENLOG_SERVICE_NAME: service,
          OPENLOG_LOG_LEVEL: 'warn',
          OPENLOG_METRIC_EXPORT_INTERVAL: '5s',
          ...c.env,
        },
        ['--import', REGISTER_ESM],
      );
      const mine = (s: CapturedSpan): boolean => s.kind === SERVER && s.resource['service.name'] === service;
      const byPath = (p: string) => (): CapturedSpan | undefined => cap.spans.find((s) => mine(s) && s.attributes['url.path'] === p);
      try {
        assert.equal((await app.get('/users/42?q=1')).status, 200);
        assert.equal((await app.get('/v2/orders/9')).status, 200);
        assert.equal((await app.get('/boom')).status, 500);

        const users = await cap.waitFor('GET /users/42 server span', byPath('/users/42'));
        assert.equal(users.name, 'GET /users/:id');
        assert.equal(users.attributes['http.route'], '/users/:id');
        assert.equal(users.attributes['http.response.status_code'], 200);

        const orders = await cap.waitFor('GET /v2/orders/9 server span', byPath('/v2/orders/9'));
        assert.equal(orders.name, 'GET /v2/orders/:orderId');
        assert.equal(orders.attributes['http.route'], '/v2/orders/:orderId');

        const boom = await cap.waitFor('GET /boom server span', byPath('/boom'));
        assert.equal(boom.name, 'GET /boom');
        assert.equal(boom.attributes['http.route'], '/boom');
        assert.equal(boom.attributes['http.response.status_code'], 500);

        if (c.nestAttrs) {
          assert.equal(users.attributes['nestjs.controller'], 'UsersController');
          assert.equal(users.attributes['nestjs.callback'], 'get');
          assert.equal(boom.attributes['nestjs.controller'], 'BoomController');
          assert.equal(boom.attributes['nestjs.callback'], 'boom');
        } else {
          assert.equal(users.attributes['nestjs.controller'], undefined);
        }
      } finally {
        const exit = await app.stop();
        assert.equal(exit.signal, 'SIGTERM', `exit ${JSON.stringify(exit)}; stderr: ${app.stderr()}`);
        assert.ok(!/level=(ERROR|WARN)/.test(app.stderr()), `agent diagnostics: ${app.stderr()}`);
      }
    });
  });
});
