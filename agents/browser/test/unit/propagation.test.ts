// W3C trace propagation and request capture (docs/contracts/rum.md §6).
import assert from 'node:assert/strict';
import { afterEach, beforeEach, describe, it } from 'node:test';

import { cleanURL, instrumentRequests, shouldPropagate, type RequestSpan } from '../../src/fetch.js';
import { installDom, type DomHarness } from '../helpers/dom.js';

let dom: DomHarness;
beforeEach(() => {
  dom = installDom({ url: 'https://shop.example.com/checkout' });
});
afterEach(() => dom.restore());

describe('shouldPropagate', () => {
  it('propagates to same-origin requests by default', () => {
    assert.equal(shouldPropagate('/api/cart', 'same-origin'), true);
    assert.equal(shouldPropagate('https://shop.example.com/api/cart', 'same-origin'), true);
  });

  it('does not propagate cross-origin by default', () => {
    // Sending traceparent to a third party both leaks trace ids and forces a preflight the other server
    // may reject, turning a working request into a failed one.
    assert.equal(shouldPropagate('https://analytics.vendor.com/t', 'same-origin'), false);
  });

  it('propagates to explicitly allowed targets, by prefix or pattern', () => {
    assert.equal(shouldPropagate('https://api.example.com/v1/cart', ['https://api.example.com']), true);
    assert.equal(shouldPropagate('https://api.example.com/v1/cart', [/^https:\/\/api\./]), true);
    assert.equal(shouldPropagate('https://other.example.com/x', ['https://api.example.com']), false);
  });
});

describe('cleanURL', () => {
  it('drops the query string and the fragment, where tokens and personal data live', () => {
    const { full, host } = cleanURL('https://shop.example.com/search?q=alice@example.com&token=abc#results');
    assert.equal(full, 'https://shop.example.com/search');
    assert.equal(host, 'shop.example.com');
  });
});

describe('instrumentRequests', () => {
  const ctx = { traceId: '4bf92f3577b34da6a3ce929d0e0e4736', parentSpanId: '00f067aa0ba902b7' };

  it('adds traceparent to a same-origin fetch and reports the span', async () => {
    const seen: RequestSpan[] = [];
    const restore = instrumentRequests({ targets: 'same-origin', context: () => ctx, report: (s) => seen.push(s) });
    await (globalThis as { fetch: (u: string, i?: RequestInit) => Promise<unknown> }).fetch('/api/cart', { method: 'POST' });
    restore();

    const headers = dom.fetches[0].init?.headers as { get(k: string): string | null };
    const tp = headers.get('traceparent')!;
    assert.match(tp, /^00-4bf92f3577b34da6a3ce929d0e0e4736-[0-9a-f]{16}-01$/);
    assert.equal(seen.length, 1);
    assert.equal(seen[0].method, 'POST');
    assert.equal(seen[0].status, 200);
    // The traceparent names the span the SDK reports, which is what links the two sides of the trace.
    assert.ok(tp.includes(seen[0].spanId));
  });

  it('does not add traceparent cross-origin but still records the request', async () => {
    const seen: RequestSpan[] = [];
    const restore = instrumentRequests({ targets: 'same-origin', context: () => ctx, report: (s) => seen.push(s) });
    await (globalThis as { fetch: (u: string, i?: RequestInit) => Promise<unknown> }).fetch('https://cdn.vendor.com/a.js');
    restore();

    const headers = dom.fetches[0].init?.headers as { get(k: string): string | null } | undefined;
    assert.equal(headers?.get('traceparent') ?? null, null);
    assert.equal(seen.length, 1, 'the call is still timed');
  });

  it('leaves requests alone when there is no page view to attach them to', async () => {
    const restore = instrumentRequests({ targets: 'same-origin', context: () => null, report: () => {} });
    await (globalThis as { fetch: (u: string) => Promise<unknown> }).fetch('/api/cart');
    restore();
    const headers = dom.fetches[0].init?.headers as { get(k: string): string | null } | undefined;
    assert.equal(headers?.get?.('traceparent') ?? null, null);
  });

  it('restores the original fetch on teardown', () => {
    const before = (globalThis as { fetch: unknown }).fetch;
    const restore = instrumentRequests({ targets: 'same-origin', context: () => ctx, report: () => {} });
    assert.notEqual((globalThis as { fetch: unknown }).fetch, before);
    restore();
    assert.equal((globalThis as { fetch: unknown }).fetch, before);
  });
});
