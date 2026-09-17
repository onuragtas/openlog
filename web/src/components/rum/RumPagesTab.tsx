// Routes of the application with their page views and load time percentiles (rum.md §7). The routes are the
// normalized ones (§4): the SDK's own route when a framework router knows it, otherwise the URL put through
// the same segment rules as an APM transaction name, so a browser route and a backend transaction of the
// same URL look alike.
import { useQuery } from "@tanstack/react-query";
import { FileText } from "lucide-react";
import { useTranslation } from "react-i18next";
import { rumPagesQuery, type RumPageSort } from "@/api/rum";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatNumber } from "@/lib/format";
import { formatMs, formatVital } from "@/lib/rum";
import type { RangeSpec } from "@/lib/time";

const SORTS: readonly RumPageSort[] = ["views", "slowest", "avg"];

export function RumPagesTab({
  range,
  app,
  environment,
  sort,
  onSort,
}: {
  range: RangeSpec;
  app: string;
  environment?: string;
  sort: RumPageSort;
  onSort: (sort: RumPageSort) => void;
}) {
  const { t, i18n } = useTranslation();
  const pages = useQuery(rumPagesQuery(range, app, sort, environment));

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center gap-2">
        <Label htmlFor="rum-pages-sort" className="text-xs text-muted-foreground">
          {t("rum.pages.sortLabel")}
        </Label>
        <NativeSelect id="rum-pages-sort" className="w-48" value={sort} onChange={(e) => onSort(e.target.value as RumPageSort)}>
          {SORTS.map((s) => (
            <option key={s} value={s}>
              {t(`rum.pages.sort.${s}`)}
            </option>
          ))}
        </NativeSelect>
      </div>

      {pages.isPending ? (
        <LoadingState />
      ) : pages.isError ? (
        <ErrorState error={pages.error} onRetry={() => void pages.refetch()} />
      ) : pages.data.length === 0 ? (
        <EmptyState icon={<FileText className="size-5" aria-hidden="true" />}>{t("rum.pages.empty")}</EmptyState>
      ) : (
        <Table mobile="stack" data-testid="rum-pages">
          <TableHeader>
            <TableRow>
              <TableHead>{t("rum.pages.columns.route")}</TableHead>
              <TableHead className="text-right">{t("rum.pages.columns.views")}</TableHead>
              <TableHead className="hidden md:table-cell text-right">{t("rum.pages.columns.avg")}</TableHead>
              <TableHead className="text-right">{t("rum.pages.columns.p75")}</TableHead>
              <TableHead className="hidden lg:table-cell text-right">{t("rum.pages.columns.p95")}</TableHead>
              <TableHead className="hidden lg:table-cell text-right">{t("rum.pages.columns.ttfb")}</TableHead>
              <TableHead className="hidden md:table-cell text-right">{t("rum.pages.columns.lcp")}</TableHead>
              <TableHead className="text-right">{t("rum.pages.columns.errors")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {pages.data.map((p) => (
              <TableRow key={p.route} data-testid="rum-page-row">
                <TableCell className="font-mono text-xs break-all">{p.route}</TableCell>
                <TableCell label={t("rum.pages.columns.views")} className="text-right tabular-nums">
                  {formatNumber(p.views, i18n.language)}
                </TableCell>
                <TableCell label={t("rum.pages.columns.avg")} className="hidden md:table-cell text-right tabular-nums">
                  {formatMs(p.avg_ms, i18n.language)}
                </TableCell>
                <TableCell label={t("rum.pages.columns.p75")} className="text-right tabular-nums">
                  {formatMs(p.p75_ms, i18n.language)}
                </TableCell>
                <TableCell label={t("rum.pages.columns.p95")} className="hidden lg:table-cell text-right tabular-nums">
                  {formatMs(p.p95_ms, i18n.language)}
                </TableCell>
                <TableCell label={t("rum.pages.columns.ttfb")} className="hidden lg:table-cell text-right tabular-nums">
                  {formatMs(p.ttfb_avg_ms, i18n.language)}
                </TableCell>
                <TableCell label={t("rum.pages.columns.lcp")} className="hidden md:table-cell text-right tabular-nums">
                  {formatVital(p.lcp_p75, "lcp", i18n.language)}
                </TableCell>
                <TableCell label={t("rum.pages.columns.errors")} className="text-right tabular-nums">
                  {formatNumber(p.errors, i18n.language)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}
