import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { AlignJustify, ArrowDownWideNarrow, ArrowUpNarrowWide, Columns3, RotateCcw, WrapText, X } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { fieldKeysQuery, logsExplorerQuery, type FilterState, type QueryFilter } from "@/api/explorer";
import { PageHeader } from "@/components/AppShell";
import { LogFilters } from "@/components/LogFilters";
import { ExplorerTable } from "@/components/logs-explorer/ExplorerTable";
import { LogDetailPanel } from "@/components/logs-explorer/LogDetailPanel";
import { LogVolumeChart } from "@/components/logs-explorer/LogVolumeChart";
import { LogTable } from "@/components/LogTable";
import { AddDataLink } from "@/components/onboarding/AddDataLink";
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
  legacyFilters,
  normalizeColumns,
  requestColumns,
  searchFromViewState,
  storeColumns,
  storedColumns,
  storedPrefs,
  storePrefs,
  toggleColumn,
  viewStateFromSearch,
  type TablePrefs,
} from "@/lib/logs-explorer";
import { conditionCount, decodeFilterState, encodeFilterState, isFilterStateEmpty, QB_LIMITS } from "@/lib/querybuilder";
import type { RangeSpec } from "@/lib/time";
import { useLogPages } from "@/lib/use-logs";
import type { LogsSearch } from "@/router";

const route = getRouteApi("/app/logs");

/** Logs Explorer; links for one APM transaction (`txn`) keep the GET /api/v1/logs listing, which alone can filter by it. */
export function LogsPage() {
  const search = route.useSearch();
  return search.txn ? <TransactionLogs /> : <LogsExplorer />;
}

