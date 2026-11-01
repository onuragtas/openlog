// The statements of every service, as the applications measured them (apm.md §7.1). They need no connection to the database server,
// which is the point. They are shown beside the server's view and not merged into it, because the two durations measure different
// things: this one includes the network and waiting for a pooled connection.
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronDown, ChevronRight } from "lucide-react";
import { Fragment, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { appDbPlansQuery, appDbQueriesQuery, type AppDbQuery, type AppDbSort } from "@/api/db";
import { ServerViewLink } from "@/components/apm/ServerViewLink";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatMs, formatRate, formatRpm } from "@/lib/apm";
import { formatDateTime } from "@/lib/format";
import type { RangeSpec } from "@/lib/time";

const SORTS: AppDbSort[] = ["time", "calls", "slowest", "errors"];

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
  const sortId = useId();
  const searchId = useId();
  const [sort, setSort] = useState<AppDbSort>("time");
  const [open, setOpen] = useState<string | null>(null);
  const queries = useQuery(appDbQueriesQuery(range, sort, q));
  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs text-muted-foreground">{t("db.app.hint")}</p>
      <div className="flex flex-wrap items-end gap-3">
        <div className="flex flex-col gap-1">
          <Label htmlFor={searchId}>{t("db.app.search")}</Label>
          <Input id={searchId} className="w-64" defaultValue={q} onChange={(e) => onSearch(e.target.value)} />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={sortId}>{t("apm.databases.sort")}</Label>
          <NativeSelect id={sortId} value={sort} onChange={(e) => setSort(e.target.value as AppDbSort)}>
            {SORTS.map((s) => (
              <option key={s} value={s}>
                {t(`apm.databases.sorts.${s}`)}
              </option>
            ))}
          </NativeSelect>
        </div>
      </div>
      {queries.isPending ? (
        <LoadingState />
      ) : queries.isError ? (
        <ErrorState error={queries.error} onRetry={() => void queries.refetch()} />
      ) : queries.data.length === 0 ? (
        <EmptyState>{t("db.app.empty")}</EmptyState>
      ) : (
        <div className="rounded-xl border bg-card">
          <Table data-testid="app-db-queries">
            <TableHeader>
              <TableRow>
                <TableHead>{t("apm.databases.statement")}</TableHead>
                <TableHead>{t("db.app.services")}</TableHead>
                <TableHead>{t("apm.databases.system")}</TableHead>
                <TableHead className="text-right">{t("apm.metrics.throughput")}</TableHead>
                <TableHead className="text-right">{t("apm.metrics.avg")}</TableHead>
                <TableHead className="text-right">{t("apm.metrics.p95")}</TableHead>
                <TableHead className="text-right">{t("apm.metrics.errorRate")}</TableHead>
                <TableHead className="text-right">{t("apm.metrics.timeShare")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {queries.data.map((d) => {
                const key = `${d.db_system}|${d.db_name}|${d.statement}`;
                const expanded = open === key;
                return (
                  <Fragment key={key}>
                    <TableRow data-testid="app-db-query">
                      <TableCell className="max-w-[32rem]">
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
                      <TableCell className="text-right font-mono tabular-nums">{formatRpm(d.throughput, locale)}</TableCell>
                      <TableCell className="text-right font-mono tabular-nums">{formatMs(d.avg_ms, locale)}</TableCell>
                      <TableCell className="text-right font-mono tabular-nums">{formatMs(d.p95_ms, locale)}</TableCell>
                      <TableCell className="text-right font-mono tabular-nums">{formatRate(d.error_rate, locale)}</TableCell>
                      <TableCell className="text-right font-mono tabular-nums">{formatRate(d.time_share, locale)}</TableCell>
                    </TableRow>
                    {expanded && (
                      <TableRow>
                        <TableCell colSpan={8} className="flex flex-col gap-2 whitespace-normal">
                          <code className="block font-mono text-xs break-all whitespace-pre-wrap">{d.statement}</code>
                          <ServerViewLink dbSystem={d.db_system} statement={d.statement} range={range} />
                          <Plans range={range} query={d} />
                        </TableCell>
                      </TableRow>
                    )}
                  </Fragment>
                );
              })}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
}
