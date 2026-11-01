import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { X } from "lucide-react";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { apmDeploymentsQuery, apmTransactionQuery, apmTransactionsQuery, type ApmTransaction, type TransactionSort } from "@/api/apm";
import { deploymentMarkers } from "@/lib/deployments";
import { ApdexBadge, LatencyHistogram, RedTiles } from "@/components/apm/Charts";
import { ChartCard } from "@/components/apm/OverviewTab";
import { ListPager } from "@/components/ListPager";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { SortableHead, Table, TableBody, TableCell, TableHead, TableHeader, TableRow, type SortDir } from "@/components/ui/table";
import { formatMs, formatRate, formatRpm, latencySeries, metricPoints, type ServiceScope } from "@/lib/apm";
import { formatDateTime } from "@/lib/format";
import { useListState, type ListControls } from "@/lib/list";
import { parseTimeParam, type RangeSpec } from "@/lib/time";

export function TransactionTable({ transactions, apdexTMs, onOpen, selected, head }: { transactions: ApmTransaction[]; apdexTMs: number; onOpen: (name: string) => void; selected?: string; head?: ListControls<TransactionSort>["head"] }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  // The overview shows the top few and nothing else, so only a list that was handed a sort has headings to click.
  const heading = (column: TransactionSort, label: string, className?: string, defaultDir?: SortDir) =>
    head ? (
      <SortableHead column={column} className={className} defaultDir={defaultDir} {...head}>
        {label}
      </SortableHead>
    ) : (
      <TableHead className={className}>{label}</TableHead>
    );
  return (
    <Table data-testid="transactions-table">
      <TableHeader>
        <TableRow>
          {heading("name", t("apm.transactions.name"), undefined, "asc")}
          {heading("throughput", t("apm.metrics.throughput"), "text-right")}
          {heading("avg", t("apm.metrics.avg"), "text-right")}
          {heading("slowest", t("apm.metrics.p95"), "text-right")}
          {heading("error_rate", t("apm.metrics.errorRate"), "text-right")}
          {heading("apdex", t("apm.metrics.apdex"), "text-right")}
          {heading("time", t("apm.metrics.timeShare"), "w-40")}
        </TableRow>
      </TableHeader>
      <TableBody>
        {transactions.map((tx) => (
          <TableRow key={`${tx.transaction_type}|${tx.transaction_name}`} data-state={tx.transaction_name === selected ? "selected" : undefined}>
            <TableCell className="max-w-[28rem]">
              <button
                type="button"
                className="block max-w-full truncate text-left font-medium hover:underline max-md:max-w-[50vw]"
                title={tx.transaction_name}
                aria-label={t("apm.transactions.open", { name: tx.transaction_name })}
                aria-pressed={tx.transaction_name === selected}
                onClick={() => onOpen(tx.transaction_name)}
              >
                {tx.transaction_name}
              </button>
              <span className="text-xs text-muted-foreground">{tx.transaction_type}</span>
            </TableCell>
            <TableCell className="text-right font-mono tabular-nums">{formatRpm(tx.throughput, locale)}</TableCell>
            <TableCell className="text-right font-mono tabular-nums">{formatMs(tx.avg_ms, locale)}</TableCell>
            <TableCell className="text-right font-mono tabular-nums">{formatMs(tx.p95_ms, locale)}</TableCell>
            <TableCell className="text-right font-mono tabular-nums">{formatRate(tx.error_rate, locale)}</TableCell>
            <TableCell className="text-right">
              <ApdexBadge apdex={tx.apdex} tMs={apdexTMs} />
            </TableCell>
            <TableCell>
              <div className="flex items-center gap-2" title={formatRate(tx.time_share, locale)}>
                <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-muted" aria-hidden="true">
                  <div className="h-full bg-primary" style={{ width: `${Math.round(tx.time_share * 100)}%` }} />
                </div>
                <span className="w-12 text-right font-mono text-xs tabular-nums">{formatRate(tx.time_share, locale)}</span>
              </div>
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

export interface TransactionsTabProps {
  scope: ServiceScope;
  range: RangeSpec;
  sort: TransactionSort;
  selected?: string;
  onSort: (sort: TransactionSort) => void;
  onSelect: (name: string | undefined) => void;
  onShowTraces: (name: string) => void;
}

export function TransactionsTab({ scope, range, sort, selected, onSort, onSelect, onShowTraces }: TransactionsTabProps) {
  const { t } = useTranslation();
  // The URL holds the column, so a shared link opens on it, and the rest of the sort and the page stay on this screen.
  const list = useListState<TransactionSort>(sort, sort === "name" ? "asc" : "desc", 50, "TransactionsTab");
  const q = useQuery(apmTransactionsQuery(scope, range, list.state));
  const { toFirstPage } = list;
  // Another range is another list, so page five of it would usually show nothing.
  useEffect(toFirstPage, [range.range, range.from, range.to, toFirstPage]);
  const head = {
    ...list.head,
    onSort: (column: TransactionSort, dir: SortDir) => {
      list.head.onSort(column, dir);
      onSort(column);
    },
  };
  return (
    <div className="flex flex-col gap-4">
      {selected && <TransactionDetail scope={scope} range={range} name={selected} onClose={() => onSelect(undefined)} onShowTraces={onShowTraces} />}
      <Card>
        <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2">
          <CardTitle>
            <h2>{t("apm.service.tabs.transactions")}</h2>
          </CardTitle>
        </CardHeader>
        <CardContent className="px-0">
          {q.isPending ? (
            <LoadingState />
          ) : q.isError ? (
            <ErrorState error={q.error} onRetry={() => void q.refetch()} />
          ) : q.data.transactions.length === 0 && list.state.offset === 0 ? (
            <EmptyState>{t("apm.transactions.empty")}</EmptyState>
          ) : (
            <>
              <TransactionTable transactions={q.data.transactions} apdexTMs={q.data.apdex_t_ms} onOpen={onSelect} selected={selected} head={head} />
              <ListPager list={list} count={q.data.transactions.length} hasMore={q.data.has_more ?? false} />
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function TransactionDetail({ scope, range, name, onClose, onShowTraces }: { scope: ServiceScope; range: RangeSpec; name: string; onClose: () => void; onShowTraces: (name: string) => void }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const q = useQuery(apmTransactionQuery(scope, range, name));
  const deployments = useQuery(apmDeploymentsQuery(scope, range));
  const points = q.data?.series ?? [];
  const markers = deploymentMarkers(deployments.data?.deployments, q.data?.from, q.data?.to);
  return (
    <Card data-testid="transaction-detail" className="border-primary/40">
      <CardHeader className="flex flex-row items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="text-xs text-muted-foreground">{t("apm.transactions.name")}</p>
          <CardTitle>
            <h2 className="break-all">{name}</h2>
          </CardTitle>
        </div>
        <div className="flex shrink-0 flex-wrap items-center justify-end gap-2">
          {q.data && (
            <Button asChild variant="outline" size="sm">
              <Link to="/logs" search={{ txn: name, txnsvc: scope.service, from: String(q.data.from), to: String(q.data.to) }}>
                {t("apm.transactions.relatedLogs")}
              </Link>
            </Button>
          )}
          <Button variant="outline" size="sm" onClick={() => onShowTraces(name)}>
            {t("apm.transactions.traces")}
          </Button>
          <Button variant="ghost" size="icon" aria-label={t("apm.transactions.close")} onClick={onClose}>
            <X className="size-4" aria-hidden="true" />
          </Button>
        </div>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {q.isPending ? (
          <LoadingState />
        ) : q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : (
          <>
            <RedTiles red={q.data.totals} apdexTMs={q.data.apdex_t_ms} />
            <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
              <ChartCard title={t("apm.transactions.histogram")}>
                <LatencyHistogram bins={q.data.histogram} />
              </ChartCard>
              <ChartCard title={t("apm.metrics.latency")}>
                <TimeSeriesChart title={t("apm.metrics.latency")} unit="ms" from={q.data.from} to={q.data.to} height={160} series={latencySeries(points)} markers={markers} />
              </ChartCard>
              <ChartCard title={t("apm.metrics.throughput")}>
                <TimeSeriesChart title={t("apm.metrics.throughput")} unit="number" from={q.data.from} to={q.data.to} height={140} series={[{ label: t("apm.metrics.throughput"), points: metricPoints(points, "throughput") }]} markers={markers} />
              </ChartCard>
              <ChartCard title={t("apm.transactions.slowest")}>
                {q.data.slowest.length === 0 ? (
                  <p className="text-sm text-muted-foreground">{t("apm.transactions.noTraces")}</p>
                ) : (
                  <ul className="flex flex-col divide-y text-sm" data-testid="slowest-traces">
                    {q.data.slowest.map((s) => (
                      <li key={s.span_id} className="flex flex-wrap items-center justify-between gap-x-3 gap-y-0.5 py-1.5">
                        <Link to="/traces/$traceId" params={{ traceId: s.trace_id }} search={{ span: s.span_id }} className="font-mono text-xs text-primary hover:underline" aria-label={t("apm.traces.openTrace", { id: s.trace_id })}>
                          {s.trace_id.slice(0, 16)}…
                        </Link>
                        <span className="text-xs text-muted-foreground">{formatDateTime(parseTimeParam(s.timestamp) ?? 0, locale)}</span>
                        {s.is_error && <Badge variant="destructive">{s.http_status_code || t("apm.traces.error")}</Badge>}
                        <span className="ml-auto font-mono tabular-nums">{formatMs(s.duration_ms, locale)}</span>
                      </li>
                    ))}
                  </ul>
                )}
              </ChartCard>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}
