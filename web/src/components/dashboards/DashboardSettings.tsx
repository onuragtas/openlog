import { ArrowDown, ArrowUp, Plus, Trash2 } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Dashboard, DashboardPage, DashboardVariable, DashboardVisibility } from "@/api/dashboards";
import { OqlEditor } from "@/components/oql/OqlEditor";
import { FormError } from "@/components/settings/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { DRAFT_ID_PREFIX, emptyVariable, VARIABLE_NAME } from "@/lib/dashboards";

export interface DashboardSettingsProps {
  dashboard: Dashboard | null;
  saving: boolean;
  error: unknown;
  onOpenChange: (open: boolean) => void;
  onSave: (next: Dashboard) => void;
}

export function DashboardSettings({ dashboard, saving, error, onOpenChange, onSave }: DashboardSettingsProps) {
  const { t } = useTranslation();
  return (
    <Sheet open={dashboard !== null} onOpenChange={onOpenChange}>
      <SheetContent side="right" title={t("dashboards.settings.title")} closeLabel={t("common.close")} className="w-full max-w-full sm:max-w-2xl">
        {dashboard && <SettingsForm dashboard={dashboard} saving={saving} error={error} onCancel={() => onOpenChange(false)} onSave={onSave} />}
      </SheetContent>
    </Sheet>
  );
}

interface VarDraft extends Omit<DashboardVariable, "values" | "default"> {
  values: string;
  default: string;
}

const splitList = (s: string) => s.split(",").map((x) => x.trim()).filter(Boolean);
let pageSeq = 0;