function LogsExplorer() {
  const { t } = useTranslation();
  const search = route.useSearch();
  const navigate = useNavigate({ from: "/logs" });
  const range = useMemo<RangeSpec>(() => ({ range: search.range, from: search.from, to: search.to }), [search.range, search.from, search.to]);
  const { f, q, severity, service, host, trace, span } = search;
  // Pre-explorer parameters (links from hosts, APM and traces) become ordinary, editable conditions.
  const filter = useMemo<FilterState>(() => {
    const base = decodeFilterState(f);
    return { filters: [...legacyFilters({ severity, service, host, trace, span }), ...base.filters], groups: base.groups, q: q ?? "" };
  }, [f, q, severity, service, host, trace, span]);
  const [fallbackColumns] = useState(storedColumns);
  const columns = useMemo(() => (search.cols ? normalizeColumns(search.cols) : (fallbackColumns ?? [...DEFAULT_COLUMNS])), [search.cols, fallbackColumns]);
  const order = search.order ?? "desc";
  const groupBy = search.gb ?? DEFAULT_GROUP_BY;
  const [prefs, setPrefs] = useState(storedPrefs);
  const [selected, setSelected] = useState<number | null>(null);

  const setSearch = (patch: Partial<LogsSearch & RangeSpec>, replace = false) => void navigate({ search: (prev) => ({ ...prev, ...patch }), replace });
  const setFilter = (v: FilterState) => {
    setSelected(null);
    setSearch({ f: encodeFilterState({ ...v, q: "" }), q: v.q.trim() || undefined, severity: undefined, service: undefined, host: undefined, trace: undefined, span: undefined });
  };
  const setColumns = (cols: string[]) => {
    storeColumns(cols);
    setSearch({ cols: isDefaultColumns(cols) ? undefined : cols }, true);
  };
  const updatePrefs = (p: TablePrefs) => {
    setPrefs(p);
    storePrefs(p);
  };
  const canFilter = conditionCount(filter) < QB_LIMITS.conditions;
  const addFilter = (c: QueryFilter) => canFilter && setFilter({ ...filter, filters: [...filter.filters, c] });

  const query = useInfiniteQuery(logsExplorerQuery({ range, filter, order, columns: requestColumns(columns) }));
  const rows = useMemo(() => query.data?.pages.flatMap((p) => p.rows), [query.data]);
  const keys = useQuery(fieldKeysQuery({ signal: "logs", range, limit: 200 }));
  const keyTypes = useMemo(() => new Map((keys.data?.keys ?? []).map((k) => [k.key, k.type])), [keys.data]);

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <PageHeader
        title={t("logs.title")}
        subtitle={rows ? t("logsExplorer.loaded", { count: rows.length }) : undefined}
        actions={
          <>
            <SavedViewsMenu
              signal="logs"
              activeId={search.view}
              getState={() => viewStateFromSearch({ ...search, f: encodeFilterState({ ...filter, q: "" }) }, columns, groupBy)}
              onApply={(v) => {
                setSelected(null);
                void navigate({ search: { ...searchFromViewState(v.state), view: v.id } });
              }}
            />
            <CopyLinkButton />
          </>
        }
      />
      <QueryBuilder signal="logs" range={range} value={filter} onChange={setFilter} />
      <LogVolumeChart
        range={range}
        filter={filter}
        groupBy={groupBy}
        onGroupByChange={(gb) => setSearch({ gb: gb === DEFAULT_GROUP_BY ? undefined : gb }, true)}
        onZoom={(from, to) => setSearch({ range: undefined, from: String(Math.round(from)), to: String(Math.round(to)) })}
      />
      <div className="flex flex-wrap items-center gap-2" role="toolbar" aria-label={t("logsExplorer.toolbar")}>
        <Button type="button" variant="outline" size="sm" onClick={() => setSearch({ order: order === "desc" ? "asc" : undefined }, true)}>
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
        {!isDefaultColumns(columns) && (
          <Button type="button" variant="ghost" size="sm" onClick={() => setColumns([...DEFAULT_COLUMNS])}>
            <RotateCcw aria-hidden="true" />
            {t("logsExplorer.columns.reset")}
          </Button>
        )}
      </div>
      <div className="min-w-0 overflow-hidden rounded-xl border bg-card">
        {query.isPending ? (
          <LoadingState />
        ) : query.isError && !rows ? (
          <ErrorState error={query.error} onRetry={() => void query.refetch()} />
        ) : !rows || rows.length === 0 ? (
          <EmptyState>
            <p>{t("logs.empty")}</p>
            {isFilterStateEmpty(filter) && <AddDataLink target="logs/host" label={t("addData.empty.logs")} />}
          </EmptyState>
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

/** Logs of one APM transaction's traces (GET /api/v1/logs `transaction`), with the classic filters. */
function TransactionLogs() {
  const { t } = useTranslation();
  const search = route.useSearch();
  const navigate = useNavigate({ from: "/logs" });
  const { query, logs } = useLogPages({
    range: { range: search.range, from: search.from, to: search.to },
    q: search.q,
    severity: search.severity,
    service: search.service,
    hostId: search.host,
    traceId: search.trace,
    spanId: search.span,
    transaction: search.txn,
    transactionService: search.txnsvc,
  });
  const filterValue = useMemo(
    () => ({ q: search.q ?? "", severity: search.severity ?? "", service: search.service ?? "", host: search.host ?? "" }),
    [search.q, search.severity, search.service, search.host],
  );
  const remove = (patch: Partial<LogsSearch>) => void navigate({ search: (prev) => ({ ...prev, ...patch }) });
  const badges: { key: string; label: string; value: string; clear: Partial<LogsSearch> }[] = [];
  if (search.trace) badges.push({ key: "trace", label: t("logs.trace"), value: search.trace, clear: { trace: undefined } });
  if (search.span) badges.push({ key: "span", label: t("logs.spanFilter"), value: search.span, clear: { span: undefined } });
  if (search.txn) {
    badges.push({ key: "txn", label: t("logs.transactionFilter"), value: search.txnsvc ? `${search.txnsvc}: ${search.txn}` : search.txn, clear: { txn: undefined, txnsvc: undefined } });
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader title={t("logs.title")} subtitle={logs ? t("logs.showing", { count: logs.length }) : undefined} />
      <LogFilters
        value={filterValue}
        onApply={(v) =>
          void navigate({
            search: (prev) => ({ ...prev, q: v.q || undefined, severity: v.severity || undefined, service: v.service || undefined, host: v.host || undefined }),
          })
        }
      />
      {badges.length > 0 && (
        <div className="flex flex-wrap gap-2" data-testid="log-context-filters">
          {badges.map((b) => (
            <Badge key={b.key} variant="secondary" className="max-w-full gap-2 font-mono">
              <span className="truncate">
                {b.label}: {b.value}
              </span>
              <button type="button" aria-label={t("logs.removeFilter", { name: b.label })} onClick={() => remove(b.clear)} className="pointer-coarse:p-1.5">
                <X aria-hidden="true" />
              </button>
            </Badge>
          ))}
        </div>
      )}
      <div className="overflow-hidden rounded-xl border bg-card">
        {query.isPending ? (
          <LoadingState />
        ) : query.isError && !logs ? (
          <ErrorState error={query.error} onRetry={() => void query.refetch()} />
        ) : !logs || logs.length === 0 ? (
          <EmptyState>
            <p>{t("logs.empty")}</p>
          </EmptyState>
        ) : (
          <LogTable logs={logs} hasMore={query.hasNextPage} loadingMore={query.isFetchingNextPage} onLoadMore={() => void query.fetchNextPage()} />
        )}
      </div>
    </div>
  );
}
