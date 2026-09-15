import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { AlignJustify, ArrowDownWideNarrow, ArrowUpNarrowWide, BarChart3, Columns3, GitBranch, RotateCcw, Timer, WrapText } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { fieldKeysQuery, tracesExplorerQuery, type QueryFilter } from "@/api/explorer";
import { PageHeader } from "@/components/AppShell";
import { TopValuesPanel } from "@/components/explorer/TopValuesPanel";
import { CopyLinkButton } from "@/components/querybuilder/CopyLinkButton";
import { KeyPicker } from "@/components/querybuilder/KeyPicker";
import { QueryBuilder } from "@/components/querybuilder/QueryBuilder";
import { SavedViewsMenu } from "@/components/querybuilder/SavedViewsMenu";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { SpanCharts } from "@/components/traces-explorer/SpanCharts";
import { SpanDetailPanel } from "@/components/traces-explorer/SpanDetailPanel";
import { SpansTable } from "@/components/traces-explorer/SpansTable";
import { Button } from "@/components/ui/button";
import { storeColumns, storedColumns, storedPrefs, storePrefs, toggleColumn, valueFilter, type TablePrefs } from "@/lib/logs-explorer";
import { conditionCount, decodeFilterState, encodeFilterState, QB_LIMITS } from "@/lib/querybuilder";
import type { RangeSpec } from "@/lib/time";
import {
  DEFAULT_SPAN_COLUMNS,
  DEFAULT_SPAN_GROUP_BY,
  isDefaultSpanColumns,
  normalizeSpanColumns,
  requestSpanColumns,
  searchFromTracesViewState,
  SPAN_STORAGE,
  tracesViewStateFromSearch,
} from "@/lib/traces-explorer";
import { cn } from "@/lib/utils";
import type { TracesSearch } from "@/router";

const route = getRouteApi("/app/traces");

const TOP_VALUE_SUGGESTIONS = ["service.name", "name", "status_code", "http.status_code"] as const;

