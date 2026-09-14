import { getRouteApi } from "@tanstack/react-router";
import { SharedDashboardView } from "@/components/dashboards/SharedDashboardView";

const route = getRouteApi("/shared/dashboards/$token");

/** Public read-only dashboard of a share link (no session, no navigation; api.md "Share links"). */
export function SharedDashboardPage() {
  const { token } = route.useParams();
  return <SharedDashboardView key={token} token={token} />;
}
