// Create or edit a cloud connection (docs/contracts/api.md "Cloud connections"). The provider catalog drives
// the form: which credential fields to ask for and which services can be collected. The test button makes one
// real provider call before anything is saved, so a wrong key is found here and not in the poll history.
// Router-free: the page passes the callbacks.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleCheck } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { CloudConnection, CloudConnectionInput, CloudProviderName } from "@/api/cloud";
import { cloudProvidersQuery, createCloudConnection, testCloudConnection, updateCloudConnection } from "@/api/cloud";
import { describedBy } from "@/components/alerts/field-utils";
import { Field, Section } from "@/components/alerts/fields";
import { FormError } from "@/components/settings/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { ErrorState, LoadingState } from "@/components/StateViews";
import {
  credentialsFor,
  formatScopes,
  hasCredentials,
  parseScopes,
  scopeLabelKey,
  validateCloudInput,
  type CloudFormErrors,
} from "@/lib/cloud";

/** i18n keys of the client-side checks (the message list stays a literal union for the typed t()). */
const VALIDATION_MESSAGES = {
  required: "cloud.validation.required",
  scopes: "cloud.validation.scopes",
  services: "cloud.validation.services",
  interval: "cloud.validation.interval",
  maxMetrics: "cloud.validation.maxMetrics",
  maxCalls: "cloud.validation.maxCalls",
  credentials: "cloud.validation.credentials",
} as const;

/** Editable form state: numbers and the parsed fields stay strings until they are submitted. */
interface Draft {
  name: string;
  provider: CloudProviderName;
  scopes: string;
  services: string[];
  poll_interval_seconds: string;
  max_metrics_per_poll: string;
  max_api_calls_per_poll: string;
  enabled: boolean;
  credentials: Record<string, string>;
}

function draftOf(c?: CloudConnection): Draft {
  return {
    name: c?.name ?? "",
    provider: (c?.provider ?? "aws") as CloudProviderName,
    scopes: formatScopes(c?.scopes),
    services: c?.services ?? [],
    poll_interval_seconds: String(c?.poll_interval_seconds ?? 300),
    max_metrics_per_poll: String(c?.max_metrics_per_poll ?? 5000),
    max_api_calls_per_poll: String(c?.max_api_calls_per_poll ?? 200),
    enabled: c?.enabled ?? true,
    credentials: {},
  };
}

export interface CloudConnectionFormProps {
  connection?: CloudConnection;
  onSaved?: (connection: CloudConnection) => void;
  onCancel?: () => void;
  readOnly?: boolean;
}

