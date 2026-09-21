// The compact usage figures of the hosts list (docs/contracts/api.md "Hosts"): a bar and a number for CPU,
// memory and the fullest filesystem, so a list of machines answers "which one is in trouble" at a glance.
//
// The colour is the only judgement made here, and it is deliberately late: a machine at 70 % is working,
// not failing. Amber at 80 %, red at 90 % — the thresholds people already reason with.
import { useTranslation } from "react-i18next";
import type { HostUsage } from "@/api/types";
import { cn } from "@/lib/utils";

export type UsageLevel = "ok" | "warning" | "critical" | "none";

export function usageLevel(share: number | null | undefined): UsageLevel {
  if (share === null || share === undefined || !Number.isFinite(share)) return "none";
  if (share >= 0.9) return "critical";
  if (share >= 0.8) return "warning";
  return "ok";
}

const FILL: Record<UsageLevel, string> = {
  ok: "bg-success",
  warning: "bg-warning",
  critical: "bg-destructive",
  none: "bg-muted-foreground/30",
};

/** A share as a whole percentage ("71%"); "–" when the host reported none. */
export function formatShare(share: number | null | undefined, locale?: string): string {
  if (share === null || share === undefined || !Number.isFinite(share)) return "–";
  return new Intl.NumberFormat(locale, { style: "percent", maximumFractionDigits: 0 }).format(share);
}

/** The load average, which is a number rather than a share ("2.4"). */
export function formatLoad(load: number | null | undefined, locale?: string): string {
  if (load === null || load === undefined || !Number.isFinite(load)) return "–";
  return new Intl.NumberFormat(locale, { maximumFractionDigits: 2 }).format(load);
}

/** One labelled bar: the share as a track and the same number in text, because a bar alone cannot be read. */
export function UsageBar({ label, share, className }: { label: string; share: number | null | undefined; className?: string }) {
  const { i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const level = usageLevel(share);
  const width = level === "none" ? 0 : Math.min(100, Math.max(0, (share ?? 0) * 100));
  const text = formatShare(share, locale);
  return (
    <div className={cn("flex min-w-20 items-center gap-1.5", className)} data-testid="usage-bar">
      <span className="h-1.5 flex-1 overflow-hidden rounded-full bg-muted" role="img" aria-label={`${label}: ${text}`}>
        <span className={cn("block h-full rounded-full", FILL[level])} style={{ width: `${width}%` }} />
      </span>
      <span className="w-9 shrink-0 text-right font-mono text-xs tabular-nums">{text}</span>
    </div>
  );
}

/**
 * The three bars of one host, for a narrow layout (the stacked table row on a phone) or a cell each on a
 * wide one. A host that reported nothing in the window shows why rather than three empty bars.
 */
export function HostUsageCells({ usage }: { usage: HostUsage | null | undefined }) {
  const { t } = useTranslation();
  if (!usage || (usage.cpu === null && usage.memory === null && usage.disk === null)) {
    return <span className="text-xs text-muted-foreground">{t("hosts.usage.none")}</span>;
  }
  return (
    <div className="flex flex-col gap-1">
      <UsageBar label={t("hosts.columns.cpu")} share={usage.cpu} />
      <UsageBar label={t("hosts.columns.memory")} share={usage.memory} />
      <UsageBar label={t("hosts.columns.disk")} share={usage.disk} />
    </div>
  );
}
