// Create or edit a job monitor (docs/contracts/api.md "Job monitoring", D-141). The schedule is either a
// crontab expression in a time zone or a reporting interval; the rest of the form is the same either way.
// Router-free: the page passes the callbacks.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { JobMonitor, JobMonitorInput, JobScheduleKind } from "@/api/jobs";
import { createJobMonitor, updateJobMonitor } from "@/api/jobs";
import { describedBy } from "@/components/alerts/field-utils";
import { Field, Section } from "@/components/alerts/fields";
import { FormError } from "@/components/settings/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { browserTimeZone, validateJobInput, type JobFormErrors } from "@/lib/jobs";

const KINDS: JobScheduleKind[] = ["cron", "interval"];

/** Common crontab expressions, offered so the field is not a blank line of syntax. */
const CRON_EXAMPLES = ["@hourly", "@daily", "0 3 * * *", "*/15 * * * *", "0 0 * * 0", "0 0 1 * *"];

/** i18n keys of the client-side checks (the message list stays a literal union for the typed t()). */
const VALIDATION_MESSAGES = {
  required: "jobs.validation.required",
  cron: "jobs.validation.cron",
  interval: "jobs.validation.interval",
  grace: "jobs.validation.grace",
  timeZone: "jobs.validation.timeZone",
} as const;

interface Draft {
  name: string;
  description: string;
  kind: JobScheduleKind;
  cron: string;
  time_zone: string;
  interval_seconds: string;
  grace_seconds: string;
  tags: string;
  enabled: boolean;
}

function draftOf(monitor?: JobMonitor): Draft {
  return {
    name: monitor?.name ?? "",
    description: monitor?.description ?? "",
    kind: (monitor?.kind as JobScheduleKind) ?? "cron",
    cron: monitor?.cron || "0 3 * * *",
    time_zone: monitor?.time_zone ?? browserTimeZone(),
    interval_seconds: String(monitor?.interval_seconds || 3600),
    grace_seconds: String(monitor?.grace_seconds ?? 300),
    tags: (monitor?.tags ?? []).join(", "),
    enabled: monitor?.enabled ?? true,
  };
}

function toInput(d: Draft): JobMonitorInput {
  const number = (s: string) => Number(s.trim());
  const common = {
    name: d.name.trim(),
    description: d.description.trim(),
    kind: d.kind,
    enabled: d.enabled,
    grace_seconds: number(d.grace_seconds),
    tags: d.tags
      .split(",")
      .map((v) => v.trim())
      .filter((v) => v !== ""),
    // The server clears the fields of the other kind; the form sends only the ones its kind uses.
    cron: "",
    time_zone: "",
    interval_seconds: 0,
  };
  return d.kind === "interval"
    ? { ...common, interval_seconds: number(d.interval_seconds) }
    : { ...common, cron: d.cron.trim(), time_zone: d.time_zone.trim() };
}

export interface JobFormProps {
  monitor?: JobMonitor;
  onSaved?: (monitor: JobMonitor) => void;
  onCancel?: () => void;
  readOnly?: boolean;
}

