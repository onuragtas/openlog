import { Plus, Trash2 } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import type { DashboardThreshold, DashboardUnit, DashboardVisualization, DashboardWidget } from "@/api/dashboards";
import type { OqlVariables } from "@/api/oql";
import { OqlEditor } from "@/components/oql/OqlEditor";
import { FormError } from "@/components/settings/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { UNITS, VISUALIZATIONS } from "@/lib/dashboards";
import type { RangeSpec } from "@/lib/time";
import { useDebounced } from "@/lib/use-debounced";
import { WidgetBody } from "./WidgetCard";

export interface WidgetEditorProps {
  widget: DashboardWidget | null;
  isNew: boolean;
  range: RangeSpec;
  variables: OqlVariables;
  saving: boolean;
  error: unknown;
  onOpenChange: (open: boolean) => void;
  onSave: (widget: DashboardWidget) => void;
}

/** Drawer to create or change a widget with a live preview (current range and variables). */
export function WidgetEditor({ widget, isNew, range, variables, saving, error, onOpenChange, onSave }: WidgetEditorProps) {
  const { t } = useTranslation();
  return (
    <Sheet open={widget !== null} onOpenChange={onOpenChange}>
      <SheetContent side="right" title={isNew ? t("dashboards.widget.addTitle") : t("dashboards.widget.editTitle")} closeLabel={t("common.close")} className="w-full max-w-full sm:max-w-2xl">
        {widget && <WidgetForm key={widget.id} initial={widget} range={range} variables={variables} saving={saving} error={error} onCancel={() => onOpenChange(false)} onSave={onSave} />}
      </SheetContent>
    </Sheet>
  );
}

const CHART_VIS: readonly DashboardVisualization[] = ["line", "area", "bar"];

