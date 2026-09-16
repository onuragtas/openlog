// Pure helpers for the SLO screens (docs/contracts/slo.md): budget levels, formatting and the burndown
// path. Rendering-independent and unit-tested through the component tests.
import type { Slo, SloBudget, SloInput, SloPoint } from "@/api/slos";

export type BudgetLevel = "healthy" | "warning" | "exhausted" | "none";

/**
 * Traffic light of an error budget: more than a quarter left is healthy, anything above zero is a warning,
 * an exhausted or negative budget (the objective is missed) is red. Without requests there is no level.
 */
export function budgetLevel(budget: SloBudget | null | undefined): BudgetLevel {
  const remaining = budget?.remaining_ratio;
  if (remaining === null || remaining === undefined || !Number.isFinite(remaining)) return "none";
  if (remaining > 0.25) return "healthy";
  if (remaining > 0) return "warning";
  return "exhausted";
}

export function budgetBadgeVariant(level: BudgetLevel): "success" | "warning" | "destructive" | "muted" {
  switch (level) {
    case "healthy":
      return "success";
    case "warning":
      return "warning";
    case "exhausted":
      return "destructive";
    default:
      return "muted";
  }
}

/** A 0..1 ratio as a percentage; budgets need more digits than a rounded percent (99.9 % ≠ 100 %). */
export function formatRatio(v: number | null | undefined, locale?: string, digits = 2): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return "–";
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: digits }).format(v * 100)}%`;
}

/** Burn rate as a multiple ("14.4×"); null without requests. */
export function formatBurnRate(v: number | null | undefined, locale?: string): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return "–";
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: v < 10 ? 2 : 1 }).format(v)}×`;
}

/** Weighted request counts are estimates of unsampled traffic, so they are rounded for display. */
export function formatRequests(v: number | null | undefined, locale?: string): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return "–";
  return new Intl.NumberFormat(locale, { maximumFractionDigits: 0 }).format(v);
}

/** Objective as it is written ("99.9%"), with up to three decimals. */
export function formatObjective(objective: number, locale?: string): string {
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 3 }).format(objective)}%`;
}

/** "checkout (prod)" / "checkout · shop (prod)" — namespace and environment only when the SLO fixes them. */
export function sloTarget(slo: Pick<Slo, "service_name" | "service_namespace" | "environment">): string {
  const parts = [slo.service_name];
  if (slo.service_namespace) parts.push(slo.service_namespace);
  const name = parts.join(" · ");
  return slo.environment ? `${name} (${slo.environment})` : name;
}

export interface BurndownGeometry {
  /** Polyline of the remaining budget share, clamped to [-0.25, 1] of the drawing area. */
  line: string;
  /** y of the zero line (budget exhausted) in the drawing area. */
  zeroY: number;
  /** Points that carry a value, for the accessible summary. */
  count: number;
}

/**
 * Builds the burndown polyline: x by time, y by remaining budget share (1 at the top, 0 at the zero line,
 * negative below it). Buckets without a value repeat the last known share, so the line has no holes.
 */
export function burndownGeometry(points: SloPoint[], width: number, height: number): BurndownGeometry {
  const min = -0.25;
  const max = 1;
  const zeroY = height - ((0 - min) / (max - min)) * height;
  const withValue = points.filter((p) => p.remaining_ratio !== null && p.remaining_ratio !== undefined);
  if (points.length === 0 || withValue.length === 0) return { line: "", zeroY, count: 0 };
  const first = points[0]!.t;
  const span = Math.max(1, points[points.length - 1]!.t - first);
  let last = 1;
  const coords = points.map((p) => {
    if (p.remaining_ratio !== null && p.remaining_ratio !== undefined) last = p.remaining_ratio;
    const clamped = Math.min(max, Math.max(min, last));
    const x = ((p.t - first) / span) * (width - 2) + 1;
    const y = height - ((clamped - min) / (max - min)) * height;
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  });
  return { line: coords.join(" "), zeroY, count: withValue.length };
}

/** Validation issues of the form; the keys are translated as slo.validation.<key>. */
export type SloValidationKey = "required" | "objective" | "latency";

export type SloFormErrors = Partial<Record<keyof SloInput, SloValidationKey>>;

/** Client-side checks mirroring internal/slo validation; the server remains authoritative. */
export function validateSloInput(input: SloInput): SloFormErrors {
  const e: SloFormErrors = {};
  if (!input.name.trim()) e.name = "required";
  if (!input.service_name.trim()) e.service_name = "required";
  if (!Number.isFinite(input.objective) || input.objective < 50 || input.objective >= 100) e.objective = "objective";
  if (input.sli_type === "latency") {
    const t = input.latency_threshold_ms;
    if (t === undefined || !Number.isInteger(t) || t < 1 || t > 600000) e.latency_threshold_ms = "latency";
  }
  return e;
}
