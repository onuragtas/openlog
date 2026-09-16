// Error budget visualisations: a bar for the remaining share and the burndown of a window (docs/contracts/slo.md
// §2). SVG with CSS-variable colors (theme-aware); every graphic has an accessible name.
import { useTranslation } from "react-i18next";
import type { SloBudget, SloPoint } from "@/api/slos";
import { budgetLevel, burndownGeometry, formatRatio } from "@/lib/slo";
import { cn } from "@/lib/utils";

const FILL: Record<string, string> = {
  healthy: "bg-success",
  warning: "bg-warning",
  exhausted: "bg-destructive",
  none: "bg-muted-foreground/40",
};

/** Horizontal bar of the remaining error budget (empty bar = exhausted or missed objective). */
export function BudgetBar({ budget, className }: { budget: SloBudget | null | undefined; className?: string }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const level = budgetLevel(budget);
  const remaining = budget?.remaining_ratio ?? null;
  const width = remaining === null ? 0 : Math.min(100, Math.max(0, remaining * 100));
  const label = remaining === null ? t("slo.status.noData") : t("slo.remainingOf", { value: formatRatio(remaining, locale) });
  return (
    <div className={cn("flex min-w-24 items-center gap-2", className)} data-testid="budget-bar">
      <span className="h-2 flex-1 overflow-hidden rounded-full bg-muted" role="img" aria-label={label}>
        <span className={cn("block h-full rounded-full", FILL[level])} style={{ width: `${width}%` }} />
      </span>
      <span className="font-mono text-xs tabular-nums">{remaining === null ? "–" : formatRatio(remaining, locale)}</span>
    </div>
  );
}

/** Burndown of the window's budget: the share still left after each bucket, with the exhausted line at 0. */
export function BurndownChart({ points, height = 140 }: { points: SloPoint[]; height?: number }) {
  const { t } = useTranslation();
  const width = 600;
  const geo = burndownGeometry(points, width, height);
  if (geo.count === 0) {
    return <p className="py-6 text-center text-sm text-muted-foreground">{t("slo.detail.seriesEmpty")}</p>;
  }
  return (
    <figure className="flex flex-col gap-1" data-testid="burndown">
      <figcaption className="sr-only">{t("slo.detail.burndownTitle")}</figcaption>
      <svg
        role="img"
        aria-label={t("slo.detail.burndownTitle")}
        viewBox={`0 0 ${width} ${height}`}
        preserveAspectRatio="none"
        className="h-[140px] w-full"
      >
        <line x1={0} x2={width} y1={geo.zeroY} y2={geo.zeroY} className="stroke-destructive/60" strokeDasharray="4 4" strokeWidth={1} />
        <polyline points={geo.line} className="fill-none stroke-primary" strokeWidth={2} strokeLinejoin="round" vectorEffect="non-scaling-stroke" />
      </svg>
      <div className="flex justify-between text-[11px] text-muted-foreground" aria-hidden="true">
        <span>{t("slo.detail.budgetFull")}</span>
        <span>{t("slo.detail.budgetEmpty")}</span>
      </div>
    </figure>
  );
}
