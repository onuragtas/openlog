// Browser screens (/rum, /rum/$app, /rum/$app/sessions/$sessionId): the browser applications, one
// application's Core Web Vitals, routes and sessions, and one session's timeline (docs/contracts/rum.md §7).
// Browser errors deliberately live in the APM error inbox of the same application rather than in a second
// inbox here (§7), so the detail links there instead of listing them again.
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { RumPageSort } from "@/api/rum";
import { PageHeader } from "@/components/AppShell";
import { RumApps } from "@/components/rum/RumApps";
import { RumOverviewTab } from "@/components/rum/RumOverviewTab";
import { RumPagesTab } from "@/components/rum/RumPagesTab";
import { RumSessionDetail } from "@/components/rum/RumSessionDetail";
import { RumSessionsTab } from "@/components/rum/RumSessionsTab";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { RumSearch } from "@/router";

const listRoute = getRouteApi("/app/rum");
const appRouteApi = getRouteApi("/app/rum/$app");
const sessionRoute = getRouteApi("/app/rum/$app/sessions/$sessionId");

/** The range lives in the URL like on every other screen, so a link keeps the window it was opened with. */
const rangeOf = (s: { range?: string; from?: string; to?: string }) => ({ range: s.range, from: s.from, to: s.to });

export function RumPage() {
  const { t } = useTranslation();
  const search = listRoute.useSearch();
  const navigate = useNavigate();

  return (
    <div className="flex flex-col gap-4">
      <PageHeader title={t("rum.title")} subtitle={t("rum.subtitle")} />
      <RumApps
        range={rangeOf(search)}
        onOpen={(a) =>
          void navigate({
            to: "/rum/$app",
            params: { app: a.app },
            search: (prev: Record<string, unknown>) => ({ ...rangeOf(prev as RumSearch), env: a.environment || undefined }) as never,
          })
        }
      />
    </div>
  );
}

export function RumAppPage() {
  const { t } = useTranslation();
  const { app } = appRouteApi.useParams();
  const search = appRouteApi.useSearch();
  const navigate = useNavigate();
  const range = rangeOf(search);
  const tab = search.tab ?? "overview";
  const setSearch = (patch: Partial<RumSearch>) => void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, ...patch }) as never, replace: true });

  return (
    <div className="flex flex-col gap-3">
      <Button asChild variant="ghost" size="sm" className="w-fit px-0 text-muted-foreground hover:text-foreground">
        <Link to="/rum" search={(prev) => rangeOf(prev as RumSearch) as never}>
          <ArrowLeft className="size-4" aria-hidden="true" />
          {t("rum.back")}
        </Link>
      </Button>
      <PageHeader title={app} subtitle={search.env ? t("rum.appSubtitleEnv", { environment: search.env }) : t("rum.appSubtitle")} />
      <Link to="/apm/errors" search={(prev) => ({ ...rangeOf(prev as RumSearch), env: search.env }) as never} className="w-fit text-sm text-primary hover:underline">
        {t("rum.errorsInbox")}
      </Link>

      <Tabs value={tab} onValueChange={(v) => setSearch({ tab: v as RumSearch["tab"] })}>
        <TabsList>
          <TabsTrigger value="overview">{t("rum.tabs.overview")}</TabsTrigger>
          <TabsTrigger value="pages">{t("rum.tabs.pages")}</TabsTrigger>
          <TabsTrigger value="sessions">{t("rum.tabs.sessions")}</TabsTrigger>
        </TabsList>
        <TabsContent value="overview">{tab === "overview" && <RumOverviewTab range={range} app={app} environment={search.env} />}</TabsContent>
        <TabsContent value="pages">
          {tab === "pages" && (
            <RumPagesTab range={range} app={app} environment={search.env} sort={search.sort ?? "views"} onSort={(sort: RumPageSort) => setSearch({ sort })} />
          )}
        </TabsContent>
        <TabsContent value="sessions">
          {tab === "sessions" && (
            <RumSessionsTab
              range={range}
              app={app}
              environment={search.env}
              onOpen={(s) =>
                void navigate({
                  to: "/rum/$app/sessions/$sessionId",
                  params: { app, sessionId: s.session_id },
                  search: (prev: Record<string, unknown>) => ({ ...rangeOf(prev as RumSearch), env: search.env }) as never,
                })
              }
            />
          )}
        </TabsContent>
      </Tabs>
    </div>
  );
}

export function RumSessionPage() {
  const { t } = useTranslation();
  const { app, sessionId } = sessionRoute.useParams();
  const search = sessionRoute.useSearch();

  return (
    <div className="flex flex-col gap-3">
      <Button asChild variant="ghost" size="sm" className="w-fit px-0 text-muted-foreground hover:text-foreground">
        <Link to="/rum/$app" params={{ app }} search={(prev) => ({ ...rangeOf(prev as RumSearch), env: search.env, tab: "sessions" }) as never}>
          <ArrowLeft className="size-4" aria-hidden="true" />
          {t("rum.session.back")}
        </Link>
      </Button>
      <PageHeader title={t("rum.session.title")} subtitle={sessionId} />
      <RumSessionDetail range={rangeOf(search)} sessionId={sessionId} />
    </div>
  );
}
