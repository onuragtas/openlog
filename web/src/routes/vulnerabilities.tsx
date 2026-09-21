// Vulnerability screens (/vulnerabilities, /vulnerabilities/$vulnId): the fleet's open findings grouped by
// advisory, and one advisory's hosts (docs/contracts/api.md "Vulnerabilities", D-142).
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { VulnSeverity } from "@/api/vulnerabilities";
import { PageHeader } from "@/components/AppShell";
import { VulnerabilitiesList } from "@/components/vulnerabilities/VulnerabilitiesList";
import { VulnerabilityDetail } from "@/components/vulnerabilities/VulnerabilityDetail";
import { Button } from "@/components/ui/button";

const listRoute = getRouteApi("/app/vulnerabilities");
const detailRoute = getRouteApi("/app/vulnerabilities/$vulnId");

export function VulnerabilitiesPage() {
  const { t } = useTranslation();
  const search = listRoute.useSearch();
  const navigate = useNavigate({ from: "/vulnerabilities" });

  return (
    <div className="flex flex-col gap-4">
      <PageHeader title={t("vulnerabilities.title")} subtitle={t("vulnerabilities.subtitle")} />
      <VulnerabilitiesList
        severity={search.severity}
        onSeverity={(severity) => void navigate({ search: (prev) => ({ ...prev, severity }), replace: true })}
        onOpen={(group) => void navigate({ to: "/vulnerabilities/$vulnId", params: { vulnId: group.vuln_id } })}
      />
    </div>
  );
}

export function VulnerabilityDetailPage() {
  const { t } = useTranslation();
  const { vulnId } = detailRoute.useParams();
  const navigate = useNavigate();

  return (
    <div className="flex flex-col gap-3">
      <Button asChild variant="ghost" size="sm" className="w-fit px-0 text-muted-foreground hover:text-foreground">
        <Link to="/vulnerabilities" search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to })}>
          <ArrowLeft className="size-4" aria-hidden="true" />
          {t("vulnerabilities.detail.back")}
        </Link>
      </Button>
      <VulnerabilityDetail id={vulnId} onOpenHost={(hostId) => void navigate({ to: "/hosts/$hostId", params: { hostId } })} />
    </div>
  );
}

/** The severity filter of the list route. */
export function parseSeverity(v: unknown): VulnSeverity | undefined {
  const value = String(v ?? "");
  return (["critical", "high", "medium", "low", "none"] as const).includes(value as VulnSeverity) ? (value as VulnSeverity) : undefined;
}
