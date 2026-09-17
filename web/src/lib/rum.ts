// Presentation helpers for the browser (RUM) screens, docs/contracts/rum.md §2.1. The thresholds and the
// rating itself come from the server — they are constants of the Core Web Vitals programme, not settings —
// so nothing here recomputes them; this only decides how they are shown.
import type { RumVital, RumVitalName } from "@/api/rum";
import { formatValue } from "@/lib/format";

/** Display order: the three vitals the programme scores a page on first, then the two diagnostics. */
export const VITALS: readonly RumVitalName[] = ["lcp", "inp", "cls", "fcp", "ttfb"];

export type RumBadgeVariant = "success" | "warning" | "destructive" | "muted";

export function ratingVariant(rating: string): RumBadgeVariant {
  switch (rating) {
    case "good":
      return "success";
    case "needs_improvement":
      return "warning";
    case "poor":
      return "destructive";
    default:
      return "muted";
  }
}

type Rating = "good" | "needs_improvement" | "poor" | "unknown";

/**
 * i18n key of a rating, including the empty rating a vital without measurements carries. The server owns the
 * rating, so anything it sends that this build does not know reads as "no data" rather than as a missing key.
 */
export function ratingKey(rating: string): `rum.rating.${Rating}` {
  const known: Rating = rating === "good" || rating === "needs_improvement" || rating === "poor" ? rating : "unknown";
  return `rum.rating.${known}`;
}

/** CLS is a unitless score around 0.1; every other vital is a duration in milliseconds. */
export function formatVital(value: number | null | undefined, name: RumVitalName, locale?: string): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return "–";
  return name === "cls" ? value.toFixed(2) : formatValue(value, "ms", locale);
}

/** Milliseconds from a page or session row; "–" while there is nothing to average. */
export function formatMs(value: number | null | undefined, locale?: string): string {
  return value === null || value === undefined || !Number.isFinite(value) ? "–" : formatValue(value, "ms", locale);
}

/** 0..1 → a percentage with no decimals; the three bands are always shares of the same total. */
export const formatShare = (share: number) => `${Math.round(share * 100)}%`;

export function sortVitals(vitals: readonly RumVital[]): RumVital[] {
  return [...vitals].sort((a, b) => VITALS.indexOf(a.name) - VITALS.indexOf(b.name));
}

/** A vital card stays legible without data: the p75 is null and the rating is "". */
export const hasMeasurements = (v: RumVital) => v.count > 0;

/** Milliseconds of an API timestamp, or null when the field is empty (never reported, still open). */
export function apiTimeMs(value: string | null | undefined): number | null {
  if (!value) return null;
  const ms = new Date(value).getTime();
  return Number.isFinite(ms) ? ms : null;
}

/** "Chrome 131" / "Chrome" / "–": the version is not always reported. */
export function browserLabel(name: string, version: string): string {
  if (name === "") return "–";
  return version === "" ? name : `${name} ${version}`;
}
