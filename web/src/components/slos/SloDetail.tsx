// SLO detail: status of the rolling window, error budget burndown and the burn-rate windows the slo_burn
// rule watches (docs/contracts/slo.md §2, §3). Router-free: the page passes the callbacks.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { deleteSlo, sloResultsQuery, type SloBurnWindowResult } from "@/api/slos";
import { FormError } from "@/components/settings/common";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { budgetBadgeVariant, budgetLevel, formatBurnRate, formatObjective, formatRatio, formatRequests, sloTarget } from "@/lib/slo";
import { BudgetBar, BurndownChart } from "./BudgetBar";
import { SloForm } from "./SloForm";

export interface SloDetailProps {
  id: string;
  canWrite?: boolean;
  /** Opens the APM service page of the SLO's target. */
  onOpenService?: (service: { service_name: string; service_namespace: string | null; environment: string | null }) => void;
  onDeleted?: () => void;
}

export function SloDetail({ id, canWrite = false, onOpenService, onDeleted }: SloDetailProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const q = useQuery(sloResultsQuery(id));
  // The defaults have translated names; a rule may configure its own, which are shown as they were named.
  const burnName = (name: string) => (name === "fast" ? t("slo.burn.names.fast") : name === "slow" ? t("slo.burn.names.slow") : name);

  const remove = useMutation({
    mutationFn: () => deleteSlo(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["slos"] });
      onDeleted?.();
    },
  });

  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const { slo, status, burn, series } = q.data;
  const budget = status.budget;
  const level = budgetLevel(budget);

  if (editing) {
    return <SloForm slo={slo} onSaved={() => setEditing(false)} onCancel={() => setEditing(false)} />;
  }

  const tiles: [string, string][] = [
    [t("slo.detail.sli"), formatRatio(budget.sli, locale, 3)],
    [t("slo.detail.objective"), formatObjective(slo.objective, locale)],
    [t("slo.detail.remaining"), formatRatio(budget.remaining_ratio, locale)],
    [t("slo.detail.burnRate"), formatBurnRate(budget.burn_rate, locale)],
    [t("slo.detail.requests"), formatRequests(budget.requests, locale)],
    [t("slo.detail.bad"), formatRequests(budget.bad, locale)],
  ];

  return (
    <div className="flex flex-col gap-4" data-testid="slo-detail">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-lg font-semibold">{slo.name}</h2>
        <Badge variant={budgetBadgeVariant(level)}>{t(`slo.status.${level}`)}</Badge>
        <Badge variant="muted">{t("slo.windowDays", { count: slo.window_days })}</Badge>
        <button type="button" className="text-sm text-primary hover:underline" onClick={() => onOpenService?.(slo)}>
          {sloTarget(slo)}
        </button>
        <span className="text-xs text-muted-foreground">
          {t(`slo.sliTypes.${slo.sli_type}`)}
          {slo.sli_type === "latency" && slo.latency_threshold_ms ? ` · ${slo.latency_threshold_ms} ms` : ""}
        </span>
        {canWrite && (
          <div className="ml-auto flex gap-2">
            <Button type="button" variant="outline" size="sm" onClick={() => setEditing(true)}>
              <Pencil className="size-4" aria-hidden="true" />
              {t("slo.detail.edit")}
            </Button>
            <Button type="button" variant="outline" size="sm" onClick={() => (confirmDelete ? remove.mutate() : setConfirmDelete(true))}>
              <Trash2 className="size-4" aria-hidden="true" />
              {confirmDelete ? t("slo.detail.confirmDelete") : t("slo.detail.delete")}
            </Button>
          </div>
        )}
      </div>
      {slo.description && <p className="text-sm text-muted-foreground">{slo.description}</p>}
      <FormError error={remove.error} />

      <dl className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6" data-testid="slo-tiles">
        {tiles.map(([label, value]) => (
          <div key={label} className="rounded-lg border bg-card px-3 py-2">
            <dt className="text-xs text-muted-foreground">{label}</dt>
            <dd className="mt-0.5 font-mono text-base font-semibold tabular-nums">{value}</dd>
          </div>
        ))}
      </dl>

      <section className="flex flex-col gap-2 rounded-xl border bg-card p-4">
        <h3 className="text-base font-semibold">{t("slo.detail.budgetTitle")}</h3>
        <BudgetBar budget={budget} />
        <BurndownChart points={series} />
      </section>

      <section className="flex flex-col gap-2 rounded-xl border bg-card p-4">
        <h3 className="text-base font-semibold">{t("slo.detail.burnTitle")}</h3>
        <p className="text-sm text-muted-foreground">{t("slo.detail.burnHint")}</p>
        <Table data-testid="burn-windows">
          <TableHeader>
            <TableRow>
              <TableHead>{t("slo.burn.window")}</TableHead>
              <TableHead className="text-right">{t("slo.burn.threshold")}</TableHead>
              <TableHead className="text-right">{t("slo.burn.long")}</TableHead>
              <TableHead className="text-right">{t("slo.burn.short")}</TableHead>
              <TableHead>{t("slo.columns.status")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {burn.map((b: SloBurnWindowResult) => (
              <TableRow key={b.name}>
                <TableCell className="font-medium">{burnName(b.name)}</TableCell>
                <TableCell className="text-right font-mono tabular-nums">{formatBurnRate(b.factor, locale)}</TableCell>
                <TableCell className="text-right font-mono tabular-nums">{formatBurnRate(b.long.burn_rate, locale)}</TableCell>
                <TableCell className="text-right font-mono tabular-nums">{formatBurnRate(b.short.burn_rate, locale)}</TableCell>
                <TableCell>
                  <Badge variant={b.breaching ? "destructive" : b.rate === null ? "muted" : "success"}>
                    {b.breaching ? t("slo.burn.breaching") : b.rate === null ? t("slo.status.none") : t("slo.burn.ok")}
                  </Badge>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </section>
    </div>
  );
}
