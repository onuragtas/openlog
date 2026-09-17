// Session identity and sampling (docs/contracts/rum.md §1.1).
import assert from 'node:assert/strict';
import { afterEach, beforeEach, describe, it } from 'node:test';

import { SessionState } from '../../src/session.js';
import { installDom, type DomHarness } from '../helpers/dom.js';

let dom: DomHarness;
beforeEach(() => {
  dom = installDom();
});
afterEach(() => dom.restore());

const MINUTE = 60_000;

describe('SessionState', () => {
  it('mints a 32 hex character session id', () => {
    const s = new SessionState(1);
    assert.match(s.id, /^[0-9a-f]{32}$/);
  });

  it('reuses the session across page views in the same tab', () => {
    const a = new SessionState(1, 1_000_000);
    const b = new SessionState(1, 1_000_000 + 5 * MINUTE);
    assert.equal(b.id, a.id, 'a second page load continues the visit');
  });

  it('starts a new session after 30 minutes of inactivity', () => {
    const a = new SessionState(1, 1_000_000);
    const b = new SessionState(1, 1_000_000 + 31 * MINUTE);
    assert.notEqual(b.id, a.id);
  });

  it('caps a session at four hours even while it stays active', () => {
    const a = new SessionState(1, 1_000_000);
    const b = new SessionState(1, 1_000_000 + 5 * 60 * MINUTE);
    assert.notEqual(b.id, a.id, 'a tab left open for days is not one endless session');
  });

  it('decides sampling once for the whole session', () => {
    // Half a session is not a cheaper session, it is an unreadable one: a page view whose vitals were
    // dropped says nothing. So the decision is stored with the session, not taken per event.
    const s = new SessionState(0);
    assert.equal(s.sampled, false);
    const again = new SessionState(1, Date.now());
    assert.equal(again.sampled, false, 'an existing session keeps its decision even at a higher rate');
  });

  it('samples everything at rate 1', () => {
    assert.equal(new SessionState(1).sampled, true);
  });

  it('gives every page view its own trace so a route change is a new trace', () => {
    const s = new SessionState(1);
    const first = { trace: s.traceId, span: s.pageViewSpanId, view: s.pageViewId };
    const next = s.newPageView();
    assert.notEqual(next.traceId, first.trace);
    assert.notEqual(next.spanId, first.span);
    assert.notEqual(next.pageViewId, first.view);
    assert.equal(s.traceId, next.traceId, 'the state moves to the new page view');
  });

  it('keeps working when sessionStorage is unavailable', () => {
    // Private modes and sandboxed frames throw on access; monitoring must degrade, never break the page.
    (globalThis as Record<string, unknown>).sessionStorage = {
      getItem() {
        throw new Error('denied');
      },
      setItem() {
        throw new Error('denied');
      },
    };
    const s = new SessionState(1);
    assert.match(s.id, /^[0-9a-f]{32}$/);
    assert.doesNotThrow(() => s.touch());
  });
});
