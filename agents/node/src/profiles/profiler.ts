// Continuous CPU profiling for Node.js (docs/contracts/profiles.md).
//
// **Off by default**, unlike the Go agent. There the switch is forced by the runtime — Go allows one CPU
// profile per process, so an agent that profiles without asking would break net/http/pprof — and the
// default is on because the cost is a few percent of one core. Node has no such lock, but it does have
// existing installations: turning this on for everyone who upgrades would multiply what they store and are
// billed for, silently. An upgrade must not do that, so this is opt-in and says why.
//
// The exporter is written here because the OpenTelemetry JS SDK has no profiles exporter — profiles are not
// part of its stable surface — and the ingest accepts OTLP/JSON, so no protobuf runtime is needed.

import { gzipSync } from 'node:zlib';
import type { Diag } from '../diag';
import { fromV8Profile, type KeyValue, type V8Profile } from './convert';

export const PROFILE_SCOPE = 'openlog-node/profiler';

/** Takes a CPU profile covering `durationMs`. Replaced in tests so they never start V8's profiler. */
export type TakeProfile = (durationMs: number) => Promise<V8Profile | null>;

/** Posts a body and answers with the HTTP status, or -1 when the request never completed. */
export type Send = (url: string, headers: Record<string, string>, body: Buffer) => Promise<number>;

/** Schedules the next cycle. Injectable so a test can hold the handle and check it was unref'd. */
export type SetTimer = (fn: () => void, ms: number) => NodeJS.Timeout;

export interface ProfilerConfig {
  endpoint: string;
  headers: Record<string, string>;
  compression: 'gzip' | 'none';
  exportTimeoutMs: number;
  profileIntervalMs: number;
  version: string;
}

export interface Profiler {
  stop(): void;
}

/** The default profile source: V8's sampling profiler, through the inspector protocol. */
export const takeV8Profile: TakeProfile = async (durationMs) => {
  const { Session } = await import('node:inspector');
  const session = new Session();
  session.connect();
  const post = (method: string): Promise<unknown> =>
    new Promise((resolve, reject) => {
      // The callback signature is (err, params); typing it loosely keeps this readable.
      (session as unknown as { post(m: string, cb: (e: Error | null, r?: unknown) => void): void }).post(method, (e, r) =>
        e ? reject(e) : resolve(r),
      );
    });
  try {
    await post('Profiler.enable');
    await post('Profiler.start');
    await new Promise((r) => setTimeout(r, durationMs).unref());
    const res = (await post('Profiler.stop')) as { profile?: V8Profile };
    return res.profile ?? null;
  } finally {
    session.disconnect();
  }
};

export const defaultSend: Send = async (url, headers, body) => {
  try {
    const res = await fetch(url, { method: 'POST', headers, body });
    return res.status;
  } catch {
    return -1;
  }
};

/**
 * Starts the profiling loop. Failures are logged and never thrown: a profile that could not be taken or
 * sent must not take the application down with it.
 */
export function startProfiler(
  cfg: ProfilerConfig,
  resource: KeyValue[],
  log: Diag,
  deps: { take?: TakeProfile; send?: Send; setTimer?: SetTimer } = {},
): Profiler {
  const take = deps.take ?? takeV8Profile;
  const send = deps.send ?? defaultSend;
  const setTimer = deps.setTimer ?? setTimeout;
  const url = `${cfg.endpoint.replace(/\/+$/, '')}/v1/profiles`;
  let stopped = false;
  let timer: NodeJS.Timeout | undefined;

  const cycle = async (): Promise<void> => {
    if (stopped) return;
    try {
      const profile = await take(cfg.profileIntervalMs);
      if (stopped) return;
      const data = profile ? fromV8Profile(profile, resource, { name: PROFILE_SCOPE, version: cfg.version }, Date.now()) : null;
      // An idle process has nothing to report. The ingest accepts a profile-less body and produces
      // nothing, so sending one would cost both sides and say nothing.
      if (!data) {
        schedule();
        return;
      }
      let body = Buffer.from(JSON.stringify(data), 'utf8');
      const headers: Record<string, string> = { ...cfg.headers, 'content-type': 'application/json' };
      if (cfg.compression === 'gzip') {
        body = gzipSync(body);
        headers['content-encoding'] = 'gzip';
      }
      const status = await send(url, headers, body);
      if (status < 200 || status >= 300) log.warn('profile export failed', { status, url });
    } catch (err) {
      log.warn('profile export failed', { error: err instanceof Error ? err.message : String(err) });
    }
    schedule();
  };

  const schedule = (): void => {
    if (stopped) return;
    // unref: profiling must never be the reason a process stays alive.
    timer = setTimer(() => void cycle(), 0);
    timer.unref();
  };

  void cycle();

  return {
    stop(): void {
      stopped = true;
      if (timer) clearTimeout(timer);
      timer = undefined;
    },
  };
}
