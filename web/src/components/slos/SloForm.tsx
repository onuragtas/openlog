// Create or edit an SLO (docs/contracts/slo.md §1). Empty namespace/environment mean "every one" (null),
// as in the alert rule editor. Router-free: the page passes the callbacks.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Slo, SloInput, SloSliType } from "@/api/slos";
import { createSlo, updateSlo } from "@/api/slos";
import { Field, Section } from "@/components/alerts/fields";
import { describedBy } from "@/components/alerts/field-utils";
import { FormError } from "@/components/settings/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { validateSloInput, type SloFormErrors } from "@/lib/slo";

const SLI_TYPES: SloSliType[] = ["availability", "latency"];
const WINDOWS = [7, 28, 30] as const;
type WindowDays = SloInput["window_days"];

/** i18n keys of the client-side checks (the message list stays a literal union for the typed t()). */
const VALIDATION_MESSAGES = {
  required: "slo.validation.required",
  objective: "slo.validation.objective",
  latency: "slo.validation.latency",
} as const;

/** Editable form state: numbers stay strings until they are submitted. */
interface Draft {
  name: string;
  description: string;
  service_name: string;
  service_namespace: string;
  environment: string;
  sli_type: SloSliType;
  latency_threshold_ms: string;
  objective: string;
  window_days: WindowDays;
}

function draftOf(slo?: Slo): Draft {
  return {
    name: slo?.name ?? "",
    description: slo?.description ?? "",
    service_name: slo?.service_name ?? "",
    service_namespace: slo?.service_namespace ?? "",
    environment: slo?.environment ?? "",
    sli_type: slo?.sli_type ?? "availability",
    latency_threshold_ms: slo?.latency_threshold_ms ? String(slo.latency_threshold_ms) : "300",
    objective: slo ? String(slo.objective) : "99.9",
    // The response type widens window_days to a number; the API only ever stores 7, 28 or 30 (slo.md §1).
    window_days: (slo?.window_days ?? 28) as WindowDays,
  };
}

function toInput(d: Draft): SloInput {
  const number = (s: string) => Number(s.trim().replace(",", "."));
  return {
    name: d.name.trim(),
    description: d.description.trim(),
    service_name: d.service_name.trim(),
    service_namespace: d.service_namespace.trim() === "" ? null : d.service_namespace.trim(),
    environment: d.environment.trim() === "" ? null : d.environment.trim(),
    sli_type: d.sli_type,
    ...(d.sli_type === "latency" ? { latency_threshold_ms: number(d.latency_threshold_ms) } : {}),
    objective: number(d.objective),
    window_days: d.window_days,
  };
}

export interface SloFormProps {
  slo?: Slo;
  onSaved?: (slo: Slo) => void;
  onCancel?: () => void;
  readOnly?: boolean;
}

