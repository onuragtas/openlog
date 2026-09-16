import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { LogsExplorerView } from "@/components/logs-explorer/LogsExplorerView";
import { AddDataLink } from "@/components/onboarding/AddDataLink";
import { EmptyState } from "@/components/StateViews";
import { legacyFilters, searchFromViewState } from "@/lib/logs-explorer";
import type { RangeSpec } from "@/lib/time";

const route = getRouteApi("/app/logs");

/**
 * Logs Explorer (D-118). Links from APM, traces and hosts keep working: `severity`, `service`, `host`, `trace` and
 * `span` become editable conditions, `txn` + `txnsvc` a removable transaction chip (POST /logs/query `transaction`).
 */
export function LogsPage() {
  const { t } = useTranslation();
  const search = route.useSearch();
  const navigate = useNavigate({ from: "/logs" });
  const range = useMemo<RangeSpec>(() => ({ range: search.range, from: search.from, to: search.to }), [search.range, search.from, search.to]);
  const { severity, service, host, trace, span, txn, txnsvc } = search;
  const legacy = useMemo(() => legacyFilters({ severity, service, host, trace, span }), [severity, service, host, trace, span]);
  const transaction = useMemo(
    () => (txn && txnsvc ? { name: txn, service: txnsvc, onRemove: () => void navigate({ search: (prev) => ({ ...prev, txn: undefined, txnsvc: undefined }) }) } : undefined),
    [txn, txnsvc, navigate],
  );

  return (
    <LogsExplorerView
      range={range}
      params={{ f: search.f, q: search.q, cols: search.cols, order: search.order, gb: search.gb, tv: search.tv, pv: search.pv }}
      legacy={legacy}
      transaction={transaction}
      onParams={(patch, opts) =>
        void navigate({
          search: (prev) => ({
            ...prev,
            ...patch,
            ...(opts.clearLegacy ? { severity: undefined, service: undefined, host: undefined, trace: undefined, span: undefined } : {}),
          }),
          replace: opts.replace,
        })
      }
      onZoom={(from, to) => void navigate({ search: (prev) => ({ ...prev, range: undefined, from: String(Math.round(from)), to: String(Math.round(to)) }) })}
      page={{
        title: t("logs.title"),
        activeViewId: search.view,
        onApplyView: (v) => void navigate({ search: { ...searchFromViewState(v.state), view: v.id } }),
      }}
      emptyUnfiltered={
        <EmptyState>
          <p>{t("logs.empty")}</p>
          <AddDataLink target="logs/host" label={t("addData.empty.logs")} />
        </EmptyState>
      }
    />
  );
}
