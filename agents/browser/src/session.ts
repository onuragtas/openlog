// Session and page view state (docs/contracts/rum.md §1.1).
//
// A session is a visit, not a person: a random id in sessionStorage, expiring after 30 minutes of inactivity
// and capped at 4 hours. It is never derived from anything about the visitor, is per tab, and disappears when
// the tab does — so it cannot be used to recognise someone across visits, which is exactly what makes it
// safe to collect without asking.

import { sessionId, spanId, traceId } from './ids.js';

const STORAGE_KEY = 'openlog.rum.session';
/** Inactivity after which a new session starts. */
const IDLE_MS = 30 * 60 * 1000;
/** Absolute cap, so a tab left open for days does not report one endless session. */
const MAX_MS = 4 * 60 * 60 * 1000;

interface StoredSession {
  id: string;
  startedAt: number;
  lastSeenAt: number;
  /** Whether this session is sampled in. Decided once, for the whole session. */
  sampled: boolean;
}

/** sessionStorage is unavailable in some privacy modes and inside sandboxed frames; degrade, never throw. */
function readStored(): StoredSession | null {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const v = JSON.parse(raw) as StoredSession;
    if (typeof v?.id === 'string' && v.id.length === 32 && typeof v.startedAt === 'number') return v;
  } catch {
    /* ignore */
  }
  return null;
}

function writeStored(s: StoredSession): void {
  try {
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(s));
  } catch {
    /* ignore */
  }
}

export class SessionState {
  private session: StoredSession;
  /** The current page view: its id, and the trace every span of the view belongs to. */
  pageViewId = spanId();
  traceId = traceId();
  /** The page view span's own id, so requests and errors become its children. */
  pageViewSpanId = spanId();

  constructor(private sampleRate: number, now = Date.now()) {
    this.session = this.load(now);
  }

  private load(now: number): StoredSession {
    const stored = readStored();
    if (stored && now - stored.lastSeenAt < IDLE_MS && now - stored.startedAt < MAX_MS) {
      stored.lastSeenAt = now;
      writeStored(stored);
      return stored;
    }
    // Sampling is decided per session, not per event: half a session is not a cheaper session, it is an
    // unreadable one — a page view whose vitals were dropped tells you nothing.
    const fresh: StoredSession = {
      id: sessionId(),
      startedAt: now,
      lastSeenAt: now,
      sampled: Math.random() < this.sampleRate,
    };
    writeStored(fresh);
    return fresh;
  }

  get id(): string {
    return this.session.id;
  }

  get sampled(): boolean {
    return this.session.sampled;
  }

  /** Records activity and rolls the session over when it has gone idle or hit the cap. */
  touch(now = Date.now()): void {
    if (now - this.session.lastSeenAt >= IDLE_MS || now - this.session.startedAt >= MAX_MS) {
      this.session = this.load(now);
      return;
    }
    this.session.lastSeenAt = now;
    writeStored(this.session);
  }

  /** Starts a new page view (a document load or an SPA route change) and returns its ids. */
  newPageView(): { pageViewId: string; traceId: string; spanId: string } {
    this.pageViewId = spanId();
    this.traceId = traceId();
    this.pageViewSpanId = spanId();
    return { pageViewId: this.pageViewId, traceId: this.traceId, spanId: this.pageViewSpanId };
  }

  /** Re-applies a sample rate the server sent; only a session that has not been decided yet can change. */
  applyServerSampleRate(rate: number, now = Date.now()): void {
    if (!(rate > 0 && rate <= 1) || rate === this.sampleRate) return;
    this.sampleRate = rate;
    const stored = readStored();
    // An existing session keeps its decision: flipping mid-visit produces exactly the half-sessions the
    // per-session decision exists to avoid.
    if (!stored) this.session = this.load(now);
  }
}
