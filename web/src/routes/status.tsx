// Public status page (docs/contracts/api.md "Status page", D-108): no session, no app navigation.
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { statusPageQuery, type StatusIncident, type StatusPage as StatusPageData } from "@/api/statusPage";
import { DateTimeText } from "@/components/settings/common";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { dayTone, formatUptime, statusTone } from "@/lib/status-page";
import { cn } from "@/lib/utils";

type ComponentID = StatusPageData["components"][number]["id"];

export function StatusPage() {
  const { t } = useTranslation();
  const q = useQuery(statusPageQuery());
  return (
    <div className="min-h-screen bg-background text-foreground">
      <main className="mx-auto flex w-full max-w-3xl flex-col gap-6 px-4 py-8">
        <header className="flex flex-wrap items-baseline justify-between gap-2">
          <h1 className="text-2xl font-semibold">{t("statusPage.title")}</h1>
          <span className="text-sm text-muted-foreground">openlog</span>
        </header>
        {q.isPending ? (
          <LoadingState />
        ) : q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : (
          <StatusBody page={q.data} />
        )}
      </main>
    </div>
  );
}

function StatusBody({ page }: { page: StatusPageData }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  return (
    <>
      <section aria-live="polite" className={cn("flex flex-wrap items-center gap-3 rounded-xl border p-4", statusTone(page.status).banner)}>
        <span className={cn("size-3 rounded-full", statusTone(page.status).dot)} aria-hidden="true" />
        <span className="text-lg font-medium">{t(`statusPage.overall.${page.status}`)}</span>
        {page.checked_at && (
          <span className="text-xs text-muted-foreground sm:ml-auto">
            {t("statusPage.checked")} <DateTimeText value={page.checked_at} relative />
          </span>
        )}
      </section>

      {page.incidents.length > 0 && <IncidentSection title={t("statusPage.incidents")} items={page.incidents} />}
      {page.maintenance.length > 0 && <IncidentSection title={t("statusPage.maintenance")} items={page.maintenance} />}

      <ul className="divide-y rounded-xl border bg-card">
        {page.components.map((c) => (
          <li key={c.id} className="p-4">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="font-medium">{t(`statusPage.components.${c.id}`)}</span>
              <span className="inline-flex items-center gap-2 text-sm">
                <span className={cn("size-2.5 rounded-full", statusTone(c.status).dot)} aria-hidden="true" />
                {t(`statusPage.status.${c.status}`)}
              </span>
            </div>
            <div className="mt-3 flex h-8 gap-px" role="img" aria-label={t("statusPage.uptime", { value: formatUptime(c.uptime_90d, locale) })}>
              {c.days.map((d, i) => (
                <span
                  key={d.date}
                  title={`${d.date}: ${t(`statusPage.dayStatus.${d.status}`)}${d.uptime !== null ? ` (${formatUptime(d.uptime, locale)})` : ""}`}
                  className={cn("flex-1 rounded-[1px]", dayTone(d.status), i < c.days.length - 30 && "hidden sm:block")}
                />
              ))}
            </div>
            <div className="mt-1 flex justify-between gap-2 text-xs text-muted-foreground">
              <span className="sm:hidden">{t("statusPage.days30")}</span>
              <span className="hidden sm:inline">{t("statusPage.days90")}</span>
              <span>{t("statusPage.uptime", { value: formatUptime(c.uptime_90d, locale) })}</span>
              <span>{t("statusPage.today")}</span>
            </div>
          </li>
        ))}
      </ul>

      <IncidentSection title={t("statusPage.history")} items={page.history} empty={t("statusPage.noIncidents")} />
    </>
  );
}

function IncidentSection({ title, items, empty }: { title: string; items: StatusIncident[]; empty?: string }) {
  const { t } = useTranslation();
  return (
    <section className="flex flex-col gap-3">
      <h2 className="text-lg font-semibold">{title}</h2>
      {items.length === 0 && empty && <p className="text-sm text-muted-foreground">{empty}</p>}
      {items.map((inc) => (
        <article key={inc.id} className="rounded-xl border bg-card p-4 text-sm">
          <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
            <h3 className="font-medium">{inc.title}</h3>
            <span className="text-xs text-muted-foreground">{t(`statusPage.incidentStatus.${inc.status}`)}</span>
            {inc.impact !== "none" && <span className="text-xs text-muted-foreground">{t(`statusPage.impact.${inc.impact}`)}</span>}
          </div>
          <p className="mt-1 text-xs text-muted-foreground">
            <DateTimeText value={inc.starts_at} />
            {inc.ends_at && (
              <>
                {" – "}
                <DateTimeText value={inc.ends_at} />
              </>
            )}
            {inc.components.length > 0 && ` · ${t("statusPage.affects", { components: inc.components.map((c) => t(`statusPage.components.${c as ComponentID}`)).join(", ") })}`}
          </p>
          {inc.updates.length > 0 && (
            <ol className="mt-2 flex flex-col gap-2 border-l pl-3">
              {inc.updates.map((u) => (
                <li key={u.id}>
                  <span className="font-medium">{t(`statusPage.incidentStatus.${u.status as StatusIncident["status"]}`)}</span> — <span className="whitespace-pre-wrap">{u.message}</span>
                  <div className="text-xs text-muted-foreground">
                    <DateTimeText value={u.created_at} />
                  </div>
                </li>
              ))}
            </ol>
          )}
        </article>
      ))}
    </section>
  );
}
