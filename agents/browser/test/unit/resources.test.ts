// Static asset timing (docs/contracts/rum.md §2.6).
//
// The whole design is a bound: a page loads far more assets than it makes requests, so what matters is not
// that the timings are read but that the number of them is decided in advance and the right ones survive.
import assert from 'node:assert/strict';
import { afterEach, beforeEach, describe, it } from 'node:test';

import { collectResources, type ResourceSample } from '../../src/resources.js';
import { installDom, type DomHarness } from '../helpers/dom.js';

let dom: DomHarness;
beforeEach(() => {
  dom = installDom();
});
afterEach(() => dom.restore());

/** A PerformanceResourceTiming as the browser reports one. */
function entry(name: string, initiatorType: string, duration: number, sizes: { transfer?: number; decoded?: number } = {}) {
  return {
    name,
    entryType: 'resource',
    startTime: 100,
    duration,
    initiatorType,
    transferSize: sizes.transfer ?? 1000,
    encodedBodySize: 900,
    decodedBodySize: sizes.decoded ?? 2000,
  };
}

function collect(limit: number): { samples: ResourceSample[]; flush: () => void; stop: () => void } {
  const samples: ResourceSample[] = [];
  const c = collectResources(limit, (s) => samples.push(s));
  return { samples, flush: c.flush, stop: c.stop };
}

describe('collectResources', () => {
  it('keeps the slowest assets and no more than the limit', () => {
    const c = collect(2);
    dom.emitPerf('resource', [
      entry('https://shop.example.com/a.js', 'script', 40),
      entry('https://shop.example.com/slow.js', 'script', 900),
      entry('https://shop.example.com/b.css', 'link', 120),
      entry('https://shop.example.com/mid.png', 'img', 300),
    ]);
    c.flush();

    assert.equal(c.samples.length, 2, 'the cap is what bounds the volume');
    assert.deepEqual(
      c.samples.map((s) => s.durationMs),
      [900, 300],
      'the slowest survive, in order',
    );
    c.stop();
  });

  it('leaves fetch and XHR to the request instrumentation', () => {
    const c = collect(10);
    dom.emitPerf('resource', [
      entry('https://shop.example.com/api/cart', 'fetch', 500),
      entry('https://shop.example.com/api/user', 'xmlhttprequest', 400),
      entry('https://shop.example.com/app.js', 'script', 50),
    ]);
    c.flush();

    // Counting them here as well would double every request in the session timeline, and these copies
    // carry neither the trace id nor the status code that fetch.ts records.
    assert.deepEqual(
      c.samples.map((s) => s.initiator),
      ['script'],
    );
    c.stop();
  });

  it('marks an asset the cache answered', () => {
    const c = collect(10);
    dom.emitPerf('resource', [
      entry('https://shop.example.com/cached.js', 'script', 0, { transfer: 0, decoded: 5000 }),
      entry('https://shop.example.com/fetched.js', 'script', 80, { transfer: 4000, decoded: 5000 }),
    ]);
    c.flush();

    const cached = c.samples.find((s) => s.url.endsWith('cached.js'));
    const fetched = c.samples.find((s) => s.url.endsWith('fetched.js'));
    // Without this a 0 ms asset is indistinguishable from an impossibly fast one.
    assert.equal(cached?.cached, true);
    assert.equal(fetched?.cached, false);
    c.stop();
  });

  it('drops the query string, like every other URL the SDK sends', () => {
    const c = collect(10);
    dom.emitPerf('resource', [entry('https://shop.example.com/app.js?token=secret&v=3', 'script', 10)]);
    c.flush();
    assert.equal(c.samples[0].url, 'https://shop.example.com/app.js');
    c.stop();
  });

  it('reports each asset once: flush empties what it sent', () => {
    const c = collect(10);
    dom.emitPerf('resource', [entry('https://shop.example.com/app.js', 'script', 10)]);
    c.flush();
    c.flush();
    // A page view accounts for its own assets; a second flush must not attribute them again to the next.
    assert.equal(c.samples.length, 1);
    c.stop();
  });
});
