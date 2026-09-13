// Pure helpers for the fleet screen (policy validation, versions, rollout progress).

export const WEEKDAYS = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"] as const;
export type Weekday = (typeof WEEKDAYS)[number];

export type WavesError = "empty" | "invalid" | "order" | "last";

/** Parses "10, 50, 100" and applies the server's rules (strictly increasing 1..100, last = 100, ≤ 20). */
export function parseWaves(text: string): { waves: number[]; error: WavesError | null } {
  const parts = text.split(/[\s,;→>]+/).filter(Boolean);
  if (parts.length === 0) return { waves: [], error: "empty" };
  const waves = parts.map((p) => (/^\d+$/.test(p) ? Number(p) : Number.NaN));
  if (waves.length > 20 || waves.some((w) => !Number.isInteger(w) || w < 1 || w > 100)) return { waves, error: "invalid" };
  for (let i = 1; i < waves.length; i++) {
    if (waves[i]! <= waves[i - 1]!) return { waves, error: "order" };
  }
  if (waves[waves.length - 1] !== 100) return { waves, error: "last" };
  return { waves, error: null };
}

export function formatWaves(waves: readonly number[]): string {
  return waves.join(", ");
}

const CLOCK = /^([01]\d|2[0-3]):[0-5]\d$/;

/** "HH:MM"; "24:00" only for window ends. */
export function validClock(value: string, allow24 = false): boolean {
  return CLOCK.test(value) || (allow24 && value === "24:00");
}

export type WindowError = "clock" | "equal";

export function windowError(w: { start: string; end: string }): WindowError | null {
  if (!validClock(w.start) || !validClock(w.end, true)) return "clock";
  if (w.start === w.end) return "equal";
  return null;
}

/** A SemVer version (build metadata ignored) or null. */
export function parseVersion(v: string): { nums: [number, number, number]; pre: string[] } | null {
  const m = /^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/.exec(v.trim());
  if (!m) return null;
  return { nums: [Number(m[1]), Number(m[2]), Number(m[3])], pre: m[4] ? m[4].split(".") : [] };
}

export function isVersion(v: string): boolean {
  return parseVersion(v) !== null;
}

/** SemVer precedence; unparsable versions sort before valid ones. */
export function compareVersions(a: string, b: string): number {
  const x = parseVersion(a);
  const y = parseVersion(b);
  if (!x || !y) return x ? 1 : y ? -1 : a.localeCompare(b);
  for (let i = 0; i < 3; i++) {
    if (x.nums[i] !== y.nums[i]) return x.nums[i]! < y.nums[i]! ? -1 : 1;
  }
  if (x.pre.length === 0 || y.pre.length === 0) return x.pre.length === y.pre.length ? 0 : x.pre.length === 0 ? 1 : -1;
  for (let i = 0; i < Math.min(x.pre.length, y.pre.length); i++) {
    const p = x.pre[i]!;
    const q = y.pre[i]!;
    if (p === q) continue;
    const pn = /^\d+$/.test(p);
    const qn = /^\d+$/.test(q);
    if (pn && qn) return Number(p) < Number(q) ? -1 : 1;
    if (pn !== qn) return pn ? -1 : 1;
    return p < q ? -1 : 1;
  }
  return x.pre.length === y.pre.length ? 0 : x.pre.length < y.pre.length ? -1 : 1;
}

export interface RolloutCounters {
  pending: number;
  attempted: number;
  succeeded: number;
  failed: number;
  rolled_back: number;
}

/**
 * Shares of the rollout's hosts for the progress bar. Hosts in progress are both attempted and
 * pending, so the total is finished (succeeded, failed, rolled back) plus pending.
 */
export function rolloutShares(c: RolloutCounters): { total: number; done: number; succeeded: number; failed: number; pending: number } {
  const failed = c.failed + c.rolled_back;
  const total = c.succeeded + failed + c.pending;
  if (total === 0) return { total: 0, done: 0, succeeded: 0, failed: 0, pending: 0 };
  const pct = (n: number) => Math.round((n / total) * 1000) / 10;
  return { total, done: c.succeeded + failed, succeeded: pct(c.succeeded), failed: pct(failed), pending: pct(c.pending) };
}

export function isOpenRollout(state: string | null | undefined): boolean {
  return state === "active" || state === "paused" || state === "halted";
}

export type BadgeTone = "success" | "warning" | "destructive" | "muted" | "secondary" | "outline";

const STATUS_TONE: Record<string, BadgeTone> = {
  offer: "secondary",
  up_to_date: "success",
  already_failed: "destructive",
  incompatible: "destructive",
  no_artifact: "destructive",
  rollout_halted: "destructive",
  invalid_version: "warning",
  not_capable: "warning",
  hold: "warning",
  rollout_paused: "warning",
  target_unavailable: "warning",
};

/** Badge tone for a host's update decision. */
export function statusTone(status: string): BadgeTone {
  return STATUS_TONE[status] ?? "muted";
}

const STATE_TONE: Record<string, BadgeTone> = {
  succeeded: "success",
  failed: "destructive",
  rolled_back: "destructive",
  idle: "muted",
};

/** Badge tone for an agent-reported update state (in-progress states are "secondary"). */
export function updateStateTone(state: string): BadgeTone {
  return STATE_TONE[state] ?? "secondary";
}

const ROLLOUT_TONE: Record<string, BadgeTone> = {
  active: "secondary",
  paused: "warning",
  halted: "destructive",
  completed: "success",
  superseded: "muted",
};

export function rolloutTone(state: string): BadgeTone {
  return ROLLOUT_TONE[state] ?? "muted";
}

/** Versions an admin may roll back to: running versions below the rollout target, newest first. */
export function rollbackCandidates(running: readonly string[], from: string | null | undefined): string[] {
  const seen = new Set<string>();
  return running
    .filter((v) => isVersion(v) && (!from || compareVersions(v, from) < 0))
    .filter((v) => (seen.has(v) ? false : (seen.add(v), true)))
    .sort((a, b) => compareVersions(b, a));
}
