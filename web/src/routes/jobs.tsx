// Job monitoring screens (/jobs, /jobs/$monitorId): the monitor list with each one's state, the detail with
// the ping command and the concluded runs, and the create/edit form (docs/contracts/api.md "Job
// monitoring", D-141).
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { atLeast } from "@/api/roles";
import { PageHeader } from "@/components/AppShell";
import { JobDetail } from "@/components/jobs/JobDetail";
import { JobForm } from "@/components/jobs/JobForm";
import { JobsList } from "@/components/jobs/JobsList";
import { ReadOnlyNotice } from "@/components/ReadOnly";
import { Button } from "@/components/ui/button";
import { usePermissions } from "@/lib/org-writable";
import type { JobsSearch } from "@/router";

const listRoute = getRouteApi("/app/jobs");
const detailRoute = getRouteApi("/app/jobs/$monitorId");

/** Members and higher may create and change monitors; the server enforces it. */
function useCanWrite(): boolean {
  const me = useMe();
  const perms = usePermissions();
  return perms.writable && atLeast(me.data?.role, "member");
}

export function JobsPage() {
  const { t } = useTranslation();
  const search = listRoute.useSearch();
  const navigate = useNavigate({ from: "/jobs" });
  const canWrite = useCanWrite();
  const setSearch = (patch: Partial<JobsSearch>) => void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });

  return (
    <div className="flex flex-col gap-4">
      <PageHeader title={t("jobs.title")} subtitle={t("jobs.subtitle")} />
      <ReadOnlyNotice />
      {search.create && canWrite ? (
        <JobForm
          onSaved={(monitor) => void navigate({ to: "/jobs/$monitorId", params: { monitorId: monitor.id } })}
          onCancel={() => setSearch({ create: undefined })}
        />
      ) : (
        <JobsList
          canWrite={canWrite}
          onNew={() => setSearch({ create: true })}
          onOpen={(monitor) => void navigate({ to: "/jobs/$monitorId", params: { monitorId: monitor.id } })}
        />
      )}
    </div>
  );
}

export function JobDetailPage() {
  const { t } = useTranslation();
  const { monitorId } = detailRoute.useParams();
  const navigate = useNavigate();
  const canWrite = useCanWrite();

  return (
    <div className="flex flex-col gap-3">
      <Button asChild variant="ghost" size="sm" className="w-fit px-0 text-muted-foreground hover:text-foreground">
        <Link to="/jobs" search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to })}>
          <ArrowLeft className="size-4" aria-hidden="true" />
          {t("jobs.detail.back")}
        </Link>
      </Button>
      <JobDetail
        id={monitorId}
        canWrite={canWrite}
        onDeleted={() => void navigate({ to: "/jobs", search: (prev: Record<string, unknown>) => ({ range: prev.range, from: prev.from, to: prev.to }) as never })}
      />
    </div>
  );
}
