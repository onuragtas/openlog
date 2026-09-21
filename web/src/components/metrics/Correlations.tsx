// "What else changed" — the series that behaved differently in a window than they did before it
// (docs/contracts/api.md "Metric correlation", D-146).
//
// Both means are printed next to the score on purpose: the number that ranks a row is derived from the two
// numbers beside it, so the ranking can be checked rather than trusted.
import { useQuery } from "@tanstack/react-query";
import { ArrowDown, ArrowUp } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { MetricCorrelation } from "@/api/correlate";
import { correlateQuery } from "@/api/correlate";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatNumber } from "@/lib/format";

export interface CorrelationsProps {
  /** The window to explain, usually an incident's. */
  from: string;
  to: string;
  hostId?: string;
  limit?: number;
}

/** A series' attributes as one line ("device=nvme0n1 · direction=read"). */
export function seriesLabel(c: Pick<MetricCorrelation, "attributes">): string {
  return Object.entries(c.attributes)
    .filter(([k]) => !k.startsWith("openlog."))
    .map(([k, v]) => `${k}=${v}`)
    .join(" · ");
}

/** The relative change as a percentage; "–" when the baseline was 0 and a ratio has no meaning. */
export function formatChange(ratio: number | null | undefined, locale?: string): string {
  if (ratio === null || ratio === undefined || !Number.isFinite(ratio)) return "–";
  return new Intl.NumberFormat(locale, {
    style: "percent",
    maximumFractionDigits: 0,
    signDisplay: "exceptZero",
  }).format(ratio);
}

/** How loud a finding is; the thresholds are standard deviations of the series' own baseline. */
export function scoreVariant(score: number): "destructive" | "warning" | "muted" {
  if (score >= 10) return "destructive";
  if (score >= 4) return "warning";
  return "muted";
}

export function Correlations({ from, to, hostId, limit }: CorrelationsProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage;
  const q = useQuery(correlateQuery({ from, to, hostId, limit }));

  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const { correlations, series_compared: compared } = q.data;

  if (correlations.length === 0) {
    return (
      <p className="py-4 text-sm text-muted-foreground">
        {compared === 0 ? t("correlations.noData") : t("correlations.empty", { series: formatNumber(compared, locale) })}
      </p>
    );
  }

  return (
    <div className="flex flex-col gap-2">
      <p className="text-xs text-muted-foreground">{t("correlations.compared", { series: formatNumber(compared, locale) })}</p>
      <Table mobile="stack" data-testid="correlations">
        <TableHeader>
          <TableRow>
            <TableHead>{t("correlations.columns.metric")}</TableHead>
            <TableHead className="text-right">{t("correlations.columns.baseline")}</TableHead>
            <TableHead className="text-right">{t("correlations.columns.window")}</TableHead>
            <TableHead className="text-right">{t("correlations.columns.change")}</TableHead>
            <TableHead className="text-right">{t("correlations.columns.score")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {correlations.map((c) => (
            <TableRow key={`${c.metric_name}/${c.series_id}`} data-testid="correlation-row">
              <TableCell className="max-w-96">
                <div className="flex items-center gap-1.5">
                  {c.direction === "up" ? (
                    <ArrowUp className="size-3.5 shrink-0 text-destructive-text" aria-hidden="true" />
                  ) : (
                    <ArrowDown className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
                  )}
                  <span className="truncate font-mono text-xs font-medium">{c.metric_name}</span>
                  <span className="sr-only">{t(`correlations.direction.${c.direction}`)}</span>
                </div>
                {seriesLabel(c) !== "" && <div className="truncate text-xs text-muted-foreground">{seriesLabel(c)}</div>}
              </TableCell>
              <TableCell label={t("correlations.columns.baseline")} className="text-right font-mono tabular-nums">
                {formatNumber(c.baseline_mean, locale)}
              </TableCell>
              <TableCell label={t("correlations.columns.window")} className="text-right font-mono tabular-nums">
                {formatNumber(c.window_mean, locale)}
              </TableCell>
              <TableCell label={t("correlations.columns.change")} className="text-right font-mono tabular-nums">
                {formatChange(c.change_ratio, locale)}
              </TableCell>
              <TableCell label={t("correlations.columns.score")} className="text-right">
                <Badge variant={scoreVariant(c.score)}>{c.score.toFixed(1)}</Badge>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
