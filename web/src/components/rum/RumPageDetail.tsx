// One route of the application: its load time, and the Core Web Vitals of that route alone.
//
// The vitals of the whole application answer "is this site fast"; a developer's next question is always
// "which page is slow, and which vital is it" — and that is a different query (GET /rum/vitals?route=),
// because a route's p75 is not derivable from the application's. The panel asks it per route rather than
// for every row: the table lists hundreds, and five vitals each would be hundreds of queries for rows
// nobody reads past the top.
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { rumVitalsQuery, type RumPage } from "@/api/rum";
import { VitalCard } from "@/components/rum/VitalCard";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { formatNumber } from "@/lib/format";
import { formatMs, formatVital, sortVitals } from "@/lib/rum";
import type { RangeSpec } from "@/lib/time";

function Figure({ label, value, muted }: { label: string; value: string; muted?: boolean }) {
  return (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className={muted ? "tabular-nums text-muted-foreground" : "tabular-nums font-medium"}>{value}</dd>
    </div>
  );
}

export interface RumPageDetailProps {
  page: RumPage | undefined;
  onClose: () => void;
  range: RangeSpec;
  app: string;
  environment?: string;
}

export function RumPageDetail({ page, onClose, range, app, environment }: RumPageDetailProps) {
  const { t } = useTranslation();
  return (
    <Sheet open={!!page} onOpenChange={(o) => !o && onClose()}>
      <SheetContent side="right" title={t("rum.page.title")} closeLabel={t("common.close")} className="w-full max-w-full sm:w-[42rem] sm:max-w-[92vw]">
        {page && <Body key={page.route} page={page} range={range} app={app} environment={environment} />}
      </SheetContent>
    </Sheet>
  );
}

function Body({ page, range, app, environment }: { page: RumPage; range: RangeSpec; app: string; environment?: string }) {
  const { t, i18n } = useTranslation();
  const vitals = useQuery(rumVitalsQuery(range, app, page.route, environment));

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-4">
      <p className="font-mono text-sm break-all">{page.route}</p>

      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-medium">{t("rum.page.load")}</h3>
        <dl className="grid grid-cols-3 gap-3 rounded-xl border bg-card p-3 sm:grid-cols-6">
          <Figure label={t("rum.pages.columns.views")} value={formatNumber(page.views, i18n.language)} />
          <Figure label={t("rum.pages.columns.avg")} value={formatMs(page.avg_ms, i18n.language)} />
          <Figure label={t("rum.vitals.percentiles.p50")} value={formatMs(page.p50_ms, i18n.language)} />
          <Figure label={t("rum.vitals.percentiles.p75")} value={formatMs(page.p75_ms, i18n.language)} />
          <Figure label={t("rum.vitals.percentiles.p95")} value={formatMs(page.p95_ms, i18n.language)} />
          {/* The worst single load of the range: a p95 of 1.2 s next to a max of 40 s is a different page
              from one whose max is 1.4 s, and only the second one is fast for everybody. */}
          <Figure label={t("rum.page.max")} value={formatMs(page.max_ms, i18n.language)} />
        </dl>
        <dl className="grid grid-cols-3 gap-3 rounded-xl border bg-card p-3">
          <Figure label={t("rum.pages.columns.ttfb")} value={formatMs(page.ttfb_avg_ms, i18n.language)} muted />
          <Figure label={t("rum.pages.columns.lcp")} value={formatVital(page.lcp_p75, "lcp", i18n.language)} muted />
          <Figure label={t("rum.pages.columns.errors")} value={formatNumber(page.errors, i18n.language)} muted />
        </dl>
      </section>

      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-medium">{t("rum.page.vitals")}</h3>
        {vitals.isPending ? (
          <LoadingState />
        ) : vitals.isError ? (
          <ErrorState error={vitals.error} onRetry={() => void vitals.refetch()} />
        ) : (
          <div className="grid gap-3 sm:grid-cols-2" data-testid="rum-page-vitals">
            {sortVitals(vitals.data.vitals).map((v) => (
              <VitalCard key={v.name} vital={v} />
            ))}
          </div>
        )}
      </section>

      {/* Browser errors live in the APM error inbox of the same application rather than in a second inbox
          here (rum.md §7), so this points there instead of listing them again. */}
      {page.errors > 0 && (
        <Link to="/apm/errors" search={(prev) => ({ ...(prev as Record<string, unknown>), env: environment }) as never} className="text-sm text-primary hover:underline">
          {t("rum.page.errorsLink", { n: Math.round(page.errors) })}
        </Link>
      )}
    </div>
  );
}
