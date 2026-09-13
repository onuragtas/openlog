export type UnitKind = "percent" | "bytes" | "bytesPerSec" | "number";

const BYTE_UNITS = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];

export function formatBytes(v: number, digits = 1): string {
  if (!Number.isFinite(v)) return "–";
  const sign = v < 0 ? "-" : "";
  let n = Math.abs(v);
  let i = 0;
  while (n >= 1024 && i < BYTE_UNITS.length - 1) {
    n /= 1024;
    i++;
  }
  return `${sign}${n.toFixed(i === 0 ? 0 : digits)} ${BYTE_UNITS[i]}`;
}

export function formatNumber(v: number, locale?: string): string {
  if (!Number.isFinite(v)) return "–";
  return new Intl.NumberFormat(locale, { maximumFractionDigits: Math.abs(v) < 10 ? 2 : 1 }).format(v);
}

export function formatValue(v: number | null | undefined, kind: UnitKind, locale?: string): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return "–";
  switch (kind) {
    case "percent":
      return `${formatNumber(v * 100, locale)}%`;
    case "bytes":
      return formatBytes(v);
    case "bytesPerSec":
      return `${formatBytes(v)}/s`;
    default:
      return formatNumber(v, locale);
  }
}

const REL_STEPS: [Intl.RelativeTimeFormatUnit, number][] = [
  ["year", 31_536_000],
  ["month", 2_592_000],
  ["day", 86_400],
  ["hour", 3_600],
  ["minute", 60],
  ["second", 1],
];

export function formatRelative(ms: number, now: number, locale: string): string {
  const diff = Math.round((ms - now) / 1000);
  const abs = Math.abs(diff);
  const rtf = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
  for (const [unit, secs] of REL_STEPS) {
    if (abs >= secs || unit === "second") {
      return rtf.format(Math.round(diff / secs), unit);
    }
  }
  return rtf.format(0, "second");
}

export function formatDateTime(ms: number, locale: string, withMs = false): string {
  return new Intl.DateTimeFormat(locale, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    ...(withMs ? { fractionalSecondDigits: 3 } : {}),
    hour12: false,
  }).format(new Date(ms));
}

/** Formats a nanosecond duration as µs/ms/s. */
export function formatDurationNs(ns: number): string {
  if (ns < 1_000) return `${ns} ns`;
  if (ns < 1_000_000) return `${(ns / 1_000).toFixed(ns < 10_000 ? 2 : 0)} µs`;
  if (ns < 1_000_000_000) return `${(ns / 1_000_000).toFixed(ns < 10_000_000 ? 2 : 1)} ms`;
  return `${(ns / 1_000_000_000).toFixed(2)} s`;
}
