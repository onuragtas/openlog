import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, Plus, Trash2 } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { can } from "@/api/roles";
import {
  normalizePolicy,
  previewTailSampling,
  putTailSampling,
  RULE_TYPES,
  tailSamplingQuery,
  type TailSamplingPolicy,
  type TailSamplingPreview,
  type TailSamplingRule,
} from "@/api/tailSampling";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { DateTimeText, FormError, SettingsSection } from "./common";

const pct = (v: number, locale: string) => new Intl.NumberFormat(locale, { style: "percent", maximumFractionDigits: 1 }).format(v);

/** Settings → APM sampling: the organization's tail sampling policy with a preview on last hour's traces. */
export function TailSamplingSettings() {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const id = useId();
  const qc = useQueryClient();
  const me = useMe().data;
  const state = useQuery(tailSamplingQuery());
  const canEdit = me?.auth === "session" && can(me?.role, "org.update");
  const [draft, setDraft] = useState<TailSamplingPolicy | null>(null);
  const [preview, setPreview] = useState<TailSamplingPreview | null>(null);

  const save = useMutation({
    mutationFn: ({ policy, version }: { policy: TailSamplingPolicy; version: number }) => putTailSampling(normalizePolicy(policy), version),
    onSuccess: (data) => {
      qc.setQueryData(tailSamplingQuery().queryKey, data);
      setDraft(null);
    },
  });
  const runPreview = useMutation({
    mutationFn: (policy: TailSamplingPolicy) => previewTailSampling(normalizePolicy(policy)),
    onSuccess: setPreview,
  });

  if (state.isPending) return <LoadingState />;
  if (state.isError) return <ErrorState error={state.error} onRetry={() => void state.refetch()} />;
  const s = state.data;
  const policy = draft ?? s.policy;
  const editing = draft !== null;
  const update = (patch: Partial<TailSamplingPolicy>) => setDraft({ ...policy, ...patch });
  const updateRule = (i: number, patch: Partial<TailSamplingRule>) =>
    update({ rules: policy.rules.map((r, j) => (j === i ? { ...r, ...patch } : r)) });
  const moveRule = (i: number, d: -1 | 1) => {
    const rules = [...policy.rules];
    const [moved] = rules.splice(i, 1);
    if (moved) rules.splice(i + d, 0, moved);
    update({ rules });
  };
  const addRule = () => {
    const names = new Set(policy.rules.map((r) => r.name));
    let n = policy.rules.length + 1;
    while (names.has(`rule-${n}`)) n++;
    update({ rules: [...policy.rules, { name: `rule-${n}`, type: "error" }] });
  };
  const num = (v: string, fallback: number) => (v.trim() === "" || Number.isNaN(Number(v)) ? fallback : Number(v));

  return (
    <div className="flex flex-col gap-4">
      <SettingsSection title={t("settings.tailSampling.title")} description={t("settings.tailSampling.description")}>
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="text-muted-foreground">{t("settings.tailSampling.stage")}</span>
          <Badge variant={s.enabled ? "default" : "secondary"}>{s.enabled ? t("settings.tailSampling.stageOn") : t("settings.tailSampling.stageOff")}</Badge>
          {!s.enabled && <span className="text-xs text-muted-foreground">{t("settings.tailSampling.stageOffHint")}</span>}
        </div>
        <div className="text-xs text-muted-foreground">
          {s.is_default ? (
            t("settings.tailSampling.isDefault")
          ) : (
            <>
              {t("settings.tailSampling.updated", { version: s.version, email: s.updated_by_email || "—" })} <DateTimeText value={s.updated_at} relative />
            </>
          )}
        </div>

        <fieldset disabled={!canEdit} className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={policy.enabled} onChange={(e) => update({ enabled: e.target.checked })} />
            {t("settings.tailSampling.enabled")}
          </label>
          <div className="flex flex-col gap-1">
            <Label htmlFor={`${id}-baseline`}>{t("settings.tailSampling.baseline")}</Label>
            <Input
              id={`${id}-baseline`}
              type="number"
              min={0}
              max={1}
              step={0.01}
              value={policy.baseline_ratio}
              onChange={(e) => update({ baseline_ratio: num(e.target.value, 0) })}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={`${id}-rate`}>{t("settings.tailSampling.rateLimit")}</Label>
            <Input
              id={`${id}-rate`}
              type="number"
              min={0}
              step={100}
              value={policy.max_spans_per_second}
              onChange={(e) => update({ max_spans_per_second: num(e.target.value, 0) })}
            />
          </div>
        </fieldset>

        <div className="overflow-x-auto">
          <table className="w-full min-w-[48rem] text-sm" aria-label={t("settings.tailSampling.rules")}>
            <thead>
              <tr className="border-b text-left text-xs text-muted-foreground">
                <th className="py-2 pr-2 font-medium">#</th>
                <th className="py-2 pr-2 font-medium">{t("settings.tailSampling.ruleName")}</th>
                <th className="py-2 pr-2 font-medium">{t("settings.tailSampling.ruleType")}</th>
                <th className="py-2 pr-2 font-medium">{t("settings.tailSampling.ruleMatch")}</th>
                <th className="py-2 pr-2 font-medium">{t("settings.tailSampling.ruleRatio")}</th>
                <th className="py-2 font-medium">
                  <span className="sr-only">{t("settings.columns.actions")}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {policy.rules.length === 0 && (
                <tr>
                  <td colSpan={6} className="py-3 text-muted-foreground">
                    {t("settings.tailSampling.noRules")}
                  </td>
                </tr>
              )}
              {policy.rules.map((r, i) => (
                <tr key={i} className="border-b align-top" data-testid="tail-sampling-rule">
                  <td className="py-2 pr-2 font-mono text-xs">{i + 1}</td>
                  <td className="py-2 pr-2">
                    <Input aria-label={t("settings.tailSampling.ruleName")} value={r.name} maxLength={64} disabled={!canEdit} onChange={(e) => updateRule(i, { name: e.target.value })} />
                  </td>
                  <td className="py-2 pr-2">
                    <NativeSelect
                      aria-label={t("settings.tailSampling.ruleType")}
                      value={r.type}
                      disabled={!canEdit}
                      onChange={(e) => updateRule(i, { type: e.target.value as TailSamplingRule["type"] })}
                    >
                      {RULE_TYPES.map((type) => (
                        <option key={type} value={type}>
                          {t(`settings.tailSampling.types.${type}`)}
                        </option>
                      ))}
                    </NativeSelect>
                  </td>
                  <td className="py-2 pr-2">
                    <RuleFields rule={r} disabled={!canEdit} onChange={(patch) => updateRule(i, patch)} />
                  </td>
                  <td className="py-2 pr-2">
                    <Input
                      aria-label={t("settings.tailSampling.ruleRatio")}
                      type="number"
                      min={0}
                      max={1}
                      step={0.01}
                      className="w-24"
                      disabled={!canEdit}
                      value={r.ratio ?? 1}
                      onChange={(e) => updateRule(i, { ratio: num(e.target.value, 1) })}
                    />
                  </td>
                  <td className="py-2 whitespace-nowrap">
                    {canEdit && (
                      <>
                        <Button type="button" variant="ghost" size="icon" aria-label={t("settings.tailSampling.moveUp", { name: r.name })} disabled={i === 0} onClick={() => moveRule(i, -1)}>
                          <ArrowUp aria-hidden="true" />
                        </Button>
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon"
                          aria-label={t("settings.tailSampling.moveDown", { name: r.name })}
                          disabled={i === policy.rules.length - 1}
                          onClick={() => moveRule(i, 1)}
                        >
                          <ArrowDown aria-hidden="true" />
                        </Button>
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon"
                          aria-label={t("settings.tailSampling.remove", { name: r.name })}
                          onClick={() => update({ rules: policy.rules.filter((_, j) => j !== i) })}
                        >
                          <Trash2 aria-hidden="true" />
                        </Button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p className="text-xs text-muted-foreground">{t("settings.tailSampling.firstMatch")}</p>

        <div className="flex flex-wrap items-center gap-2">
          {canEdit && (
            <Button type="button" variant="outline" size="sm" onClick={addRule}>
              <Plus aria-hidden="true" />
              {t("settings.tailSampling.addRule")}
            </Button>
          )}
          <Button type="button" variant="outline" size="sm" disabled={runPreview.isPending} onClick={() => runPreview.mutate(policy)}>
            {t("settings.tailSampling.preview")}
          </Button>
          {canEdit && editing && (
            <>
              <Button type="button" size="sm" disabled={save.isPending} onClick={() => save.mutate({ policy, version: s.version })}>
                {t("settings.save")}
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => {
                  setDraft(null);
                  save.reset();
                }}
              >
                {t("common.cancel")}
              </Button>
            </>
          )}
        </div>
        <FormError error={save.error ?? runPreview.error} />
      </SettingsSection>

      {preview && (
        <SettingsSection title={t("settings.tailSampling.previewTitle")} description={t("settings.tailSampling.previewDescription", { minutes: preview.window_minutes })}>
          <dl className="grid grid-cols-2 gap-x-6 gap-y-2 text-sm sm:grid-cols-4" data-testid="tail-sampling-preview">
            <dt className="text-muted-foreground">{t("settings.tailSampling.keptTraces")}</dt>
            <dd className="font-mono">{pct(preview.kept_trace_ratio, locale)}</dd>
            <dt className="text-muted-foreground">{t("settings.tailSampling.keptSpans")}</dt>
            <dd className="font-mono">{pct(preview.kept_span_ratio, locale)}</dd>
            <dt className="text-muted-foreground">{t("settings.tailSampling.examined")}</dt>
            <dd className="font-mono">{preview.traces_examined.toLocaleString(locale)}</dd>
            <dt className="text-muted-foreground">{t("settings.tailSampling.estimated")}</dt>
            <dd className="font-mono">{Math.round(preview.estimated_traces).toLocaleString(locale)}</dd>
          </dl>
          {preview.traces_examined === 0 ? (
            <p className="text-sm text-muted-foreground">{t("settings.tailSampling.previewEmpty")}</p>
          ) : (
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b text-left text-xs text-muted-foreground">
                  <th className="py-1 pr-2 font-medium">{t("settings.tailSampling.ruleName")}</th>
                  <th className="py-1 pr-2 font-medium">{t("settings.tailSampling.matched")}</th>
                  <th className="py-1 font-medium">{t("settings.tailSampling.kept")}</th>
                </tr>
              </thead>
              <tbody>
                {preview.rules.map((r) => (
                  <tr key={r.name} className="border-b">
                    <td className="py-1 pr-2">{r.name === "baseline" ? t("settings.tailSampling.baselineRow") : r.name}</td>
                    <td className="py-1 pr-2 font-mono">{pct(r.matched_trace_ratio, locale)}</td>
                    <td className="py-1 font-mono">{pct(r.kept_trace_ratio, locale)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </SettingsSection>
      )}
    </div>
  );
}

function RuleFields({ rule, disabled, onChange }: { rule: TailSamplingRule; disabled: boolean; onChange: (patch: Partial<TailSamplingRule>) => void }) {
  const { t } = useTranslation();
  const service = (
    <Input
      aria-label={t("settings.tailSampling.fields.service")}
      placeholder={t("settings.tailSampling.fields.serviceOptional")}
      value={rule.service ?? ""}
      disabled={disabled}
      onChange={(e) => onChange({ service: e.target.value })}
    />
  );
  switch (rule.type) {
    case "error":
      return <div className="flex flex-col gap-1">{service}</div>;
    case "latency":
      return (
        <div className="flex flex-col gap-1">
          <Input
            aria-label={t("settings.tailSampling.fields.thresholdMs")}
            placeholder={t("settings.tailSampling.fields.thresholdMs")}
            type="number"
            min={1}
            value={rule.threshold_ms ?? ""}
            disabled={disabled}
            onChange={(e) => onChange({ threshold_ms: e.target.value === "" ? undefined : Number(e.target.value) })}
          />
          {service}
        </div>
      );
    case "service":
      return (
        <Input
          aria-label={t("settings.tailSampling.fields.services")}
          placeholder={t("settings.tailSampling.fields.services")}
          value={(rule.services ?? []).join(", ")}
          disabled={disabled}
          onChange={(e) => onChange({ services: e.target.value.split(",") })}
        />
      );
    case "route":
      return (
        <div className="flex flex-col gap-1">
          <Input
            aria-label={t("settings.tailSampling.fields.route")}
            placeholder="/api/orders/*"
            value={rule.route ?? ""}
            disabled={disabled}
            onChange={(e) => onChange({ route: e.target.value })}
          />
          {service}
        </div>
      );
    case "attribute":
      return (
        <div className="flex flex-col gap-1">
          <div className="flex gap-1">
            <Input aria-label={t("settings.tailSampling.fields.key")} placeholder={t("settings.tailSampling.fields.key")} value={rule.key ?? ""} disabled={disabled} onChange={(e) => onChange({ key: e.target.value })} />
            <Input
              aria-label={t("settings.tailSampling.fields.value")}
              placeholder={t("settings.tailSampling.fields.valueOptional")}
              value={rule.value ?? ""}
              disabled={disabled}
              onChange={(e) => onChange({ value: e.target.value })}
            />
          </div>
          {service}
        </div>
      );
  }
}
