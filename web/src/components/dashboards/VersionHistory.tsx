import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { History, RotateCcw } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiError } from "@/api/client";
import type { Dashboard } from "@/api/dashboards";
import { dashboardVersionQuery, dashboardVersionsQuery, restoreDashboardVersion, type DashboardDiff } from "@/api/dashboardSharing";
import { FormError } from "@/components/settings/common";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { formatDateTime } from "@/lib/format";
import { cn } from "@/lib/utils";

export interface VersionHistoryProps {
  dashboard: Dashboard;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Called with the new current version after a restore. */
  onRestored: (d: Dashboard) => void;
}

const parseTs = (s: string) => Date.parse(s.replace(/(\.\d{3})\d+/, "$1"));

/** Version history of a dashboard: stored versions, what each changed, and restore (api.md "Version history"). */
export function VersionHistory({ dashboard, open, onOpenChange, onRestored }: VersionHistoryProps) {
  const { t } = useTranslation();
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" title={t("dashboards.history.title")} closeLabel={t("common.close")} className="w-full max-w-full sm:max-w-xl">
        {open && <HistoryBody dashboard={dashboard} onRestored={onRestored} />}
      </SheetContent>
    </Sheet>
  );
}

function HistoryBody({ dashboard, onRestored }: { dashboard: Dashboard; onRestored: (d: Dashboard) => void }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const queryClient = useQueryClient();
  const list = useQuery(dashboardVersionsQuery(dashboard.id));
  const [selected, setSelected] = useState<number | null>(null);
  const [confirm, setConfirm] = useState(false);
  const detail = useQuery(dashboardVersionQuery(dashboard.id, selected));
  const restore = useMutation({
    mutationFn: (version: number) => restoreDashboardVersion(dashboard.id, version, dashboard.version),
    onSuccess: (d) => {
      void queryClient.invalidateQueries({ queryKey: ["dashboards", "versions", dashboard.id] });
      setSelected(null);
      setConfirm(false);
      onRestored(d);
    },
  });

  if (list.isPending) return <LoadingState />;
  if (list.isError) return <ErrorState error={list.error} onRetry={() => void list.refetch()} />;
  const { versions, can_restore } = list.data;
  const current = dashboard.version;

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-4" data-testid="version-history">
      <p className="text-sm text-muted-foreground">{t("dashboards.history.hint", { count: versions.length })}</p>
      <ol className="flex flex-col gap-1.5" aria-label={t("dashboards.history.list")}>
        {versions.map((v) => {
          const active = v.version === selected;
          return (
            <li key={v.version}>
              <button
                type="button"
                aria-pressed={active}
                onClick={() => {
                  setSelected(active ? null : v.version);
                  setConfirm(false);
                  restore.reset();
                }}
                className={cn("flex w-full min-w-0 flex-col gap-0.5 rounded-lg border px-3 py-2 text-left text-sm hover:border-primary", active && "border-primary bg-accent")}
                data-testid="version-item"
              >
                <span className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{t("dashboards.history.version", { version: v.version })}</span>
                  {v.version === current && <Badge variant="muted">{t("dashboards.history.current")}</Badge>}
                  {v.restored_from !== null && <Badge variant="outline">{t("dashboards.history.restoredFrom", { version: v.restored_from })}</Badge>}
                </span>
                <span className="min-w-0 truncate text-xs text-muted-foreground">
                  {formatDateTime(parseTs(v.created_at), locale)} · {v.author_email || t("dashboards.history.unknownAuthor")} ·{" "}
                  {t("dashboards.counts", { pages: v.page_count, widgets: v.widget_count })}
                </span>
              </button>
            </li>
          );
        })}
      </ol>

      {selected !== null && (
        <section className="flex flex-col gap-3 rounded-lg border p-3" aria-label={t("dashboards.history.version", { version: selected })} data-testid="version-detail">
          {detail.isPending || !detail.data ? (
            detail.isError ? <ErrorState error={detail.error} onRetry={() => void detail.refetch()} /> : <LoadingState />
          ) : (
            <>
              <div>
                <h3 className="mb-1 text-sm font-semibold">{t("dashboards.history.changes")}</h3>
                {detail.data.changes ? <DiffSummary diff={detail.data.changes} /> : <p className="text-sm text-muted-foreground">{t("dashboards.history.firstVersion")}</p>}
              </div>
              {selected !== current && (
                <div>
                  <h3 className="mb-1 text-sm font-semibold">{t("dashboards.history.restoreEffect")}</h3>
                  <DiffSummary diff={detail.data.differences_from_current} />
                </div>
              )}
              {can_restore && selected !== current && (
                <div className="flex flex-wrap items-center gap-2">
                  {confirm ? (
                    <>
                      <Button onClick={() => restore.mutate(selected)} disabled={restore.isPending} data-testid="restore-version-confirm">
                        <RotateCcw aria-hidden="true" />
                        {t("dashboards.history.confirmRestore", { version: selected })}
                      </Button>
                      <Button variant="outline" onClick={() => setConfirm(false)}>
                        {t("common.cancel")}
                      </Button>
                    </>
                  ) : (
                    <Button variant="outline" onClick={() => setConfirm(true)} data-testid="restore-version">
                      <History aria-hidden="true" />
                      {t("dashboards.history.restore")}
                    </Button>
                  )}
                </div>
              )}
              {restore.error instanceof ApiError && restore.error.status === 409 ? (
                <p role="alert" className="text-sm text-destructive-text">
                  {t("dashboards.conflict")}
                </p>
              ) : (
                <FormError error={restore.error} />
              )}
            </>
          )}
        </section>
      )}
    </div>
  );
}

