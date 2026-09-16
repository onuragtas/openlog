// Guided query builder of the console: someone who does not know OQL picks what to look at, what to measure, optional
// conditions and a split, and gets a runnable query (lib/oql-builder.ts writes the text).
import { useQuery } from "@tanstack/react-query";
import { Play, Plus, RotateCcw, X } from "lucide-react";
import { useId, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { fieldValuesQuery, type FieldSignal } from "@/api/explorer";
import { oqlSchemaQuery, type OqlEventType } from "@/api/oql";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import { OQL_EVENT_TYPES } from "@/lib/oql";
import {
  BUILDER_MEASURES,
  BUILDER_OPS,
  buildOql,
  builderIssue,
  defaultBuilderState,
  MAX_CONDITIONS,
  MAX_GROUP_BY,
  NUMBER_MEASURES,
  type BuilderCondition,
  type BuilderMeasure,
  type BuilderOp,
  type BuilderState,
} from "@/lib/oql-builder";
import { tDynamic } from "@/lib/onboarding-key";
import type { RangeSpec } from "@/lib/time";

/** Explorer signal of an event type, for value suggestions; hosts and containers have none. */
const SIGNAL: Partial<Record<OqlEventType, FieldSignal>> = { Log: "logs", Span: "traces", Transaction: "traces", Metric: "metrics" };

export interface QueryWizardProps {
  range: RangeSpec;
  onRun: (query: string) => void;
}

export function QueryWizard({ range, onRun }: QueryWizardProps) {
  const { t } = useTranslation();
  const uid = useId();
  const [state, setState] = useState<BuilderState>(() => defaultBuilderState());
  const schema = useQuery(oqlSchemaQuery(state.eventType));
  const patch = (p: Partial<BuilderState>) => setState((s) => ({ ...s, ...p }));

  const event = schema.data?.event_types.find((e) => e.name === state.eventType);
  const attributes = event?.attributes ?? [];
  const numberAttributes = attributes.filter((a) => a.type === "number");
  const mapKeys = [...(schema.data?.attribute_keys ?? []).map((k) => `attributes.${k}`), ...(schema.data?.resource_keys ?? []).map((k) => `resource.${k}`)];
  const allKeys = [...attributes.map((a) => a.name), ...mapKeys];
  const measureKeys = (NUMBER_MEASURES as readonly BuilderMeasure[]).includes(state.measure) ? [...numberAttributes.map((a) => a.name), ...mapKeys] : allKeys;

  const query = buildOql(state);
  const issue = builderIssue(state);

  const setEventType = (name: OqlEventType) => setState({ ...defaultBuilderState(name), timeseries: state.timeseries });
  const setCondition = (i: number, c: Partial<BuilderCondition>) =>
    patch({ conditions: state.conditions.map((old, k) => (k === i ? { ...old, ...c } : old)) });

  return (
    <div className="flex min-w-0 flex-col gap-3" data-testid="query-wizard">
      <div className="flex min-w-0 flex-wrap items-end gap-3">
        <Field label={t("oql.wizard.dataType")} id={`${uid}-type`}>
          <NativeSelect id={`${uid}-type`} value={state.eventType} onChange={(e) => setEventType(e.target.value as OqlEventType)}>
            {OQL_EVENT_TYPES.map((name) => (
              <option key={name} value={name}>
                {tDynamic(t, `oql.wizard.dataTypes.${name}`)}
              </option>
            ))}
          </NativeSelect>
        </Field>

        <Field label={t("oql.wizard.measure")} id={`${uid}-measure`}>
          <NativeSelect id={`${uid}-measure`} value={state.measure} onChange={(e) => patch({ measure: e.target.value as BuilderMeasure })}>
            {BUILDER_MEASURES.map((m) => (
              <option key={m} value={m}>
                {tDynamic(t, `oql.wizard.measures.${m}`)}
              </option>
            ))}
          </NativeSelect>
        </Field>

        {state.measure !== "count" && (
          <Field label={t("oql.wizard.attribute")} id={`${uid}-attr`}>
            <NativeSelect id={`${uid}-attr`} value={state.attribute} onChange={(e) => patch({ attribute: e.target.value })}>
              <option value="">{t("oql.wizard.chooseAttribute")}</option>
              {measureKeys.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </NativeSelect>
          </Field>
        )}

        {state.eventType === "Metric" && (
          <Field label={t("oql.wizard.metric")} id={`${uid}-metric`}>
            <NativeSelect id={`${uid}-metric`} value={state.metricName} onChange={(e) => patch({ metricName: e.target.value })}>
              <option value="">{t("oql.wizard.chooseMetric")}</option>
              {(schema.data?.metric_names ?? []).map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
            </NativeSelect>
          </Field>
        )}

        <label className="flex items-center gap-2 pb-1.5 text-sm">
          <input type="checkbox" checked={state.timeseries} onChange={(e) => patch({ timeseries: e.target.checked })} className="size-4" />
          {t("oql.wizard.timeseries")}
        </label>
      </div>

      <fieldset className="flex min-w-0 flex-col gap-2">
        <legend className="text-xs font-medium text-muted-foreground">{t("oql.wizard.filters")}</legend>
        {state.conditions.map((c, i) => (
          <ConditionRow
            key={i}
            condition={c}
            keys={allKeys}
            eventType={state.eventType}
            metric={state.metricName}
            range={range}
            onChange={(p) => setCondition(i, p)}
            onRemove={() => patch({ conditions: state.conditions.filter((_, k) => k !== i) })}
          />
        ))}
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={state.conditions.length >= MAX_CONDITIONS}
            onClick={() => patch({ conditions: [...state.conditions, { key: allKeys[0] ?? "", op: "=", value: "" }] })}
          >
            <Plus aria-hidden="true" />
            {t("oql.wizard.addFilter")}
          </Button>
        </div>
      </fieldset>

      <div className="flex min-w-0 flex-col gap-2">
        <div className="flex flex-wrap items-end gap-3">
          <Field label={t("oql.wizard.groupBy")} id={`${uid}-group`} hint={t("oql.wizard.groupByHint")}>
            <NativeSelect
              id={`${uid}-group`}
              value=""
              disabled={state.groupBy.length >= MAX_GROUP_BY}
              onChange={(e) => e.target.value && patch({ groupBy: [...state.groupBy, e.target.value] })}
            >
              <option value="">{t("oql.wizard.addGroupBy")}</option>
              {allKeys.filter((k) => !state.groupBy.includes(k)).map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </NativeSelect>
          </Field>
          {state.groupBy.length > 0 && (
            <Field label={t("oql.wizard.limit")} id={`${uid}-limit`}>
              <input
                id={`${uid}-limit`}
                type="number"
                min={1}
                max={50}
                value={state.limit}
                onChange={(e) => patch({ limit: Number(e.target.value) || 1 })}
                className="h-9 w-20 rounded-md border border-input bg-background px-2 text-sm shadow-xs"
              />
            </Field>
          )}
        </div>
        {state.groupBy.length > 0 && (
          <ul className="flex flex-wrap gap-1.5">
            {state.groupBy.map((k) => (
              <li key={k}>
                <Button type="button" variant="secondary" size="sm" className="font-mono" onClick={() => patch({ groupBy: state.groupBy.filter((g) => g !== k) })} aria-label={t("oql.wizard.removeGroupBy", { key: k })}>
                  {k}
                  <X aria-hidden="true" />
                </Button>
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className="flex min-w-0 flex-wrap items-center gap-2 rounded-lg border bg-muted/40 p-2">
        <span className="text-xs text-muted-foreground">{t("oql.wizard.preview")}</span>
        <code data-testid="wizard-preview" className="min-w-0 flex-1 font-mono text-xs break-all">
          {query || tDynamic(t, `oql.wizard.missing.${issue ?? "attribute"}`)}
        </code>
        <Button type="button" size="sm" disabled={!query} onClick={() => onRun(query)}>
          <Play aria-hidden="true" />
          {t("oql.wizard.run")}
        </Button>
        <Button type="button" size="sm" variant="ghost" onClick={() => setState(defaultBuilderState())}>
          <RotateCcw aria-hidden="true" />
          {t("oql.wizard.reset")}
        </Button>
      </div>
    </div>
  );
}

function Field({ label, id, hint, children }: { label: string; id: string; hint?: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <label htmlFor={id} className="text-xs font-medium text-muted-foreground">
        {label}
      </label>
      {children}
      {hint && <span className="text-[11px] text-muted-foreground">{hint}</span>}
    </div>
  );
}

function ConditionRow({
  condition,
  keys,
  eventType,
  metric,
  range,
  onChange,
  onRemove,
}: {
  condition: BuilderCondition;
  keys: string[];
  eventType: OqlEventType;
  metric: string;
  range: RangeSpec;
  onChange: (p: Partial<BuilderCondition>) => void;
  onRemove: () => void;
}) {
  const { t } = useTranslation();
  const uid = useId();
  const signal = SIGNAL[eventType];
  const needsValue = condition.op !== "is_null" && condition.op !== "is_not_null";
  // Value suggestions come from the explorer's field values; hosts and containers have no such endpoint.
  const values = useQuery({
    ...fieldValuesQuery({ signal: signal ?? "logs", key: condition.key, range, metric: eventType === "Metric" ? metric : undefined, limit: 20 }),
    enabled: !!signal && needsValue && condition.key !== "",
  });

  return (
    <div className="flex min-w-0 flex-wrap items-center gap-2">
      <NativeSelect aria-label={t("oql.wizard.filterKey")} value={condition.key} onChange={(e) => onChange({ key: e.target.value })} className="min-w-0 font-mono">
        {keys.map((k) => (
          <option key={k} value={k}>
            {k}
          </option>
        ))}
      </NativeSelect>
      <NativeSelect aria-label={t("oql.wizard.filterOp")} value={condition.op} onChange={(e) => onChange({ op: e.target.value as BuilderOp })}>
        {BUILDER_OPS.map((op) => (
          <option key={op} value={op}>
            {tDynamic(t, `oql.wizard.ops.${op}`)}
          </option>
        ))}
      </NativeSelect>
      {needsValue && (
        <>
          <input
            aria-label={t("oql.wizard.filterValue")}
            list={`${uid}-values`}
            value={condition.value}
            onChange={(e) => onChange({ value: e.target.value })}
            className="h-9 min-w-0 flex-1 rounded-md border border-input bg-background px-2 font-mono text-sm shadow-xs"
          />
          <datalist id={`${uid}-values`}>
            {(values.data?.values ?? []).map((v) => (
              <option key={v.value} value={v.value} />
            ))}
          </datalist>
        </>
      )}
      <Button type="button" variant="ghost" size="icon" className="size-8" onClick={onRemove} aria-label={t("oql.wizard.removeFilter")}>
        <X aria-hidden="true" />
      </Button>
    </div>
  );
}
