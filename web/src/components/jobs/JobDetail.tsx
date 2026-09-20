// Job monitor detail: the ping command to put in the crontab, the current state and the concluded runs
// (docs/contracts/api.md "Job monitoring", D-141). Router-free: the page passes the callbacks.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Pencil, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { deleteJobMonitor, jobRunsQuery, rotateJobMonitorToken } from "@/api/jobs";
import { FormError } from "@/components/settings/common";
import { SsoCopyField } from "@/components/settings/SsoCopyField";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
  formatAgo,
  formatDuration,
  formatLate,
  formatRunTime,
  jobState,
  jobStateBadgeVariant,
  pingCommand,
  pingCommandWithStart,
  runStatusBadgeVariant,
  scheduleText,
} from "@/lib/jobs";
import { JobForm } from "./JobForm";

export interface JobDetailProps {
  id: string;
  canWrite?: boolean;
  onDeleted?: () => void;
}

export function JobDetail({ id, canWrite = false, onDeleted }: JobDetailProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [showStart, setShowStart] = useState(false);
  const q = useQuery(jobRunsQuery(id));

  const remove = useMutation({
    mutationFn: () => deleteJobMonitor(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["jobs"] });
      onDeleted?.();
    },
  });
  const rotate = useMutation({
    mutationFn: () => rotateJobMonitorToken(id),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["jobs"] }),
  });

  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const { monitor, runs, summary } = q.data;
  const state = jobState(monitor);

  if (editing) {
    return <JobForm monitor={monitor} onSaved={() => setEditing(false)} onCancel={() => setEditing(false)} />;
  }

  const tiles: [string, string][] = [
    [t("jobs.detail.runs"), String(summary.runs)],
    [t("jobs.detail.failures"), String(summary.failures)],
    [t("jobs.detail.missed"), String(summary.missed)],
    [t("jobs.detail.avg"), formatDuration(summary.avg_ms, locale)],
    [t("jobs.detail.max"), formatDuration(summary.max_ms, locale)],
    [t("jobs.detail.nextRun"), monitor.enabled ? formatAgo(monitor.state.expected_at, locale) : t("jobs.state.paused")],
  ];

  return (
    <div className="flex flex-col gap-4" data-testid="job-detail">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-lg font-semibold">{monitor.name}</h2>
        <Badge variant={jobStateBadgeVariant(state)}>{t(`jobs.state.${state}`)}</Badge>
        <span className="font-mono text-xs text-muted-foreground">{scheduleText(monitor, (v) => t("jobs.every", { interval: v }))}</span>
        {canWrite && (
          <div className="ml-auto flex flex-wrap gap-2">
            <Button type="button" variant="outline" size="sm" onClick={() => setEditing(true)}>
              <Pencil className="size-4" aria-hidden="true" />
              {t("jobs.detail.edit")}
            </Button>
            <Button type="button" variant="outline" size="sm" disabled={rotate.isPending} onClick={() => rotate.mutate()}>
              <KeyRound className="size-4" aria-hidden="true" />
              {t("jobs.detail.rotate")}
            </Button>
            <Button type="button" variant="outline" size="sm" onClick={() => (confirmDelete ? remove.mutate() : setConfirmDelete(true))}>
              <Trash2 className="size-4" aria-hidden="true" />
              {confirmDelete ? t("jobs.detail.confirmDelete") : t("jobs.detail.delete")}
            </Button>
          </div>
        )}
      </div>
      {monitor.description && <p className="text-sm text-muted-foreground">{monitor.description}</p>}
      {remove.isError && <FormError error={remove.error} />}
      {rotate.isError && <FormError error={rotate.error} />}

      {/* The ping command comes first: a monitor nobody wired up is a monitor that will report "missed". */}
      <section className="flex flex-col gap-2 rounded-xl border bg-card p-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h3 className="text-sm font-semibold">{t("jobs.detail.pingTitle")}</h3>
          <Button type="button" variant="ghost" size="sm" onClick={() => setShowStart((v) => !v)}>
            {showStart ? t("jobs.detail.simpleCommand") : t("jobs.detail.startCommand")}
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">{showStart ? t("jobs.detail.startHint") : t("jobs.detail.pingHint")}</p>
        <SsoCopyField
          label={t("jobs.detail.command")}
          value={showStart ? pingCommandWithStart(monitor.ping_url) : pingCommand(monitor.ping_url)}
          multiline={showStart}
        />
        <SsoCopyField label={t("jobs.detail.pingUrl")} value={monitor.ping_url} />
        <p className="text-xs text-muted-foreground">{t("jobs.detail.tokenNote")}</p>
      </section>

      <section className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
        {tiles.map(([label, value]) => (
          <div key={label} className="rounded-xl border bg-card p-3">
            <div className="text-xs text-muted-foreground">{label}</div>
            <div className="mt-1 font-mono text-lg tabular-nums">{value}</div>
          </div>
        ))}
      </section>

      {monitor.state.last_message && (
        <section className="rounded-xl border bg-card p-4">
          <h3 className="mb-1 text-sm font-semibold">{t("jobs.detail.lastOutput")}</h3>
          <pre className="max-h-48 overflow-auto rounded-md bg-muted/40 p-2 font-mono text-xs whitespace-pre-wrap">{monitor.state.last_message}</pre>
        </section>
      )}

      <section className="rounded-xl border bg-card">
        <h3 className="border-b px-4 py-2 text-sm font-semibold">{t("jobs.detail.runsTitle")}</h3>
        {runs.length === 0 ? (
          <p className="px-4 py-6 text-center text-sm text-muted-foreground">{t("jobs.detail.runsEmpty")}</p>
        ) : (
          <Table mobile="stack" data-testid="job-runs">
            <TableHeader>
              <TableRow>
                <TableHead>{t("jobs.columns.time")}</TableHead>
                <TableHead>{t("jobs.columns.status")}</TableHead>
                <TableHead className="text-right">{t("jobs.columns.duration")}</TableHead>
                <TableHead className="text-right">{t("jobs.columns.late")}</TableHead>
                <TableHead className="text-right">{t("jobs.columns.exitCode")}</TableHead>
                <TableHead>{t("jobs.columns.output")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {runs.map((run, i) => (
                <TableRow key={`${run.timestamp}-${i}`}>
                  <TableCell className="text-xs whitespace-nowrap">{formatRunTime(run.timestamp, locale)}</TableCell>
                  <TableCell className="max-md:w-auto">
                    <Badge variant={runStatusBadgeVariant(run.status)}>{t(`jobs.runStatus.${run.status}`, { defaultValue: run.status })}</Badge>
                  </TableCell>
                  <TableCell label={t("jobs.columns.duration")} className="text-right font-mono tabular-nums">
                    {formatDuration(run.duration_ms, locale)}
                  </TableCell>
                  <TableCell label={t("jobs.columns.late")} className="text-right font-mono tabular-nums">
                    {formatLate(run.late_seconds)}
                  </TableCell>
                  <TableCell label={t("jobs.columns.exitCode")} className="text-right font-mono tabular-nums">
                    {run.exit_code || "–"}
                  </TableCell>
                  <TableCell label={t("jobs.columns.output")} className="max-w-96 truncate font-mono text-xs" title={run.message}>
                    {run.message || "–"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </section>
    </div>
  );
}
