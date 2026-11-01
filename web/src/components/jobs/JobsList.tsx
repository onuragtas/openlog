// Job monitor list: every cron and heartbeat monitor of the organization with the state it is in, its
// schedule and when it is next due (docs/contracts/api.md "Job monitoring", D-141).
// Router-free: the page passes the navigation callbacks.
import { useQuery } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { JobMonitor } from "@/api/jobs";
import { jobMonitorsQuery } from "@/api/jobs";
import { ListPager } from "@/components/ListPager";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { SortableHead, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatAgo, formatDuration, jobState, jobStateBadgeVariant, scheduleText, type JobState } from "@/lib/jobs";
import { timeOf, useLocalList } from "@/lib/list";

export interface JobsListProps {
  onOpen: (monitor: JobMonitor) => void;
  onNew?: () => void;
  canWrite?: boolean;
}

// The worst first when sorted largest first.
const STATE_RANK: Record<JobState, number> = { paused: 0, unknown: 1, ok: 2, running: 3, late: 4, failing: 5 };

const MONITOR_COLUMNS = {
  name: (m: JobMonitor) => m.name,
  schedule: (m: JobMonitor) => m.interval_seconds || m.cron,
  status: (m: JobMonitor) => STATE_RANK[jobState(m)],
  last_duration: (m: JobMonitor) => m.state.last_duration_ms,
  last_run: (m: JobMonitor) => timeOf(m.state.last_ping_at),
  // A paused monitor expects nothing, whatever its schedule says.
  next_run: (m: JobMonitor) => (m.enabled ? timeOf(m.state.expected_at) : null),
};

export function JobsList({ onOpen, onNew, canWrite = false }: JobsListProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const q = useQuery(jobMonitorsQuery());
  const list = useLocalList(q.data?.monitors, MONITOR_COLUMNS, "name");

  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const monitors = q.data.monitors;

  return (
    <div className="flex flex-col gap-3">
      {canWrite && onNew && (
        <div className="flex justify-end">
          <Button type="button" onClick={onNew}>
            <Plus className="size-4" aria-hidden="true" />
            {t("jobs.new")}
          </Button>
        </div>
      )}
      <div className="rounded-xl border bg-card">
        {monitors.length === 0 ? (
          <EmptyState>
            <p>{t("jobs.empty")}</p>
            <p className="mt-1 text-xs">{t("jobs.emptyHint")}</p>
          </EmptyState>
        ) : (
          <>
            <Table mobile="stack" data-testid="jobs-list">
              <TableHeader>
                <TableRow>
                  <SortableHead column="name" defaultDir="asc" {...list.head}>{t("jobs.columns.name")}</SortableHead>
                  <TableHead>{t("jobs.columns.schedule")}</TableHead>
                  <SortableHead column="status" {...list.head}>{t("jobs.columns.status")}</SortableHead>
                  <SortableHead column="last_duration" className="text-right" {...list.head}>{t("jobs.columns.lastDuration")}</SortableHead>
                  <SortableHead column="last_run" {...list.head}>{t("jobs.columns.lastRun")}</SortableHead>
                  <SortableHead column="next_run" defaultDir="asc" {...list.head}>{t("jobs.columns.nextRun")}</SortableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.visible.map((m) => {
                  const state = jobState(m);
                  return (
                    <TableRow key={m.id} className="cursor-pointer" data-testid="job-row" onClick={() => onOpen(m)}>
                      <TableCell className="max-w-72">
                        <button type="button" className="block truncate text-left font-medium hover:underline" onClick={() => onOpen(m)}>
                          {m.name}
                        </button>
                        {m.tags.length > 0 && (
                          <div className="mt-0.5 flex flex-wrap gap-1">
                            {m.tags.map((tag) => (
                              <Badge key={tag} variant="outline">
                                {tag}
                              </Badge>
                            ))}
                          </div>
                        )}
                      </TableCell>
                      <TableCell label={t("jobs.columns.schedule")} className="font-mono text-xs">
                        {scheduleText(m, (v) => t("jobs.every", { interval: v }))}
                      </TableCell>
                      <TableCell className="max-md:w-auto">
                        <Badge variant={jobStateBadgeVariant(state)}>{t(`jobs.state.${state}`)}</Badge>
                      </TableCell>
                      <TableCell label={t("jobs.columns.lastDuration")} className="text-right font-mono tabular-nums">
                        {formatDuration(m.state.last_duration_ms, locale)}
                      </TableCell>
                      <TableCell label={t("jobs.columns.lastRun")} className="text-xs text-muted-foreground">
                        {m.state.last_ping_at ? formatAgo(m.state.last_ping_at, locale) : t("jobs.never")}
                      </TableCell>
                      <TableCell label={t("jobs.columns.nextRun")} className="text-xs text-muted-foreground">
                        {m.enabled ? formatAgo(m.state.expected_at, locale) : t("jobs.state.paused")}
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
    </div>
  );
}
