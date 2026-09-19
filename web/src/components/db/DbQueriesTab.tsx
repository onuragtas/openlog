import { useQuery } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { useId } from "react";
import { useTranslation } from "react-i18next";
import { dbQueriesQuery, type DbQuerySort } from "@/api/db";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatNumber, formatValue } from "@/lib/format";
import type { RangeSpec } from "@/lib/time";

const DB_SORTS: DbQuerySort[] = ["time", "calls", "avg", "rows", "errors", "reads"];

/** The statements of an instance, heaviest first; the share bar makes "where does the time go" readable at once. */
export function DbQueriesTab({
  instance,
  range,
  sort,
  q,
  onSort,
  onSearch,
  onOpen,
}: {
  instance: string;
  range: RangeSpec;
  sort: DbQuerySort;
  q: string;
  onSort: (s: DbQuerySort) => void;
  onSearch: (q: string) => void;
  onOpen: (fingerprint: string) => void;
}) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const sortId = useId();
  const searchId = useId();
  const res = useQuery(dbQueriesQuery({ instance, range, sort, q }));
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
        <div className="flex items-center gap-2">
          <Label htmlFor={sortId}>{t("db.queries.sort")}</Label>
          <NativeSelect id={sortId} value={sort} onChange={(e) => onSort(e.target.value as DbQuerySort)}>
            {DB_SORTS.map((s) => (
              <option key={s} value={s}>
                {t(`db.queries.sorts.${s}`)}
              </option>
            ))}
          </NativeSelect>
        </div>
      </div>
      {res.isPending ? (
        <LoadingState />
      ) : res.isError ? (
        <ErrorState error={res.error} onRetry={() => void res.refetch()} />
      ) : res.data.queries.length === 0 ? (
        <EmptyState>{t("db.queries.empty")}</EmptyState>
      ) : (
        <div className="rounded-xl border bg-card" data-testid="db-queries">
          <Table mobile="stack">
            <TableHeader>
              <TableRow>
                <TableHead>{t("db.columns.statement")}</TableHead>
                <TableHead className="text-right">{t("db.columns.share")}</TableHead>
                <TableHead className="text-right">{t("db.columns.throughput")}</TableHead>
                <TableHead className="text-right">{t("db.columns.avg")}</TableHead>
                <TableHead className="text-right">{t("db.columns.rowsPerCall")}</TableHead>
                <TableHead className="text-right">{t("db.columns.errors")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {res.data.queries.map((d) => (
                <TableRow key={d.fingerprint} data-testid="db-query">
                  <TableCell className="max-w-xl">
                    <button type="button" className="line-clamp-3 text-left font-mono text-xs break-all text-primary hover:underline" onClick={() => onOpen(d.fingerprint)}>
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
                  <TableCell label={t("db.columns.errors")} className="text-right tabular-nums">
                    {formatNumber(d.errors, locale)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
}
