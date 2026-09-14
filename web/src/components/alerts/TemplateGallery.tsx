import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { BellPlus, ChevronDown, ChevronUp, Pencil } from "lucide-react";
import { lazy, Suspense, useEffect, useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { alertPreviewQuery, alertTemplateRenderQuery, alertTemplatesQuery, createAlertRule, type AlertRule, type AlertTemplate, type AlertTemplateParam } from "@/api/alerts";
import { hostsQuery } from "@/api/queries";
import { can } from "@/api/roles";
import { FormError } from "@/components/settings/common";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Button, buttonVariants } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  buildParams,
  displayRange,
  editableParams,
  groupTemplates,
  initialValues,
  targetParams,
  templateLanguage,
  templateText,
  templateUsable,
  type ParamError,
  type TemplateCategory,
  type TemplateTarget,
} from "@/lib/alert-templates";
import { previewChartData, unitKindFor } from "@/lib/alerts";
import { formatValue } from "@/lib/format";
import { cn } from "@/lib/utils";
import { SeverityBadge } from "./badges";
import { Field } from "./fields";

const PreviewChart = lazy(() => import("./PreviewChart").then((m) => ({ default: m.PreviewChart })));

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setV(value), ms);
    return () => clearTimeout(id);
  }, [value, ms]);
  return v;
}

function unitSuffix(p: AlertTemplateParam, units: { seconds: string; perSecond: string }): string | undefined {
  switch (p.unit) {
    case "ratio":
      return "%";
    case "seconds":
      return units.seconds;
    case "ms":
      return "ms";
    case "per_second":
      return units.perSecond;
    default:
      return undefined;
  }
}

function ParamInput({ p, value, error, onChange, hostOptions }: { p: AlertTemplateParam; value: string; error?: ParamError; onChange: (v: string) => void; hostOptions: { id: string; name: string }[] }) {
  const { t, i18n } = useTranslation();
  const uid = useId();
  const suffix = unitSuffix(p, { seconds: t("alerts.templates.units.seconds"), perSecond: t("alerts.templates.units.perSecond") });
  const range = displayRange(p);
  const message = !error
    ? undefined
    : error.key === "range"
      ? t("alerts.validation.range", { min: formatValue(error.min, "number", i18n.resolvedLanguage), max: formatValue(error.max, "number", i18n.resolvedLanguage) })
      : t(`alerts.validation.${error.key}`);
  const numeric = p.kind === "number" || p.kind === "duration";
  return (
    <Field id={uid} label={`${templateText(p.label, i18n.resolvedLanguage)}${suffix ? ` (${suffix})` : ""}`} error={message}>
      <Input
        id={uid}
        inputMode={numeric ? "decimal" : undefined}
        list={p.kind === "host" ? `${uid}-hosts` : undefined}
        value={value}
        min={range.min}
        max={range.max}
        aria-invalid={!!error}
        aria-describedby={error ? `${uid}-error` : undefined}
        onChange={(e) => onChange(e.target.value)}
      />
      {p.kind === "host" && (
        <datalist id={`${uid}-hosts`}>
          {hostOptions.map((h) => (
            <option key={h.id} value={h.id}>
              {h.name}
            </option>
          ))}
        </datalist>
      )}
    </Field>
  );
}

