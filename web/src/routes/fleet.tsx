import { useQuery } from "@tanstack/react-query";
import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { fleetPolicyQuery, fleetSummaryQuery } from "@/api/fleet";
import { PageHeader } from "@/components/AppShell";
import { FleetHostsTable } from "@/components/fleet/FleetHostsTable";
import { FleetSummaryCards } from "@/components/fleet/FleetSummaryCards";
import { PolicyEditor } from "@/components/fleet/PolicyEditor";
import { RolloutPanel } from "@/components/fleet/RolloutPanel";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Card, CardContent } from "@/components/ui/card";
import { ReadOnlyNotice } from "@/components/ReadOnly";
import { usePermissions } from "@/lib/org-writable";

const route = getRouteApi("/app/fleet");

/** Filo / Fleet: agent versions, rollouts, update policy and per-host exceptions. */
export function FleetPage() {
  const { t } = useTranslation();
  const search = route.useSearch();
  const navigate = useNavigate({ from: "/fleet" });
  const canManage = usePermissions().can("fleet.manage");
  const summary = useQuery(fleetSummaryQuery());
  const policy = useQuery(fleetPolicyQuery());

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title={t("fleet.title")} subtitle={t("fleet.subtitle")} />
      <ReadOnlyNotice />

      {summary.isPending ? (
        <LoadingState />
      ) : summary.isError ? (
        <ErrorState error={summary.error} onRetry={() => void summary.refetch()} />
      ) : (
        <>
          <FleetSummaryCards summary={summary.data} />
          <RolloutPanel summary={summary.data} canManage={canManage} />
        </>
      )}

      <Card>
        <CardContent>
          {policy.isPending ? (
            <LoadingState />
          ) : policy.isError ? (
            <ErrorState error={policy.error} onRetry={() => void policy.refetch()} />
          ) : (
            // Keyed by role only: a save resets the form itself and keeps the "saved" message.
            <PolicyEditor key={String(canManage)} policy={policy.data} canManage={canManage} />
          )}
        </CardContent>
      </Card>

      <section aria-labelledby="fleet-hosts-title" className="flex flex-col gap-3">
        <h2 id="fleet-hosts-title" className="text-base font-semibold">
          {t("fleet.hosts.title")}
        </h2>
        <FleetHostsTable
          filter={{ q: search.q, version: search.version, state: search.state }}
          onFilterChange={(f) => void navigate({ search: (prev) => ({ ...prev, q: f.q, version: f.version, state: f.state }), replace: true })}
          versions={summary.data?.versions.map((v) => v.version) ?? []}
          canManage={canManage}
        />
      </section>
    </div>
  );
}