function WidgetForm({ initial, range, variables, saving, error, onCancel, onSave }: { initial: DashboardWidget; range: RangeSpec; variables: OqlVariables; saving: boolean; error: unknown; onCancel: () => void; onSave: (w: DashboardWidget) => void }) {
  const { t } = useTranslation();
  const uid = useId();
  const [draft, setDraft] = useState<DashboardWidget>(initial);
  const preview = useDebounced(draft, 500);
  const set = (patch: Partial<DashboardWidget>) => setDraft((d) => ({ ...d, ...patch }));
  const setThreshold = (i: number, patch: Partial<DashboardThreshold>) => set({ thresholds: draft.thresholds.map((x, j) => (j === i ? { ...x, ...patch } : x)) });
  const markdown = draft.visualization === "markdown";
  const invalid = markdown ? draft.markdown.length > 20000 : !draft.query.trim();

  return (
    <form
      className="flex min-h-0 flex-1 flex-col"
      onSubmit={(e) => {
        e.preventDefault();
        if (!invalid) onSave({ ...draft, title: draft.title.trim() });
      }}
    >
      <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${uid}-title`}>{t("dashboards.widget.title")}</Label>
            <Input id={`${uid}-title`} value={draft.title} onChange={(e) => set({ title: e.target.value })} maxLength={200} />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${uid}-vis`}>{t("dashboards.widget.visualization")}</Label>
            <NativeSelect id={`${uid}-vis`} value={draft.visualization} onChange={(e) => set({ visualization: e.target.value as DashboardVisualization })}>
              {VISUALIZATIONS.map((v) => (
                <option key={v} value={v}>
                  {t(`dashboards.visualizations.${v}`)}
                </option>
              ))}
            </NativeSelect>
          </div>
        </div>

        {markdown ? (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${uid}-md`}>{t("dashboards.widget.markdown")}</Label>
            <textarea
              id={`${uid}-md`}
              rows={8}
              value={draft.markdown}
              onChange={(e) => set({ markdown: e.target.value })}
              className="rounded-md border border-input bg-background px-3 py-2 font-mono text-sm shadow-xs"
            />
            <p className="text-xs text-muted-foreground">{t("dashboards.widget.markdownHint")}</p>
          </div>
        ) : (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${uid}-query`}>{t("dashboards.widget.query")}</Label>
            <OqlEditor id={`${uid}-query`} label={t("dashboards.widget.query")} value={draft.query} onChange={(query) => set({ query })} variables={variables} minRows={4} />
          </div>
        )}

        {!markdown && (
          <>
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="flex flex-col gap-1.5">
                <Label htmlFor={`${uid}-unit`}>{t("dashboards.widget.unit")}</Label>
                <NativeSelect id={`${uid}-unit`} value={draft.unit} onChange={(e) => set({ unit: e.target.value as DashboardUnit })}>
                  {UNITS.map((u) => (
                    <option key={u} value={u}>
                      {t(`dashboards.units.${u || "auto"}`)}
                    </option>
                  ))}
                </NativeSelect>
              </div>
              <fieldset className="flex flex-col gap-1.5">
                <legend className="mb-1.5 text-sm font-medium">{t("dashboards.widget.options")}</legend>
                {CHART_VIS.includes(draft.visualization) && (
                  <label className="flex items-center gap-2 text-sm">
                    <input type="checkbox" checked={draft.visualization === "area" ? draft.options.stacked !== false : !!draft.options.stacked} onChange={(e) => set({ options: { ...draft.options, stacked: e.target.checked } })} />
                    {t("dashboards.widget.stacked")}
                  </label>
                )}
                {[...CHART_VIS, "pie"].includes(draft.visualization) && (
                  <label className="flex items-center gap-2 text-sm">
                    <input type="checkbox" checked={draft.options.legend !== false} onChange={(e) => set({ options: { ...draft.options, legend: e.target.checked } })} />
                    {t("dashboards.widget.legend")}
                  </label>
                )}
              </fieldset>
            </div>

            <fieldset className="flex flex-col gap-2">
              <div className="flex items-center justify-between">
                <legend className="text-sm font-medium">{t("dashboards.widget.thresholds")}</legend>
                <Button type="button" variant="outline" size="sm" disabled={draft.thresholds.length >= 10} onClick={() => set({ thresholds: [...draft.thresholds, { value: 0, severity: "warning" }] })}>
                  <Plus aria-hidden="true" />
                  {t("dashboards.widget.addThreshold")}
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">{t("dashboards.widget.thresholdsHint")}</p>
              {draft.thresholds.map((th, i) => (
                <div key={i} className="flex flex-wrap items-center gap-2" data-testid="threshold-row">
                  <Input
                    type="number"
                    step="any"
                    className="w-32"
                    aria-label={t("dashboards.widget.thresholdValue", { n: i + 1 })}
                    value={Number.isFinite(th.value) ? th.value : ""}
                    onChange={(e) => setThreshold(i, { value: e.target.valueAsNumber })}
                  />
                  <NativeSelect aria-label={t("dashboards.widget.thresholdSeverity", { n: i + 1 })} value={th.severity} onChange={(e) => setThreshold(i, { severity: e.target.value as DashboardThreshold["severity"] })}>
                    <option value="warning">{t("oql.result.threshold.warning")}</option>
                    <option value="critical">{t("oql.result.threshold.critical")}</option>
                  </NativeSelect>
                  <Button type="button" variant="ghost" size="icon" aria-label={t("dashboards.widget.removeThreshold", { n: i + 1 })} onClick={() => set({ thresholds: draft.thresholds.filter((_, j) => j !== i) })}>
                    <Trash2 aria-hidden="true" />
                  </Button>
                </div>
              ))}
            </fieldset>
          </>
        )}

        <section aria-label={t("dashboards.widget.preview")} className="flex min-w-0 flex-col gap-2 rounded-lg border p-3" data-testid="widget-preview">
          <h3 className="text-sm font-semibold">{t("dashboards.widget.preview")}</h3>
          <WidgetBody widget={{ ...preview, thresholds: preview.thresholds.filter((x) => Number.isFinite(x.value)) }} range={range} variables={variables} height={200} />
        </section>
      </div>
      <div className="flex flex-wrap items-center gap-2 border-t p-4">
        <Button type="submit" disabled={invalid || saving}>
          {t("dashboards.widget.save")}
        </Button>
        <Button type="button" variant="outline" onClick={onCancel}>
          {t("common.cancel")}
        </Button>
        <FormError error={error} />
      </div>
    </form>
  );
}
