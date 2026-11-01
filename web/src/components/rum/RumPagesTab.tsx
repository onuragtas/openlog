// Routes of the application with their page views and load time percentiles (rum.md §7). The routes are the
// normalized ones (§4): the SDK's own route when a framework router knows it, otherwise the URL put through
// the same segment rules as an APM transaction name, so a browser route and a backend transaction of the
// same URL look alike.
import { useQuery } from "@tanstack/react-query";
import { FileText } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { rumPagesQuery, type RumFacets, type RumPage, type RumPageSort } from "@/api/rum";
import { ListPager } from "@/components/ListPager";
import { RumFacetPicker } from "@/components/rum/RumFacetPicker";
import { RumPageDetail } from "@/components/rum/RumPageDetail";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { SortableHead, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatNumber } from "@/lib/format";
import { useListState } from "@/lib/list";
import { formatMs, formatVital } from "@/lib/rum";
import type { RangeSpec } from "@/lib/time";

export function RumPagesTab({
  range,
  app,
  environment,
  initialSort = "views",
  facets,
  onFacets,
}: {
  range: RangeSpec;
  app: string;
  environment?: string;
  /** A link can still open the list on a sort: `?sort=slowest` from before the headings did this. */
  initialSort?: RumPageSort;
  facets: RumFacets;
  onFacets: (facets: RumFacets) => void;
}) {
  const { t, i18n } = useTranslation();
  const list = useListState<RumPageSort>(initialSort);
  const pages = useQuery(rumPagesQuery(range, app, list.state, environment, facets));
  const { toFirstPage } = list;
  // A new filter or window changes what the pages hold, so staying on page five of it would usually show nothing.
  useEffect(toFirstPage, [app, environment, facets.device_type, facets.browser, range.range, range.from, range.to, toFirstPage]);
  // Which route's panel is open, by value: the row already holds every number the panel shows apart from
  // the vitals, so opening one costs a single query rather than six.
  const [open, setOpen] = useState<RumPage | null>(null);

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-end gap-3">
        <RumFacetPicker id="rum-pages" facets={facets} onChange={onFacets} />
      </div>

      {pages.isPending ? (
        <LoadingState />
      ) : pages.isError ? (
        <ErrorState error={pages.error} onRetry={() => void pages.refetch()} />
      ) : pages.data.pages.length === 0 && list.state.offset === 0 ? (
        <EmptyState icon={<FileText className="size-5" aria-hidden="true" />}>{t("rum.pages.empty")}</EmptyState>
      ) : (
        <div className="flex flex-col">
        {/* It scrolls on a phone rather than stacking into cards: the headings are the only way to sort this list now, and cards hide them. The
            percentiles, the LCP and the errors stay plain headings, because the server works them out after a page is cut or does not have them. */}
        <Table data-testid="rum-pages">
          <TableHeader>
            <TableRow>
              <SortableHead column="route" defaultDir="asc" {...list.head}>{t("rum.pages.columns.route")}</SortableHead>
              <SortableHead column="views" className="text-right" {...list.head}>{t("rum.pages.columns.views")}</SortableHead>
              <SortableHead column="slowest" className="hidden md:table-cell text-right" {...list.head}>{t("rum.pages.columns.totalTime")}</SortableHead>
              <SortableHead column="avg" className="hidden md:table-cell text-right" {...list.head}>{t("rum.pages.columns.avg")}</SortableHead>
              <TableHead className="text-right">{t("rum.pages.columns.p75")}</TableHead>
              <TableHead className="hidden lg:table-cell text-right">{t("rum.pages.columns.p95")}</TableHead>
              <SortableHead column="ttfb" className="hidden lg:table-cell text-right" {...list.head}>{t("rum.pages.columns.ttfb")}</SortableHead>
              <TableHead className="hidden md:table-cell text-right">{t("rum.pages.columns.lcp")}</TableHead>
              <TableHead className="text-right">{t("rum.pages.columns.errors")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {pages.data.pages.map((p) => (
              <TableRow key={p.route} data-testid="rum-page-row">
                <TableCell className="font-mono text-xs break-all">
                  <button type="button" className="text-left text-primary hover:underline max-md:block max-md:max-w-[50vw] max-md:truncate" onClick={() => setOpen(p)} aria-label={t("rum.page.open", { route: p.route })}>
                    {p.route}
                  </button>
                </TableCell>
                <TableCell label={t("rum.pages.columns.views")} className="text-right tabular-nums">
                  {formatNumber(p.views, i18n.language)}
                </TableCell>
                <TableCell label={t("rum.pages.columns.totalTime")} className="hidden md:table-cell text-right tabular-nums">
                  {formatMs(p.avg_ms === null ? null : p.avg_ms * p.views, i18n.language)}
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
        <ListPager list={list} count={pages.data.pages.length} hasMore={pages.data.has_more ?? false} />
        </div>
      )}
      <RumPageDetail page={open ?? undefined} onClose={() => setOpen(null)} range={range} app={app} environment={environment} facets={facets} />
    </div>
  );
}
