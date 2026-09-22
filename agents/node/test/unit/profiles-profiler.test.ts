import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import { gunzipSync } from 'node:zlib';
import { Diag } from '../../src/diag';
import { startProfiler, type ProfilerConfig } from '../../src/profiles/profiler';
import type { V8Profile } from '../../src/profiles/convert';

const cfg = (over: Partial<ProfilerConfig> = {}): ProfilerConfig => ({
  endpoint: 'https://ingest.example.com:4318',
  headers: { 'openlog-license-key': 'olk_test' },
  compression: 'none',
  exportTimeoutMs: 10_000,
  profileIntervalMs: 60_000,
  version: '1.2.3',
  ...over,
});

const busy = (): V8Profile => ({
  nodes: [{ id: 1, callFrame: { functionName: 'work' } }],
  startTime: 0,
  endTime: 1_000_000,
  samples: [1],
  timeDeltas: [1000],
});

/** Collects what would have been posted. */
function server(status = 200) {
  const calls: { url: string; headers: Record<string, string>; body: Buffer }[] = [];
  return {
    calls,
    send: async (url: string, headers: Record<string, string>, body: Buffer) => {
      calls.push({ url, headers, body });
      return status;
    },
  };
}

const quiet = new Diag('off');
const settle = (): Promise<void> => new Promise((r) => setImmediate(r));

describe('startProfiler', () => {
  it('posts one profile per cycle, to the profiles endpoint with the key', async () => {
    const s = server();
    const p = startProfiler(cfg(), [{ key: 'service.name', value: { stringValue: 'checkout' } }], quiet, {
      take: async () => busy(),
      send: s.send,
    });
    await settle();
    p.stop();

    assert.equal(s.calls.length >= 1, true);
    assert.equal(s.calls[0].url, 'https://ingest.example.com:4318/v1/profiles');
    assert.equal(s.calls[0].headers['openlog-license-key'], 'olk_test');
    assert.equal(s.calls[0].headers['content-type'], 'application/json');
    const sent = JSON.parse(s.calls[0].body.toString('utf8')) as Record<string, unknown>;
    assert.ok(sent.dictionary, 'the payload carries the dictionary the server expands');
  });

  it('sends nothing when the process was idle', async () => {
    const s = server();
    // The ingest accepts a profile-less body and produces nothing, so sending one would cost both sides.
    const p = startProfiler(cfg(), [], quiet, { take: async () => null, send: s.send });
    await settle();
    p.stop();
    assert.equal(s.calls.length, 0);
  });

  it('gzips when the agent is configured to', async () => {
    const s = server();
    const p = startProfiler(cfg({ compression: 'gzip' }), [], quiet, { take: async () => busy(), send: s.send });
    await settle();
    p.stop();
    assert.equal(s.calls[0].headers['content-encoding'], 'gzip');
    // Readable again: a body that says it is gzip and is not would be rejected as a decode failure.
    assert.ok(JSON.parse(gunzipSync(s.calls[0].body).toString('utf8')));
  });

  it('survives a refusal and a thrown profiler', async () => {
    const refused = server(503);
    const p1 = startProfiler(cfg(), [], quiet, { take: async () => busy(), send: refused.send });
    await settle();
    p1.stop();

    // A profile that cannot be taken must not take the application down with it.
    const s = server();
    const p2 = startProfiler(cfg(), [], quiet, {
      take: async () => {
        throw new Error('inspector unavailable');
      },
      send: s.send,
    });
    await settle();
    p2.stop();
    assert.equal(s.calls.length, 0);
  });

  it('says so when an export is refused', async () => {
    // A profile that silently fails to arrive is worse than one never taken: the screen is empty and
    // nothing anywhere says why.
    const lines: string[] = [];
    const log = new Diag('warn', (l) => lines.push(l));
    const refused = server(503);
    const p = startProfiler(cfg(), [], log, { take: async () => busy(), send: refused.send });
    await settle();
    p.stop();
    assert.equal(
      lines.some((l) => l.includes('profile export failed') && l.includes('status=503')),
      true,
      lines.join('\n'),
    );
  });

  it('never holds the process open', async () => {
    const timers: NodeJS.Timeout[] = [];
    const s = server();
    const p = startProfiler(cfg(), [], quiet, {
      take: async () => busy(),
      send: s.send,
      setTimer: (fn, ms) => {
        const handle = setTimeout(fn, ms);
        timers.push(handle);
        return handle;
      },
    });
    await settle();
    p.stop();
    assert.ok(timers.length > 0, 'a cycle was scheduled');
    // Profiling must never be the reason a process stays alive.
    assert.equal(
      timers.some((h) => h.hasRef()),
      false,
      'a scheduled timer was still holding the event loop',
    );
  });

  it('does not post a profile that was in flight when the agent stopped', async () => {
    // The realistic shutdown: a profile covers a whole interval, so when the process is asked to stop there
    // is almost always one being taken. Cancelling the next cycle is not enough — the one already running
    // must not deliver after the agent was told to stop.
    let release!: () => void;
    const inFlight = new Promise<void>((r) => (release = r));
    const s = server();
    const p = startProfiler(cfg(), [], quiet, {
      take: async () => {
        await inFlight;
        return busy();
      },
      send: s.send,
    });
    await settle();
    p.stop();
    release();
    await settle();
    await settle();
    assert.equal(s.calls.length, 0, 'a profile arrived after shutdown');
  });

  it('stops posting once stopped', async () => {
    const s = server();
    const p = startProfiler(cfg(), [], quiet, { take: async () => busy(), send: s.send });
    await settle();
    const afterFirst = s.calls.length;
    p.stop();
    await settle();
    await settle();
    assert.equal(s.calls.length, afterFirst, 'no cycle runs after stop');
  });
});