export function SloForm({ slo, onSaved, onCancel, readOnly = false }: SloFormProps) {
  const { t } = useTranslation();
  const uid = useId();
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<Draft>(() => draftOf(slo));
  const [submitted, setSubmitted] = useState(false);
  const errors: SloFormErrors = useMemo(() => validateSloInput(toInput(draft)), [draft]);
  const id = (name: string) => `${uid}-${name}`;
  const err = (field: keyof SloInput) => (submitted ? errors[field] : undefined);
  const message = (field: keyof SloInput): string | undefined => {
    const key = err(field);
    return key ? t(VALIDATION_MESSAGES[key]) : undefined;
  };
  const update = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }));

  const save = useMutation({
    mutationFn: (input: SloInput) => (slo ? updateSlo(slo.id, input) : createSlo(input)),
    onSuccess: (saved) => {
      void queryClient.invalidateQueries({ queryKey: ["slos"] });
      onSaved?.(saved);
    },
  });

  return (
    <form
      noValidate
      className="flex flex-col gap-4"
      data-testid="slo-form"
      onSubmit={(e) => {
        e.preventDefault();
        setSubmitted(true);
        if (readOnly || Object.keys(errors).length > 0) return;
        save.mutate(toInput(draft));
      }}
    >
      <fieldset disabled={readOnly} className="flex min-w-0 flex-col gap-4">
        <Section title={slo ? t("slo.form.editTitle") : t("slo.form.createTitle")}>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id={id("name")} label={t("slo.form.name")} error={message("name")}>
              <Input id={id("name")} value={draft.name} onChange={(e) => update({ name: e.target.value })} {...describedBy(id("name"), message("name"))} />
            </Field>
            <Field id={id("service")} label={t("slo.form.service")} error={message("service_name")}>
              <Input
                id={id("service")}
                value={draft.service_name}
                placeholder="checkout"
                onChange={(e) => update({ service_name: e.target.value })}
                {...describedBy(id("service"), message("service_name"))}
              />
            </Field>
            <Field id={id("ns")} label={t("slo.form.namespace")} hint={t("slo.form.namespaceHint")}>
              <Input id={id("ns")} value={draft.service_namespace} onChange={(e) => update({ service_namespace: e.target.value })} {...describedBy(id("ns"), undefined, t("slo.form.namespaceHint"))} />
            </Field>
            <Field id={id("env")} label={t("slo.form.environment")} hint={t("slo.form.environmentHint")}>
              <Input id={id("env")} value={draft.environment} onChange={(e) => update({ environment: e.target.value })} {...describedBy(id("env"), undefined, t("slo.form.environmentHint"))} />
            </Field>
            <Field id={id("sli")} label={t("slo.form.sliType")} hint={t(`slo.sliHints.${draft.sli_type}`)}>
              <NativeSelect id={id("sli")} value={draft.sli_type} onChange={(e) => update({ sli_type: e.target.value as SloSliType })}>
                {SLI_TYPES.map((v) => (
                  <option key={v} value={v}>
                    {t(`slo.sliTypes.${v}`)}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            {draft.sli_type === "latency" ? (
              <Field id={id("threshold")} label={t("slo.form.latencyThreshold")} error={message("latency_threshold_ms")}>
                <Input
                  id={id("threshold")}
                  inputMode="numeric"
                  value={draft.latency_threshold_ms}
                  onChange={(e) => update({ latency_threshold_ms: e.target.value })}
                  {...describedBy(id("threshold"), message("latency_threshold_ms"))}
                />
              </Field>
            ) : (
              <div aria-hidden="true" />
            )}
            <Field id={id("objective")} label={t("slo.form.objective")} hint={t("slo.form.objectiveHint")} error={message("objective")}>
              <Input
                id={id("objective")}
                inputMode="decimal"
                value={draft.objective}
                onChange={(e) => update({ objective: e.target.value })}
                {...describedBy(id("objective"), message("objective"), t("slo.form.objectiveHint"))}
              />
            </Field>
            <Field id={id("window")} label={t("slo.form.window")}>
              <NativeSelect id={id("window")} value={draft.window_days} onChange={(e) => update({ window_days: Number(e.target.value) as WindowDays })}>
                {WINDOWS.map((d) => (
                  <option key={d} value={d}>
                    {t("slo.windowDays", { count: d })}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            <Field id={id("description")} label={t("slo.form.description")} className="sm:col-span-2">
              <textarea
                id={id("description")}
                rows={2}
                value={draft.description}
                onChange={(e) => update({ description: e.target.value })}
                className="rounded-md border border-input bg-background px-3 py-2 text-sm shadow-xs"
              />
            </Field>
          </div>
        </Section>
      </fieldset>
      <div className="flex flex-wrap items-center gap-3">
        {!readOnly && (
          <Button type="submit" disabled={save.isPending}>
            {slo ? t("slo.form.save") : t("slo.form.create")}
          </Button>
        )}
        {onCancel && (
          <Button type="button" variant="outline" onClick={onCancel}>
            {t("slo.form.cancel")}
          </Button>
        )}
        {readOnly && <p className="text-sm text-muted-foreground">{t("slo.form.readOnly")}</p>}
        <FormError error={save.error} />
      </div>
    </form>
  );
}
