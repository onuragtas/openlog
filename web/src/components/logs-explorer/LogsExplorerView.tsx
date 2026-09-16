// The Logs Explorer body (D-118, D-122): query builder, volume histogram, dynamic-column table with cell menus, top
// values panel and record details. Used by the /logs page and embedded in the host, container and pod log tabs, where
// the page context (host, container, pod) is a locked condition and saved views are left to /logs. URL state is owned
// by the caller: it maps its own search parameters to LogsExplorerParams.
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { AlignJustify, ArrowDownWideNarrow, ArrowUpNarrowWide, BarChart3, Columns3, ExternalLink, Layers, RotateCcw, WrapText, X } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { fieldKeysQuery, logsExplorerQuery, type ExplorerContext, type FilterState, type QueryFilter, type SavedView } from "@/api/explorer";
import { PageHeader } from "@/components/AppShell";
import { TopValuesPanel } from "@/components/explorer/TopValuesPanel";
import { ExplorerTable } from "@/components/logs-explorer/ExplorerTable";
import { LogDetailPanel } from "@/components/logs-explorer/LogDetailPanel";
import { LogPatternsPanel } from "@/components/logs-explorer/LogPatternsPanel";
import { LogVolumeChart } from "@/components/logs-explorer/LogVolumeChart";
import { CopyLinkButton } from "@/components/querybuilder/CopyLinkButton";
import { KeyPicker } from "@/components/querybuilder/KeyPicker";
import { QueryBuilder } from "@/components/querybuilder/QueryBuilder";
import { SavedViewsMenu } from "@/components/querybuilder/SavedViewsMenu";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DEFAULT_COLUMNS,
  DEFAULT_GROUP_BY,
  isDefaultColumns,
  normalizeColumns,
  requestColumns,
  storeColumns,
  storedColumns,
  storedPrefs,
  storePrefs,
  toggleColumn,
  valueFilter,
  viewStateFromSearch,
  type TablePrefs,
} from "@/lib/logs-explorer";
import { conditionCount, decodeFilterState, encodeFilterState, isFilterStateEmpty, QB_LIMITS, type CompactFilterState } from "@/lib/querybuilder";
import type { RangeSpec } from "@/lib/time";
import { cn } from "@/lib/utils";

/** URL state of one Logs Explorer instance. */
export interface LogsExplorerParams {
  f?: CompactFilterState;
  q?: string;
  cols?: string[];
  order?: "asc";
  gb?: string;
  /** top values key ("" or absent: panel closed unless opened in this session) */
  tv?: string;
  /** patterns instead of the record list (D-128) */
  pv?: boolean;
}

export interface LogsExplorerViewProps {
  range: RangeSpec;
  params: LogsExplorerParams;
  /** Conditions of pre-explorer URL parameters, shown as editable chips until the filters change (memoized). */
  legacy?: QueryFilter[];
  /** Updates the caller's URL; clearLegacy: drop the pre-explorer parameters (now part of `f`). */
  onParams: (patch: LogsExplorerParams, opts: { replace?: boolean; clearLegacy?: boolean }) => void;
  onZoom: (from: number, to: number) => void;
  /** Page context conditions: always applied, shown locked; "Open in Logs Explorer" carries them as ordinary chips. */
  locked?: QueryFilter[];
  /** Logs of one APM transaction's traces, shown as a removable chip. */
  transaction?: { name: string; service: string; onRemove: () => void };
  /** /logs page: title and saved views; embedded tabs: omitted. */
  page?: { title: string; activeViewId?: string; onApplyView: (view: SavedView) => void };
  /** Empty result without filters (e.g. agent setup help). */
  emptyUnfiltered?: ReactNode;
  emptyText?: string;
}

const TOP_VALUE_SUGGESTIONS = ["severity_text", "service.name", "host.name", "attributes.log.file.path"] as const;

