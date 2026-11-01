// The statements of every service, as the applications measured them (apm.md §7.1). They need no connection to the database server,
// which is the point. They are shown beside the server's view and not merged into it, because the two durations measure different
// things: this one includes the network and waiting for a pooled connection.
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronDown, ChevronRight } from "lucide-react";
import { Fragment, useEffect, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { appDbPlansQuery, appDbQueriesQuery, type AppDbQuery, type AppDbSort } from "@/api/db";
import { ServerViewLink } from "@/components/apm/ServerViewLink";
import { ListPager } from "@/components/ListPager";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SortableHead, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatMs, formatRate, formatRpm } from "@/lib/apm";
import { formatDateTime } from "@/lib/format";
import { useListState } from "@/lib/list";
import type { RangeSpec } from "@/lib/time";

function Plans({ range, query }: { range: RangeSpec; query: AppDbQuery }) {
  const { t, i18n } = useTranslation();
  const q = useQuery(appDbPlansQuery(range, query.db_system, query.statement));
  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (q.data.length === 0) return <p className="text-xs text-muted-foreground">{t("db.app.noPlan")}</p>;
  return (
    <div className="flex flex-col gap-3" data-testid="app-db-plans">
      {q.data.map((p) => (
        <div key={p.trace_id + p.captured_at} className="flex flex-col gap-1">
          <p className="text-xs text-muted-foreground">
            {t("db.app.plan")} · {p.service} · {formatDateTime(Date.parse(p.captured_at), i18n.language)}
          </p>
          <pre className="max-h-96 overflow-auto rounded-lg bg-muted p-3 font-mono text-xs">{p.plan}</pre>
          {p.generic && <p className="text-xs text-muted-foreground">{t("db.app.generic")}</p>}
        </div>
      ))}
    </div>
  );
}

export function AppDbQueries({ range, q, onSearch }: { range: RangeSpec; q: string; onSearch: (q: string) => void }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const searchId = useId();
  const list = useListState<AppDbSort>("time", "desc", 50, "AppDbQueries");
  const [open, setOpen] = useState<string | null>(null);
  const queries = useQuery(appDbQueriesQuery(range, list.state, q));
  const { toFirstPage } = list;
  // A search changes what the pages hold, so staying on page five of it would usually show nothing.
  useEffect(toFirstPage, [q, toFirstPage]);
  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs text-muted-foreground">{t("db.app.hint")}</p>
      <div className="flex flex-wrap items-end gap-3">
        <div className="flex flex-col gap-1">
          <Label htmlFor={searchId}>{t("db.app.search")}</Label>
          <Input id={searchId} className="w-64" defaultValue={q} onChange={(e) => onSearch(e.target.value)} />
        </div>
      </div>
      {queries.isPending ? (
        <LoadingState />
      ) : queries.isError ? (
        <ErrorState error={queries.error} onRetry={() => void queries.refetch()} />
      ) : queries.data.queries.length === 0 && list.state.offset === 0 ? (
        <EmptyState>{t("db.app.empty")}</EmptyState>
      ) : (
        <div className="rounded-xl border bg-card">
          <Table data-testid="app-db-queries">
            <TableHeader>
              <TableRow>
                <SortableHead column="statement" defaultDir="asc" {...list.head}>{t("apm.databases.statement")}</SortableHead>
                <TableHead>{t("db.app.services")}</TableHead>
                <SortableHead column="database" defaultDir="asc" {...list.head}>{t("apm.databases.system")}</SortableHead>
                <SortableHead column="calls" className="text-right" {...list.head}>{t("apm.metrics.throughput")}</SortableHead>
                <SortableHead column="slowest" className="text-right" {...list.head}>{t("apm.metrics.avg")}</SortableHead>
                <TableHead className="text-right">{t("apm.metrics.p95")}</TableHead>
                <SortableHead column="errors" className="text-right" {...list.head}>{t("apm.metrics.errorRate")}</SortableHead>
                <SortableHead column="time" className="text-right" {...list.head}>{t("apm.metrics.timeShare")}</SortableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {queries.data.queries.map((d) => {
                const key = `${d.db_system}|${d.db_name}|${d.statement}`;
                const expanded = open === key;
                return (
                  <Fragment key={key}>
                    <TableRow data-testid="app-db-query">
                      <TableCell className="max-w-[22rem]">
                        <button
                          type="button"
                          aria-expanded={expanded}
                          className="flex w-full items-center gap-1 text-left"
                          onClick={() => setOpen(expanded ? null : key)}
                        >
                          {expanded ? <ChevronDown className="size-3.5 shrink-0" aria-hidden="true" /> : <ChevronRight className="size-3.5 shrink-0" aria-hidden="true" />}
                          <code className="block truncate font-mono text-xs max-md:max-w-[50vw]" title={d.statement}>
                            {d.statement}
                          </code>
                        </button>
                      </TableCell>
                      <TableCell>
                        <div className="flex flex-wrap gap-x-2 gap-y-0.5 text-xs">
                          {(d.services ?? []).map((s) => (
                            <Link key={s} to="/apm/services/$service" params={{ service: s }} className="text-primary hover:underline">
                              {s}
                            </Link>
                          ))}
                        </div>
                      </TableCell>
                      <TableCell className="whitespace-nowrap">
                        <Badge variant="secondary">{d.db_name ? `${d.db_system}/${d.db_name}` : d.db_system}</Badge>
                      </TableCell>
                      <TableCell className="text-right font-mono whitespace-nowrap tabular-nums">{formatRpm(d.throughput, locale)}</TableCell>
                      <TableCell className="text-right font-mono whitespace-nowrap tabular-nums">{formatMs(d.avg_ms, locale)}</TableCell>
                      <TableCell className="text-right font-mono whitespace-nowrap tabular-nums">{formatMs(d.p95_ms, locale)}</TableCell>
                      <TableCell className="text-right font-mono whitespace-nowrap tabular-nums">{formatRate(d.error_rate, locale)}</TableCell>
                      <TableCell className="text-right font-mono whitespace-nowrap tabular-nums">{formatRate(d.time_share, locale)}</TableCell>
                    </TableRow>
                    {expanded && (
                      <TableRow>
                        {/* The flex is inside the cell: a cell that is itself flex stops being a table cell and loses its colSpan. */}
                        <TableCell colSpan={8} className="whitespace-normal">
                          <div className="flex max-w-[calc(100vw-20rem)] flex-col items-start gap-2 max-md:max-w-[85vw]">
                            <code className="block font-mono text-xs break-all whitespace-pre-wrap">{d.statement}</code>
                            <ServerViewLink dbSystem={d.db_system} statement={d.statement} range={range} />
                            <div className="w-full">
                              <Plans range={range} query={d} />
                            </div>
                          </div>
                        </TableCell>
                      </TableRow>
                    )}
                  </Fragment>
                );
              })}
            </TableBody>
          </Table>
          <ListPager list={list} count={queries.data.queries.length} hasMore={queries.data.has_more ?? false} />
        </div>
      )}
    </div>
  );
}
