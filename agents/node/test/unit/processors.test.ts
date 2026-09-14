import { context, ROOT_CONTEXT, SpanKind, trace } from '@opentelemetry/api';
import { AsyncLocalStorageContextManager } from '@opentelemetry/context-async-hooks';
import { BasicTracerProvider, InMemorySpanExporter, SimpleSpanProcessor } from '@opentelemetry/sdk-trace-base';
import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import type { DbQueryTextMode } from '../../src/config';
import { DbStatementProcessor, RouteProcessor } from '../../src/processors';

before(() => {
  context.setGlobalContextManager(new AsyncLocalStorageContextManager().enable());
});
after(() => context.disable());

function setup(mode: DbQueryTextMode = 'sanitized') {
  const exporter = new InMemorySpanExporter();
  const provider = new BasicTracerProvider({
    spanProcessors: [new RouteProcessor(), new DbStatementProcessor(mode), new SimpleSpanProcessor(exporter)],
  });
  return { exporter, tracer: provider.getTracer('t') };
}

test('db statements are sanitized when set after the span started', () => {
  const { exporter, tracer } = setup();
  const pg = tracer.startSpan('pg.query:SELECT shop', { kind: SpanKind.CLIENT, attributes: { 'db.system.name': 'postgresql' } });
  pg.setAttribute('db.query.text', "SELECT * FROM orders WHERE id = 42 AND note = 'secret'");
  pg.end();
  const my = tracer.startSpan('SELECT', { kind: SpanKind.CLIENT, attributes: { 'db.system': 'mysql', 'db.statement': 'SELECT * FROM t WHERE name = "bob"' } });
  my.end();
  const redis = tracer.startSpan('get', { kind: SpanKind.CLIENT, attributes: { 'db.system.name': 'redis', 'db.query.text': 'get products:42' } });
  redis.end();
  const mongo = tracer.startSpan('find', { kind: SpanKind.CLIENT, attributes: { 'db.system.name': 'mongodb', 'db.query.text': '{"filter":{"_id":"?"}}' } });
  mongo.end();
  const long = tracer.startSpan('q', { kind: SpanKind.CLIENT, attributes: { 'db.system.name': 'postgresql', 'db.query.text': 'SELECT ' + 'col, '.repeat(2000) + 'x FROM t' } });
  long.end();
  const spans = exporter.getFinishedSpans();
  assert.equal(spans[0].attributes['db.query.text'], 'SELECT * FROM orders WHERE id = ? AND note = ?');
  assert.equal(spans[1].attributes['db.statement'], 'SELECT * FROM t WHERE name = ?');
  assert.equal(spans[2].attributes['db.query.text'], 'GET ?');
  assert.equal(spans[3].attributes['db.query.text'], '{"filter":{"_id":"?"}}');
  assert.equal((spans[4].attributes['db.query.text'] as string).length, 4096);
});

test('db statement modes raw and off', () => {
  const raw = setup('raw');
  raw.tracer.startSpan('q', { attributes: { 'db.system.name': 'postgresql', 'db.query.text': 'SELECT 1' } }).end();
  assert.equal(raw.exporter.getFinishedSpans()[0].attributes['db.query.text'], 'SELECT 1');
  const off = setup('off');
  off.tracer.startSpan('q', { attributes: { 'db.system.name': 'postgresql', 'db.query.text': 'SELECT 1', 'db.statement': 'SELECT 1' } }).end();
  const attrs = off.exporter.getFinishedSpans()[0].attributes;
  assert.ok(!('db.query.text' in attrs) && !('db.statement' in attrs));
});

test('route of a descendant span names the entry server span', () => {
  const { exporter, tracer } = setup();
  const server = tracer.startSpan('GET', { kind: SpanKind.SERVER, attributes: { 'http.request.method': 'GET' } }, ROOT_CONTEXT);
  const sctx = trace.setSpan(ROOT_CONTEXT, server);
  const mw = tracer.startSpan('middleware - router', { attributes: { 'http.route': '/api' } }, sctx);
  const handler = tracer.startSpan('UsersController.get', { attributes: { 'http.route': '/api/users/:id' } }, trace.setSpan(sctx, mw));
  handler.end();
  mw.end();
  server.end();
  const s = exporter.getFinishedSpans().find((x) => x.kind === SpanKind.SERVER)!;
  assert.equal(s.attributes['http.route'], '/api/users/:id');
  assert.equal(s.name, 'GET /api/users/:id');
});

test('an existing http.route and remote/child server spans are left alone', () => {
  const { exporter, tracer } = setup();
  const server = tracer.startSpan('GET /a/:b', { kind: SpanKind.SERVER, attributes: { 'http.request.method': 'GET', 'http.route': '/a/:b' } });
  tracer.startSpan('h', { attributes: { 'http.route': '/a/:b/longer' } }, trace.setSpan(ROOT_CONTEXT, server)).end();
  server.end();
  const plain = tracer.startSpan('GET', { kind: SpanKind.SERVER, attributes: { 'http.method': 'GET' } });
  plain.end();
  const spans = exporter.getFinishedSpans();
  assert.equal(spans.find((x) => x.name.startsWith('GET /a'))!.attributes['http.route'], '/a/:b');
  assert.equal(spans.find((x) => x.name === 'GET')!.attributes['http.route'], undefined);
});
