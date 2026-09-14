import { useQueries, useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { DashboardWidget } from "@/api/dashboards";
import { renderDashboard, renderWidgetResult, takeRenderToken } from "@/api/dashboardRender";
import { QueryResult } from "@/components/oql/QueryResult";

/** Widgets captured per render (openlog-renderer caps responses at 60 images). */
export const MAX_PRINT_WIDGETS = 60;

const plotHeight = (w: DashboardWidget) => (w.visualization === "billboard" ? 96 : w.visualization === "table" ? 320 : 240);

function setRenderState(state: "ready" | "error", message = "") {
  const root = document.documentElement;
  root.dataset.renderMessage = message;
  root.dataset.renderState = state;
}

/**
 * Print view of a scheduled report's dashboard for openlog-renderer (D-097): every query widget in its own fixed-width
 * block marked `data-render-id`, light theme, no navigation. When all results settled the page sets
 * `document.documentElement.dataset.renderState` to "ready" ("error" when the dashboard cannot be loaded); widgets whose
 * query failed carry `data-render-error` and are sent as tables instead.
 */
export function PrintDashboardView() {
  const { t } = useTranslation();
  const [token] = useState(() => takeRenderToken());
  const dash = useQuery({ queryKey: ["render-dashboard"], queryFn: ({ signal }) => renderDashboard(token ?? "", signal), enabled: token !== null, retry: false, staleTime: Infinity });
  const widgets = (dash.data?.pages ?? [])
    .flatMap((p) => p.widgets)
    .filter((w) => w.visualization !== "markdown")
    .slice(0, MAX_PRINT_WIDGETS)
    .map((w): DashboardWidget => ({ ...w, query: "" }));
  const results = useQueries({
    queries: widgets.map((w) => ({
      queryKey: ["render-dashboard", "widget", w.id],
      queryFn: ({ signal }: { signal: AbortSignal }) => renderWidgetResult(token ?? "", w.id, signal),
      retry: false,
      staleTime: Infinity,
    })),
  });
  const settled = dash.isSuccess && results.every((r) => !r.isPending);
  const failed = token === null || dash.isError;

  useEffect(() => {
    document.documentElement.classList.remove("dark");
  }, []);
  useEffect(() => {
    if (failed) {
      setRenderState("error", token === null ? "missing render token" : String(dash.error?.message ?? "dashboard unavailable"));
      return;
    }
    if (!settled) return;
    // Two frames: charts measure their container and draw after the results arrive.
    let id = requestAnimationFrame(() => {
      id = requestAnimationFrame(() => setRenderState("ready"));
    });
    return () => cancelAnimationFrame(id);
  }, [failed, settled, token, dash.error]);

  return (
    <main className="min-h-dvh bg-white text-foreground" data-testid="print-dashboard">
      {dash.data && <h1 className="sr-only">{dash.data.name}</h1>}
      <div className="flex w-full flex-col gap-4 p-0">
        {widgets.map((w, i) => {
          const r = results[i];
          return (
            <section
              key={w.id}
              data-render-id={w.id}
              data-render-error={r?.isError ? "1" : undefined}
              aria-label={w.title || t("dashboards.widget.untitled")}
              className="w-full bg-white p-3"
            >
              <QueryResult
                result={r?.data}
                visualization={w.visualization}
                title={w.title || t("dashboards.widget.untitled")}
                unit={w.unit}
                thresholds={w.thresholds}
                options={w.options}
                height={plotHeight(w)}
                isLoading={r?.isPending}
                error={r?.error}
              />
            </section>
          );
        })}
      </div>
    </main>
  );
}
