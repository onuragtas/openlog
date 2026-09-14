import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { useTranslation } from "react-i18next";
import { onboardingQuery } from "@/api/onboarding";
import { PageHeader } from "@/components/AppShell";
import { AddDataCatalog } from "@/components/onboarding/AddDataCatalog";
import { InstallFlow } from "@/components/onboarding/InstallFlow";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { findTarget } from "@/lib/install-commands";
import { tDynamic } from "@/lib/onboarding-key";

const targetRoute = getRouteApi("/app/add-data/$");

/** /add-data: catalog of data sources. */
export function AddDataPage() {
  const { t } = useTranslation();
  return (
    <div className="mx-auto flex w-full max-w-6xl min-w-0 flex-col">
      <PageHeader title={t("addData.title")} subtitle={t("addData.subtitle")} />
      <AddDataCatalog />
    </div>
  );
}

function BackLink() {
  const { t } = useTranslation();
  return (
    <Link to="/add-data" className="mb-3 flex w-fit items-center gap-1.5 text-sm text-primary hover:underline">
      <ArrowLeft className="size-4" aria-hidden="true" />
      {t("addData.back")}
    </Link>
  );
}

/** /add-data/<card> (e.g. /add-data/linux, /add-data/apm/node): guided setup. Search: host, hostName, service prefill. */
export function AddDataTargetPage() {
  const { t } = useTranslation();
  const { _splat } = targetRoute.useParams();
  const search = targetRoute.useSearch();
  const target = findTarget(_splat);
  const onboarding = useQuery({ ...onboardingQuery(), enabled: !!target });

  // Client-side navigation to /add-data can match this splat route with an empty splat: show the catalog.
  if (!_splat || _splat.replace(/\//g, "") === "") return <AddDataPage />;

  if (!target) {
    return (
      <div className="mx-auto w-full max-w-4xl min-w-0">
        <BackLink />
        <h1 className="sr-only">{t("addData.title")}</h1>
        <EmptyState>{t("addData.notFound")}</EmptyState>
      </div>
    );
  }
  const title = tDynamic(t, `addData.targets.${target.id}.title`);
  const forHost = search.hostName || search.host;

  return (
    <div className="mx-auto flex w-full max-w-4xl min-w-0 flex-col">
      <BackLink />
      <PageHeader
        title={`${t("addData.title")}: ${title}`}
        subtitle={tDynamic(t, `addData.targets.${target.id}.description`)}
        actions={
          <div className="flex min-w-0 flex-wrap gap-2">
            <Badge variant="secondary">{tDynamic(t, `addData.groups.${target.group}`)}</Badge>
            {forHost && (
              <Badge variant="outline" className="max-w-full truncate font-mono" data-testid="add-data-host">
                {forHost}
              </Badge>
            )}
          </div>
        }
      />
      {onboarding.isPending ? (
        <LoadingState />
      ) : onboarding.isError ? (
        <ErrorState error={onboarding.error} onRetry={() => void onboarding.refetch()} />
      ) : (
        <InstallFlow
          key={target.id}
          target={target}
          onboarding={onboarding.data}
          initial={{ ...(search.hostName ? { hostName: search.hostName } : {}), ...(search.service ? { serviceName: search.service } : {}) }}
        />
      )}
    </div>
  );
}
