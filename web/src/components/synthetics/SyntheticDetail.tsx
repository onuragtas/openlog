// Synthetic check detail: uptime and latency over the range, the per-location schedule state and the most
// recent failed runs (docs/contracts/api.md "Synthetic monitoring").
// Router-free: the page passes the callbacks.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { deleteSyntheticCheck, syntheticResultsQuery, type SyntheticFailure, type SyntheticLocationStatus } from "@/api/synthetics";
import { Sparkline } from "@/components/apm/Charts";
import { FormError } from "@/components/settings/common";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
  checkState,
  checkTarget,
  formatCount,
  formatDuration,
  formatRunAgo,
  formatRunTime,
  formatUptime,
  latencyPoints,
  stateBadgeVariant,
} from "@/lib/synthetics";
import { SyntheticForm } from "./SyntheticForm";

export interface SyntheticDetailProps {
  id: string;
  canWrite?: boolean;
  onDeleted?: () => void;
}

export function SyntheticDetail({ id, canWrite = false, onDeleted }: SyntheticDetailProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const q = useQuery(syntheticResultsQuery(id));

  const remove = useMutation({
    mutationFn: () => deleteSyntheticCheck(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["synthetics"] });
      onDeleted?.();
    },
  });

  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const { check, summary, failures } = q.data;
  const state = checkState(check);

  if (editing) {
    return <SyntheticForm check={check} onSaved={() => setEditing(false)} onCancel={() => setEditing(false)} />;
  }

  const tiles: [string, string][] = [
    [t("synthetics.detail.uptime"), formatUptime(summary.uptime, locale)],
    [t("synthetics.detail.runs"), formatCount(summary.runs, locale)],
    [t("synthetics.detail.failures"), formatCount(summary.failures, locale)],
    [t("synthetics.detail.avg"), formatDuration(summary.avg_ms, locale)],
    [t("synthetics.detail.p95"), formatDuration(summary.p95_ms, locale)],
    [t("synthetics.detail.p99"), formatDuration(summary.p99_ms, locale)],
  ];

  return (
    <div className="flex flex-col gap-4" data-testid="synthetic-detail">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-lg font-semibold">{check.name}</h2>
        <Badge variant={stateBadgeVariant(state)}>{t(`synthetics.state.${state}`)}</Badge>
        <span className="font-mono text-xs text-muted-foreground">{checkTarget(check)}</span>
        {canWrite && (
          <div className="ml-auto flex gap-2">
            <Button type="button" variant="outline" size="sm" onClick={() => setEditing(true)}>
              <Pencil className="size-4" aria-hidden="true" />
              {t("synthetics.detail.edit")}
            </Button>
            <Button type="button" variant="outline" size="sm" onClick={() => (confirmDelete ? remove.mutate() : setConfirmDelete(true))}>
              <Trash2 className="size-4" aria-hidden="true" />
              {confirmDelete ? t("synthetics.detail.confirmDelete") : t("synthetics.detail.delete")}
            </Button>
          </div>
        )}
      </div>
      <FormError error={remove.error} />

      <dl className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6" data-testid="synthetic-tiles">
        {tiles.map(([label, value]) => (
          <div key={label} className="rounded-lg border bg-card px-3 py-2">
            <dt className="text-xs text-muted-foreground">{label}</dt>
            <dd className="mt-0.5 font-mono text-base font-semibold tabular-nums">{value}</dd>
          </div>
        ))}
      </dl>

      <section className="flex flex-col gap-2 rounded-xl border bg-card p-4">
        <h3 className="text-base font-semibold">{t("synthetics.detail.summaryTitle")}</h3>
        {summary.points.length === 0 ? (
          <p className="py-6 text-center text-sm text-muted-foreground">{t("synthetics.detail.seriesEmpty")}</p>
        ) : (
          <Sparkline
            points={latencyPoints(summary)}
            label={t("synthetics.detail.summaryTitle")}
            width={600}
            height={80}
            className="w-full"
          />
        )}
      </section>

      <section className="flex flex-col gap-2 rounded-xl border bg-card p-4">
        <h3 className="text-base font-semibold">{t("synthetics.detail.locationsTitle")}</h3>
        <Table data-testid="synthetic-locations">
          <TableHeader>
            <TableRow>
              <TableHead>{t("synthetics.form.locations")}</TableHead>
              <TableHead>{t("synthetics.columns.status")}</TableHead>
              <TableHead className="hidden sm:table-cell">{t("synthetics.detail.lastRun")}</TableHead>
              <TableHead className="hidden sm:table-cell">{t("synthetics.detail.nextRun")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {check.status.map((s: SyntheticLocationStatus) => (
              <TableRow key={s.location}>
                <TableCell>{t(`synthetics.locations.${s.location}`, { defaultValue: s.location })}</TableCell>
                <TableCell>
                  {s.last_success === null || s.last_success === undefined ? (
                    <Badge variant="muted">{t("synthetics.state.unknown")}</Badge>
                  ) : (
                    <Badge variant={s.last_success ? "success" : "destructive"}>
                      {s.last_success ? t("synthetics.state.up") : t("synthetics.state.down")}
                    </Badge>
                  )}
                  {s.last_error && <div className="mt-1 text-xs text-muted-foreground">{s.last_error}</div>}
                </TableCell>
                <TableCell className="hidden sm:table-cell text-xs text-muted-foreground">
                  {s.last_run_at ? formatRunAgo(s.last_run_at, locale) : t("synthetics.detail.never")}
                </TableCell>
                <TableCell className="hidden sm:table-cell text-xs text-muted-foreground">{formatRunAgo(s.next_run_at, locale)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </section>

      <section className="flex flex-col gap-2 rounded-xl border bg-card p-4">
        <h3 className="text-base font-semibold">{t("synthetics.detail.failuresTitle")}</h3>
        {failures.length === 0 ? (
          <p className="py-6 text-center text-sm text-muted-foreground">{t("synthetics.detail.failuresEmpty")}</p>
        ) : (
          <Table data-testid="synthetic-failures">
            <TableHeader>
              <TableRow>
                <TableHead>{t("synthetics.columns.lastRun")}</TableHead>
                <TableHead>{t("synthetics.form.locations")}</TableHead>
                <TableHead>{t("synthetics.columns.status")}</TableHead>
                <TableHead className="text-right">{t("synthetics.columns.latency")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {failures.map((f: SyntheticFailure, i: number) => (
                <TableRow key={`${f.timestamp}-${i}`}>
                  <TableCell className="text-xs">{formatRunTime(f.timestamp, locale)}</TableCell>
                  <TableCell>{t(`synthetics.locations.${f.location}`, { defaultValue: f.location })}</TableCell>
                  <TableCell>
                    <div className="flex flex-wrap items-center gap-2">
                      {f.error_kind && <Badge variant="destructive">{t(`synthetics.errorKinds.${f.error_kind}`, { defaultValue: f.error_kind })}</Badge>}
                      <span className="text-xs text-muted-foreground">{f.error}</span>
                    </div>
                  </TableCell>
                  <TableCell className="text-right font-mono tabular-nums">{formatDuration(f.duration_ms, locale)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </section>
    </div>
  );
}
