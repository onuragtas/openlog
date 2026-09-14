import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { X } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { PageHeader } from "@/components/AppShell";
import { LogFilters } from "@/components/LogFilters";
import { LogTable } from "@/components/LogTable";
import { AddDataLink } from "@/components/onboarding/AddDataLink";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { useLogPages } from "@/lib/use-logs";
import type { LogsSearch } from "@/router";

const route = getRouteApi("/app/logs");

export function LogsPage() {
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
            {!search.q && !search.severity && !search.service && !search.host && !search.trace && !search.span && !search.txn && (
              <AddDataLink target="logs/host" label={t("addData.empty.logs")} />
            )}
          </EmptyState>
        ) : (
          <LogTable
            logs={logs}
            hasMore={query.hasNextPage}
            loadingMore={query.isFetchingNextPage}
            onLoadMore={() => void query.fetchNextPage()}
          />
        )}
      </div>
    </div>
  );
}
