import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { lazy, Suspense, useEffect, useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import {
  alertChannelsQuery,
  alertMetricNamesQuery,
  alertPreviewQuery,
  alertRuleTypesQuery,
  createAlertRule,
  updateAlertRule,
  type AlertRule,
  type AlertRuleInput,
  type AlertRuleType,
} from "@/api/alerts";
import { can } from "@/api/roles";
import { FormError } from "@/components/settings/common";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import {
  canEditOwned,
  changeType,
  draftFromRule,
  draftToInput,
  emptyDraft,
  FILTER_OPS,
  hasErrors,
  previewChartData,
  unitKindFor,
  validateDraft,
  type DraftErrors,
  type FilterDraft,
  type FilterOp,
  type RuleDraft,
} from "@/lib/alerts";
import { OqlEditor } from "@/components/oql/OqlEditor";
import { alertQueryIssues } from "@/lib/oql";
import { ChannelTypeIcon } from "./badges";
import { describedBy, useIssue } from "./field-utils";
import { DurationField, Field, Section } from "./fields";

// uPlot is loaded only when a preview is shown.
const PreviewChart = lazy(() => import("./PreviewChart").then((m) => ({ default: m.PreviewChart })));

const TYPES: AlertRuleType[] = ["metric_threshold", "log_match", "no_data", "discovery", "apm", "apm_no_data", "apm_error", "oql"];
const AGGREGATIONS = ["avg", "min", "max", "sum", "last", "count", "rate", "p50", "p95", "p99"] as const;
const SERIES_AGGREGATIONS = ["avg", "sum", "min", "max"] as const;
const OPERATORS = ["gt", "gte", "lt", "lte"] as const;
const SEVERITIES = ["critical", "warning", "info"] as const;
const APM_METRICS = ["throughput", "error_rate", "errors", "avg_ms", "p50_ms", "p95_ms", "p99_ms", "apdex"] as const;
const LOG_SEVERITIES = ["", "TRACE", "DEBUG", "INFO", "WARN", "ERROR", "FATAL"] as const;
const PREVIEW_HOURS = [1, 3, 6, 12, 24] as const;
const FIELD_KINDS = ["host.id", "host.name", "service.name", "attr", "resource"] as const;
type FieldKind = (typeof FIELD_KINDS)[number];

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setV(value), ms);
    return () => clearTimeout(id);
  }, [value, ms]);
  return v;
}

function kindOf(field: string): { kind: FieldKind; key: string } {
  if (field.startsWith("attr.")) return { kind: "attr", key: field.slice(5) };
  if (field.startsWith("resource.")) return { kind: "resource", key: field.slice(9) };
  return { kind: (FIELD_KINDS as readonly string[]).includes(field) ? (field as FieldKind) : "host.id", key: "" };
}

function allowedKinds(d: RuleDraft): FieldKind[] {
  if (d.type === "discovery" || (d.type === "no_data" && d.signal === "host")) return ["host.id", "host.name", "resource"];
  return [...FIELD_KINDS];
}

export interface RuleEditorProps {
  rule?: AlertRule;
  initial?: RuleDraft;
  onSaved?: (rule: AlertRule) => void;
  onCancel?: () => void;
}

