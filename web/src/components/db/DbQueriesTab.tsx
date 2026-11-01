import { useQuery } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { useEffect, useId } from "react";
import { useTranslation } from "react-i18next";
import { dbQueriesQuery, type DbQuerySort } from "@/api/db";
import { ListPager } from "@/components/ListPager";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SortableHead, Table, TableBody, TableCell, TableHeader, TableRow } from "@/components/ui/table";
import { formatNumber, formatValue } from "@/lib/format";
import { useListState } from "@/lib/list";
import type { RangeSpec } from "@/lib/time";

/** The statements of an instance, heaviest first; the share bar makes "where does the time go" readable at once. */
export function DbQueriesTab({
  instance,
  range,
  initialSort = "time",
  q,
  onSearch,
  onOpen,
}: {
  instance: string;
  range: RangeSpec;
  /** A link can still open the list on a sort: `?sort=reads` from before the headings did this. */
  initialSort?: DbQuerySort;
  q: string;
  onSearch: (q: string) => void;
  onOpen: (fingerprint: string) => void;
}) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const searchId = useId();
  const list = useListState<DbQuerySort>(initialSort, initialSort === "statement" ? "asc" : "desc", 50, "DbQueriesTab");
  const res = useQuery(dbQueriesQuery({ instance, range, list: list.state, q }));
  const { toFirstPage } = list;
  // A new search or window changes what the pages hold, so staying on page five of it would usually show nothing.
  useEffect(toFirstPage, [q, instance, range.range, range.from, range.to, toFirstPage]);
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-end gap-3">
        <div className="relative w-full sm:w-80">
          <Label htmlFor={searchId} className="sr-only">
            {t("db.queries.search")}
          </Label>
          <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" aria-hidden="true" />
          <Input id={searchId} type="search" className="pl-8" placeholder={t("db.queries.search")} value={q} onChange={(e) => onSearch(e.target.value)} />
        </div>
      </div>
      {res.isPending ? (
        <LoadingState />
      ) : res.isError ? (
        <ErrorState error={res.error} onRetry={() => void res.refetch()} />
      ) : res.data.queries.length === 0 && list.state.offset === 0 ? (
        <EmptyState>{t("db.queries.empty")}</EmptyState>
      ) : (
        <div className="rounded-xl border bg-card" data-testid="db-queries">
          {/* It scrolls on a phone rather than stacking into cards: the headings are the only way to sort this list now, and cards hide them. */}
          <Table>
            <TableHeader>
              <TableRow>
                <SortableHead column="statement" defaultDir="asc" {...list.head}>{t("db.columns.statement")}</SortableHead>
                <SortableHead column="time" className="text-right" {...list.head}>{t("db.columns.share")}</SortableHead>
                <SortableHead column="calls" className="text-right" {...list.head}>{t("db.columns.throughput")}</SortableHead>
                <SortableHead column="avg" className="text-right" {...list.head}>{t("db.columns.avg")}</SortableHead>
                <SortableHead column="rows_per_call" className="text-right" {...list.head}>{t("db.columns.rowsPerCall")}</SortableHead>
                <SortableHead column="rows" className="text-right" {...list.head}>{t("db.columns.rows")}</SortableHead>
                <SortableHead column="reads" className="text-right" {...list.head}>{t("db.columns.reads")}</SortableHead>
                <SortableHead column="errors" className="text-right" {...list.head}>{t("db.columns.errors")}</SortableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {res.data.queries.map((d) => (
                <TableRow key={d.fingerprint} data-testid="db-query">
                  <TableCell className="max-w-xl">
                    <button type="button" className="line-clamp-3 text-left font-mono text-xs break-all text-primary hover:underline max-md:block max-md:max-w-[50vw] max-md:truncate" onClick={() => onOpen(d.fingerprint)}>
                      {d.text}
                    </button>
                    {d.db_names.length > 0 && <span className="mt-1 block text-xs text-muted-foreground">{d.db_names.join(", ")}</span>}
                  </TableCell>
                  <TableCell label={t("db.columns.share")} className="min-w-28 text-right">
                    <span className="tabular-nums">{formatNumber(d.time_share * 100, locale)} %</span>
                    <span className="mt-1 block h-1 overflow-hidden rounded-full bg-muted" aria-hidden="true">
                      <span className="block h-full bg-primary" style={{ width: `${Math.min(d.time_share, 1) * 100}%` }} />
                    </span>
                  </TableCell>
                  <TableCell label={t("db.columns.throughput")} className="text-right tabular-nums">
                    {t("db.perSecond", { value: formatNumber(d.throughput, locale) })}
                  </TableCell>
                  <TableCell label={t("db.columns.avg")} className="text-right tabular-nums">
                    {d.avg_ms === null ? "–" : formatValue(d.avg_ms, "ms", locale)}
                  </TableCell>
                  <TableCell label={t("db.columns.rowsPerCall")} className="text-right tabular-nums">
                    {d.rows_per_call === null ? "–" : formatNumber(d.rows_per_call, locale)}
                  </TableCell>
                  <TableCell label={t("db.columns.rows")} className="text-right tabular-nums">
                    {formatNumber(d.rows, locale)}
                  </TableCell>
                  <TableCell label={t("db.columns.reads")} className="text-right tabular-nums">
                    {formatNumber(d.blocks_read, locale)}
                  </TableCell>
                  <TableCell label={t("db.columns.errors")} className="text-right tabular-nums">
                    {formatNumber(d.errors, locale)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          <ListPager list={list} count={res.data.queries.length} hasMore={res.data.has_more ?? false} />
        </div>
      )}
    </div>
  );
}
