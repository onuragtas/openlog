import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Info, Pause, Play, Undo2 } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { fleetRolloutsQuery, pauseRollout, resumeRollout, rollbackFleet, type FleetRollout, type FleetSummary } from "@/api/fleet";
import { DateTimeText, FormError } from "@/components/settings/common";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { translateOptional } from "@/i18n/dynamic";
import { rollbackCandidates, rolloutShares, rolloutTone } from "@/lib/fleet";
import { cn } from "@/lib/utils";
import { ConfirmAction } from "./ConfirmAction";

function RolloutTitle({ rollout }: { rollout: FleetRollout }) {
  const { t } = useTranslation();
  if (!rollout.to_version) return <>{t("fleet.rollout.patch")}</>;
  return <>{rollout.action === "rollback" ? t("fleet.rollout.rollback", { version: rollout.to_version }) : t("fleet.rollout.upgrade", { version: rollout.to_version })}</>;
}

function StateBadge({ state }: { state: string }) {
  return <Badge variant={rolloutTone(state)}>{translateOptional(`fleet.rollout.state.${state}`, state)}</Badge>;
}

/** Current rollout with wave progress and admin actions, plus recent rollouts. */
export function RolloutPanel({ summary, canManage }: { summary: FleetSummary; canManage: boolean }) {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const rollout = summary.current_rollout;
  const history = useQuery(fleetRolloutsQuery());
  const running = summary.versions.map((v) => v.version);
  const candidates = rollbackCandidates(running, rollout?.to_version ?? summary.target?.version);
  const [rollbackTo, setRollbackTo] = useState("");
  const selected = candidates.includes(rollbackTo) ? rollbackTo : (candidates[0] ?? "");

  const invalidate = () => void qc.invalidateQueries({ queryKey: ["fleet"] });
  const pause = useMutation({ mutationFn: pauseRollout, onSettled: invalidate });
  const resume = useMutation({ mutationFn: resumeRollout, onSettled: invalidate });
  const rollback = useMutation({ mutationFn: rollbackFleet, onSettled: invalidate });
  const busy = pause.isPending || resume.isPending || rollback.isPending;

  return (
    <Card aria-labelledby={`${id}-title`}>
      <CardHeader>
        <CardTitle>
          <h2 id={`${id}-title`}>{t("fleet.rollout.title")}</h2>
        </CardTitle>
        {summary.update_available && summary.policy_mode === "notify" && (
          <CardDescription className="flex items-center gap-1.5 text-sm text-foreground" role="status">
            <Info className="size-4 text-primary" aria-hidden="true" />
            {t("fleet.summary.updateAvailable", { count: summary.outdated, version: summary.target?.version ?? "" })} {t("fleet.summary.updateAvailableNotify")}
          </CardDescription>
        )}
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {!rollout ? (
          <p className="text-sm text-muted-foreground">{t("fleet.rollout.none")}</p>
        ) : (
          <RolloutDetails rollout={rollout} />
        )}

        {canManage && (
          <div className="flex flex-wrap items-end gap-2 border-t pt-4">
            {rollout?.state === "active" && (
              <ConfirmAction
                label={t("fleet.rollout.pause")}
                confirmLabel={t("fleet.rollout.confirmPause")}
                pending={busy}
                onConfirm={() => pause.mutate(rollout.id)}
              />
            )}
            {(rollout?.state === "paused" || rollout?.state === "halted") && (
              <ConfirmAction
                label={t("fleet.rollout.resume")}
                confirmLabel={t("fleet.rollout.confirmResume")}
                pending={busy}
                onConfirm={() => resume.mutate(rollout.id)}
              />
            )}
            <div className="ml-auto flex flex-wrap items-end gap-2">
              <div className="flex flex-col gap-1.5">
                <label htmlFor={`${id}-rollback`} className="text-xs font-medium">
                  {t("fleet.rollout.rollbackVersion")}
                </label>
                <NativeSelect id={`${id}-rollback`} value={selected} disabled={candidates.length === 0} onChange={(e) => setRollbackTo(e.target.value)}>
                  {candidates.length === 0 && <option value="">{t("fleet.rollout.rollbackNone")}</option>}
                  {candidates.map((v) => (
                    <option key={v} value={v}>
                      {v}
                    </option>
                  ))}
                </NativeSelect>
              </div>
              <ConfirmAction
                label={t("fleet.rollout.rollbackAction")}
                confirmLabel={t("fleet.rollout.confirmRollback")}
                destructive
                pending={busy}
                disabled={!selected}
                onConfirm={() => rollback.mutate(selected)}
              />
            </div>
          </div>
        )}
        <FormError error={pause.error ?? resume.error ?? rollback.error} />

        {history.data && history.data.length > 1 && (
          <details className="text-sm">
            <summary className="cursor-pointer text-muted-foreground">{t("fleet.rollout.history")}</summary>
            <Table className="mt-2" mobile="stack">
              <TableHeader>
                <TableRow>
                  <TableHead>{t("fleet.rollout.columns.rollout")}</TableHead>
                  <TableHead>{t("fleet.rollout.columns.state")}</TableHead>
                  <TableHead>{t("fleet.rollout.columns.progress")}</TableHead>
                  <TableHead>{t("fleet.rollout.columns.started")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {history.data.map((r) => (
                  <TableRow key={r.id}>
                    <TableCell>
                      <RolloutTitle rollout={r} />
                    </TableCell>
                    <TableCell className="max-md:w-auto">
                      <StateBadge state={r.state} />
                    </TableCell>
                    <TableCell label={t("fleet.rollout.columns.progress")} className="text-xs tabular-nums">
                      {r.counters.succeeded} ✓ · {r.counters.failed + r.counters.rolled_back} ✗ · {r.counters.pending} …
                    </TableCell>
                    <TableCell label={t("fleet.rollout.columns.started")}>
                      <DateTimeText value={r.created_at} relative />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </details>
        )}
      </CardContent>
    </Card>
  );
}

function RolloutDetails({ rollout }: { rollout: FleetRollout }) {
  const { t } = useTranslation();
  const shares = rolloutShares(rollout.counters);
  const total = rollout.waves.length;
  const Icon = rollout.action === "rollback" ? Undo2 : rollout.state === "paused" ? Pause : Play;

  return (
    <div className="flex flex-col gap-4" data-testid="rollout-details">
      <div className="flex flex-wrap items-center gap-2">
        <Icon className="size-4 text-muted-foreground" aria-hidden="true" />
        <h3 className="text-base font-semibold">
          <RolloutTitle rollout={rollout} />
        </h3>
        {rollout.from_version && <span className="text-sm text-muted-foreground">{t("fleet.rollout.from", { version: rollout.from_version })}</span>}
        <StateBadge state={rollout.state} />
        <span className="text-xs text-muted-foreground">
          {rollout.created_by_email ? t("fleet.rollout.createdBy", { email: rollout.created_by_email }) : t("fleet.rollout.createdByController")} ·{" "}
          <DateTimeText value={rollout.created_at} relative />
        </span>
      </div>

      {rollout.state === "halted" && rollout.state_reason && (
        <p role="alert" className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive-text">
          {rollout.state_reason}
        </p>
      )}

      <ol className="grid gap-2 sm:grid-cols-[repeat(auto-fit,minmax(9rem,1fr))]" aria-label={t("fleet.rollout.wave", { current: rollout.current_wave + 1, total })}>
        {rollout.waves.map((pct, i) => {
          const done = rollout.state === "completed" || i < rollout.current_wave;
          const current = !done && i === rollout.current_wave;
          return (
            <li
              key={i}
              aria-current={current ? "step" : undefined}
              className={cn(
                "rounded-lg border px-3 py-2 text-sm",
                done && "border-success/50 bg-success/10",
                current && "border-primary bg-primary/5",
              )}
            >
              <span className="font-medium">{t("fleet.rollout.waveStep", { n: i + 1, percent: pct })}</span>
              <span className="block text-xs text-muted-foreground">{done ? t("fleet.rollout.waveDone") : current ? t("fleet.rollout.waveCurrent") : t("fleet.rollout.waveNext")}</span>
            </li>
          );
        })}
      </ol>

      <div className="flex flex-col gap-1.5">
        <div className="flex flex-wrap items-baseline justify-between gap-2 text-sm">
          <span className="font-medium" data-testid="rollout-wave">
            {rollout.state === "completed" ? translateOptional("fleet.rollout.state.completed", "completed") : t("fleet.rollout.wave", { current: rollout.current_wave + 1, total })}
            {rollout.state !== "completed" && <span className="ml-2 text-muted-foreground">{t("fleet.rollout.wavePercent", { percent: rollout.wave_percent })}</span>}
          </span>
          {rollout.next_wave_at && (
            <span className="text-xs text-muted-foreground">
              {t("fleet.rollout.nextWave")} <DateTimeText value={rollout.next_wave_at} relative />
            </span>
          )}
        </div>
        <div
          role="progressbar"
          aria-label={t("fleet.rollout.progress")}
          aria-valuemin={0}
          aria-valuemax={shares.total}
          aria-valuenow={shares.done}
          aria-valuetext={t("fleet.rollout.progressValue", { done: shares.done, total: shares.total })}
          className="flex h-3 overflow-hidden rounded-full bg-muted"
        >
          <span className="h-full bg-success transition-[width]" style={{ width: `${shares.succeeded}%` }} />
          <span className="h-full bg-destructive transition-[width]" style={{ width: `${shares.failed}%` }} />
        </div>
        <p className="text-xs text-muted-foreground">{t("fleet.rollout.progressValue", { done: shares.done, total: shares.total })}</p>
      </div>

      <dl className="grid grid-cols-2 gap-2 sm:grid-cols-5">
        {(["pending", "attempted", "succeeded", "failed", "rolled_back"] as const).map((k) => (
          <div key={k} className="rounded-lg border px-3 py-2">
            <dt className="text-xs text-muted-foreground">{t(`fleet.rollout.counters.${k}`)}</dt>
            <dd
              className={cn("text-lg font-semibold tabular-nums", (k === "failed" || k === "rolled_back") && rollout.counters[k] > 0 && "text-destructive-text")}
              data-testid={`rollout-${k}`}
            >
              {rollout.counters[k]}
            </dd>
          </div>
        ))}
      </dl>
    </div>
  );
}