/** Parameter form, preview and one-click creation of one template for a target. */
function TemplateSetup({ template, target, onCreated }: { template: AlertTemplate; target: TemplateTarget; onCreated?: (rule: AlertRule) => void }) {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();
  const canWrite = can(useMe().data?.role, "alerts.write");
  const params = useMemo(() => editableParams(template, target), [template, target]);
  const [values, setValues] = useState(() => initialValues(params));
  const [created, setCreated] = useState<AlertRule | null>(null);
  const hosts = useQuery({ ...hostsQuery(), enabled: params.some((p) => p.kind === "host") });
  const hostOptions = (hosts.data ?? []).map((h) => ({ id: h.host_id, name: h.host_name || h.host_id }));
  const built = buildParams(params, values);
  const valid = Object.keys(built.errors).length === 0;
  const lang = templateLanguage(i18n.resolvedLanguage);
  const inputJSON = valid ? JSON.stringify({ params: { ...targetParams(template, target), ...built.params }, language: lang }) : "";
  const debounced = useDebounced(inputJSON, 400);
  const render = useQuery(alertTemplateRenderQuery(template.id, debounced ? JSON.parse(debounced) : null));
  const rule = debounced && render.data ? render.data.rule : null;
  const preview = useQuery(alertPreviewQuery(rule, 6));
  const chart = useMemo(() => (preview.data ? previewChartData(preview.data, t("charts.series")) : null), [preview.data, t]);
  const create = useMutation({
    mutationFn: () => createAlertRule(rule!),
    onSuccess: (r) => {
      setCreated(r);
      void queryClient.invalidateQueries({ queryKey: ["alerts"] });
      onCreated?.(r);
    },
  });
  const parse = (s: string) => Date.parse(s.replace(/(\.\d{3})\d+/, "$1"));
  const reference = render.data?.reference;

  return (
    <div className="flex flex-col gap-3 border-t pt-3" data-testid="template-setup">
      {params.length > 0 && (
        <div className="grid gap-3 sm:grid-cols-2">
          {params.map((p) => (
            <ParamInput key={p.key} p={p} value={values[p.key] ?? ""} error={built.errors[p.key]} hostOptions={hostOptions} onChange={(v) => setValues((s) => ({ ...s, [p.key]: v }))} />
          ))}
        </div>
      )}
      {reference && (
        <p className="text-xs text-muted-foreground" data-testid="template-reference">
          {t("alerts.templates.reference", {
            metric: reference.metric,
            value: formatValue(reference.value, reference.metric.includes("memory") ? "bytes" : "number", i18n.resolvedLanguage),
            threshold: formatValue(Math.round(reference.value * reference.ratio), reference.metric.includes("memory") ? "bytes" : "number", i18n.resolvedLanguage),
          })}
        </p>
      )}
      {!valid ? null : render.isError ? (
        <ErrorState error={render.error} className="py-4" />
      ) : !rule ? (
        <LoadingState label={t("alerts.preview.loading")} />
      ) : preview.isError ? (
        <ErrorState error={preview.error} onRetry={() => void preview.refetch()} className="py-4" />
      ) : !preview.data || !chart ? (
        <LoadingState label={t("alerts.preview.loading")} />
      ) : chart.series.length === 0 ? (
        <EmptyState className="py-4">{t("alerts.templates.noData")}</EmptyState>
      ) : (
        <div className="flex flex-col gap-2" data-testid="template-preview">
          <p role="status" className="text-sm font-medium">
            {t("alerts.templates.wouldFire", { count: chart.incidents })}
          </p>
          <Suspense fallback={<LoadingState />}>
            <PreviewChart
              series={chart.series}
              unit={unitKindFor(preview.data.unit, rule.type, (rule.condition as { metric?: string }).metric)}
              threshold={preview.data.threshold}
              recoveryThreshold={preview.data.recovery_threshold}
              bands={chart.bands}
              fires={chart.fires}
              from={parse(preview.data.from)}
              to={parse(preview.data.to)}
              title={templateText(template.name, i18n.resolvedLanguage)}
              height={180}
            />
          </Suspense>
        </div>
      )}
      <div className="flex flex-wrap items-center gap-2">
        {canWrite && !created && (
          <Button type="button" size="sm" className="min-h-10" disabled={!rule || create.isPending || render.isFetching} onClick={() => create.mutate()}>
            <BellPlus aria-hidden="true" />
            {t("alerts.templates.create")}
          </Button>
        )}
        {canWrite && valid && debounced && (
          <Link
            to="/alerts/rules/new"
            search={{ template: template.id, tparams: JSON.stringify(JSON.parse(debounced).params) } as never}
            className={buttonVariants({ variant: "outline", size: "sm", className: "min-h-10" })}
          >
            <Pencil aria-hidden="true" />
            {t("alerts.templates.customize")}
          </Link>
        )}
        {created && (
          <p role="status" className="text-sm text-success-text" data-testid="template-created">
            {t("alerts.templates.created")}{" "}
            <Link to="/alerts/rules/$ruleId" params={{ ruleId: created.id }} className="font-medium text-primary hover:underline">
              {created.name}
            </Link>
          </p>
        )}
        <FormError error={create.error} />
      </div>
    </div>
  );
}

function TemplateCard({ template, target }: { template: AlertTemplate; target: TemplateTarget }) {
  const { t, i18n } = useTranslation();
  const [open, setOpen] = useState(false);
  const panelId = useId();
  const name = templateText(template.name, i18n.resolvedLanguage);
  return (
    <li className="flex flex-col gap-2 rounded-lg border bg-card p-3" data-testid="alert-template">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium">{name}</p>
          <p className="text-xs text-muted-foreground">{templateText(template.description, i18n.resolvedLanguage)}</p>
        </div>
        <SeverityBadge severity={template.severity} />
      </div>
      <Button
        type="button"
        variant={open ? "secondary" : "outline"}
        size="sm"
        className="min-h-10 self-start"
        aria-expanded={open}
        aria-controls={panelId}
        aria-label={t("alerts.templates.setUpFor", { name })}
        onClick={() => setOpen((o) => !o)}
      >
        {open ? <ChevronUp aria-hidden="true" /> : <ChevronDown aria-hidden="true" />}
        {t("alerts.templates.setUp")}
      </Button>
      <div id={panelId} hidden={!open}>
        {open && <TemplateSetup template={template} target={target} />}
      </div>
    </li>
  );
}

export interface TemplateGalleryProps {
  category?: TemplateCategory;
  integration?: string;
  target?: TemplateTarget;
  /** Wider cards in one or two columns (integration panels use four). */
  columns?: "2" | "3";
}

/** Recommended alert templates (alerting.md §2.8) for a category, integration or target. */
export function TemplateGallery({ category, integration, target = {}, columns = "2" }: TemplateGalleryProps) {
  const { t } = useTranslation();
  const q = useQuery(alertTemplatesQuery({ category, integration }));
  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const usable = q.data.filter((tpl) => templateUsable(tpl, target));
  if (usable.length === 0) return <EmptyState>{t("alerts.templates.empty")}</EmptyState>;
  const grouped = category || integration ? [{ key: "all", category: undefined, integration: undefined, templates: usable }] : groupTemplates(usable);
  return (
    <div className="flex flex-col gap-4">
      {grouped.map((g) => (
        <section key={g.key} aria-label={g.category ? (g.integration ? g.integration : t(`alerts.templates.categories.${g.category}`)) : undefined} className="flex flex-col gap-2">
          {g.category && <h3 className="text-sm font-semibold">{g.integration ? t("alerts.templates.integrationGroup", { name: g.integration }) : t(`alerts.templates.categories.${g.category}`)}</h3>}
          <ul className={cn("grid grid-cols-1 items-start gap-2", columns === "3" ? "md:grid-cols-2 xl:grid-cols-3" : "lg:grid-cols-2")}>
            {g.templates.map((tpl) => (
              <TemplateCard key={tpl.id} template={tpl} target={target} />
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}
