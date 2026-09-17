// Core Web Vitals collection (docs/contracts/rum.md §2).
import assert from 'node:assert/strict';
import { afterEach, beforeEach, describe, it } from 'node:test';

import { collectVitals, type VitalSample } from '../../src/vitals.js';
import { installDom, type DomHarness } from '../helpers/dom.js';

let dom: DomHarness;
let seen: VitalSample[];
const value = (name: string) => seen.find((v) => v.name === name)?.value;

beforeEach(() => {
  dom = installDom();
  seen = [];
});
afterEach(() => dom.restore());

describe('collectVitals', () => {
  it('reports TTFB from the navigation entry immediately', () => {
    collectVitals((v) => seen.push(v));
    // TTFB is final as soon as the response starts; nothing is gained by holding it.
    assert.equal(value('ttfb'), 80);
  });

  it('reports FCP from the paint entry', () => {
    const { finalize } = collectVitals((v) => seen.push(v));
    dom.emitPerf('paint', [{ name: 'first-contentful-paint', startTime: 420 }]);
    finalize();
    assert.equal(value('fcp'), 420);
  });

  it('holds LCP until the page is hidden and keeps the largest candidate', () => {
    const { finalize } = collectVitals((v) => seen.push(v));
    dom.emitPerf('largest-contentful-paint', [{ startTime: 900 }]);
    dom.emitPerf('largest-contentful-paint', [{ startTime: 2100 }]);
    // Before finalize the value is not final: a larger element can still paint.
    assert.equal(value('lcp'), undefined);
    finalize();
    assert.equal(value('lcp'), 2100, 'the last (largest) candidate wins');
  });

  it('scores CLS as the worst burst, not the sum of every shift', () => {
    const { finalize } = collectVitals((v) => seen.push(v));
    // Two shifts inside one burst (<1s apart) add up to 0.09...
    dom.emitPerf('layout-shift', [
      { startTime: 100, value: 0.05, hadRecentInput: false },
      { startTime: 400, value: 0.04, hadRecentInput: false },
    ]);
    // ...then a gap larger than 1s starts a new burst worth 0.06, which must not be added to the first.
    dom.emitPerf('layout-shift', [{ startTime: 3000, value: 0.06, hadRecentInput: false }]);
    finalize();
    assert.ok(Math.abs(value('cls')! - 0.09) < 1e-9, `expected the 0.09 burst, got ${value('cls')}`);
  });

  it('ignores layout shifts the visitor caused', () => {
    const { finalize } = collectVitals((v) => seen.push(v));
    dom.emitPerf('layout-shift', [{ startTime: 100, value: 0.5, hadRecentInput: true }]);
    finalize();
    assert.equal(value('cls'), 0, 'a shift right after an interaction is the user opening something');
  });

  it('reports CLS even when it is zero', () => {
    // "This page does not shift" is a real measurement; omitting it would make a perfect page look
    // indistinguishable from one that could not be measured.
    const { finalize } = collectVitals((v) => seen.push(v));
    finalize();
    assert.equal(value('cls'), 0);
  });

  it('takes INP from interaction entries only', () => {
    const { finalize } = collectVitals((v) => seen.push(v));
    dom.emitPerf('event', [
      { duration: 40, interactionId: 1 },
      { duration: 250, interactionId: 2 },
      { duration: 900 }, // no interactionId: not an interaction, must not count
    ]);
    finalize();
    assert.equal(value('inp'), 250);
  });

  it('reports each vital once, however often finalize runs', () => {
    const { finalize } = collectVitals((v) => seen.push(v));
    dom.emitPerf('largest-contentful-paint', [{ startTime: 1000 }]);
    finalize();
    finalize();
    assert.equal(seen.filter((v) => v.name === 'lcp').length, 1);
  });

  it('survives a browser without a given entry type', () => {
    // observe() throws for unsupported types in real browsers; the vital is skipped, not fatal.
    assert.doesNotThrow(() => collectVitals((v) => seen.push(v)).finalize());
  });
});
