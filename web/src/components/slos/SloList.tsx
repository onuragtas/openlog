// SLO list: every objective of the organization with its current error budget (docs/contracts/slo.md §1).
// Router-free: the page passes the navigation callbacks.
import { useQuery } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { SloListItem } from "@/api/slos";
import { slosQuery } from "@/api/slos";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { budgetBadgeVariant, budgetLevel, formatBurnRate, formatObjective, formatRatio, sloTarget } from "@/lib/slo";
import { BudgetBar } from "./BudgetBar";

export interface SloListProps {
  onOpen: (slo: SloListItem) => void;
  onNew?: () => void;
  canWrite?: boolean;
}

export function SloList({ onOpen, onNew, canWrite = false }: SloListProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const q = useQuery(slosQuery());

  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const slos = q.data.slos;

  return (
    <div className="flex flex-col gap-3">
      {canWrite && onNew && (
        <div className="flex justify-end">
          <Button type="button" onClick={onNew}>
            <Plus className="size-4" aria-hidden="true" />
            {t("slo.new")}
          </Button>
        </div>
      )}
      <div className="rounded-xl border bg-card">
        {slos.length === 0 ? (
          <EmptyState>
            <p>{t("slo.empty")}</p>
            <p className="mt-1 text-xs">{t("slo.emptyHint")}</p>
          </EmptyState>
        ) : (
          <Table data-testid="slo-list">
            <TableHeader>
              <TableRow>
                <TableHead>{t("slo.columns.name")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("slo.columns.service")}</TableHead>
                <TableHead className="text-right">{t("slo.columns.objective")}</TableHead>
                <TableHead className="text-right">{t("slo.columns.sli")}</TableHead>
                <TableHead>{t("slo.columns.budget")}</TableHead>
                <TableHead className="hidden lg:table-cell text-right">{t("slo.columns.burnRate")}</TableHead>
                <TableHead>{t("slo.columns.status")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {slos.map((s) => {
                const budget = s.status?.budget ?? null;
                const level = budgetLevel(budget);
                return (
                  <TableRow key={s.id}>
                    <TableCell>
                      <button type="button" className="font-medium hover:underline" onClick={() => onOpen(s)}>
                        {s.name}
                      </button>
                      <div className="text-xs text-muted-foreground">
                        {t(`slo.sliTypes.${s.sli_type}`)}
                        {s.sli_type === "latency" && s.latency_threshold_ms ? ` · ${s.latency_threshold_ms} ms` : ""}
                        {` · ${t("slo.windowDays", { count: s.window_days })}`}
                      </div>
                    </TableCell>
                    <TableCell className="hidden md:table-cell">{sloTarget(s)}</TableCell>
                    <TableCell className="text-right font-mono tabular-nums">{formatObjective(s.objective, locale)}</TableCell>
                    <TableCell className="text-right font-mono tabular-nums">{formatRatio(budget?.sli, locale, 3)}</TableCell>
                    <TableCell>
                      <BudgetBar budget={budget} />
                    </TableCell>
                    <TableCell className="hidden lg:table-cell text-right font-mono tabular-nums">
                      {formatBurnRate(budget?.burn_rate, locale)}
                    </TableCell>
                    <TableCell>
                      <Badge variant={budgetBadgeVariant(level)}>{t(`slo.status.${level}`)}</Badge>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </div>
      {q.data.status_truncated && <p className="text-xs text-muted-foreground">{t("slo.statusTruncated")}</p>}
    </div>
  );
}
