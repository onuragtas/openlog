// The persistent visitor id behind DAU, MAU and retention (docs/contracts/analytics.md §1).
//
// Unlike a session this recognises someone across visits, so what it may do is narrower than what it looks like:
//
//   - It is written to the device only once the server said this key is in `visitors` mode. Until then it lives in
//     memory, so a key whose operator left analytics off never leaves anything behind in a visitor's browser.
//   - A withdrawal outlives the page. `setEnabled(false)` is remembered, because otherwise the next load would hand
//     someone who just said no a new id. Only an explicit `setEnabled(true)` undoes it, never an init() default.

import { sessionId } from './ids.js';

const STORAGE_KEY = 'openlog.rum.visitor';
/** Rotated after 13 months, matching how long the server keeps an actor (analytics.md §1.2). */
const MAX_AGE_MS = 395 * 24 * 60 * 60 * 1000;

interface StoredVisitor {
  id?: string;
  createdAt?: number;
  /** The visitor withdrew: no id is created or sent until the application enables collection again. */
  optOut?: boolean;
}

/** localStorage is unavailable in some privacy modes and inside sandboxed frames; degrade, never throw. */
function readStored(): StoredVisitor {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw) return (JSON.parse(raw) as StoredVisitor) ?? {};
  } catch {
    /* ignore */
  }
  return {};
}

function writeStored(v: StoredVisitor | null): void {
  try {
    if (v) localStorage.setItem(STORAGE_KEY, JSON.stringify(v));
    else localStorage.removeItem(STORAGE_KEY);
  } catch {
    /* ignore */
  }
}

function validId(v: unknown): v is string {
  return typeof v === 'string' && /^[0-9a-f]{32}$/.test(v);
}

/**
 * Where this visitor falls in [0, 1): FNV-1a of the id over 2^32. Sampling is once per visitor when there is one
 * (analytics.md §2), because a loyal user dropped on nine visits out of ten would look as if they had left. Every SDK
 * computes exactly this, so one person is in or out of the sample on all of them.
 */
export function visitorRoll(id: string): number {
  let h = 2166136261;
  for (let i = 0; i < id.length; i++) {
    h ^= id.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return (h >>> 0) / 4294967296;
}

export class Visitor {
  private current = '';
  private createdAt = 0;
  /** The server confirmed `visitors` mode, so the id may be written to the device. */
  private persistent = false;

  constructor(enabled: boolean, private readonly now: () => number = Date.now) {
    // An id in storage survives a default of false: an application that asks first starts every load disabled and
    // enables after reading its own consent, and the person it asked must not become new on each visit.
    const stored = readStored();
    if (enabled && !stored.optOut) this.adopt(stored);
  }

  private adopt(stored: StoredVisitor): void {
    const fresh = validId(stored.id) && this.now() - (stored.createdAt ?? 0) < MAX_AGE_MS;
    this.current = fresh ? (stored.id as string) : sessionId();
    this.createdAt = fresh ? (stored.createdAt as number) : this.now();
  }

  /** The id to send, or '' when there is none. */
  get id(): string {
    return this.current;
  }

  /** The server says this key recognises visitors. */
  confirm(): void {
    this.persistent = true;
    if (this.current) writeStored({ id: this.current, createdAt: this.createdAt });
  }

  /** The server says this key does not: nothing stays on the device, except a visitor's own withdrawal. */
  discard(): void {
    this.persistent = false;
    this.current = '';
    if (!readStored().optOut) writeStored(null);
  }

  /** Firebase's setAnalyticsCollectionEnabled. Disabling deletes the id, so enabling again starts a new one. */
  setEnabled(enabled: boolean): void {
    if (!enabled) {
      this.current = '';
      writeStored({ optOut: true });
      return;
    }
    if (this.current) return;
    const stored = readStored();
    this.adopt(stored);
    if (this.persistent) writeStored({ id: this.current, createdAt: this.createdAt });
    else if (stored.optOut) writeStored(null);
  }
}