/** Traces Explorer (D-122): every span, filtered with the shared query builder. */
export function TracesPage() {
  const { t } = useTranslation();
  const search = route.useSearch();
  const navigate = useNavigate({ from: "/traces" });
  const range = useMemo<RangeSpec>(() => ({ range: search.range, from: search.from, to: search.to }), [search.range, search.from, search.to]);
  const filter = useMemo(() => decodeFilterState(search.f), [search.f]);
  const [fallbackColumns] = useState(() => storedColumns(SPAN_STORAGE.columns, DEFAULT_SPAN_COLUMNS));
  const columns = useMemo(() => (search.cols ? normalizeSpanColumns(search.cols) : (fallbackColumns ?? [...DEFAULT_SPAN_COLUMNS])), [search.cols, fallbackColumns]);
  const order = search.order ?? "desc";
  const byDuration = search.sort === "duration";
  const rootOnly = search.root === true;
  const groupBy = search.gb ?? DEFAULT_SPAN_GROUP_BY;
  const [prefs, setPrefs] = useState(() => storedPrefs(SPAN_STORAGE.prefs));
  const [selected, setSelected] = useState<number | null>(null);
  const [topOpen, setTopOpen] = useState(!!search.tv);

  const setSearch = (patch: Partial<TracesSearch & RangeSpec>, replace = false) => {
    setSelected(null);
    void navigate({ search: (prev) => ({ ...prev, ...patch }), replace });
  };
  const setFilter = (v: { filters: QueryFilter[]; groups: QueryFilter[][] }) => setSearch({ f: encodeFilterState({ ...v, q: "" }) });
  const setColumns = (cols: string[]) => {
    storeColumns(cols, SPAN_STORAGE.columns);
    void navigate({ search: (prev) => ({ ...prev, cols: isDefaultSpanColumns(cols) ? undefined : cols }), replace: true });
  };
  const updatePrefs = (p: TablePrefs) => {
    setPrefs(p);
    storePrefs(p, SPAN_STORAGE.prefs);
  };
  const canFilter = conditionCount(filter) < QB_LIMITS.conditions;
  const addFilter = (c: QueryFilter) => canFilter && setFilter({ ...filter, filters: [...filter.filters, c] });

  const query = useInfiniteQuery(tracesExplorerQuery({ range, filter, rootOnly, order, sort: byDuration ? "duration" : "timestamp", columns: requestSpanColumns(columns) }));
  const rows = useMemo(() => query.data?.pages.flatMap((p) => p.rows), [query.data]);
  const keys = useQuery(fieldKeysQuery({ signal: "traces", range, limit: 200 }));
  const keyTypes = useMemo(() => new Map((keys.data?.keys ?? []).map((k) => [k.key, k.type])), [keys.data]);
  const cellActions = { onFilter: (key: string, value: string, exclude: boolean) => addFilter(valueFilter(key, value, exclude, keyTypes.get(key))), onToggleColumn: (k: string) => setColumns(toggleColumn(columns, k)), canFilter };

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <PageHeader
        title={t("tracesExplorer.title")}
        subtitle={rows ? (byDuration ? t("tracesExplorer.slowestOnePage") : t("tracesExplorer.loaded", { count: rows.length })) : undefined}
        actions={
          <>
            <SavedViewsMenu
              signal="traces"
              activeId={search.view}
              getState={() => tracesViewStateFromSearch(search, columns, groupBy)}
              onApply={(v) => {
                setSelected(null);
                void navigate({ search: { ...searchFromTracesViewState(v.state), view: v.id } });
              }}
            />
            <CopyLinkButton />
          </>
        }
      />
      <QueryBuilder signal="traces" range={range} value={filter} onChange={setFilter} />
      <SpanCharts
        range={range}
        filter={filter}
        rootOnly={rootOnly}
        groupBy={groupBy}
        onGroupByChange={(gb) => setSearch({ gb: gb === DEFAULT_SPAN_GROUP_BY ? undefined : gb }, true)}
        onZoom={(from, to) => setSearch({ range: undefined, from: String(Math.round(from)), to: String(Math.round(to)) })}
      />
      <div className="flex flex-wrap items-center gap-2" role="toolbar" aria-label={t("logsExplorer.toolbar")}>
        <Button type="button" variant="outline" size="sm" aria-pressed={rootOnly} onClick={() => setSearch({ root: rootOnly ? undefined : true }, true)}>
          <GitBranch aria-hidden="true" />
          {t("tracesExplorer.rootOnly")}
        </Button>
        <Button type="button" variant="outline" size="sm" aria-pressed={byDuration} onClick={() => setSearch({ sort: byDuration ? undefined : "duration", order: undefined }, true)}>
          <Timer aria-hidden="true" />
          {t("tracesExplorer.slowestFirst")}
        </Button>
        <Button type="button" variant="outline" size="sm" disabled={byDuration} onClick={() => setSearch({ order: order === "desc" ? "asc" : undefined }, true)}>
          {order === "desc" ? <ArrowDownWideNarrow aria-hidden="true" /> : <ArrowUpNarrowWide aria-hidden="true" />}
          {order === "desc" ? t("tracesExplorer.newestFirst") : t("tracesExplorer.oldestFirst")}
        </Button>
        <KeyPicker signal="traces" range={range} label={t("logsExplorer.columns.pick")} selected={columns} multiple onSelect={(k) => setColumns(toggleColumn(columns, k))}>
          <Columns3 aria-hidden="true" />
          {t("logsExplorer.columns.button")}
        </KeyPicker>
        <Button type="button" variant="outline" size="sm" aria-pressed={prefs.wrap} onClick={() => updatePrefs({ ...prefs, wrap: !prefs.wrap })}>
          <WrapText aria-hidden="true" />
          {t("logsExplorer.wrap")}
        </Button>
        <Button type="button" variant="outline" size="sm" aria-pressed={prefs.density === "comfortable"} onClick={() => updatePrefs({ ...prefs, density: prefs.density === "comfortable" ? "compact" : "comfortable" })}>
          <AlignJustify aria-hidden="true" />
          {t("logsExplorer.comfortable")}
        </Button>
        <Button type="button" variant="outline" size="sm" aria-pressed={topOpen} onClick={() => setTopOpen(!topOpen)}>
          <BarChart3 aria-hidden="true" />
          {t("explorer.topValues.button")}
        </Button>
        {!isDefaultSpanColumns(columns) && (
          <Button type="button" variant="ghost" size="sm" onClick={() => setColumns([...DEFAULT_SPAN_COLUMNS])}>
            <RotateCcw aria-hidden="true" />
            {t("logsExplorer.columns.reset")}
          </Button>
        )}
      </div>
      <div className={cn("grid min-w-0 gap-3", topOpen && "lg:grid-cols-[minmax(0,1fr)_19rem]")}>
        <div className="min-w-0 overflow-hidden rounded-xl border bg-card">
          {query.isPending ? (
            <LoadingState />
          ) : query.isError && !rows ? (
            <ErrorState error={query.error} onRetry={() => void query.refetch()} />
          ) : !rows || rows.length === 0 ? (
            <EmptyState>
              <p>{t("tracesExplorer.empty")}</p>
            </EmptyState>
          ) : (
            <SpansTable
              rows={rows}
              columns={columns}
              onColumnsChange={setColumns}
              onOpen={setSelected}
              selectedIndex={selected}
              order={order}
              byDuration={byDuration}
              wrap={prefs.wrap}
              density={prefs.density}
              hasMore={query.hasNextPage}
              loadingMore={query.isFetchingNextPage}
              onLoadMore={() => void query.fetchNextPage()}
              cellActions={cellActions}
            />
          )}
        </div>
        {topOpen && (
          <TopValuesPanel
            signal="traces"
            range={range}
            filter={{ ...filter, q: "" }}
            context={{ rootOnly }}
            keyName={search.tv}
            onKeyChange={(k) => setSearch({ tv: k }, true)}
            suggestions={TOP_VALUE_SUGGESTIONS}
            canFilter={canFilter}
            onFilter={(key, value, exclude, type) => addFilter(valueFilter(key, value, exclude, type))}
            onClose={() => {
              setTopOpen(false);
              setSearch({ tv: undefined }, true);
            }}
          />
        )}
      </div>
      <SpanDetailPanel
        row={selected !== null ? rows?.[selected] : undefined}
        onClose={() => setSelected(null)}
        columns={columns}
        onToggleColumn={(k) => setColumns(toggleColumn(columns, k))}
        onFilter={addFilter}
        keyTypes={keyTypes}
        canFilter={canFilter}
      />
    </div>
  );
}
