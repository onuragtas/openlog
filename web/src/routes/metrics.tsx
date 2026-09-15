import { useQueries } from "@tanstack/react-query";
import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { metricExplorerSeriesQuery } from "@/api/explorer";
import type { MetricSeries } from "@/api/types";
import { PageHeader } from "@/components/AppShell";
import { FormulaCard } from "@/components/metrics-explorer/FormulaCard";
import { MetricList } from "@/components/metrics-explorer/MetricList";
import { QueryCard } from "@/components/metrics-explorer/QueryCard";
import { CopyLinkButton } from "@/components/querybuilder/CopyLinkButton";
import { SavedViewsMenu } from "@/components/querybuilder/SavedViewsMenu";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { useIsBelowLg } from "@/lib/media";
import {
  decodeMetricQueries,
  encodeMetricQueries,
  metricsSearchFromViewState,
  metricsViewState,
  newMetricQuery,
  nextQueryId,
  type MetricQueryState,
} from "@/lib/metrics-explorer";
import type { RangeSpec } from "@/lib/time";

const route = getRouteApi("/app/metrics");

/** Metrics Explorer: every OTLP metric with filters, aggregation, group-by, several queries and a formula; state in the URL. */
export function MetricsPage() {
  const { t } = useTranslation();
  const search = route.useSearch();
  const navigate = useNavigate({ from: "/metrics" });
  const narrow = useIsBelowLg();
  const range = useMemo<RangeSpec>(() => ({ range: search.range, from: search.from, to: search.to }), [search.range, search.from, search.to]);
  const queries = useMemo(() => decodeMetricQueries(search.mq), [search.mq]);
  const [activeId, setActiveId] = useState("A");
  const [listOpen, setListOpen] = useState(false);
  const listRef = useRef<HTMLDivElement>(null);
  const target = queries.find((q) => q.id === activeId) ?? queries[0]!;

  const setQueries = (qs: MetricQueryState[]) => void navigate({ search: (prev) => ({ ...prev, mq: encodeMetricQueries(qs) }), replace: true });
  const setQuery = (q: MetricQueryState) => setQueries(queries.map((x) => (x.id === q.id ? q : x)));

  const results = useQueries({
    queries: queries.map((q) => metricExplorerSeriesQuery({ metric: q.metric, range, filter: q, aggregation: q.aggregation, groupBy: q.groupBy })),
  });
  const data: Record<string, MetricSeries[] | undefined> = {};
  queries.forEach((q, i) => (data[q.id] = results[i]?.data?.series));
  const loadedWindow = results.find((r) => r.data)?.data;
  const ids = queries.filter((q) => q.metric).map((q) => q.id);
  const nextId = nextQueryId(queries);

  const chooseMetric = (id: string) => {
    setActiveId(id);
    if (narrow) setListOpen(true);
    else listRef.current?.querySelector("input")?.focus();
  };

  const list = (
    <MetricList
      range={range}
      selected={target.metric}
      target={target.id}
      onSelect={(name) => {
        // Attribute keys differ per metric: a new metric starts without filters, aggregation and group-by.
        setQuery(newMetricQuery(target.id, name));
        setListOpen(false);
      }}
      className={narrow ? "border-0 p-4" : "max-h-[calc(100dvh-10rem)] lg:sticky lg:top-0"}
    />
  );

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <PageHeader
        title={t("metricsExplorer.title")}
        subtitle={t("metricsExplorer.subtitle")}
        actions={
          <>
            <SavedViewsMenu
              signal="metrics"
              getState={() => metricsViewState(search)}
              onApply={(v) => void navigate({ search: metricsSearchFromViewState(v.state) })}
            />
            <CopyLinkButton />
          </>
        }
      />
      <div className="grid min-w-0 gap-4 lg:grid-cols-[20rem_minmax(0,1fr)]">
        {narrow ? (
          <Sheet open={listOpen} onOpenChange={setListOpen}>
            <SheetContent side="bottom" title={t("metricsExplorer.list.title")} closeLabel={t("common.close")} className="h-[85dvh]">
              {list}
            </SheetContent>
          </Sheet>
        ) : (
          <div ref={listRef} className="min-w-0">
            {list}
          </div>
        )}
        <div className="flex min-w-0 flex-col gap-3">
          {queries.map((q, i) => (
            <QueryCard
              key={q.id}
              query={q}
              result={results[i]!}
              range={range}
              active={queries.length > 1 && q.id === target.id}
              onActivate={() => setActiveId(q.id)}
              onChooseMetric={() => chooseMetric(q.id)}
              onChange={setQuery}
              onRemove={queries.length > 1 ? () => setQueries(queries.filter((x) => x.id !== q.id)) : undefined}
            />
          ))}
          {nextId && (
            <Button
              type="button"
              variant="outline"
              className="self-start"
              onClick={() => {
                setQueries([...queries, newMetricQuery(nextId)]);
                chooseMetric(nextId);
              }}
            >
              <Plus aria-hidden="true" />
              {t("metricsExplorer.addQuery")}
            </Button>
          )}
          {ids.length > 0 && (
            <FormulaCard
              key={search.formula ?? ""}
              formula={search.formula ?? ""}
              onChange={(formula) => void navigate({ search: (prev) => ({ ...prev, formula: formula || undefined }), replace: true })}
              ids={ids}
              data={data}
              loading={results.some((r) => r.isFetching)}
              from={loadedWindow?.from}
              to={loadedWindow?.to}
            />
          )}
        </div>
      </div>
    </div>
  );
}
