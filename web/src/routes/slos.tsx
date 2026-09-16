// SLO screens (/slos, /slos/$sloId): the list with error budgets, the detail with burndown and burn-rate
// windows, and the create/edit form (docs/contracts/slo.md, D-125).
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { atLeast } from "@/api/roles";
import { PageHeader } from "@/components/AppShell";
import { ReadOnlyNotice } from "@/components/ReadOnly";
import { SloDetail } from "@/components/slos/SloDetail";
import { SloForm } from "@/components/slos/SloForm";
import { SloList } from "@/components/slos/SloList";
import { Button } from "@/components/ui/button";
import { usePermissions } from "@/lib/org-writable";
import type { SlosSearch } from "@/router";

const listRoute = getRouteApi("/app/slos");
const detailRoute = getRouteApi("/app/slos/$sloId");

/** Members and higher may create and change SLOs (slo.md §1); the server enforces it. */
function useCanWrite(): boolean {
  const me = useMe();
  const perms = usePermissions();
  return perms.writable && atLeast(me.data?.role, "member");
}

export function SlosPage() {
  const { t } = useTranslation();
  const search = listRoute.useSearch();
  const navigate = useNavigate({ from: "/slos" });
  const canWrite = useCanWrite();
  const setSearch = (patch: Partial<SlosSearch>) => void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });

  return (
    <div className="flex flex-col gap-4">
      <PageHeader title={t("slo.title")} subtitle={t("slo.subtitle")} />
      <ReadOnlyNotice />
      {search.create && canWrite ? (
        <SloForm
          onSaved={(slo) => void navigate({ to: "/slos/$sloId", params: { sloId: slo.id } })}
          onCancel={() => setSearch({ create: undefined })}
        />
      ) : (
        <SloList
          canWrite={canWrite}
          onNew={() => setSearch({ create: true })}
          onOpen={(slo) => void navigate({ to: "/slos/$sloId", params: { sloId: slo.id } })}
        />
      )}
    </div>
  );
}

export function SloDetailPage() {
  const { t } = useTranslation();
  const { sloId } = detailRoute.useParams();
  const navigate = useNavigate();
  const canWrite = useCanWrite();

  return (
    <div className="flex flex-col gap-3">
      <Button asChild variant="ghost" size="sm" className="w-fit px-0 text-muted-foreground hover:text-foreground">
        <Link to="/slos" search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to })}>
          <ArrowLeft className="size-4" aria-hidden="true" />
          {t("slo.detail.back")}
        </Link>
      </Button>
      <SloDetail
        id={sloId}
        canWrite={canWrite}
        onDeleted={() => void navigate({ to: "/slos", search: (prev: Record<string, unknown>) => ({ range: prev.range, from: prev.from, to: prev.to }) as never })}
        onOpenService={(s) =>
          void navigate({
            to: "/apm/services/$service",
            params: { service: s.service_name },
            search: (prev: Record<string, unknown>) =>
              ({ range: prev.range, from: prev.from, to: prev.to, ns: s.service_namespace || undefined, env: s.environment || undefined }) as never,
          })
        }
      />
    </div>
  );
}
