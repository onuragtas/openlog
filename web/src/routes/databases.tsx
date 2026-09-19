// Database screens (/databases, /databases/instance, /databases/query; db-monitoring.md §5, D-138): the instances
// with query monitoring, one instance's load, statements and sessions, and one statement. An instance id
// (service.instance.id, e.g. db1.internal:5432) contains characters a path segment would need to escape, so it is
// a search parameter like the range.
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { DbQuerySort } from "@/api/db";
import { PageHeader } from "@/components/AppShell";
import { DbActivityTab } from "@/components/db/DbActivityTab";
import { DbInstances } from "@/components/db/DbInstances";
import { DbQueriesTab } from "@/components/db/DbQueriesTab";
import { DbQueryDetail } from "@/components/db/DbQueryDetail";
import { DbSessionsTab } from "@/components/db/DbSessionsTab";
import { DbInstanceTitle } from "@/components/db/DbInstanceTitle";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { DatabasesSearch } from "@/router";

const listRoute = getRouteApi("/app/databases");
const instanceRoute = getRouteApi("/app/databases/instance");
const queryRoute = getRouteApi("/app/databases/query");

const rangeOf = (s: { range?: string; from?: string; to?: string }) => ({ range: s.range, from: s.from, to: s.to });

export function DatabasesPage() {
  const { t } = useTranslation();
  const search = listRoute.useSearch();
  const navigate = useNavigate();
  return (
    <div className="flex flex-col gap-4">
      <PageHeader title={t("db.title")} subtitle={t("db.subtitle")} />
      <DbInstances
        range={rangeOf(search)}
        onOpen={(i) => void navigate({ to: "/databases/instance", search: (prev: Record<string, unknown>) => ({ ...rangeOf(prev as DatabasesSearch), instance: i.instance }) as never })}
      />
    </div>
  );
}

export function DatabaseInstancePage() {
  const { t } = useTranslation();
  const search = instanceRoute.useSearch();
  const navigate = useNavigate();
  const instance = search.instance ?? "";
  const tab = search.tab ?? "activity";
  const setSearch = (patch: Partial<DatabasesSearch>) => void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, ...patch }) as never, replace: true });
  const openQuery = (fp: string) =>
    void navigate({ to: "/databases/query", search: (prev: Record<string, unknown>) => ({ ...rangeOf(prev as DatabasesSearch), instance, fp }) as never });
  return (
    <div className="flex flex-col gap-3">
      <Button asChild variant="ghost" size="sm" className="w-fit px-0 text-muted-foreground hover:text-foreground">
        <Link to="/databases" search={(prev) => rangeOf(prev as DatabasesSearch) as never}>
          <ArrowLeft className="size-4" aria-hidden="true" />
          {t("db.back")}
        </Link>
      </Button>
      <PageHeader title={<DbInstanceTitle instance={instance} range={rangeOf(search)} />} subtitle={t("db.instanceSubtitle")} />
      <Tabs value={tab} onValueChange={(v) => setSearch({ tab: v as DatabasesSearch["tab"] })}>
        <TabsList>
          <TabsTrigger value="activity">{t("db.tabs.activity")}</TabsTrigger>
          <TabsTrigger value="queries">{t("db.tabs.queries")}</TabsTrigger>
          <TabsTrigger value="sessions">{t("db.tabs.sessions")}</TabsTrigger>
        </TabsList>
        <TabsContent value="activity">{tab === "activity" && <DbActivityTab instance={instance} range={rangeOf(search)} onOpenQuery={openQuery} />}</TabsContent>
        <TabsContent value="queries">
          {tab === "queries" && (
            <DbQueriesTab
              instance={instance}
              range={rangeOf(search)}
              sort={search.sort ?? "time"}
              q={search.q ?? ""}
              onSort={(sort: DbQuerySort) => setSearch({ sort })}
              onSearch={(q) => setSearch({ q: q || undefined })}
              onOpen={openQuery}
            />
          )}
        </TabsContent>
        <TabsContent value="sessions">{tab === "sessions" && <DbSessionsTab instance={instance} onOpenQuery={openQuery} />}</TabsContent>
      </Tabs>
    </div>
  );
}

export function DatabaseQueryPage() {
  const { t } = useTranslation();
  const search = queryRoute.useSearch();
  const instance = search.instance ?? "";
  return (
    <div className="flex flex-col gap-3">
      <Button asChild variant="ghost" size="sm" className="w-fit px-0 text-muted-foreground hover:text-foreground">
        <Link to="/databases/instance" search={(prev) => ({ ...rangeOf(prev as DatabasesSearch), instance, tab: "queries" }) as never}>
          <ArrowLeft className="size-4" aria-hidden="true" />
          {t("db.query.back", { instance })}
        </Link>
      </Button>
      <PageHeader title={t("db.query.title")} subtitle={instance} />
      {search.fp ? <DbQueryDetail instance={instance} fingerprint={search.fp} range={rangeOf(search)} /> : null}
    </div>
  );
}
