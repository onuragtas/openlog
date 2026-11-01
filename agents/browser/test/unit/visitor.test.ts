// The persistent visitor id (docs/contracts/analytics.md §1).
import assert from 'node:assert/strict';
import { afterEach, beforeEach, describe, it } from 'node:test';

import { landingAttribution } from '../../src/navigation.js';
import { SessionState } from '../../src/session.js';
import { Visitor, visitorRoll } from '../../src/visitor.js';
import { installDom, type DomHarness } from '../helpers/dom.js';

const STORAGE_KEY = 'openlog.rum.visitor';
const DAY = 24 * 60 * 60 * 1000;

let dom: DomHarness;
const original = Object.getOwnPropertyDescriptor(globalThis, 'localStorage');
let store: Map<string, string>;

beforeEach(() => {
  dom = installDom();
  store = new Map();
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k),
    },
  });
});
afterEach(() => {
  dom.restore();
  if (original) Object.defineProperty(globalThis, 'localStorage', original);
  else delete (globalThis as { localStorage?: unknown }).localStorage;
});

describe('Visitor', () => {
  it('leaves nothing on the device until the server confirms visitors mode', () => {
    // This is what keeps a key whose operator left analytics off from writing an id into someone's browser.
    const v = new Visitor(true);
    assert.match(v.id, /^[0-9a-f]{32}$/);
    assert.equal(store.size, 0);
    v.confirm();
    assert.ok(store.has(STORAGE_KEY));
  });

  it('removes what is stored when the server says the key does not recognise visitors', () => {
    const a = new Visitor(true);
    a.confirm();
    const b = new Visitor(true);
    b.discard();
    assert.equal(b.id, '');
    assert.equal(store.size, 0);
  });

  it('recognises the same visitor on the next load', () => {
    const a = new Visitor(true);
    a.confirm();
    assert.equal(new Visitor(true).id, a.id);
  });

  it('keeps a withdrawal across loads, whatever init() defaults to', () => {
    const a = new Visitor(true);
    a.confirm();
    a.setEnabled(false);
    assert.equal(a.id, '');
    assert.equal(store.get(STORAGE_KEY)?.includes(a.id || 'x'), false);

    const reload = new Visitor(true);
    assert.equal(reload.id, '', 'the default must not undo a person saying no');
    reload.confirm();
    assert.equal(JSON.parse(store.get(STORAGE_KEY) as string).id, undefined);
  });

  it('keeps a withdrawal through a server answer', () => {
    new Visitor(true).setEnabled(false);
    new Visitor(true).discard();
    assert.equal(JSON.parse(store.get(STORAGE_KEY) as string).optOut, true);
  });

  it('gives a new id when collection is enabled again', () => {
    const a = new Visitor(true);
    a.confirm();
    const before = a.id;
    a.setEnabled(false);
    a.setEnabled(true);
    assert.match(a.id, /^[0-9a-f]{32}$/);
    assert.notEqual(a.id, before, 'someone who withdrew is not recognised on returning');
  });

  it('starts with no id when the application asks first, and keeps the earlier one on consent', () => {
    const consented = new Visitor(true);
    consented.confirm();

    const v = new Visitor(false);
    assert.equal(v.id, '');
    v.confirm();
    v.setEnabled(true);
    assert.equal(v.id, consented.id, 'the person it asked must not become new on each visit');
  });

  it('persists on enable once the server has confirmed', () => {
    const v = new Visitor(false);
    v.confirm();
    assert.equal(store.size, 0);
    v.setEnabled(true);
    assert.equal(JSON.parse(store.get(STORAGE_KEY) as string).id, v.id);
  });

  it('rotates the id after 13 months', () => {
    let now = 1_000_000;
    const a = new Visitor(true, () => now);
    a.confirm();
    now += 394 * DAY;
    assert.equal(new Visitor(true, () => now).id, a.id);
    now += 2 * DAY;
    assert.notEqual(new Visitor(true, () => now).id, a.id);
  });

  it('survives corrupt storage and storage that throws', () => {
    store.set(STORAGE_KEY, '{not json');
    assert.match(new Visitor(true).id, /^[0-9a-f]{32}$/);
    Object.defineProperty(globalThis, 'localStorage', { configurable: true, get: () => { throw new Error('denied'); } });
    const v = new Visitor(true);
    v.confirm();
    v.setEnabled(false);
    assert.equal(v.id, '');
  });
});

describe('visitorRoll', () => {
  it('matches the vector every SDK is tested against', () => {
    // analytics.md §2. A different value here means one person is sampled differently per platform.
    assert.equal(visitorRoll('fedcba9876543210fedcba9876543210'), 777805509 / 4294967296);
  });

  it('decides sampling by the visitor and not by the session', () => {
    const id = 'fedcba9876543210fedcba9876543210';
    const roll = visitorRoll(id);
    for (let i = 0; i < 20; i++) {
      dom.restore();
      dom = installDom();
      assert.equal(new SessionState(roll + 0.01, undefined, () => visitorRoll(id)).sampled, true);
      dom.restore();
      dom = installDom();
      assert.equal(new SessionState(roll - 0.01, undefined, () => visitorRoll(id)).sampled, false);
    }
  });
});

describe('SessionState landing', () => {
  it('reports the landing once per session, and not on a reload within it', () => {
    const a = new SessionState(1, 1_000_000);
    assert.equal(a.takeLanding(), true);
    assert.equal(a.takeLanding(), false);
    assert.equal(new SessionState(1, 1_000_000 + 1000).takeLanding(), false);
  });

  it('reports it again when an idle session rolls over', () => {
    const a = new SessionState(1, 1_000_000);
    a.takeLanding();
    a.touch(1_000_000 + 31 * 60_000);
    assert.equal(a.takeLanding(), true);
  });
});

describe('landingAttribution', () => {
  function at(url: string, referrer: string) {
    const u = new URL(url);
    Object.defineProperty(globalThis, 'location', { configurable: true, value: u });
    Object.defineProperty(globalThis, 'document', { configurable: true, value: { referrer } });
  }

  it('sends the host of the referrer and never its path', () => {
    at('https://shop.example/x', 'https://news.example/article/secret?token=1');
    assert.deepEqual(landingAttribution(true)['openlog.rum.referrer.host'], 'news.example');
  });

  it('treats a same-site referrer as direct', () => {
    at('https://shop.example/x', 'https://shop.example/y');
    assert.equal(landingAttribution(true)['openlog.rum.referrer.host'], undefined);
  });

  it('ignores the document referrer on a route change, which is stale by then', () => {
    at('https://shop.example/x', 'https://news.example/');
    assert.equal(landingAttribution(false)['openlog.rum.referrer.host'], undefined);
  });

  it('reads only source, medium and campaign', () => {
    at('https://shop.example/x?utm_source=mail&utm_medium=email&utm_campaign=spring&utm_term=me%40x.example&utm_content=y', '');
    assert.deepEqual(landingAttribution(true), {
      'openlog.rum.utm.source': 'mail',
      'openlog.rum.utm.medium': 'email',
      'openlog.rum.utm.campaign': 'spring',
    });
  });
});