function SettingsForm({ dashboard, saving, error, onCancel, onSave }: { dashboard: Dashboard; saving: boolean; error: unknown; onCancel: () => void; onSave: (d: Dashboard) => void }) {
  const { t } = useTranslation();
  const uid = useId();
  const [name, setName] = useState(dashboard.name);
  const [description, setDescription] = useState(dashboard.description);
  const [visibility, setVisibility] = useState<DashboardVisibility>(dashboard.visibility);
  const [pages, setPages] = useState<DashboardPage[]>(dashboard.pages);
  const [vars, setVars] = useState<VarDraft[]>(dashboard.variables.map((v) => ({ ...v, values: v.values.join(", "), default: v.default.join(", ") })));

  const nameError = !name.trim() ? t("dashboards.validation.name") : null;
  const varErrors = vars.map((v, i) =>
    !VARIABLE_NAME.test(v.name) ? t("dashboards.validation.variableName") : vars.findIndex((x) => x.name === v.name) !== i ? t("dashboards.validation.variableDuplicate") : null,
  );
  const pageErrors = pages.map((p) => (!p.name.trim() ? t("dashboards.validation.pageName") : null));
  const invalid = !!nameError || varErrors.some(Boolean) || pageErrors.some(Boolean) || pages.length === 0;

  const move = (i: number, d: -1 | 1) =>
    setPages((ps) => {
      const next = [...ps];
      const [p] = next.splice(i, 1);
      next.splice(i + d, 0, p!);
      return next;
    });
  const setVar = (i: number, patch: Partial<VarDraft>) => setVars((vs) => vs.map((v, j) => (j === i ? { ...v, ...patch } : v)));

  return (
    <form
      className="flex min-h-0 flex-1 flex-col"
      noValidate
      onSubmit={(e) => {
        e.preventDefault();
        if (invalid) return;
        onSave({
          ...dashboard,
          name: name.trim(),
          description,
          visibility,
          pages: pages.map((p) => ({ ...p, name: p.name.trim() })),
          variables: vars.map((v) => ({ ...v, label: v.label.trim(), values: splitList(v.values), default: splitList(v.default) })),
        });
      }}
    >
      <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto p-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${uid}-name`}>{t("dashboards.fields.name")}</Label>
            <Input id={`${uid}-name`} value={name} maxLength={200} onChange={(e) => setName(e.target.value)} aria-invalid={!!nameError} />
            {nameError && <p className="text-xs text-destructive-text">{nameError}</p>}
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${uid}-vis`}>{t("dashboards.fields.visibility")}</Label>
            <NativeSelect id={`${uid}-vis`} value={visibility} onChange={(e) => setVisibility(e.target.value as DashboardVisibility)}>
              <option value="org">{t("dashboards.visibility.org")}</option>
              <option value="private">{t("dashboards.visibility.private")}</option>
            </NativeSelect>
          </div>
          <div className="flex flex-col gap-1.5 sm:col-span-2">
            <Label htmlFor={`${uid}-desc`}>{t("dashboards.fields.description")}</Label>
            <textarea id={`${uid}-desc`} rows={2} maxLength={2000} value={description} onChange={(e) => setDescription(e.target.value)} className="rounded-md border border-input bg-background px-3 py-2 text-sm shadow-xs" />
          </div>
        </div>

        <fieldset className="flex flex-col gap-2">
          <div className="flex items-center justify-between">
            <legend className="text-sm font-semibold">{t("dashboards.settings.pages")}</legend>
            <Button type="button" variant="outline" size="sm" disabled={pages.length >= 20} onClick={() => setPages((ps) => [...ps, { id: `${DRAFT_ID_PREFIX}page-${++pageSeq}`, name: t("dashboards.settings.newPage", { n: ps.length + 1 }), widgets: [] }])}>
              <Plus aria-hidden="true" />
              {t("dashboards.settings.addPage")}
            </Button>
          </div>
          {pages.map((p, i) => (
            <div key={p.id} className="flex flex-wrap items-center gap-1" data-testid="page-row">
              <Input className="min-w-40 flex-1" aria-label={t("dashboards.settings.pageName", { n: i + 1 })} value={p.name} maxLength={100} aria-invalid={!!pageErrors[i]} onChange={(e) => setPages((ps) => ps.map((x, j) => (j === i ? { ...x, name: e.target.value } : x)))} />
              <Button type="button" variant="ghost" size="icon" disabled={i === 0} aria-label={t("dashboards.settings.moveUp", { name: p.name })} onClick={() => move(i, -1)}>
                <ArrowUp aria-hidden="true" />
              </Button>
              <Button type="button" variant="ghost" size="icon" disabled={i === pages.length - 1} aria-label={t("dashboards.settings.moveDown", { name: p.name })} onClick={() => move(i, 1)}>
                <ArrowDown aria-hidden="true" />
              </Button>
              <Button type="button" variant="ghost" size="icon" disabled={pages.length === 1} aria-label={t("dashboards.settings.removePage", { name: p.name })} onClick={() => setPages((ps) => ps.filter((_, j) => j !== i))}>
                <Trash2 aria-hidden="true" />
              </Button>
              {p.widgets.length > 0 && <span className="w-full text-xs text-muted-foreground">{t("dashboards.settings.pageWidgets", { count: p.widgets.length })}</span>}
            </div>
          ))}
        </fieldset>

        <fieldset className="flex flex-col gap-3">
          <div className="flex items-center justify-between">
            <legend className="text-sm font-semibold">{t("dashboards.variables.title")}</legend>
            <Button type="button" variant="outline" size="sm" disabled={vars.length >= 10} onClick={() => setVars((vs) => [...vs, { ...emptyVariable(vs.map((v) => ({ ...v, values: [], default: [] }))), values: "", default: "" }])}>
              <Plus aria-hidden="true" />
              {t("dashboards.variables.add")}
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">{t("dashboards.variables.hint", { syntax: "{{name}}", example: "host.name IN ({{host}})" })}</p>
          {vars.map((v, i) => (
            <div key={i} className="flex flex-col gap-3 rounded-lg border p-3" data-testid="variable-row">
              <div className="grid gap-3 sm:grid-cols-3">
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor={`${uid}-vn${i}`}>{t("dashboards.variables.name")}</Label>
                  <Input id={`${uid}-vn${i}`} value={v.name} aria-invalid={!!varErrors[i]} onChange={(e) => setVar(i, { name: e.target.value })} />
                  {varErrors[i] && <p className="text-xs text-destructive-text">{varErrors[i]}</p>}
                </div>
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor={`${uid}-vl${i}`}>{t("dashboards.variables.label")}</Label>
                  <Input id={`${uid}-vl${i}`} value={v.label} onChange={(e) => setVar(i, { label: e.target.value })} />
                </div>
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor={`${uid}-vt${i}`}>{t("dashboards.variables.type")}</Label>
                  <NativeSelect id={`${uid}-vt${i}`} value={v.type} onChange={(e) => setVar(i, { type: e.target.value as DashboardVariable["type"] })}>
                    {(["query", "list", "text"] as const).map((ty) => (
                      <option key={ty} value={ty}>
                        {t(`dashboards.variables.types.${ty}`)}
                      </option>
                    ))}
                  </NativeSelect>
                </div>
              </div>
              {v.type === "query" && (
                <div className="flex flex-col gap-1.5">
                  <span className="text-sm font-medium">{t("dashboards.variables.query")}</span>
                  <OqlEditor label={t("dashboards.variables.queryLabel", { name: v.name })} value={v.query} onChange={(query) => setVar(i, { query })} minRows={2} placeholder="SELECT count(*) FROM Log FACET host.name" />
                </div>
              )}
              {v.type === "list" && (
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor={`${uid}-vv${i}`}>{t("dashboards.variables.values")}</Label>
                  <Input id={`${uid}-vv${i}`} value={v.values} placeholder="prod, staging" onChange={(e) => setVar(i, { values: e.target.value })} />
                </div>
              )}
              <div className="grid gap-3 sm:grid-cols-3">
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor={`${uid}-vd${i}`}>{t("dashboards.variables.default")}</Label>
                  <Input id={`${uid}-vd${i}`} value={v.default} onChange={(e) => setVar(i, { default: e.target.value })} />
                </div>
                {v.type !== "text" && (
                  <div className="flex flex-col justify-end gap-1.5 pb-1 sm:col-span-2">
                    <label className="flex items-center gap-2 text-sm">
                      <input type="checkbox" checked={v.multi} onChange={(e) => setVar(i, { multi: e.target.checked })} />
                      {t("dashboards.variables.multi")}
                    </label>
                    <label className="flex items-center gap-2 text-sm">
                      <input type="checkbox" checked={v.include_all} onChange={(e) => setVar(i, { include_all: e.target.checked })} />
                      {t("dashboards.variables.includeAll")}
                    </label>
                  </div>
                )}
              </div>
              <Button type="button" variant="ghost" size="sm" className="w-fit" onClick={() => setVars((vs) => vs.filter((_, j) => j !== i))}>
                <Trash2 aria-hidden="true" />
                {t("dashboards.variables.remove", { name: v.name })}
              </Button>
            </div>
          ))}
        </fieldset>
      </div>
      <div className="flex flex-wrap items-center gap-2 border-t p-4">
        <Button type="submit" disabled={invalid || saving}>
          {t("dashboards.settings.save")}
        </Button>
        <Button type="button" variant="outline" onClick={onCancel}>
          {t("common.cancel")}
        </Button>
        <FormError error={error} />
      </div>
    </form>
  );
}