export function LogsExplorerView({ range, params, legacy, onParams, onZoom, locked = [], transaction, page, emptyUnfiltered, emptyText }: LogsExplorerViewProps) {
  const { t } = useTranslation();
  const filter = useMemo<FilterState>(() => {
    const base = decodeFilterState(params.f);
    return { filters: [...(legacy ?? []), ...base.filters], groups: base.groups, q: params.q ?? "" };
  }, [params.f, params.q, legacy]);
  const requestFilter = useMemo<FilterState>(() => (locked.length ? { ...filter, filters: [...locked, ...filter.filters] } : filter), [filter, locked]);
  const context = useMemo<ExplorerContext | undefined>(() => (transaction ? { transaction: transaction.name, transactionService: transaction.service } : undefined), [transaction]);
  const [fallbackColumns] = useState(() => storedColumns());
  const columns = useMemo(() => (params.cols ? normalizeColumns(params.cols) : (fallbackColumns ?? [...DEFAULT_COLUMNS])), [params.cols, fallbackColumns]);
  const order = params.order ?? "desc";
  const groupBy = params.gb ?? DEFAULT_GROUP_BY;
  const [prefs, setPrefs] = useState(() => storedPrefs());
  const [selected, setSelected] = useState<number | null>(null);
  const [topOpen, setTopOpen] = useState(!!params.tv);
  // Kept locally as well as in the URL: the embedded explorers map their own parameters and may not carry `pv`.
  const [patternsView, setPatternsView] = useState(!!params.pv);

  const setFilter = (v: FilterState) => {
    setSelected(null);
    onParams({ f: encodeFilterState({ ...v, q: "" }), q: v.q.trim() || undefined }, { clearLegacy: true });
  };
  const setColumns = (cols: string[]) => {
    storeColumns(cols);
    onParams({ cols: isDefaultColumns(cols) ? undefined : cols }, { replace: true });
  };
  const updatePrefs = (p: TablePrefs) => {
    setPrefs(p);
    storePrefs(p);
  };
  const canFilter = conditionCount(requestFilter) < QB_LIMITS.conditions;
  const addFilter = (c: QueryFilter) => canFilter && setFilter({ ...filter, filters: [...filter.filters, c] });
  const showPatterns = (on: boolean) => {
    setPatternsView(on);
    onParams({ pv: on || undefined }, { replace: true });
  };
  // Picking a pattern filters the records by it and goes back to the list, which then shows only that message.
  const selectPattern = (patternId: string) => {
    if (!canFilter) return;
    setPatternsView(false);
    setSelected(null);
    const next = { ...filter, filters: [...filter.filters, { key: "pattern_id", op: "=" as const, value: patternId }] };
    onParams({ f: encodeFilterState({ ...next, q: "" }), q: filter.q.trim() || undefined, pv: undefined }, { clearLegacy: true });
  };

  const query = useInfiniteQuery(logsExplorerQuery({ range, filter: requestFilter, order, columns: requestColumns(columns), context, enabled: !patternsView }));
  const rows = useMemo(() => query.data?.pages.flatMap((p) => p.rows), [query.data]);
  const keys = useQuery(fieldKeysQuery({ signal: "logs", range, limit: 200 }));
  const keyTypes = useMemo(() => new Map((keys.data?.keys ?? []).map((k) => [k.key, k.type])), [keys.data]);
  const filtered = !isFilterStateEmpty(filter) || !!transaction;
  const cellActions = { onFilter: (key: string, value: string, exclude: boolean) => addFilter(valueFilter(key, value, exclude, keyTypes.get(key))), onToggleColumn: (k: string) => setColumns(toggleColumn(columns, k)), canFilter };

  const openInLogs = page ? null : (
    <Button asChild variant="outline" size="sm">
      <Link to="/logs" search={{ range: range.range, from: range.from, to: range.to, f: encodeFilterState({ ...requestFilter, q: "" }), q: filter.q || undefined, cols: params.cols }}>
        <ExternalLink aria-hidden="true" />
        {t("explorer.context.openInLogs")}
      </Link>
    </Button>
  );

  return (
    <div className="flex min-w-0 flex-col gap-3">
      {page && (
        <PageHeader
          title={page.title}
          subtitle={rows ? t("logsExplorer.loaded", { count: rows.length }) : undefined}
          actions={
            <>
              <SavedViewsMenu
                signal="logs"
                activeId={page.activeViewId}
                getState={() => viewStateFromSearch({ ...range, q: params.q, f: encodeFilterState({ ...filter, q: "" }), cols: params.cols, order: params.order }, columns, groupBy)}
                onApply={(v) => {
                  setSelected(null);
                  page.onApplyView(v);
                }}
              />
              <CopyLinkButton />
            </>
          }
        />
      )}
      {transaction && (
        <div className="flex flex-wrap gap-2" data-testid="log-context-filters">
          <Badge variant="secondary" className="max-w-full gap-2 font-mono">
            <span className="truncate">
              {t("logs.transactionFilter")}: {transaction.service}: {transaction.name}
            </span>
            <button type="button" aria-label={t("logs.removeFilter", { name: t("logs.transactionFilter") })} onClick={transaction.onRemove} className="pointer-coarse:p-1.5">
              <X aria-hidden="true" />
            </button>
          </Badge>
        </div>
      )}
      <QueryBuilder signal="logs" range={range} value={filter} onChange={setFilter} locked={locked} />
      <LogVolumeChart
        range={range}
        filter={requestFilter}
        context={context}
        groupBy={groupBy}
        onGroupByChange={(gb) => onParams({ gb: gb === DEFAULT_GROUP_BY ? undefined : gb }, { replace: true })}
        onZoom={onZoom}
      />
      <div className="flex flex-wrap items-center gap-2" role="toolbar" aria-label={t("logsExplorer.toolbar")}>
        <Button type="button" variant="outline" size="sm" onClick={() => onParams({ order: order === "desc" ? "asc" : undefined }, { replace: true })}>
          {order === "desc" ? <ArrowDownWideNarrow aria-hidden="true" /> : <ArrowUpNarrowWide aria-hidden="true" />}
          {order === "desc" ? t("logsExplorer.newestFirst") : t("logsExplorer.oldestFirst")}
        </Button>
        <KeyPicker signal="logs" range={range} label={t("logsExplorer.columns.pick")} selected={columns} multiple onSelect={(k) => setColumns(toggleColumn(columns, k))}>
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
        <Button type="button" variant="outline" size="sm" aria-pressed={patternsView} onClick={() => showPatterns(!patternsView)}>
          <Layers aria-hidden="true" />
          {t("logsExplorer.patterns.button")}
        </Button>
        {!isDefaultColumns(columns) && (
          <Button type="button" variant="ghost" size="sm" onClick={() => setColumns([...DEFAULT_COLUMNS])}>
            <RotateCcw aria-hidden="true" />
            {t("logsExplorer.columns.reset")}
          </Button>
        )}
        {openInLogs && <div className="ml-auto">{openInLogs}</div>}
      </div>
      <div className={cn("grid min-w-0 gap-3", topOpen && "lg:grid-cols-[minmax(0,1fr)_19rem]")}>
        <div className="min-w-0 overflow-hidden rounded-xl border bg-card">
          {patternsView ? (
            <LogPatternsPanel range={range} filter={requestFilter} context={context} canFilter={canFilter} onSelect={selectPattern} />
          ) : query.isPending ? (
            <LoadingState />
          ) : query.isError && !rows ? (
            <ErrorState error={query.error} onRetry={() => void query.refetch()} />
          ) : !rows || rows.length === 0 ? (
            !filtered && emptyUnfiltered ? (
              emptyUnfiltered
            ) : (
              <EmptyState>
                <p>{filtered ? t("logs.empty") : (emptyText ?? t("logs.empty"))}</p>
              </EmptyState>
            )
          ) : (
            <ExplorerTable
              rows={rows}
              columns={columns}
              onColumnsChange={setColumns}
              onOpen={setSelected}
              selectedIndex={selected}
              order={order}
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
            signal="logs"
            range={range}
            filter={requestFilter}
            context={context}
            keyName={params.tv}
            onKeyChange={(k) => onParams({ tv: k }, { replace: true })}
            suggestions={TOP_VALUE_SUGGESTIONS}
            canFilter={canFilter}
            onFilter={(key, value, exclude, type) => addFilter(valueFilter(key, value, exclude, type))}
            onClose={() => {
              setTopOpen(false);
              onParams({ tv: undefined }, { replace: true });
            }}
          />
        )}
      </div>
      <LogDetailPanel
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
