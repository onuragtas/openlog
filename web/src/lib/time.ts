// Time range handling. The URL carries either a relative `range` (15m, 1h,
// 6h, 24h, 7d, …) or absolute `from`/`to` (unix ms or RFC3339). Absolute wins
// when both from and to are valid.

export const PRESET_RANGES = ["15m", "1h", "6h", "24h", "7d"] as const;
export const DEFAULT_RANGE = "1h";

export interface RangeSpec {
  range?: string;
  from?: string;
  to?: string;
}

export interface ResolvedRange {
  from: number;
  to: number;
}

const UNIT_MS: Record<string, number> = {
  s: 1000,
  m: 60_000,
  h: 3_600_000,
  d: 86_400_000,
  w: 604_800_000,
};

/** Parses a relative duration like "15m", "1h", "7d" to milliseconds, or null. */
export function parseDuration(v: string | undefined | null): number | null {
  if (!v) return null;
  const m = /^(\d+)([smhdw])$/.exec(v.trim());
  if (!m) return null;
  const n = Number(m[1]);
  if (!Number.isFinite(n) || n <= 0) return null;
  return n * UNIT_MS[m[2]!]!;
}

/** Parses a time param: unix milliseconds or RFC3339. */
export function parseTimeParam(v: string | number | undefined | null): number | null {
  if (v === undefined || v === null || v === "") return null;
  if (typeof v === "number") return Number.isFinite(v) ? v : null;
  const s = v.trim();
  if (/^\d+$/.test(s)) return Number(s);
  if (!/^\d{4}-\d{2}-\d{2}T/.test(s)) return null;
  const t = Date.parse(s);
  return Number.isNaN(t) ? null : t;
}

/** Resolves a range spec against `now`; invalid input falls back to the default range. */
export function resolveRange(spec: RangeSpec, now: number): ResolvedRange {
  const from = parseTimeParam(spec.from);
  const to = parseTimeParam(spec.to);
  if (from !== null && to !== null && from < to) return { from, to };
  const d = parseDuration(spec.range) ?? parseDuration(DEFAULT_RANGE)!;
  return { from: now - d, to: now };
}

/** Normalizes untrusted URL search values into a RangeSpec (for route validateSearch). */
export function validateRangeSearch(search: Record<string, unknown>): RangeSpec {
  const str = (v: unknown) => (typeof v === "string" ? v : typeof v === "number" ? String(v) : undefined);
  const from = str(search.from);
  const to = str(search.to);
  const range = str(search.range);
  const out: RangeSpec = {};
  const f = parseTimeParam(from);
  const t = parseTimeParam(to);
  if (f !== null && t !== null && f < t) {
    out.from = from;
    out.to = to;
  } else if (range && parseDuration(range) !== null) {
    out.range = range;
  }
  return out;
}

export function isCustomRange(spec: RangeSpec): boolean {
  return spec.from !== undefined && spec.to !== undefined;
}

/** Value for <input type="datetime-local"> in local time. */
export function toDateTimeLocal(ms: number): string {
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export function fromDateTimeLocal(v: string): number | null {
  if (!v) return null;
  const t = new Date(v).getTime();
  return Number.isNaN(t) ? null : t;
}

/**
 * Parses an RFC3339 timestamp with up to nanosecond precision into epoch
 * nanoseconds (BigInt, exact). Returns null when invalid.
 */
export function parseTimestampNs(s: string): bigint | null {
  const m = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/.exec(s);
  if (!m) return null;
  const secMs = Date.parse(`${m[1]}${m[3]}`);
  if (Number.isNaN(secMs)) return null;
  const frac = BigInt((m[2] ?? "").padEnd(9, "0") || "0");
  return BigInt(secMs) * 1_000_000n + frac;
}
