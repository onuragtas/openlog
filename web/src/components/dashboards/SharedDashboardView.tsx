import { useQuery } from "@tanstack/react-query";
import { Eye } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiError } from "@/api/client";
import type { DashboardWidget } from "@/api/dashboards";
import { sharedDashboardQuery, sharedWidgetResultQuery, type SharedDashboard } from "@/api/dashboardSharing";
import { Markdown } from "@/components/oql/Markdown";
import { QueryResult } from "@/components/oql/QueryResult";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ROW_HEIGHT } from "@/lib/dashboards";
import { formatDateTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { DashboardGrid } from "./DashboardGrid";

const parseTs = (s: string) => Date.parse(s.replace(/(\.\d{3})\d+/, "$1"));
const chartHeightFor = (h: number) => Math.max(80, h * ROW_HEIGHT + (h - 1) * 12 - 110);

/** Page frame of the public share view: no navigation, no links into the app. */
function SharedShell({ children }: { children: React.ReactNode }) {
  const { t } = useTranslation();
  return (
    <div className="min-h-dvh bg-background text-foreground">
      <header className="flex min-h-12 items-center gap-2 border-b px-4 text-sm">
        <span className="font-semibold">{t("app.name")}</span>
        <span className="inline-flex items-center gap-1 text-muted-foreground">
          <Eye className="size-4" aria-hidden="true" />
          {t("dashboards.shared.readOnly")}
        </span>
      </header>
      <main id="main" className="mx-auto flex w-full max-w-screen-2xl min-w-0 flex-col gap-4 p-4">
        {children}
      </main>
    </div>
  );
}

/** Read-only dashboard of a public share link (api.md "Share links"): fixed or relative range and locked variables. */
export function SharedDashboardView({ token }: { token: string }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const q = useQuery(sharedDashboardQuery(token));
  const [pageId, setPageId] = useState<string | null>(null);

  if (q.isPending) {
    return (
      <SharedShell>
        <LoadingState />
      </SharedShell>
    );
  }
  if (q.isError) {
    const gone = q.error instanceof ApiError && q.error.status === 404;
    return (
      <SharedShell>
        {gone ? (
          <EmptyState>
            <h1 className="mb-1 text-base font-semibold text-foreground">{t("dashboards.shared.invalidTitle")}</h1>
            <p>{t("dashboards.shared.invalid")}</p>
          </EmptyState>
        ) : (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        )}
      </SharedShell>
    );
  }
  const d = q.data;
  const page = d.pages.find((p) => p.id === pageId) ?? d.pages[0];
  const relative = d.time_range.range !== null;
  const rangeText = relative
    ? t("dashboards.share.rangeRelative", { range: d.time_range.range ?? "" })
    : t("dashboards.share.rangeFixed", { from: formatDateTime(parseTs(d.time_range.from ?? ""), locale), to: formatDateTime(parseTs(d.time_range.to ?? ""), locale) });

  return (
    <SharedShell>
      <div className="flex flex-col gap-1" data-testid="shared-dashboard">
        <h1 className="text-xl font-semibold tracking-tight break-words">{d.name}</h1>
        {d.description && <p className="text-sm break-words text-muted-foreground">{d.description}</p>}
        <p className="text-xs break-words text-muted-foreground">
          {rangeText} · {t("dashboards.share.expires", { time: formatDateTime(parseTs(d.expires_at), locale) })}
        </p>
        <LockedVariables variables={d.variables} />
      </div>
      {d.pages.length > 1 && page && (
        <Tabs value={page.id} onValueChange={setPageId}>
          <TabsList aria-label={t("dashboards.pages")}>
            {d.pages.map((p) => (
              <TabsTrigger key={p.id} value={p.id}>
                {p.name}
              </TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
      )}
      {!page || page.widgets.length === 0 ? (
        <EmptyState>{t("dashboards.emptyPage")}</EmptyState>
      ) : (
        <DashboardGrid
          key={page.id}
          widgets={page.widgets.map((w): DashboardWidget => ({ ...w, query: "" }))}
          edit={false}
          onLayoutChange={() => {}}
          renderWidget={(w, mode) => <SharedWidgetCard token={token} widget={w} relative={relative} fill={mode === "grid"} />}
        />
      )}
    </SharedShell>
  );
}

function LockedVariables({ variables }: { variables: SharedDashboard["variables"] }) {
  const { t } = useTranslation();
  if (variables.length === 0) return null;
  return (
    <ul className="flex flex-wrap gap-2 pt-1 text-xs" aria-label={t("dashboards.variables.bar")} data-testid="shared-variables">
      {variables.map((v) => (
        <li key={v.name} className="max-w-full truncate rounded-full border bg-muted px-2.5 py-0.5">
          <span className="text-muted-foreground">{v.label || v.name}:</span> {v.values.length > 0 ? v.values.join(", ") : t("dashboards.shared.all")}
        </li>
      ))}
    </ul>
  );
}

function SharedWidgetCard({ token, widget, relative, fill }: { token: string; widget: DashboardWidget; relative: boolean; fill: boolean }) {
  const { t } = useTranslation();
  const titleId = useId();
  const markdown = widget.visualization === "markdown";
  const q = useQuery({ ...sharedWidgetResultQuery(token, widget.id, relative ? 60_000 : false), enabled: !markdown });
  const title = widget.title || t("dashboards.widget.untitled");
  return (
    <section
      aria-labelledby={titleId}
      data-testid="dashboard-widget"
      data-visualization={widget.visualization}
      className={cn("flex min-w-0 flex-col rounded-xl border bg-card text-card-foreground shadow-xs", fill ? "h-full" : "")}
      style={fill ? undefined : { minHeight: widget.layout.h * ROW_HEIGHT }}
    >
      <header className="flex min-h-10 items-center border-b px-3 py-1.5">
        <h2 id={titleId} className="min-w-0 flex-1 truncate text-sm font-semibold" title={title}>
          {title}
        </h2>
      </header>
      <div className={cn("min-h-0 min-w-0 flex-1 overflow-auto p-3", widget.visualization === "billboard" && "flex flex-col justify-center")}>
        {markdown ? (
          <Markdown text={widget.markdown} />
        ) : (
          <QueryResult
            result={q.data}
            visualization={widget.visualization}
            title={title}
            unit={widget.unit}
            thresholds={widget.thresholds}
            options={widget.options}
            height={chartHeightFor(widget.layout.h)}
            isLoading={q.isFetching}
            error={q.error}
            onRetry={() => void q.refetch()}
          />
        )}
      </div>
    </section>
  );
}
