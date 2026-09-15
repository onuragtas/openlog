import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  addDashboardWidget,
  createDashboard,
  dashboardQuery,
  dashboardsQuery,
  isDashboardsUnavailable,
  type Dashboard,
  type DashboardUnit,
  type DashboardVisualization,
  type DashboardWidgetInput,
  type DashboardWidgetOptions,
} from "@/api/dashboards";
import { FormError } from "@/components/settings/common";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { defaultLayout, VISUALIZATIONS } from "@/lib/dashboards";

export interface AddToDashboardDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  query: string;
  defaultTitle?: string;
  defaultVisualization?: DashboardVisualization;
  /** Widget unit and options (e.g. stacked bars of an explorer chart); not editable here. */
  defaultUnit?: DashboardUnit;
  defaultOptions?: DashboardWidgetOptions;
}

/** Adds a query as a widget to an existing dashboard (POST …/widgets) or to a new one (POST /dashboards). */
export function AddToDashboardDialog({ open, onOpenChange, query, defaultTitle = "", defaultVisualization = "line", defaultUnit = "", defaultOptions }: AddToDashboardDialogProps) {
  const { t } = useTranslation();
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" title={t("oql.addToDashboard.title")} closeLabel={t("common.close")} className="w-full max-w-full sm:max-w-md">
        {open && (
          <AddToDashboardForm query={query} defaultTitle={defaultTitle} defaultVisualization={defaultVisualization} unit={defaultUnit} options={defaultOptions} onDone={() => onOpenChange(false)} />
        )}
      </SheetContent>
    </Sheet>
  );
}

function AddToDashboardForm({
  query,
  defaultTitle,
  defaultVisualization,
  unit,
  options,
  onDone,
}: {
  query: string;
  defaultTitle: string;
  defaultVisualization: DashboardVisualization;
  unit: DashboardUnit;
  options?: DashboardWidgetOptions;
  onDone: () => void;
}) {
  const { t } = useTranslation();
  const uid = useId();
  const queryClient = useQueryClient();
  const list = useQuery(dashboardsQuery());
  const editable = (list.data ?? []).filter((d) => d.can_edit);
  const [chosenMode, setMode] = useState<"existing" | "new">("existing");
  const [chosenDashboard, setDashboardId] = useState("");
  const [pageId, setPageId] = useState("");
  const [name, setName] = useState("");
  const [title, setTitle] = useState(defaultTitle);
  const [visualization, setVisualization] = useState<DashboardVisualization>(defaultVisualization);
  const [done, setDone] = useState<Dashboard | null>(null);
  // Without editable dashboards only "new" is possible; the first editable dashboard is preselected.
  const mode = list.data && editable.length === 0 ? "new" : chosenMode;
  const dashboardId = chosenDashboard || editable[0]?.id || "";
  const detail = useQuery({ ...dashboardQuery(dashboardId), enabled: mode === "existing" && dashboardId !== "" });

  const save = useMutation({
    mutationFn: async () => {
      const widget: DashboardWidgetInput = { title: title.trim(), visualization, layout: defaultLayout(visualization, []), query, unit, thresholds: [], options: options ?? { legend: true } };
      if (mode === "existing") return addDashboardWidget(dashboardId, widget, pageId || undefined);
      return createDashboard({ name: name.trim(), description: "", visibility: "org", pages: [{ name: t("dashboards.defaultPage"), widgets: [widget] }] });
    },
    onSuccess: (d) => {
      setDone(d);
      queryClient.setQueryData(dashboardQuery(d.id).queryKey, d);
      void queryClient.invalidateQueries({ queryKey: ["dashboards", "list"] });
    },
  });

  if (list.isPending) return <LoadingState />;
  if (list.isError) {
    return isDashboardsUnavailable(list.error) ? <EmptyState className="p-4">{t("dashboards.unavailable")}</EmptyState> : <ErrorState error={list.error} onRetry={() => void list.refetch()} />;
  }
  if (done) {
    return (
      <div className="flex flex-col gap-3 p-4" role="status">
        <p className="text-sm">{t("oql.addToDashboard.added", { name: done.name })}</p>
        <div className="flex flex-wrap gap-2">
          <Button asChild>
            <Link to="/dashboards/$dashboardId" params={{ dashboardId: done.id }} search={(prev: Record<string, unknown>) => ({ range: prev.range, from: prev.from, to: prev.to }) as never}>
              {t("oql.addToDashboard.open")}
            </Link>
          </Button>
          <Button variant="outline" onClick={onDone}>
            {t("common.close")}
          </Button>
        </div>
      </div>
    );
  }

  const invalid = (mode === "new" && !name.trim()) || (mode === "existing" && !dashboardId) || !query.trim();
  return (
    <form
      className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-4"
      onSubmit={(e) => {
        e.preventDefault();
        if (!invalid) save.mutate();
      }}
    >
      <fieldset className="flex flex-col gap-2">
        <legend className="mb-1 text-sm font-medium">{t("oql.addToDashboard.target")}</legend>
        <label className="flex items-center gap-2 text-sm">
          <input type="radio" name={`${uid}-mode`} checked={mode === "existing"} disabled={editable.length === 0} onChange={() => setMode("existing")} />
          {t("oql.addToDashboard.existing")}
        </label>
        <label className="flex items-center gap-2 text-sm">
          <input type="radio" name={`${uid}-mode`} checked={mode === "new"} onChange={() => setMode("new")} />
          {t("oql.addToDashboard.new")}
        </label>
      </fieldset>
      {mode === "existing" ? (
        <>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${uid}-dash`}>{t("oql.addToDashboard.dashboard")}</Label>
            <NativeSelect id={`${uid}-dash`} value={dashboardId} onChange={(e) => (setDashboardId(e.target.value), setPageId(""))}>
              {editable.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.name}
                </option>
              ))}
            </NativeSelect>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${uid}-page`}>{t("oql.addToDashboard.page")}</Label>
            <NativeSelect id={`${uid}-page`} value={pageId} onChange={(e) => setPageId(e.target.value)} disabled={!detail.data}>
              {(detail.data?.pages ?? []).map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </NativeSelect>
          </div>
        </>
      ) : (
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${uid}-name`}>{t("dashboards.fields.name")}</Label>
          <Input id={`${uid}-name`} value={name} onChange={(e) => setName(e.target.value)} maxLength={200} />
        </div>
      )}
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${uid}-title`}>{t("dashboards.widget.title")}</Label>
        <Input id={`${uid}-title`} value={title} onChange={(e) => setTitle(e.target.value)} />
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${uid}-vis`}>{t("dashboards.widget.visualization")}</Label>
        <NativeSelect id={`${uid}-vis`} value={visualization} onChange={(e) => setVisualization(e.target.value as DashboardVisualization)}>
          {VISUALIZATIONS.filter((v) => v !== "markdown").map((v) => (
            <option key={v} value={v}>
              {t(`dashboards.visualizations.${v}`)}
            </option>
          ))}
        </NativeSelect>
      </div>
      <pre className="max-h-32 overflow-auto rounded-md bg-muted p-2 font-mono text-xs whitespace-pre-wrap">{query}</pre>
      <FormError error={save.error} />
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={invalid || save.isPending}>
          {t("oql.addToDashboard.submit")}
        </Button>
        <Button type="button" variant="outline" onClick={onDone}>
          {t("common.cancel")}
        </Button>
      </div>
    </form>
  );
}
