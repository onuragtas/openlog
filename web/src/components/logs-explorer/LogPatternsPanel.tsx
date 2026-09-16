// "Patterns" view of the Logs Explorer (POST /api/v1/logs/patterns, D-128): the distinct messages behind the records
// that match the current conditions, most frequent first, each with a share-of-volume bar. Selecting one adds a
// pattern_id condition, which takes the reader back to the record list showing only that pattern.
import { useQuery } from "@tanstack/react-query";
import { Filter } from "lucide-react";
import { useTranslation } from "react-i18next";
import { logPatternsQuery, type ExplorerContext, type FilterState } from "@/api/explorer";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { severityBadgeVariant, valueShare } from "@/lib/logs-explorer";
import { severityLabel } from "@/lib/severity";
import type { RangeSpec } from "@/lib/time";

export interface LogPatternsPanelProps {
  range: RangeSpec;
  /** Request filter of the explorer (locked context conditions included). */
  filter: FilterState;
  context?: ExplorerContext;
  canFilter: boolean;
  onSelect: (patternId: string) => void;
}

export function LogPatternsPanel({ range, filter, context, canFilter, onSelect }: LogPatternsPanelProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const q = useQuery(logPatternsQuery({ range, filter, context }));
  const pct = new Intl.NumberFormat(locale, { maximumFractionDigits: 1 });

  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (q.data.patterns.length === 0) {
    return (
      <EmptyState>
        <p>{t("logsExplorer.patterns.empty")}</p>
      </EmptyState>
    );
  }

  return (
    <div className="flex min-w-0 flex-col gap-2 p-3" data-testid="log-patterns">
      <p className="text-xs text-muted-foreground">
        {t("logsExplorer.patterns.summary", { patterns: q.data.patterns.length, value: q.data.total.toLocaleString(locale) })}
      </p>
      <ol className="flex min-w-0 flex-col" aria-label={t("logsExplorer.patterns.listLabel")}>
        {q.data.patterns.map((p) => {
          const share = valueShare(p.count, q.data.total);
          const severity = severityLabel(null, p.max_severity_number);
          return (
            <li key={p.pattern_id} className="flex min-w-0 flex-col gap-1 border-b py-2 last:border-0" data-testid="log-pattern">
              <div className="flex min-w-0 items-start gap-2">
                {severity && <Badge variant={severityBadgeVariant(p.max_severity_number)}>{severity}</Badge>}
                <span className="min-w-0 flex-1 font-mono text-xs break-all" title={p.template}>
                  {p.template}
                </span>
                <span className="shrink-0 text-[11px] text-muted-foreground tabular-nums">
                  {t("logsExplorer.patterns.count", { value: p.count.toLocaleString(locale) })} · {t("logsExplorer.patterns.share", { value: pct.format(share) })}
                </span>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="size-7 shrink-0 pointer-coarse:size-9"
                  disabled={!canFilter}
                  aria-label={t("logsExplorer.patterns.filter", { template: p.template })}
                  onClick={() => onSelect(p.pattern_id)}
                >
                  <Filter aria-hidden="true" />
                </Button>
              </div>
              <div className="h-1 w-full overflow-hidden rounded-full bg-muted" aria-hidden="true">
                <div className="h-full rounded-full bg-primary/70" style={{ width: `${share}%` }} />
              </div>
              <span className="truncate font-mono text-[11px] text-muted-foreground" title={p.sample.body}>
                {p.sample.body}
              </span>
              {p.services.length > 0 && (
                <span className="truncate text-[11px] text-muted-foreground">
                  {t("logsExplorer.patterns.services")}: {p.services.join(", ")}
                </span>
              )}
            </li>
          );
        })}
      </ol>
      {q.data.unclassified > 0 && <p className="text-[11px] text-muted-foreground">{t("logsExplorer.patterns.unclassified", { value: q.data.unclassified.toLocaleString(locale) })}</p>}
      {q.data.rollup && <p className="text-[11px] text-muted-foreground">{t("logsExplorer.patterns.rollup")}</p>}
      {q.data.truncated && <p className="text-[11px] text-muted-foreground">{t("logsExplorer.patterns.truncated", { value: q.data.patterns.length })}</p>}
    </div>
  );
}
