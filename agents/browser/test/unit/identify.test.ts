// User identity on the session's spans (docs/contracts/rum.md §3.7).
//
// identify() is the one call that turns a visit into a person, so what it does and what it deliberately does
// not do are both worth holding in place: it attaches an id to later spans, it never attaches an empty one,
// and it keeps nothing across a reload.
import assert from 'node:assert/strict';
import { after, afterEach, before, describe, it } from 'node:test';

import { init } from '../../src/index.js';
import { installDom, type DomHarness } from '../helpers/dom.js';

// Every background collector is off: what is under test is identify(), not page views, vitals, errors or
// request instrumentation. Leaving them on would mean this file also depends on when each of them decides
// to emit — and the first page view is emitted asynchronously, after the test that started it has ended.
const OPTIONS = {
  key: 'olb_test',
  endpoint: 'https://ingest.example.com:4318/',
  maxBatchSize: 1,
  capturePageViews: false,
  captureVitals: false,
  captureErrors: false,
  captureRequests: false,
};

// The DOM double is installed once for the whole file rather than per test, and that is about asynchrony
// rather than tidiness. These are the only tests that drive init(), and init() starts a background fetch of
// /v1/rum/config whose continuation runs after the test body returns. Restoring the globals between tests
// pulls `location` out from under that continuation, which then throws from a test node:test has already
// finished. Each test still gets a fresh SDK: shutdown() clears the module-level instance.
let dom: DomHarness;
let sdk: ReturnType<typeof init> | null = null;

before(() => {
  dom = installDom();
});
after(() => dom.restore());
afterEach(() => {
  sdk?.shutdown();
  sdk = null;
});

/** The attributes of the span in the most recent request, as a plain object. */
function lastSpanAttributes(): Record<string, string> {
  assert.ok(dom.fetches.length > 0, 'nothing was sent');
  const body = JSON.parse(String(dom.fetches[dom.fetches.length - 1].init?.body));
  const span = body.resourceSpans[0].scopeSpans[0].spans[0];
  const out: Record<string, string> = {};
  for (const kv of span.attributes as { key: string; value: { stringValue?: string } }[]) {
    if (typeof kv.value.stringValue === 'string') out[kv.key] = kv.value.stringValue;
  }
  return out;
}

describe('identify', () => {
  it('attaches the id to spans recorded after the call', () => {
    sdk = init(OPTIONS);
    sdk.identify('acct_8f3a2b');
    sdk.recordEvent('checkout_started');
    assert.equal(lastSpanAttributes()['user.id'], 'acct_8f3a2b');
  });

  it('sends no user.id at all until the application calls it', () => {
    sdk = init(OPTIONS);
    sdk.recordEvent('checkout_started');
    // Absent, not empty. Enforced by buildSpan, which drops empty attributes (see the transport suite);
    // this holds the end-to-end promise rather than that one mechanism.
    assert.equal('user.id' in lastSpanAttributes(), false);
  });

  it('clears the identity on sign-out', () => {
    sdk = init(OPTIONS);
    sdk.identify('acct_8f3a2b');
    sdk.identify('');
    sdk.recordEvent('signed_out');
    assert.equal('user.id' in lastSpanAttributes(), false);
  });

  it('bounds the id to what the server stores', () => {
    sdk = init(OPTIONS);
    sdk.identify('u'.repeat(500));
    sdk.recordEvent('checkout_started');
    // Truncated here as well as on the server: a page should not discover the limit by having its spans
    // silently change shape somewhere it cannot see.
    assert.equal(lastSpanAttributes()['user.id'].length, 128);
  });

  it('trims what it is given', () => {
    sdk = init(OPTIONS);
    sdk.identify('  acct_8f3a2b  ');
    sdk.recordEvent('checkout_started');
    assert.equal(lastSpanAttributes()['user.id'], 'acct_8f3a2b');
  });
});
