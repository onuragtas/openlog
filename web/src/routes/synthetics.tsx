// Synthetics screens (/synthetics, /synthetics/$checkId): the check list with uptime and latency, the
// detail with recent failures, and the create/edit form (docs/contracts/api.md "Synthetic monitoring",
// D-132).
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { atLeast } from "@/api/roles";
import { PageHeader } from "@/components/AppShell";
import { ReadOnlyNotice } from "@/components/ReadOnly";
import { SyntheticDetail } from "@/components/synthetics/SyntheticDetail";
import { SyntheticForm } from "@/components/synthetics/SyntheticForm";
import { SyntheticsList } from "@/components/synthetics/SyntheticsList";
import { Button } from "@/components/ui/button";
import { usePermissions } from "@/lib/org-writable";
import type { SyntheticsSearch } from "@/router";

const listRoute = getRouteApi("/app/synthetics");
const detailRoute = getRouteApi("/app/synthetics/$checkId");

/** Members and higher may create and change checks; the server enforces it. */
function useCanWrite(): boolean {
  const me = useMe();
  const perms = usePermissions();
  return perms.writable && atLeast(me.data?.role, "member");
}

export function SyntheticsPage() {
  const { t } = useTranslation();
  const search = listRoute.useSearch();
  const navigate = useNavigate({ from: "/synthetics" });
  const canWrite = useCanWrite();
  const setSearch = (patch: Partial<SyntheticsSearch>) => void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });

  return (
    <div className="flex flex-col gap-4">
      <PageHeader title={t("synthetics.title")} subtitle={t("synthetics.subtitle")} />
      <ReadOnlyNotice />
      {search.create && canWrite ? (
        <SyntheticForm
          onSaved={(check) => void navigate({ to: "/synthetics/$checkId", params: { checkId: check.id } })}
          onCancel={() => setSearch({ create: undefined })}
        />
      ) : (
        <SyntheticsList
          canWrite={canWrite}
          onNew={() => setSearch({ create: true })}
          onOpen={(check) => void navigate({ to: "/synthetics/$checkId", params: { checkId: check.id } })}
        />
      )}
    </div>
  );
}

export function SyntheticDetailPage() {
  const { t } = useTranslation();
  const { checkId } = detailRoute.useParams();
  const navigate = useNavigate();
  const canWrite = useCanWrite();

  return (
    <div className="flex flex-col gap-3">
      <Button asChild variant="ghost" size="sm" className="w-fit px-0 text-muted-foreground hover:text-foreground">
        <Link to="/synthetics" search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to })}>
          <ArrowLeft className="size-4" aria-hidden="true" />
          {t("synthetics.detail.back")}
        </Link>
      </Button>
      <SyntheticDetail
        id={checkId}
        canWrite={canWrite}
        onDeleted={() =>
          void navigate({ to: "/synthetics", search: (prev: Record<string, unknown>) => ({ range: prev.range, from: prev.from, to: prev.to }) as never })
        }
      />
    </div>
  );
}
