// Organization-wide error inbox (/apm/errors): the inbox of every service with service, namespace and
// environment filters. Opening a group goes to its service page (Errors tab).
import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { useId, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { apmServicesQuery } from "@/api/apm";
import { ErrorInbox, type ErrorInboxFilterValues } from "@/components/apm/errors/ErrorInbox";
import { PageHeader } from "@/components/AppShell";
import { NativeSelect } from "@/components/ui/native-select";
import type { ApmErrorsSearch } from "@/router";

const route = getRouteApi("/app/apm/errors");

const uniq = (values: string[]) => [...new Set(values.filter(Boolean))].sort();

export function ApmErrorsPage() {
  const { t } = useTranslation();
  const search = route.useSearch();
  const navigate = useNavigate({ from: "/apm/errors" });
  const ids = { svc: useId(), env: useId(), ns: useId() };
  const range = useMemo(() => ({ range: search.range, from: search.from, to: search.to }), [search.range, search.from, search.to]);
  const services = useQuery(apmServicesQuery(range));
  const list = services.data?.services ?? [];
  const options = { svc: uniq(list.map((s) => s.service_name)), env: uniq(list.map((s) => s.environment)), ns: uniq(list.map((s) => s.service_namespace)) };
  const setSearch = (patch: Partial<ApmErrorsSearch>) => void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });
  const filters: ErrorInboxFilterValues = { status: search.estatus ?? "unresolved", assignee: search.eassignee ?? "any", q: search.eq ?? "", sort: search.esort ?? "count" };

  const select = (id: string, key: "svc" | "env" | "ns", label: string, all: string) => (
    <div className="flex items-center gap-2">
      <label htmlFor={id} className="sr-only">
        {label}
      </label>
      <NativeSelect id={id} value={search[key] ?? ""} onChange={(e) => setSearch({ [key]: e.target.value || undefined })} className="max-w-[12rem]">
        <option value="">{all}</option>
        {options[key].map((v) => (
          <option key={v} value={v}>
            {v}
          </option>
        ))}
      </NativeSelect>
    </div>
  );

  return (
    <div className="flex flex-col gap-4">
      <div>
        <Link to="/apm" search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to })} className="mb-2 inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
          <ArrowLeft className="size-3" aria-hidden="true" />
          {t("apm.service.back")}
        </Link>
        <PageHeader
          title={t("apm.errors.inboxTitle")}
          subtitle={t("apm.errors.inboxSubtitle")}
          actions={
            <div className="contents">
              {select(ids.svc, "svc", t("apm.errors.service"), t("apm.errors.allServices"))}
              {select(ids.env, "env", t("apm.environment"), t("apm.allEnvironments"))}
              {select(ids.ns, "ns", t("apm.errors.namespace"), t("apm.errors.allNamespaces"))}
            </div>
          }
        />
      </div>
      <ErrorInbox
        range={range}
        orgFilter={{ service: search.svc, environment: search.env, namespace: search.ns }}
        filters={filters}
        onFilters={(p) =>
          setSearch({
            ...(p.status !== undefined ? { estatus: p.status === "unresolved" ? undefined : p.status } : {}),
            ...(p.assignee !== undefined ? { eassignee: p.assignee === "any" ? undefined : p.assignee } : {}),
            ...(p.q !== undefined ? { eq: p.q || undefined } : {}),
            ...(p.sort !== undefined ? { esort: p.sort === "count" ? undefined : p.sort } : {}),
          })
        }
        onOpen={(g) =>
          void navigate({
            to: "/apm/services/$service",
            params: { service: g.service_name },
            search: (prev: Record<string, unknown>) =>
              ({ range: prev.range, from: prev.from, to: prev.to, ns: g.service_namespace || undefined, env: g.environment || undefined, tab: "errors", group: g.group_id }) as never,
          })
        }
      />
    </div>
  );
}