/** Create or edit an alert rule: condition builder per type, evaluation settings, channels and a live preview. */
export function RuleEditor({ rule, initial, onSaved, onCancel }: RuleEditorProps) {
  const { t } = useTranslation();
  const uid = useId();
  const meQuery = useMe();
  const me = meQuery.data;
  const issue = useIssue();
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<RuleDraft>(() => (rule ? draftFromRule(rule) : (initial ?? emptyDraft())));
  const [submitted, setSubmitted] = useState(false);
  const [dirty, setDirty] = useState<Set<string>>(() => new Set());
  const [saved, setSaved] = useState(false);
  const canEdit = rule ? canEditOwned(me?.role, rule.created_by_user_id, me?.user?.id) : can(me?.role, "alerts.write");
  // Until the role is known the form stays editable and the save button hidden (no read-only flash).
  const readOnly = !meQuery.isPending && !canEdit;
  const errors = useMemo(() => validateDraft(draft), [draft]);
  const types = useQuery(alertRuleTypesQuery());
  const channels = useQuery(alertChannelsQuery());
  const metricNames = useQuery({ ...alertMetricNamesQuery(), enabled: draft.type === "metric_threshold" || draft.signal === "metric" });

  const update = (patch: Partial<RuleDraft>, field?: string) => {
    setSaved(false);
    setDraft((d) => ({ ...d, ...patch }));
    if (field) setDirty((s) => (s.has(field) ? s : new Set(s).add(field)));
  };
  const err = (field: string) => (submitted || dirty.has(field) ? issue(errors[field]) : undefined);
  const id = (name: string) => `${uid}-${name}`;

  const save = useMutation({
    mutationFn: (input: AlertRuleInput) => (rule ? updateAlertRule(rule.id, input) : createAlertRule(input)),
    onSuccess: (r) => {
      setSaved(true);
      void queryClient.invalidateQueries({ queryKey: ["alerts"] });
      onSaved?.(r);
    },
  });

  const numberField = (name: keyof RuleDraft, label: string, hint?: string) => (
    <Field id={id(name)} label={label} hint={hint} error={err(name)}>
      <Input
        id={id(name)}
        inputMode="decimal"
        value={String(draft[name] ?? "")}
        onChange={(e) => update({ [name]: e.target.value } as Partial<RuleDraft>, name)}
        {...describedBy(id(name), err(name), hint)}
      />
    </Field>
  );

  const thresholdFields = (
    <div className="grid gap-4 sm:grid-cols-3">
      <Field id={id("operator")} label={t("alerts.editor.operator")}>
        <NativeSelect id={id("operator")} value={draft.operator} onChange={(e) => update({ operator: e.target.value as RuleDraft["operator"] })}>
          {OPERATORS.map((o) => (
            <option key={o} value={o}>
              {t(`alerts.editor.operators.${o}`)}
            </option>
          ))}
        </NativeSelect>
      </Field>
      {numberField("threshold", t("alerts.editor.threshold"))}
      {numberField("recovery_threshold", t("alerts.editor.recoveryThreshold"), t("alerts.editor.recoveryHint"))}
    </div>
  );

  const missingField = (
    <Field id={id("missing")} label={t("alerts.editor.missingData")}>
      <NativeSelect id={id("missing")} value={draft.missing_data} onChange={(e) => update({ missing_data: e.target.value as RuleDraft["missing_data"] })}>
        {(["keep", "ok", "breach"] as const).map((m) => (
          <option key={m} value={m}>
            {t(`alerts.editor.missing.${m}`)}
          </option>
        ))}
      </NativeSelect>
    </Field>
  );

  const toggleGroup = (g: string) => update({ group_by: draft.group_by.includes(g) ? draft.group_by.filter((x) => x !== g) : [...draft.group_by, g] });
  const groupToggles = (options: { value: string; label: string }[], withDimensions: boolean) => {
    const dims = draft.group_by.filter((g) => !options.some((o) => o.value === g));
    return (
      <div className="flex flex-col gap-3">
        <div role="group" aria-label={t("alerts.editor.groupBy")} className="flex flex-wrap items-center gap-2">
          <span className="text-sm font-medium">{t("alerts.editor.groupBy")}</span>
          {options.map((o) => (
            <Button key={o.value} type="button" size="sm" variant={draft.group_by.includes(o.value) ? "default" : "outline"} aria-pressed={draft.group_by.includes(o.value)} onClick={() => toggleGroup(o.value)}>
              {o.label}
            </Button>
          ))}
        </div>
        {withDimensions && (
          <Field id={id("dims")} label={t("alerts.editor.dimensions")} hint={t("alerts.editor.dimensionsHint")}>
            <Input
              id={id("dims")}
              defaultValue={dims.join(", ")}
              onBlur={(e) => {
                const extra = e.target.value
                  .split(",")
                  .map((s) => s.trim())
                  .filter((s) => s.startsWith("attr.") || s.startsWith("resource."));
                update({ group_by: [...draft.group_by.filter((g) => options.some((o) => o.value === g)), ...extra] });
              }}
              {...describedBy(id("dims"), undefined, t("alerts.editor.dimensionsHint"))}
            />
          </Field>
        )}
      </div>
    );
  };

  const filtersEditor = (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <span className="text-sm font-medium">{t("alerts.editor.filters")}</span>
        <Button type="button" variant="outline" size="sm" onClick={() => update({ filters: [...draft.filters, { field: allowedKinds(draft)[0]!, op: "eq", values: "" }] })}>
          <Plus aria-hidden="true" />
          {t("alerts.editor.addFilter")}
        </Button>
      </div>
      {draft.filters.map((f, i) => {
        const { kind, key } = kindOf(f.field);
        const setFilter = (patch: Partial<FilterDraft>) => update({ filters: draft.filters.map((x, j) => (j === i ? { ...x, ...patch } : x)) }, `filters.${i}`);
        const n = i + 1;
        const e = err(`filters.${i}`);
        return (
          <div key={i} data-testid="filter-row" className="grid items-end gap-2 rounded-lg border p-2 sm:grid-cols-[10rem_minmax(0,1fr)_8rem_minmax(0,1.5fr)_auto]">
            <Field id={id(`ff${i}`)} label={`${t("alerts.editor.filterField")} ${n}`}>
              <NativeSelect id={id(`ff${i}`)} value={kind} onChange={(ev) => {
                const k = ev.target.value as FieldKind;
                setFilter({ field: k === "attr" || k === "resource" ? `${k}.${key}` : k });
              }}>
                {allowedKinds(draft).map((k) => (
                  <option key={k} value={k}>
                    {t(`alerts.editor.fields.${k}`)}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            {kind === "attr" || kind === "resource" ? (
              <Field id={id(`fk${i}`)} label={`${t("alerts.editor.filterKey")} ${n}`}>
                <Input id={id(`fk${i}`)} value={key} placeholder="cpu.mode" onChange={(ev) => setFilter({ field: `${kind}.${ev.target.value}` })} />
              </Field>
            ) : (
              <div aria-hidden="true" />
            )}
            <Field id={id(`fo${i}`)} label={`${t("alerts.editor.filterOp")} ${n}`}>
              <NativeSelect id={id(`fo${i}`)} value={f.op} onChange={(ev) => setFilter({ op: ev.target.value as FilterOp })}>
                {FILTER_OPS.map((o) => (
                  <option key={o} value={o}>
                    {t(`alerts.editor.ops.${o}`)}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            <Field id={id(`fv${i}`)} label={`${t("alerts.editor.filterValues")} ${n}`} error={e}>
              <Input id={id(`fv${i}`)} value={f.values} onChange={(ev) => setFilter({ values: ev.target.value })} {...describedBy(id(`fv${i}`), e)} />
            </Field>
            <Button type="button" variant="ghost" size="icon" aria-label={t("alerts.editor.removeFilter", { n })} onClick={() => update({ filters: draft.filters.filter((_, j) => j !== i) })}>
              <Trash2 aria-hidden="true" />
            </Button>
          </div>
        );
      })}
    </div>
  );

  const metricPicker = (
    <Field id={id("metric")} label={t("alerts.editor.metric")} hint={t("alerts.editor.metricHint")} error={err("metric")}>
      <Input
        id={id("metric")}
        list={id("metric-names")}
        autoComplete="off"
        value={draft.metric}
        placeholder="system.cpu.utilization"
        onChange={(e) => update({ metric: e.target.value }, "metric")}
        {...describedBy(id("metric"), err("metric"), t("alerts.editor.metricHint"))}
      />
      <datalist id={id("metric-names")}>
        {(metricNames.data ?? []).map((m) => (
          <option key={m.name} value={m.name}>
            {m.type}
            {m.unit ? ` · ${m.unit}` : ""}
          </option>
        ))}
      </datalist>
    </Field>
  );

  let condition: React.ReactNode;
  switch (draft.type) {
    case "metric_threshold":
      condition = (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            {metricPicker}
            <DurationField id={id("window")} label={t("alerts.editor.window")} seconds={draft.window_seconds} onChange={(s) => update({ window_seconds: s }, "window_seconds")} error={err("window_seconds")} />
            <Field id={id("agg")} label={t("alerts.editor.aggregation")}>
              <NativeSelect id={id("agg")} value={draft.aggregation} onChange={(e) => update({ aggregation: e.target.value as RuleDraft["aggregation"] })}>
                {AGGREGATIONS.map((a) => (
                  <option key={a} value={a}>
                    {t(`alerts.editor.aggregations.${a}`)}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            <Field id={id("sagg")} label={t("alerts.editor.seriesAggregation")}>
              <NativeSelect id={id("sagg")} value={draft.series_aggregation} onChange={(e) => update({ series_aggregation: e.target.value as RuleDraft["series_aggregation"] })}>
                <option value="">{t("alerts.editor.auto")}</option>
                {SERIES_AGGREGATIONS.map((a) => (
                  <option key={a} value={a}>
                    {t(`alerts.editor.seriesAggregations.${a}`)}
                  </option>
                ))}
              </NativeSelect>
            </Field>
          </div>
          {filtersEditor}
          {groupToggles([{ value: "host", label: t("alerts.editor.groupHost") }, { value: "service", label: t("alerts.editor.groupService") }], true)}
          {thresholdFields}
          {missingField}
        </>
      );
      break;
    case "log_match":
      condition = (
        <>
          <div className="grid gap-4 sm:grid-cols-3">
            <Field id={id("query")} label={t("alerts.editor.query")}>
              <Input id={id("query")} value={draft.query} placeholder="timeout" onChange={(e) => update({ query: e.target.value })} />
            </Field>
            <Field id={id("sevmin")} label={t("alerts.editor.severityMin")}>
              <NativeSelect id={id("sevmin")} value={draft.severity_min} onChange={(e) => update({ severity_min: e.target.value })}>
                {LOG_SEVERITIES.map((s) => (
                  <option key={s} value={s}>
                    {s || t("alerts.editor.anySeverity")}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            <DurationField id={id("window")} label={t("alerts.editor.window")} seconds={draft.window_seconds} onChange={(s) => update({ window_seconds: s }, "window_seconds")} error={err("window_seconds")} />
          </div>
          {filtersEditor}
          {groupToggles([{ value: "host", label: t("alerts.editor.groupHost") }, { value: "service", label: t("alerts.editor.groupService") }], true)}
          {thresholdFields}
        </>
      );
      break;
    case "no_data":
      condition = (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id={id("signal")} label={t("alerts.editor.signal")}>
              <NativeSelect
                id={id("signal")}
                value={draft.signal}
                onChange={(e) => {
                  const signal = e.target.value as RuleDraft["signal"];
                  update({ signal, group_by: ["host"], filters: signal === "host" ? draft.filters.filter((f) => !f.field.startsWith("attr.") && f.field !== "service.name") : draft.filters });
                }}
              >
                {(["host", "metric", "log"] as const).map((s) => (
                  <option key={s} value={s}>
                    {t(`alerts.editor.signals.${s}`)}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            {draft.signal === "metric" ? metricPicker : <div />}
            <DurationField id={id("window")} label={t("alerts.editor.window")} seconds={draft.window_seconds} onChange={(s) => update({ window_seconds: s }, "window_seconds")} error={err("window_seconds")} />
            <DurationField id={id("lookback")} label={t("alerts.editor.lookback")} seconds={draft.lookback_seconds} onChange={(s) => update({ lookback_seconds: s }, "lookback_seconds")} error={err("lookback_seconds")} />
          </div>
          {filtersEditor}
          {draft.signal !== "host" && (
            <Field id={id("ndgroup")} label={t("alerts.editor.groupBy")}>
              <NativeSelect id={id("ndgroup")} value={draft.group_by[0] ?? "host"} onChange={(e) => update({ group_by: [e.target.value] })}>
                <option value="host">{t("alerts.editor.groupHost")}</option>
                <option value="service">{t("alerts.editor.groupService")}</option>
              </NativeSelect>
            </Field>
          )}
        </>
      );
      break;
    case "discovery":
      condition = (
        <>
          <p className="text-sm text-muted-foreground">{t("alerts.editor.eventHint")}</p>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id={id("event")} label={t("alerts.editor.event")}>
              <NativeSelect id={id("event")} value={draft.event} onChange={(e) => update({ event: e.target.value as RuleDraft["event"] })}>
                {(["service_disappeared", "port_opened"] as const).map((ev) => (
                  <option key={ev} value={ev}>
                    {t(`alerts.editor.events.${ev}`)}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            <Field id={id("match")} label={t("alerts.editor.match")}>
              <Input id={id("match")} value={draft.match} placeholder="redis" onChange={(e) => update({ match: e.target.value })} />
            </Field>
            <DurationField id={id("window")} label={t("alerts.editor.window")} seconds={draft.window_seconds} onChange={(s) => update({ window_seconds: s }, "window_seconds")} error={err("window_seconds")} />
            <DurationField id={id("lookback")} label={t("alerts.editor.lookback")} seconds={draft.lookback_seconds} onChange={(s) => update({ lookback_seconds: s }, "lookback_seconds")} error={err("lookback_seconds")} />
          </div>
          {filtersEditor}
        </>
      );
      break;
    case "apm":
      condition = (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id={id("svc")} label={t("alerts.editor.serviceName")} error={err("service_name")}>
              <Input id={id("svc")} value={draft.service_name} placeholder="checkout" onChange={(e) => update({ service_name: e.target.value }, "service_name")} {...describedBy(id("svc"), err("service_name"))} />
            </Field>
            <Field id={id("apmmetric")} label={t("alerts.editor.apmMetric")}>
              <NativeSelect id={id("apmmetric")} value={draft.metric} onChange={(e) => update({ metric: e.target.value })}>
                {APM_METRICS.map((m) => (
                  <option key={m} value={m}>
                    {t(`alerts.editor.apmMetrics.${m}`)}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            <Field id={id("env")} label={t("alerts.editor.environment")} hint={t("alerts.editor.environmentHint")}>
              <Input id={id("env")} value={draft.environment} onChange={(e) => update({ environment: e.target.value })} {...describedBy(id("env"), undefined, t("alerts.editor.environmentHint"))} />
            </Field>
            <Field id={id("tx")} label={t("alerts.editor.transactionName")} hint={t("alerts.editor.transactionHint")}>
              <Input id={id("tx")} value={draft.transaction_name} onChange={(e) => update({ transaction_name: e.target.value })} {...describedBy(id("tx"), undefined, t("alerts.editor.transactionHint"))} />
            </Field>
            <DurationField id={id("window")} label={t("alerts.editor.window")} seconds={draft.window_seconds} onChange={(s) => update({ window_seconds: s }, "window_seconds")} error={err("window_seconds")} />
            {numberField("min_requests", t("alerts.editor.minRequests"))}
          </div>
          {groupToggles([{ value: "environment", label: t("alerts.editor.groupEnvironment") }, { value: "transaction", label: t("alerts.editor.groupTransaction") }], false)}
          {thresholdFields}
          {missingField}
        </>
      );
      break;
    case "apm_no_data":
      condition = (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id={id("svc")} label={t("alerts.editor.serviceName")} hint={t("alerts.editor.serviceNameAllHint")}>
              <Input id={id("svc")} value={draft.service_name} placeholder="checkout" onChange={(e) => update({ service_name: e.target.value })} {...describedBy(id("svc"), undefined, t("alerts.editor.serviceNameAllHint"))} />
            </Field>
            <Field id={id("env")} label={t("alerts.editor.environment")} hint={t("alerts.editor.environmentHint")}>
              <Input id={id("env")} value={draft.environment} onChange={(e) => update({ environment: e.target.value })} {...describedBy(id("env"), undefined, t("alerts.editor.environmentHint"))} />
            </Field>
            <DurationField id={id("window")} label={t("alerts.editor.window")} seconds={draft.window_seconds} onChange={(s) => update({ window_seconds: s }, "window_seconds")} error={err("window_seconds")} />
            <DurationField id={id("lookback")} label={t("alerts.editor.lookback")} seconds={draft.lookback_seconds} onChange={(s) => update({ lookback_seconds: s }, "lookback_seconds")} error={err("lookback_seconds")} />
          </div>
          {groupToggles([{ value: "namespace", label: t("alerts.editor.groupNamespace") }, { value: "environment", label: t("alerts.editor.groupEnvironment") }], false)}
        </>
      );
      break;
    case "apm_error":
      condition = (
        <>
          <p className="text-sm text-muted-foreground">{t("alerts.editor.errorEventHint")}</p>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id={id("event")} label={t("alerts.editor.event")}>
              <NativeSelect id={id("event")} value={draft.event} onChange={(e) => update({ event: e.target.value as RuleDraft["event"] })}>
                {(["new_group", "regressed"] as const).map((ev) => (
                  <option key={ev} value={ev}>
                    {t(`alerts.editor.events.${ev}`)}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            <Field id={id("svc")} label={t("alerts.editor.serviceName")} hint={t("alerts.editor.serviceNameAllHint")}>
              <Input id={id("svc")} value={draft.service_name} placeholder="checkout" onChange={(e) => update({ service_name: e.target.value })} {...describedBy(id("svc"), undefined, t("alerts.editor.serviceNameAllHint"))} />
            </Field>
            <Field id={id("env")} label={t("alerts.editor.environment")} hint={t("alerts.editor.environmentHint")}>
              <Input id={id("env")} value={draft.environment} onChange={(e) => update({ environment: e.target.value })} {...describedBy(id("env"), undefined, t("alerts.editor.environmentHint"))} />
            </Field>
            <Field id={id("match")} label={t("alerts.editor.errorMatch")}>
              <Input id={id("match")} value={draft.match} placeholder="timeout" onChange={(e) => update({ match: e.target.value })} />
            </Field>
            <DurationField id={id("window")} label={t("alerts.editor.window")} seconds={draft.window_seconds} onChange={(s) => update({ window_seconds: s }, "window_seconds")} error={err("window_seconds")} />
            {draft.event === "new_group" && numberField("min_count", t("alerts.editor.minCount"))}
          </div>
        </>
      );
      break;
    case "oql": {
      const restrictions = alertQueryIssues(draft.query);
      condition = (
        <>
          <Field id={id("oql")} label={t("alerts.editor.oqlQuery")} hint={t("alerts.editor.oqlQueryHint")} error={err("query")}>
            <OqlEditor
              id={id("oql")}
              label={t("alerts.editor.oqlQuery")}
              value={draft.query}
              onChange={(query) => update({ query }, "query")}
              minRows={3}
              placeholder="SELECT count(*) FROM Log WHERE severity = 'ERROR' FACET service.name"
              describedBy={err("query") ? `${id("oql")}-error` : `${id("oql")}-hint`}
            />
          </Field>
          {restrictions.length > 0 && (
            <ul className="-mt-2 flex flex-col gap-0.5 text-xs text-destructive-text" data-testid="oql-restrictions">
              {restrictions.map((r) => (
                <li key={r.key}>{t(`oql.alertRestrictions.${r.key}`)}</li>
              ))}
            </ul>
          )}
          <div className="grid gap-4 sm:grid-cols-2">
            <DurationField id={id("window")} label={t("alerts.editor.window")} seconds={draft.window_seconds} onChange={(s) => update({ window_seconds: s }, "window_seconds")} error={err("window_seconds")} />
            {missingField}
          </div>
          {thresholdFields}
        </>
      );
      break;
    }
  }

  return (
    <form
      noValidate
      className="flex flex-col gap-4"
      onSubmit={(e) => {
        e.preventDefault();
        setSubmitted(true);
        if (!canEdit || hasErrors(errors)) return;
        save.mutate(draftToInput(draft));
      }}
    >
      {readOnly && (
        <p role="note" className="rounded-lg border bg-muted/50 px-3 py-2 text-sm text-muted-foreground">
          {t("alerts.editor.readOnly")}
        </p>
      )}
      <fieldset disabled={readOnly} className="flex min-w-0 flex-col gap-4">
        <Section title={t("alerts.editor.sections.general")}>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id={id("name")} label={t("alerts.editor.name")} error={err("name")}>
              <Input id={id("name")} value={draft.name} onChange={(e) => update({ name: e.target.value }, "name")} {...describedBy(id("name"), err("name"))} />
            </Field>
            <Field id={id("severity")} label={t("alerts.editor.severity")}>
              <NativeSelect id={id("severity")} value={draft.severity} onChange={(e) => update({ severity: e.target.value as RuleDraft["severity"] })}>
                {SEVERITIES.map((s) => (
                  <option key={s} value={s}>
                    {t(`alerts.severity.${s}`)}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            <Field id={id("description")} label={t("alerts.editor.description")} className="sm:col-span-2">
              <textarea
                id={id("description")}
                rows={2}
                value={draft.description}
                onChange={(e) => update({ description: e.target.value })}
                className="rounded-md border border-input bg-background px-3 py-2 text-sm shadow-xs"
              />
            </Field>
          </div>
          <fieldset className="flex flex-col gap-2">
            <legend className="mb-2 text-sm font-medium">{t("alerts.editor.type")}</legend>
            <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-5">
              {TYPES.map((ty) => {
                const info = types.data?.find((x) => x.type === ty);
                const unavailable = info ? !info.available : false;
                return (
                  <label
                    key={ty}
                    title={unavailable ? info?.reason : undefined}
                    className="flex cursor-pointer flex-col gap-1 rounded-lg border p-3 text-sm has-checked:border-primary has-checked:bg-accent has-disabled:cursor-not-allowed has-disabled:opacity-60"
                  >
                    <span className="flex items-center gap-2 font-medium">
                      <input
                        type="radio"
                        name={id("type")}
                        value={ty}
                        checked={draft.type === ty}
                        disabled={unavailable || (!!rule && rule.type !== ty && false)}
                        onChange={() => setDraft((d) => changeType(d, ty))}
                      />
                      {t(`alerts.types.${ty}`)}
                      {unavailable && <span className="text-xs text-muted-foreground">({t("alerts.comingSoon")})</span>}
                    </span>
                    <span className="text-xs text-muted-foreground">{t(`alerts.typeHelp.${ty}`)}</span>
                  </label>
                );
              })}
            </div>
          </fieldset>
        </Section>

        <Section title={t("alerts.editor.sections.condition")}>{condition}</Section>

        <PreviewPanel draft={draft} errors={errors} />

        <Section title={t("alerts.editor.sections.evaluation")}>
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <DurationField id={id("interval")} label={t("alerts.editor.interval")} seconds={draft.interval_seconds} onChange={(s) => update({ interval_seconds: s }, "interval_seconds")} error={err("interval_seconds")} />
            {draft.type !== "discovery" && draft.type !== "apm_error" && (
              <DurationField id={id("for")} label={t("alerts.editor.for")} hint={t("alerts.editor.forHint")} seconds={draft.for_seconds} onChange={(s) => update({ for_seconds: s }, "for_seconds")} error={err("for_seconds")} />
            )}
            <DurationField id={id("recfor")} label={t("alerts.editor.recoveryFor")} seconds={draft.recovery_for_seconds} onChange={(s) => update({ recovery_for_seconds: s }, "recovery_for_seconds")} error={err("recovery_for_seconds")} />
            <DurationField id={id("renotify")} label={t("alerts.editor.renotify")} hint={t("alerts.editor.renotifyHint")} seconds={draft.renotify_interval_seconds} onChange={(s) => update({ renotify_interval_seconds: s }, "renotify_interval_seconds")} error={err("renotify_interval_seconds")} />
          </div>
          <fieldset className="flex flex-col gap-3 rounded-lg border p-3">
            <legend className="px-1 text-sm font-medium">{t("alerts.editor.flapping")}</legend>
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={draft.flapping.enabled} onChange={(e) => update({ flapping: { ...draft.flapping, enabled: e.target.checked } })} />
              {t("alerts.editor.enabled")}
            </label>
            <p className="text-xs text-muted-foreground">{t("alerts.editor.flappingHint")}</p>
            {draft.flapping.enabled && (
              <div className="grid gap-4 sm:grid-cols-3">
                <Field id={id("ftrans")} label={t("alerts.editor.flappingTransitions")}>
                  <Input id={id("ftrans")} type="number" min={2} max={20} value={draft.flapping.transitions} onChange={(e) => update({ flapping: { ...draft.flapping, transitions: Number(e.target.value) } })} />
                </Field>
                <DurationField id={id("fwin")} label={t("alerts.editor.flappingWindow")} seconds={draft.flapping.window_seconds} onChange={(s) => update({ flapping: { ...draft.flapping, window_seconds: s } })} />
                <DurationField id={id("fhold")} label={t("alerts.editor.flappingHold")} seconds={draft.flapping.hold_seconds} onChange={(s) => update({ flapping: { ...draft.flapping, hold_seconds: s } })} />
              </div>
            )}
          </fieldset>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id={id("runbook")} label={t("alerts.editor.runbook")} error={err("runbook_url")}>
              <Input id={id("runbook")} type="url" value={draft.runbook_url} placeholder="https://" onChange={(e) => update({ runbook_url: e.target.value }, "runbook_url")} {...describedBy(id("runbook"), err("runbook_url"))} />
            </Field>
            <div className="flex flex-col gap-2">
              <div className="flex items-center justify-between">
                <span className="text-sm font-medium">{t("alerts.editor.labels")}</span>
                <Button type="button" variant="outline" size="sm" onClick={() => update({ labels: [...draft.labels, { key: "", value: "" }] })}>
                  <Plus aria-hidden="true" />
                  {t("alerts.editor.addLabel")}
                </Button>
              </div>
              {draft.labels.map((l, i) => (
                <div key={i} className="flex items-start gap-2">
                  <Input aria-label={t("alerts.editor.labelKey", { n: i + 1 })} value={l.key} placeholder="team" aria-invalid={!!err(`labels.${i}`)}
                    onChange={(e) => update({ labels: draft.labels.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)) }, `labels.${i}`)} />
                  <Input aria-label={t("alerts.editor.labelValue", { n: i + 1 })} value={l.value} placeholder="infra"
                    onChange={(e) => update({ labels: draft.labels.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)) })} />
                  <Button type="button" variant="ghost" size="icon" aria-label={t("alerts.editor.removeLabel", { n: i + 1 })} onClick={() => update({ labels: draft.labels.filter((_, j) => j !== i) })}>
                    <Trash2 aria-hidden="true" />
                  </Button>
                </div>
              ))}
            </div>
          </div>
        </Section>

        <Section title={t("alerts.editor.sections.notifications")}>
          <fieldset>
            <legend className="mb-2 text-sm font-medium">{t("alerts.editor.channels")}</legend>
            {channels.isError ? (
              <ErrorState error={channels.error} className="py-4" />
            ) : (channels.data?.channels ?? []).length === 0 ? (
              <p className="text-sm text-muted-foreground">{channels.isPending ? t("common.loading") : t("alerts.editor.noChannels")}</p>
            ) : (
              <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
                {channels.data!.channels.map((c) => (
                  <label key={c.id} className="flex items-center gap-2 rounded-lg border px-3 py-2 text-sm has-checked:border-primary">
                    <input
                      type="checkbox"
                      checked={draft.channel_ids.includes(c.id)}
                      onChange={(e) => update({ channel_ids: e.target.checked ? [...draft.channel_ids, c.id] : draft.channel_ids.filter((x) => x !== c.id) })}
                    />
                    <ChannelTypeIcon type={c.type} />
                    <span className="truncate">{c.name}</span>
                    {!c.enabled && <span className="text-xs text-muted-foreground">({t("alerts.editor.channelDisabled")})</span>}
                  </label>
                ))}
              </div>
            )}
          </fieldset>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={draft.enabled} onChange={(e) => update({ enabled: e.target.checked })} />
            {t("alerts.editor.enabled")}
          </label>
        </Section>
      </fieldset>

      <div className="flex flex-wrap items-center gap-3">
        {canEdit && (
          <Button type="submit" disabled={save.isPending}>
            {rule ? t("alerts.editor.save") : t("alerts.editor.create")}
          </Button>
        )}
        {onCancel && (
          <Button type="button" variant="outline" onClick={onCancel}>
            {t("alerts.cancel")}
          </Button>
        )}
        {submitted && hasErrors(errors) && (
          <p role="alert" className="text-sm text-destructive-text">
            {t("alerts.editor.fixErrors")}
          </p>
        )}
        {saved && (
          <p role="status" className="text-sm text-success-text">
            {t("alerts.editor.saved")}
          </p>
        )}
        <FormError error={save.error} />
      </div>
    </form>
  );
}

const PREVIEW_IGNORED = ["name", "runbook_url", "renotify_interval_seconds", "recovery_for_seconds"];

function PreviewPanel({ draft, errors }: { draft: RuleDraft; errors: DraftErrors }) {
  const { t } = useTranslation();
  const uid = useId();
  const [hours, setHours] = useState<number>(6);
  const conditionErrors = Object.keys(errors).filter((k) => !PREVIEW_IGNORED.includes(k) && !k.startsWith("labels."));
  const inputJSON =
    conditionErrors.length > 0 ? "" : JSON.stringify(draftToInput({ ...draft, name: draft.name.trim() || "preview", labels: [], runbook_url: "", renotify_interval_seconds: 0 }));
  const debounced = useDebounced(inputJSON, 600);
  const query = useQuery(alertPreviewQuery(debounced ? (JSON.parse(debounced) as AlertRuleInput) : null, hours));
  const data = useMemo(() => (query.data ? previewChartData(query.data, t("charts.series")) : null), [query.data, t]);
  const unit = unitKindFor(query.data?.unit ?? "", draft.type, draft.metric);
  const parse = (s: string) => Date.parse(s.replace(/(\.\d{3})\d+/, "$1"));

  return (
    <Section title={t("alerts.preview.title")}>
      <div className="flex flex-wrap items-center gap-2">
        <label htmlFor={`${uid}-hours`} className="text-sm font-medium">
          {t("alerts.preview.hours")}
        </label>
        <NativeSelect id={`${uid}-hours`} value={hours} onChange={(e) => setHours(Number(e.target.value))}>
          {PREVIEW_HOURS.map((h) => (
            <option key={h} value={h}>
              {t("alerts.preview.hoursOption", { count: h })}
            </option>
          ))}
        </NativeSelect>
        {query.isFetching && debounced && <span className="text-xs text-muted-foreground">{t("alerts.preview.loading")}</span>}
      </div>
      {!debounced ? (
        <EmptyState className="py-6">{t("alerts.preview.invalid")}</EmptyState>
      ) : query.isError ? (
        <ErrorState error={query.error} onRetry={() => void query.refetch()} className="py-6" />
      ) : !query.data || !data ? (
        <LoadingState label={t("alerts.preview.loading")} />
      ) : data.series.length === 0 ? (
        <EmptyState className="py-6">{t("alerts.preview.noSeries")}</EmptyState>
      ) : (
        <div className="flex flex-col gap-2" data-testid="alert-preview">
          <p role="status" className="text-sm font-medium" data-testid="preview-summary">
            {t("alerts.preview.wouldFire", { count: data.incidents })}
          </p>
          <Suspense fallback={<LoadingState />}>
            <PreviewChart
              series={data.series}
              unit={unit}
              threshold={query.data.threshold}
              recoveryThreshold={query.data.recovery_threshold}
              bands={data.bands}
              fires={data.fires}
              from={parse(query.data.from)}
              to={parse(query.data.to)}
              title={t("alerts.preview.chartTitle")}
            />
          </Suspense>
          <div className="flex flex-wrap gap-x-4 text-xs text-muted-foreground">
            {data.hiddenSeries > 0 && <span>{t("alerts.preview.moreSeries", { count: data.hiddenSeries })}</span>}
            {query.data.truncated && <span>{t("alerts.preview.truncated")}</span>}
            {query.data.approximate && <span>{t("alerts.preview.approximate")}</span>}
          </div>
        </div>
      )}
    </Section>
  );
}
