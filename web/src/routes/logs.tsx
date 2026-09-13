import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { X } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { PageHeader } from "@/components/AppShell";
import { LogFilters } from "@/components/LogFilters";
import { LogTable } from "@/components/LogTable";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { useLogPages } from "@/lib/use-logs";

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
  });
  const filterValue = useMemo(
    () => ({ q: search.q ?? "", severity: search.severity ?? "", service: search.service ?? "", host: search.host ?? "" }),
    [search.q, search.severity, search.service, search.host],
  );

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
      {search.trace && (
        <div>
          <Badge variant="secondary" className="gap-2 font-mono">
            {t("logs.trace")}: {search.trace}
            <button type="button" aria-label={t("common.close")} onClick={() => void navigate({ search: (prev) => ({ ...prev, trace: undefined }) })}>
              <X aria-hidden="true" />
            </button>
          </Badge>
        </div>
      )}
      <div className="overflow-hidden rounded-xl border bg-card">
        {query.isPending ? (
          <LoadingState />
        ) : query.isError && !logs ? (
          <ErrorState error={query.error} onRetry={() => void query.refetch()} />
        ) : !logs || logs.length === 0 ? (
          <EmptyState>{t("logs.empty")}</EmptyState>
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
