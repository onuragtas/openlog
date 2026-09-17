// Cloud connection detail: the schedule state of every scope and the recent polls — what was collected, what
// it cost in provider requests and what failed (docs/contracts/api.md "Cloud connections").
// Router-free: the page passes the callbacks.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil, Trash2 } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import type { CloudRun, CloudScopeStatus } from "@/api/cloud";
import { cloudRunsQuery, deleteCloudConnection } from "@/api/cloud";
import { FormError } from "@/components/settings/common";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
  connectionState,
  formatCount,
  formatDurationMs,
  formatRunAgo,
  formatRunTime,
  runBadgeVariant,
  scopeLabelKey,
  stateBadgeVariant,
} from "@/lib/cloud";
import { CloudConnectionForm } from "./CloudConnectionForm";

export interface CloudConnectionDetailProps {
  id: string;
  canManage?: boolean;
  onDeleted?: () => void;
}

export function CloudConnectionDetail({ id, canManage = false, onDeleted }: CloudConnectionDetailProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const queryClient = useQueryClient();
  const uid = useId();
  const [editing, setEditing] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [scope, setScope] = useState("");
  const q = useQuery(cloudRunsQuery(id, scope || undefined));

  const remove = useMutation({
    mutationFn: () => deleteCloudConnection(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["cloud"] });
      onDeleted?.();
    },
  });

  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const { connection, runs } = q.data;
  const state = connectionState(connection);
  const scopeKey = scopeLabelKey(connection.provider);

  if (editing) {
    return (
      <CloudConnectionForm
        connection={connection}
        onSaved={() => setEditing(false)}
        onCancel={() => setEditing(false)}
      />
    );
  }

  return (
    <div className="flex flex-col gap-4" data-testid="cloud-detail">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-lg font-semibold">{connection.name}</h2>
        <Badge variant={stateBadgeVariant(state)}>{t(`cloud.state.${state}`)}</Badge>
        <span className="text-xs text-muted-foreground">{t(`cloud.providers.${connection.provider}`)}</span>
        {canManage && (
          <div className="ml-auto flex gap-2">
            <Button type="button" variant="outline" size="sm" onClick={() => setEditing(true)}>
              <Pencil className="size-4" aria-hidden="true" />
              {t("cloud.detail.edit")}
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => (confirmDelete ? remove.mutate() : setConfirmDelete(true))}
            >
              <Trash2 className="size-4" aria-hidden="true" />
              {confirmDelete ? t("cloud.detail.confirmDelete") : t("cloud.detail.delete")}
            </Button>
          </div>
        )}
      </div>
      <FormError error={remove.error} />

      <section className="flex flex-col gap-2 rounded-xl border bg-card p-4">
        <h3 className="text-base font-semibold">{t("cloud.detail.scopesTitle")}</h3>
        <p className="text-xs text-muted-foreground">{t("cloud.detail.scopesHint")}</p>
        <Table data-testid="cloud-scopes">
          <TableHeader>
            <TableRow>
              <TableHead>{t(`cloud.scopeLabel.${scopeKey}`)}</TableHead>
              <TableHead>{t("cloud.columns.status")}</TableHead>
              <TableHead className="text-right">{t("cloud.detail.collected")}</TableHead>
              <TableHead className="hidden sm:table-cell text-right">{t("cloud.detail.apiCalls")}</TableHead>
              <TableHead className="hidden md:table-cell">{t("cloud.detail.lastRun")}</TableHead>
              <TableHead className="hidden md:table-cell">{t("cloud.detail.nextRun")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {connection.status.map((s: CloudScopeStatus) => (
              <TableRow key={s.scope}>
                <TableCell className="font-mono text-xs">{s.scope}</TableCell>
                <TableCell>
                  {s.last_status === "" ? (
                    <Badge variant="muted">{t("cloud.state.unknown")}</Badge>
                  ) : (
                    <Badge variant={runBadgeVariant(s.last_status)}>{t(`cloud.state.${s.last_status}`)}</Badge>
                  )}
                  {s.last_error && <div className="mt-1 max-w-md text-xs break-words text-muted-foreground">{s.last_error}</div>}
                </TableCell>
                <TableCell className="text-right font-mono tabular-nums">{formatCount(s.last_metrics, locale)}</TableCell>
                <TableCell className="hidden sm:table-cell text-right font-mono tabular-nums">
                  {formatCount(s.last_api_calls, locale)}
                </TableCell>
                <TableCell className="hidden md:table-cell text-xs text-muted-foreground">
                  {s.last_run_at ? formatRunAgo(s.last_run_at, locale) : t("cloud.detail.never")}
                </TableCell>
                <TableCell className="hidden md:table-cell text-xs text-muted-foreground">
                  {formatRunAgo(s.next_run_at, locale)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </section>

      <section className="flex flex-col gap-2 rounded-xl border bg-card p-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h3 className="text-base font-semibold">{t("cloud.detail.runsTitle")}</h3>
          {connection.scopes.length > 1 && (
            <div className="flex items-center gap-2">
              <label htmlFor={`${uid}-scope`} className="text-xs text-muted-foreground">
                {t("cloud.detail.filterScope")}
              </label>
              <NativeSelect id={`${uid}-scope`} value={scope} onChange={(e) => setScope(e.target.value)}>
                <option value="">{t("cloud.detail.allScopes")}</option>
                {connection.scopes.map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
              </NativeSelect>
            </div>
          )}
        </div>
        {runs.length === 0 ? (
          <p className="py-6 text-center text-sm text-muted-foreground">{t("cloud.detail.runsEmpty")}</p>
        ) : (
          <Table data-testid="cloud-runs">
            <TableHeader>
              <TableRow>
                <TableHead>{t("cloud.detail.startedAt")}</TableHead>
                <TableHead className="hidden sm:table-cell">{t(`cloud.scopeLabel.${scopeKey}`)}</TableHead>
                <TableHead>{t("cloud.columns.status")}</TableHead>
                <TableHead className="text-right">{t("cloud.detail.collected")}</TableHead>
                <TableHead className="hidden md:table-cell text-right">{t("cloud.detail.apiCalls")}</TableHead>
                <TableHead className="hidden lg:table-cell text-right">{t("cloud.detail.throttled")}</TableHead>
                <TableHead className="hidden lg:table-cell text-right">{t("cloud.detail.duration")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {runs.map((r: CloudRun) => (
                <TableRow key={r.id}>
                  <TableCell className="text-xs">{formatRunTime(r.started_at, locale)}</TableCell>
                  <TableCell className="hidden sm:table-cell font-mono text-xs">{r.scope}</TableCell>
                  <TableCell>
                    <Badge variant={runBadgeVariant(r.status)}>{t(`cloud.state.${r.status}`)}</Badge>
                    {r.error && <div className="mt-1 max-w-md text-xs break-words text-muted-foreground">{r.error}</div>}
                    {r.services.length > 0 && (
                      <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
                        {/* One text node per service, so the breakdown reads as "RDS: 330" rather than as
                            three separate fragments to a screen reader. */}
                        {r.services.map((s) => (
                          <span key={s.service} className="tabular-nums">
                            {`${t(`cloud.services.${s.service}`, { defaultValue: s.service })}: ${formatCount(s.metrics, locale)}`}
                          </span>
                        ))}
                      </div>
                    )}
                  </TableCell>
                  <TableCell className="text-right font-mono tabular-nums">{formatCount(r.metrics, locale)}</TableCell>
                  <TableCell className="hidden md:table-cell text-right font-mono tabular-nums">
                    {formatCount(r.api_calls, locale)}
                  </TableCell>
                  <TableCell className="hidden lg:table-cell text-right font-mono tabular-nums">
                    {formatCount(r.throttled, locale)}
                  </TableCell>
                  <TableCell className="hidden lg:table-cell text-right font-mono tabular-nums">
                    {formatDurationMs(r.duration_ms, locale)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        <p className="text-xs text-muted-foreground">{t("cloud.detail.runsHint")}</p>
      </section>
    </div>
  );
}
