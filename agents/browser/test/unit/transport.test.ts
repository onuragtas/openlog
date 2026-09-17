// Batching, the unload flush and the OTLP body (docs/contracts/rum.md §1.2).
import assert from 'node:assert/strict';
import { afterEach, beforeEach, describe, it } from 'node:test';

import { resolveConfig } from '../../src/config.js';
import { buildSpan } from '../../src/otlp.js';
import { onPageHidden, Transport } from '../../src/transport.js';
import { installDom, type DomHarness } from '../helpers/dom.js';

const OPTIONS = { key: 'olb_test', endpoint: 'https://ingest.example.com:4318/' };
const span = (name: string) => buildSpan({ name, event: 'page_view', startMs: 1_700_000_000_000, durationMs: 10 });

let dom: DomHarness;
beforeEach(() => {
  dom = installDom();
});
afterEach(() => dom.restore());

describe('Transport', () => {
  it('buffers until the batch is full, then sends one request with every span', () => {
    const cfg = resolveConfig({ ...OPTIONS, maxBatchSize: 3 });
    const t = new Transport(cfg);
    t.add(span('a'));
    t.add(span('b'));
    assert.equal(dom.fetches.length, 0, 'an incomplete batch is not sent');
    t.add(span('c'));
    assert.equal(dom.fetches.length, 1);
    const body = JSON.parse(String(dom.fetches[0].init?.body));
    assert.deepEqual(
      body.resourceSpans[0].scopeSpans[0].spans.map((s: { name: string }) => s.name),
      ['a', 'b', 'c'],
    );
  });

  it('posts to /v1/rum with the key in a header and no credentials', () => {
    const cfg = resolveConfig({ ...OPTIONS, maxBatchSize: 1 });
    new Transport(cfg).add(span('a'));
    const req = dom.fetches[0];
    assert.equal(req.url, 'https://ingest.example.com:4318/v1/rum');
    const headers = req.init?.headers as Record<string, string>;
    assert.equal(headers['openlog-browser-key'], 'olb_test');
    assert.equal(req.init?.credentials, 'omit');
    assert.equal(req.init?.keepalive, true);
  });

  it('flushes with sendBeacon on unload, carrying the key in the query string', () => {
    // sendBeacon cannot set headers and cannot survive a preflight, so the key moves to the URL and the
    // body is text/plain. Losing this is losing the last batch of every visit.
    const cfg = resolveConfig(OPTIONS);
    const t = new Transport(cfg);
    t.add(span('a'));
    assert.equal(dom.fetches.length, 0);
    t.flush(true);
    assert.equal(dom.beacons.length, 1);
    assert.equal(dom.beacons[0].url, 'https://ingest.example.com:4318/v1/rum?k=olb_test');
    assert.match(dom.beacons[0].body, /"name":"a"/);
    assert.equal(dom.fetches.length, 0, 'a successful beacon does not also fetch');
  });

  it('flushes when the page is hidden and again on pagehide, without double-sending', () => {
    const cfg = resolveConfig(OPTIONS);
    const t = new Transport(cfg);
    onPageHidden(() => t.flush(true));
    t.add(span('a'));
    dom.hidePage();
    assert.equal(dom.beacons.length, 1, 'the queue is emptied by the first flush');
  });

  it('sends nothing when the queue is empty', () => {
    const t = new Transport(resolveConfig(OPTIONS));
    t.flush(true);
    assert.equal(dom.beacons.length, 0);
    assert.equal(dom.fetches.length, 0);
  });

  it('drops the oldest spans rather than growing without bound', () => {
    const cfg = resolveConfig({ ...OPTIONS, maxBatchSize: 1000 });
    const t = new Transport(cfg);
    for (let i = 0; i < 300; i++) t.add(span(`s${i}`));
    t.flush(true);
    const body = JSON.parse(dom.beacons[0].body);
    const names = body.resourceSpans[0].scopeSpans[0].spans.map((s: { name: string }) => s.name);
    assert.equal(names.length, 256, 'the queue is capped');
    assert.equal(names[names.length - 1], 's299', 'the newest span survives');
    assert.equal(names[0], 's44', 'the oldest were dropped');
  });

  it('describes itself as the openlog browser SDK in the resource', () => {
    const cfg = resolveConfig({ ...OPTIONS, maxBatchSize: 1, serviceName: 'shop-web', environment: 'production' });
    new Transport(cfg).add(span('a'));
    const body = JSON.parse(String(dom.fetches[0].init?.body));
    const attrs: Record<string, string> = {};
    for (const kv of body.resourceSpans[0].resource.attributes) attrs[kv.key] = kv.value.stringValue;
    assert.equal(attrs['service.name'], 'shop-web');
    assert.equal(attrs['deployment.environment.name'], 'production');
    assert.equal(attrs['telemetry.sdk.name'], 'openlog-browser');
    assert.equal(attrs['telemetry.sdk.language'], 'webjs');
  });
});

describe('buildSpan', () => {
  it('encodes timestamps as unix nanoseconds without losing precision', () => {
    const s = buildSpan({ name: 'x', event: 'page_view', startMs: 1_700_000_000_123, durationMs: 250 });
    assert.equal(s.startTimeUnixNano, '1700000000123000000');
    assert.equal(s.endTimeUnixNano, '1700000000373000000');
  });

  it('puts an exception on a span event, the shape the error inbox groups', () => {
    const s = buildSpan({
      name: 'error TypeError',
      event: 'error',
      startMs: 1,
      error: true,
      exception: { type: 'TypeError', message: 'x is not a function', stacktrace: '    at main.js:1:1' },
    });
    assert.equal(s.status?.code, 2);
    assert.equal(s.events?.[0].name, 'exception');
    const attrs = Object.fromEntries(s.events![0].attributes.map((a) => [a.key, a.value.stringValue]));
    assert.equal(attrs['exception.type'], 'TypeError');
    assert.equal(attrs['exception.stacktrace'], '    at main.js:1:1');
  });

  it('omits empty attributes instead of sending empty strings', () => {
    const s = buildSpan({ name: 'x', event: 'vital', startMs: 1, attributes: { a: 'v', b: '', c: undefined, d: 0 } });
    const keys = s.attributes.map((a) => a.key);
    assert.deepEqual(keys, ['a', 'd']);
  });
});
