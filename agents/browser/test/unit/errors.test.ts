// Uncaught errors and unhandled rejections (docs/contracts/rum.md §2.3).
import assert from 'node:assert/strict';
import { afterEach, beforeEach, describe, it } from 'node:test';

import { captureErrors, type CapturedError } from '../../src/errors.js';
import { installDom, type DomHarness } from '../helpers/dom.js';

let dom: DomHarness;
let seen: CapturedError[];

beforeEach(() => {
  dom = installDom();
  seen = [];
});
afterEach(() => dom.restore());

/** Fires a listener registered on window by the SDK. */
function fire(type: string, ev: unknown): void {
  for (const fn of dom.listeners.get(`window:${type}`) ?? []) fn(ev);
}

describe('captureErrors', () => {
  it('captures an uncaught error with its type, message and stack', () => {
    const restore = captureErrors((e) => seen.push(e), false);
    const err = new TypeError('x is not a function');
    err.stack = 'TypeError: x is not a function\n    at main.3f2a1b9c.js:1:1';
    fire('error', { error: err, message: err.message });
    restore();

    assert.equal(seen.length, 1);
    assert.equal(seen[0].type, 'TypeError');
    assert.equal(seen[0].message, 'x is not a function');
    // The stack is what makes a JS error actionable, and what the backend fingerprints on.
    assert.match(seen[0].stacktrace, /main\.3f2a1b9c\.js/);
    assert.equal(seen[0].source, 'error');
  });

  it('synthesises a frame when there is no Error object', () => {
    // A cross-origin script gives "Script error." with no Error; the file and line are all we get.
    const restore = captureErrors((e) => seen.push(e), false);
    fire('error', { message: 'Script error.', filename: 'https://cdn.example.com/a.js', lineno: 12, colno: 3 });
    restore();
    assert.equal(seen[0].message, 'Script error.');
    assert.match(seen[0].stacktrace, /https:\/\/cdn\.example\.com\/a\.js:12:3/);
  });

  it('captures an unhandled promise rejection', () => {
    const restore = captureErrors((e) => seen.push(e), false);
    fire('unhandledrejection', { reason: new RangeError('too big') });
    restore();
    assert.equal(seen[0].type, 'RangeError');
    assert.equal(seen[0].source, 'unhandledrejection');
  });

  it('captures a rejection of a non-Error value', () => {
    const restore = captureErrors((e) => seen.push(e), false);
    fire('unhandledrejection', { reason: { status: 503, detail: 'upstream down' } });
    restore();
    assert.equal(seen.length, 1, 'a rejected plain object is still worth reporting');
    assert.match(seen[0].message, /upstream down/);
  });

  it('leaves console.error alone unless asked', () => {
    const original = console.error;
    const restore = captureErrors((e) => seen.push(e), false);
    assert.equal(console.error, original, 'patching console by default would be a surprise');
    restore();
  });

  it('reports console.error when enabled and still calls through', () => {
    const calls: unknown[][] = [];
    const original = console.error;
    console.error = (...args: unknown[]) => void calls.push(args);
    const restore = captureErrors((e) => seen.push(e), true);
    console.error('checkout failed', new Error('boom'));
    restore();
    console.error = original;

    assert.equal(seen.length, 1);
    assert.equal(seen[0].source, 'console');
    assert.equal(calls.length, 1, 'the application still sees its own console output');
  });

  it('removes its listeners on teardown', () => {
    const restore = captureErrors((e) => seen.push(e), false);
    restore();
    fire('error', { error: new Error('after teardown') });
    assert.equal(seen.length, 0);
  });
});