const FIELDS = ["title", "visualization", "layout", "query", "markdown", "unit", "thresholds", "options", "page"] as const;

export function DiffSummary({ diff }: { diff: DashboardDiff }) {
  const { t } = useTranslation();
  const lines: string[] = [];
  if (diff.name) lines.push(t("dashboards.history.diff.name"));
  if (diff.description) lines.push(t("dashboards.history.diff.description"));
  if (diff.visibility) lines.push(t("dashboards.history.diff.visibility"));
  if (diff.variables) lines.push(t("dashboards.history.diff.variables"));
  const names = (list: { name?: string; title?: string }[]) => list.map((x) => x.name || x.title || t("dashboards.widget.untitled")).join(", ");
  if (diff.pages_added.length) lines.push(t("dashboards.history.diff.pagesAdded", { names: names(diff.pages_added) }));
  if (diff.pages_removed.length) lines.push(t("dashboards.history.diff.pagesRemoved", { names: names(diff.pages_removed) }));
  if (diff.pages_renamed.length) lines.push(t("dashboards.history.diff.pagesRenamed", { names: names(diff.pages_renamed) }));
  if (diff.widgets_added.length) lines.push(t("dashboards.history.diff.widgetsAdded", { count: diff.widgets_added.length, names: names(diff.widgets_added) }));
  if (diff.widgets_removed.length) lines.push(t("dashboards.history.diff.widgetsRemoved", { count: diff.widgets_removed.length, names: names(diff.widgets_removed) }));
  if (lines.length === 0 && diff.widgets_changed.length === 0) return <p className="text-sm text-muted-foreground">{t("dashboards.history.diff.none")}</p>;
  return (
    <ul className="flex list-disc flex-col gap-0.5 pl-5 text-sm break-words" data-testid="version-diff">
      {lines.map((l) => (
        <li key={l}>{l}</li>
      ))}
      {diff.widgets_changed.map((w) => (
        <li key={w.id}>
          {t("dashboards.history.diff.widgetChanged", {
            name: w.title || t("dashboards.widget.untitled"),
            fields: (w.fields ?? []).filter((f): f is (typeof FIELDS)[number] => (FIELDS as readonly string[]).includes(f)).map((f) => t(`dashboards.history.fields.${f}`)).join(", "),
          })}
        </li>
      ))}
    </ul>
  );
}
