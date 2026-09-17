// Cloud screens (/integrations/cloud, /integrations/cloud/$connectionId): the connection list, the
// create/edit form and the per-connection detail (docs/contracts/api.md "Cloud connections", D-135).
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { useTranslation } from "react-i18next";
import { PageHeader } from "@/components/AppShell";
import { CloudConnectionDetail } from "@/components/cloud/CloudConnectionDetail";
import { CloudConnectionForm } from "@/components/cloud/CloudConnectionForm";
import { CloudConnectionsList } from "@/components/cloud/CloudConnectionsList";
import { ReadOnlyNotice } from "@/components/ReadOnly";
import { Button } from "@/components/ui/button";
import { usePermissions } from "@/lib/org-writable";
import type { CloudSearch } from "@/router";

const listRoute = getRouteApi("/app/integrations/cloud");
const detailRoute = getRouteApi("/app/integrations/cloud/$connectionId");

/** A connection stores cloud credentials, so only admins and owners may change one; the server enforces it. */
function useCanManage(): boolean {
  return usePermissions().can("cloud_connections.manage");
}

export function CloudPage() {
  const { t } = useTranslation();
  const search = listRoute.useSearch();
  const navigate = useNavigate({ from: "/integrations/cloud" });
  const canManage = useCanManage();
  const setSearch = (patch: Partial<CloudSearch>) =>
    void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={t("cloud.title")}
        subtitle={t("cloud.subtitle")}
        actions={
          <Link to="/integrations" className="inline-flex min-h-10 items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
            <ArrowLeft className="size-4" aria-hidden="true" />
            {t("integrations.title")}
          </Link>
        }
      />
      <ReadOnlyNotice />
      {search.create && canManage ? (
        <CloudConnectionForm
          onSaved={(c) => void navigate({ to: "/integrations/cloud/$connectionId", params: { connectionId: c.id } })}
          onCancel={() => setSearch({ create: undefined })}
        />
      ) : (
        <CloudConnectionsList
          canManage={canManage}
          onNew={() => setSearch({ create: true })}
          onOpen={(c) => void navigate({ to: "/integrations/cloud/$connectionId", params: { connectionId: c.id } })}
        />
      )}
    </div>
  );
}

export function CloudDetailPage() {
  const { t } = useTranslation();
  const { connectionId } = detailRoute.useParams();
  const navigate = useNavigate();
  const canManage = useCanManage();

  return (
    <div className="flex flex-col gap-3">
      <Button asChild variant="ghost" size="sm" className="w-fit px-0 text-muted-foreground hover:text-foreground">
        <Link to="/integrations/cloud" search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to })}>
          <ArrowLeft className="size-4" aria-hidden="true" />
          {t("cloud.detail.back")}
        </Link>
      </Button>
      <CloudConnectionDetail
        id={connectionId}
        canManage={canManage}
        onDeleted={() =>
          void navigate({
            to: "/integrations/cloud",
            search: (prev: Record<string, unknown>) => ({ range: prev.range, from: prev.from, to: prev.to }) as never,
          })
        }
      />
    </div>
  );
}
