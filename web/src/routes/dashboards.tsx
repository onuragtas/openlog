import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { DashboardsList } from "@/components/dashboards/DashboardsList";
import { DashboardView } from "@/components/dashboards/DashboardView";

const listRoute = getRouteApi("/app/dashboards");
const viewRoute = getRouteApi("/app/dashboards/$dashboardId");

/** Custom dashboards (api.md "Dashboards"). */
export function DashboardsPage() {
  const search = listRoute.useSearch();
  const navigate = useNavigate();
  return (
    <DashboardsList
      q={search.q}
      onSearchChange={(q) => void navigate({ to: "/dashboards", search: (prev) => ({ ...prev, q }), replace: true })}
      onOpenDashboard={(id) => void navigate({ to: "/dashboards/$dashboardId", params: { dashboardId: id }, search: (prev) => ({ range: prev.range, from: prev.from, to: prev.to }) })}
    />
  );
}

export function DashboardPage() {
  const { dashboardId } = viewRoute.useParams();
  const search = viewRoute.useSearch();
  const navigate = useNavigate();
  return (
    <DashboardView
      key={dashboardId}
      dashboardId={dashboardId}
      search={{ page: search.page, vars: search.vars, edit: search.edit }}
      range={{ range: search.range, from: search.from, to: search.to }}
      onSearchChange={(patch) => void navigate({ to: "/dashboards/$dashboardId", params: { dashboardId }, search: (prev) => ({ ...prev, ...patch }), replace: true })}
      onOpenDashboard={(id) => void navigate({ to: "/dashboards/$dashboardId", params: { dashboardId: id }, search: (prev) => ({ range: prev.range, from: prev.from, to: prev.to }) })}
    />
  );
}