export function JobForm({ monitor, onSaved, onCancel, readOnly = false }: JobFormProps) {
  const { t } = useTranslation();
  const uid = useId();
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<Draft>(() => draftOf(monitor));
  const [submitted, setSubmitted] = useState(false);
  const errors: JobFormErrors = useMemo(() => validateJobInput(toInput(draft)), [draft]);
  const id = (name: string) => `${uid}-${name}`;
  const err = (field: keyof JobMonitorInput) => (submitted ? errors[field] : undefined);
  const message = (field: keyof JobMonitorInput): string | undefined => {
    const key = err(field);
    return key ? t(VALIDATION_MESSAGES[key]) : undefined;
  };
  const update = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }));

  const save = useMutation({
    mutationFn: (input: JobMonitorInput) => (monitor ? updateJobMonitor(monitor.id, input) : createJobMonitor(input)),
    onSuccess: (saved) => {
      void queryClient.invalidateQueries({ queryKey: ["jobs"] });
      onSaved?.(saved);
    },
  });

  return (
    <form
      noValidate
      className="flex flex-col gap-4"
      data-testid="job-form"
      onSubmit={(e) => {
        e.preventDefault();
        setSubmitted(true);
        if (readOnly || Object.keys(errors).length > 0) return;
        save.mutate(toInput(draft));
      }}
    >
      <fieldset disabled={readOnly} className="flex min-w-0 flex-col gap-4">
        <Section title={monitor ? t("jobs.form.editTitle") : t("jobs.form.createTitle")}>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id={id("name")} label={t("jobs.form.name")} error={message("name")}>
              <Input id={id("name")} value={draft.name} onChange={(e) => update({ name: e.target.value })} {...describedBy(id("name"), message("name"))} />
            </Field>
            <Field id={id("kind")} label={t("jobs.form.kind")} hint={t(`jobs.kindHints.${draft.kind}`)}>
              <NativeSelect
                id={id("kind")}
                value={draft.kind}
                onChange={(e) => update({ kind: e.target.value as JobScheduleKind })}
                {...describedBy(id("kind"), undefined, t(`jobs.kindHints.${draft.kind}`))}
              >
                {KINDS.map((k) => (
                  <option key={k} value={k}>
                    {t(`jobs.kinds.${k}`)}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            {draft.kind === "cron" ? (
              <>
                <Field id={id("cron")} label={t("jobs.form.cron")} hint={t("jobs.form.cronHint")} error={message("cron")}>
                  <Input
                    id={id("cron")}
                    value={draft.cron}
                    list={id("cron-examples")}
                    placeholder="0 3 * * *"
                    className="font-mono"
                    onChange={(e) => update({ cron: e.target.value })}
                    {...describedBy(id("cron"), message("cron"), t("jobs.form.cronHint"))}
                  />
                  <datalist id={id("cron-examples")}>
                    {CRON_EXAMPLES.map((v) => (
                      <option key={v} value={v} />
                    ))}
                  </datalist>
                </Field>
                <Field id={id("zone")} label={t("jobs.form.timeZone")} hint={t("jobs.form.timeZoneHint")} error={message("time_zone")}>
                  <Input
                    id={id("zone")}
                    value={draft.time_zone}
                    placeholder="Europe/Istanbul"
                    onChange={(e) => update({ time_zone: e.target.value })}
                    {...describedBy(id("zone"), message("time_zone"), t("jobs.form.timeZoneHint"))}
                  />
                </Field>
              </>
            ) : (
              <Field id={id("interval")} label={t("jobs.form.interval")} hint={t("jobs.form.intervalHint")} error={message("interval_seconds")}>
                <Input
                  id={id("interval")}
                  inputMode="numeric"
                  value={draft.interval_seconds}
                  onChange={(e) => update({ interval_seconds: e.target.value })}
                  {...describedBy(id("interval"), message("interval_seconds"), t("jobs.form.intervalHint"))}
                />
              </Field>
            )}
            <Field id={id("grace")} label={t("jobs.form.grace")} hint={t("jobs.form.graceHint")} error={message("grace_seconds")}>
              <Input
                id={id("grace")}
                inputMode="numeric"
                value={draft.grace_seconds}
                onChange={(e) => update({ grace_seconds: e.target.value })}
                {...describedBy(id("grace"), message("grace_seconds"), t("jobs.form.graceHint"))}
              />
            </Field>
            <Field id={id("tags")} label={t("jobs.form.tags")} hint={t("jobs.form.tagsHint")}>
              <Input
                id={id("tags")}
                value={draft.tags}
                placeholder="backup, nightly"
                onChange={(e) => update({ tags: e.target.value })}
                {...describedBy(id("tags"), undefined, t("jobs.form.tagsHint"))}
              />
            </Field>
            <Field id={id("description")} label={t("jobs.form.description")} hint={t("jobs.form.descriptionHint")} className="sm:col-span-2">
              <Input
                id={id("description")}
                value={draft.description}
                onChange={(e) => update({ description: e.target.value })}
                {...describedBy(id("description"), undefined, t("jobs.form.descriptionHint"))}
              />
            </Field>
            <Field id={id("enabled")} label={t("jobs.form.enabled")} hint={t("jobs.form.enabledHint")}>
              <input
                id={id("enabled")}
                type="checkbox"
                checked={draft.enabled}
                onChange={(e) => update({ enabled: e.target.checked })}
                className="size-4 accent-primary"
                {...describedBy(id("enabled"), undefined, t("jobs.form.enabledHint"))}
              />
            </Field>
          </div>
        </Section>
        {save.isError && <FormError error={save.error} />}
        {readOnly && <p className="text-xs text-muted-foreground">{t("jobs.form.readOnly")}</p>}
        <div className="flex flex-wrap gap-2">
          <Button type="submit" disabled={readOnly || save.isPending}>
            {monitor ? t("jobs.form.save") : t("jobs.form.create")}
          </Button>
          {onCancel && (
            <Button type="button" variant="outline" onClick={onCancel}>
              {t("jobs.form.cancel")}
            </Button>
          )}
        </div>
      </fieldset>
    </form>
  );
}
