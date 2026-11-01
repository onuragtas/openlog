// SLO list: every objective of the organization with its current error budget (docs/contracts/slo.md §1).
// Router-free: the page passes the navigation callbacks.
import { useQuery } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { SloListItem } from "@/api/slos";
import { slosQuery } from "@/api/slos";
import { ListPager } from "@/components/ListPager";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { SortableHead, Table, TableBody, TableCell, TableHeader, TableRow } from "@/components/ui/table";
import { useLocalList } from "@/lib/list";
import { budgetBadgeVariant, budgetLevel, formatBurnRate, formatObjective, formatRatio, sloTarget } from "@/lib/slo";
import { BudgetBar } from "./BudgetBar";

export interface SloListProps {
  onOpen: (slo: SloListItem) => void;
  onNew?: () => void;
  canWrite?: boolean;
}

const SLO_COLUMNS = {
  name: (s: SloListItem) => s.name,
  service: (s: SloListItem) => sloTarget(s),
  objective: (s: SloListItem) => s.objective,
  sli: (s: SloListItem) => s.status?.budget?.sli,
  budget: (s: SloListItem) => s.status?.budget?.remaining_ratio,
  burn_rate: (s: SloListItem) => s.status?.budget?.burn_rate,
  // Sorted by what is left of the budget, which is what the status is a summary of.
  status: (s: SloListItem) => s.status?.budget?.remaining_ratio,
};

export function SloList({ onOpen, onNew, canWrite = false }: SloListProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const q = useQuery(slosQuery());
  const list = useLocalList(q.data?.slos, SLO_COLUMNS, "name", "asc", 50, "SloList");

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
          <>
            <Table data-testid="slo-list">
              <TableHeader>
                <TableRow>
                  <SortableHead column="name" defaultDir="asc" {...list.head}>{t("slo.columns.name")}</SortableHead>
                  <SortableHead column="service" defaultDir="asc" className="hidden md:table-cell" {...list.head}>{t("slo.columns.service")}</SortableHead>
                  <SortableHead column="objective" className="text-right" {...list.head}>{t("slo.columns.objective")}</SortableHead>
                  <SortableHead column="sli" defaultDir="asc" className="text-right" {...list.head}>{t("slo.columns.sli")}</SortableHead>
                  <SortableHead column="budget" defaultDir="asc" {...list.head}>{t("slo.columns.budget")}</SortableHead>
                  <SortableHead column="burn_rate" className="hidden lg:table-cell text-right" {...list.head}>{t("slo.columns.burnRate")}</SortableHead>
                  <SortableHead column="status" defaultDir="asc" {...list.head}>{t("slo.columns.status")}</SortableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.visible.map((s) => {
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
            <ListPager list={list} count={list.visible.length} hasMore={list.hasMore} />
          </>
        )}
      </div>
      {q.data.status_truncated && <p className="text-xs text-muted-foreground">{t("slo.statusTruncated")}</p>}
    </div>
  );
}