export function CloudConnectionForm({ connection, onSaved, onCancel, readOnly = false }: CloudConnectionFormProps) {
  const { t } = useTranslation();
  const uid = useId();
  const queryClient = useQueryClient();
  const catalog = useQuery(cloudProvidersQuery());
  const [draft, setDraft] = useState<Draft>(() => draftOf(connection));
  const [submitted, setSubmitted] = useState(false);

  const provider = catalog.data?.providers.find((p) => p.id === draft.provider);
  const credentialFields = provider?.credentials ?? [];
  const typedCredentials = hasCredentials(credentialFields, draft.credentials);
  // On an edit the stored credentials are kept when nothing was typed, so they are not required again.
  const requiredCredentials = useMemo(
    () => (connection?.credentials_set && !typedCredentials ? [] : credentialFields.filter((f) => f.required)),
    [connection?.credentials_set, typedCredentials, credentialFields],
  );

  const toInput = (d: Draft): CloudConnectionInput => ({
    name: d.name.trim(),
    provider: d.provider,
    ingest_mode: "poll",
    enabled: d.enabled,
    scopes: parseScopes(d.scopes) ?? [],
    services: d.services,
    poll_interval_seconds: Number(d.poll_interval_seconds.trim()),
    max_metrics_per_poll: Number(d.max_metrics_per_poll.trim()),
    max_api_calls_per_poll: Number(d.max_api_calls_per_poll.trim()),
    credentials: typedCredentials ? credentialsFor(credentialFields, d.credentials) : null,
  });

  const errors: CloudFormErrors = useMemo(() => {
    const e = validateCloudInput(toInput(draft), { requiredCredentials, values: draft.credentials });
    // A parse failure cannot be expressed on the parsed value, so it is reported on the raw field.
    if (parseScopes(draft.scopes) === null) e.scopes = "scopes";
    return e;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [draft, requiredCredentials]);

  const id = (name: string) => `${uid}-${name}`;
  const message = (field: keyof CloudConnectionInput): string | undefined => {
    const key = submitted ? errors[field] : undefined;
    return key ? t(VALIDATION_MESSAGES[key]) : undefined;
  };
  const update = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }));
  const setCredential = (key: string, value: string) =>
    setDraft((d) => ({ ...d, credentials: { ...d.credentials, [key]: value } }));

  const save = useMutation({
    mutationFn: (input: CloudConnectionInput) =>
      connection ? updateCloudConnection(connection.id, input) : createCloudConnection(input),
    onSuccess: (saved) => {
      void queryClient.invalidateQueries({ queryKey: ["cloud"] });
      onSaved?.(saved);
    },
  });

  // The test uses the typed credentials when there are any, else the stored ones of the connection.
  const test = useMutation({
    mutationFn: () => {
      const scopes = parseScopes(draft.scopes) ?? [];
      return testCloudConnection({
        connection_id: !typedCredentials && connection ? connection.id : undefined,
        provider: draft.provider,
        scope: scopes[0],
        credentials: typedCredentials ? credentialsFor(credentialFields, draft.credentials) : null,
      });
    },
  });

  if (catalog.isPending) return <LoadingState />;
  if (catalog.isError) return <ErrorState error={catalog.error} onRetry={() => void catalog.refetch()} />;

  const scopeKey = scopeLabelKey(draft.provider);
  const scopes = parseScopes(draft.scopes) ?? [];
  const canTest = catalog.data.test_supported && scopes.length > 0 && (typedCredentials || !!connection?.credentials_set);

  return (
    <form
      noValidate
      className="flex flex-col gap-4"
      data-testid="cloud-form"
      onSubmit={(e) => {
        e.preventDefault();
        setSubmitted(true);
        if (readOnly || Object.keys(errors).length > 0) return;
        save.mutate(toInput(draft));
      }}
    >
      <fieldset disabled={readOnly} className="flex min-w-0 flex-col gap-4">
        <Section title={connection ? t("cloud.form.editTitle") : t("cloud.form.createTitle")}>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id={id("name")} label={t("cloud.form.name")} error={message("name")}>
              <Input
                id={id("name")}
                value={draft.name}
                onChange={(e) => update({ name: e.target.value })}
                {...describedBy(id("name"), message("name"))}
              />
            </Field>
            <Field id={id("provider")} label={t("cloud.form.provider")}>
              <NativeSelect
                id={id("provider")}
                value={draft.provider}
                onChange={(e) =>
                  // Services and credentials belong to a provider, so switching clears both.
                  update({ provider: e.target.value as CloudProviderName, services: [], credentials: {} })
                }
              >
                {catalog.data.providers.map((p) => (
                  <option key={p.id} value={p.id}>
                    {t(`cloud.providers.${p.id}`)}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            <Field
              id={id("scopes")}
              label={t(`cloud.scopeLabelPlural.${scopeKey}`)}
              hint={t("cloud.form.scopesHint")}
              error={message("scopes")}
              className="sm:col-span-2"
            >
              <textarea
                id={id("scopes")}
                rows={3}
                value={draft.scopes}
                placeholder={draft.provider === "aws" ? "eu-central-1" : draft.provider === "gcp" ? "my-project" : "00000000-1111-2222-3333-444444444444"}
                onChange={(e) => update({ scopes: e.target.value })}
                className="rounded-md border border-input bg-background px-3 py-2 font-mono text-sm shadow-xs"
                {...describedBy(id("scopes"), message("scopes"), t("cloud.form.scopesHint"))}
              />
            </Field>
          </div>
        </Section>

        <Section title={t("cloud.form.services")} description={t("cloud.form.servicesHint")}>
          <div className="flex flex-col gap-2">
            <ul className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
              {(provider?.services ?? []).map((s) => (
                <li key={s.id} className="flex items-center gap-2">
                  <input
                    id={id(`svc-${s.id}`)}
                    type="checkbox"
                    className="size-4 accent-primary"
                    checked={draft.services.includes(s.id)}
                    onChange={(e) =>
                      update({
                        services: e.target.checked
                          ? [...draft.services, s.id]
                          : draft.services.filter((x) => x !== s.id),
                      })
                    }
                  />
                  <label htmlFor={id(`svc-${s.id}`)} className="text-sm">
                    {t(`cloud.services.${s.id}`, { defaultValue: s.id })}
                  </label>
                </li>
              ))}
            </ul>
            {message("services") && (
              <p className="text-xs text-destructive-text">{message("services")}</p>
            )}
          </div>
        </Section>

        <Section
          title={t("cloud.form.credentialsTitle")}
          description={connection?.credentials_set ? t("cloud.form.credentialsKept") : t("cloud.form.credentialsNever")}
        >
          <div className="grid gap-4 sm:grid-cols-2">
            {credentialFields.map((f) => (
              <Field
                key={f.key}
                id={id(f.key)}
                label={t(`cloud.credentials.${f.key}`, { defaultValue: f.key })}
                error={f.required ? message("credentials") : undefined}
                className={f.key === "private_key" ? "sm:col-span-2" : undefined}
              >
                {f.key === "private_key" ? (
                  <textarea
                    id={id(f.key)}
                    rows={4}
                    value={draft.credentials[f.key] ?? ""}
                    onChange={(e) => setCredential(f.key, e.target.value)}
                    className="rounded-md border border-input bg-background px-3 py-2 font-mono text-xs shadow-xs"
                    {...describedBy(id(f.key), f.required ? message("credentials") : undefined)}
                  />
                ) : (
                  <Input
                    id={id(f.key)}
                    type={f.secret ? "password" : "text"}
                    autoComplete="off"
                    value={draft.credentials[f.key] ?? ""}
                    onChange={(e) => setCredential(f.key, e.target.value)}
                    {...describedBy(id(f.key), f.required ? message("credentials") : undefined)}
                  />
                )}
              </Field>
            ))}
          </div>
        </Section>

        <Section title={t("cloud.form.limitsTitle")}>
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <Field id={id("interval")} label={t("cloud.form.interval")} hint={t("cloud.form.intervalHint")} error={message("poll_interval_seconds")}>
              <Input
                id={id("interval")}
                inputMode="numeric"
                value={draft.poll_interval_seconds}
                onChange={(e) => update({ poll_interval_seconds: e.target.value })}
                {...describedBy(id("interval"), message("poll_interval_seconds"), t("cloud.form.intervalHint"))}
              />
            </Field>
            <Field id={id("maxMetrics")} label={t("cloud.form.maxMetrics")} hint={t("cloud.form.maxMetricsHint")} error={message("max_metrics_per_poll")}>
              <Input
                id={id("maxMetrics")}
                inputMode="numeric"
                value={draft.max_metrics_per_poll}
                onChange={(e) => update({ max_metrics_per_poll: e.target.value })}
                {...describedBy(id("maxMetrics"), message("max_metrics_per_poll"), t("cloud.form.maxMetricsHint"))}
              />
            </Field>
            <Field id={id("maxCalls")} label={t("cloud.form.maxCalls")} hint={t("cloud.form.maxCallsHint")} error={message("max_api_calls_per_poll")}>
              <Input
                id={id("maxCalls")}
                inputMode="numeric"
                value={draft.max_api_calls_per_poll}
                onChange={(e) => update({ max_api_calls_per_poll: e.target.value })}
                {...describedBy(id("maxCalls"), message("max_api_calls_per_poll"), t("cloud.form.maxCallsHint"))}
              />
            </Field>
            <Field id={id("enabled")} label={t("cloud.form.enabled")} hint={t("cloud.form.enabledHint")}>
              <input
                id={id("enabled")}
                type="checkbox"
                checked={draft.enabled}
                onChange={(e) => update({ enabled: e.target.checked })}
                className="size-4 accent-primary"
                {...describedBy(id("enabled"), undefined, t("cloud.form.enabledHint"))}
              />
            </Field>
          </div>
        </Section>
      </fieldset>

      <div className="flex flex-wrap items-center gap-3">
        {!readOnly && (
          <Button type="submit" disabled={save.isPending}>
            {connection ? t("cloud.form.save") : t("cloud.form.create")}
          </Button>
        )}
        {!readOnly && (
          <Button type="button" variant="outline" onClick={() => test.mutate()} disabled={!canTest || test.isPending}>
            {test.isPending ? t("cloud.form.testing") : t("cloud.form.test")}
          </Button>
        )}
        {onCancel && (
          <Button type="button" variant="outline" onClick={onCancel}>
            {t("cloud.form.cancel")}
          </Button>
        )}
        {readOnly && <p className="text-sm text-muted-foreground">{t("cloud.readOnly")}</p>}
        {test.data?.ok && (
          <p className="inline-flex items-center gap-1.5 text-sm text-success-text" data-testid="cloud-test-ok">
            <CircleCheck className="size-4" aria-hidden="true" />
            {t("cloud.form.testOk", { scope: t(`cloud.scopeLabel.${scopeKey}`).toLowerCase() })}
          </p>
        )}
        {test.data && !test.data.ok && (
          <p role="alert" className="text-sm text-destructive" data-testid="cloud-test-error">
            {test.data.error}
          </p>
        )}
        <FormError error={test.error} />
        <FormError error={save.error} />
      </div>
    </form>
  );
}
