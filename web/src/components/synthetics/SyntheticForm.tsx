// Create or edit a synthetic check (docs/contracts/api.md "Synthetic monitoring"). Headers are edited as
// "Name: value" lines and the expected status codes as a comma separated list; both are parsed by
// lib/synthetics. Router-free: the page passes the callbacks.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type {
  SyntheticAssertionType,
  SyntheticCheck,
  SyntheticCheckInput,
} from "@/api/synthetics";
import { createSyntheticCheck, updateSyntheticCheck } from "@/api/synthetics";
import { describedBy } from "@/components/alerts/field-utils";
import { Field, Section } from "@/components/alerts/fields";
import { FormError } from "@/components/settings/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import {
  CHECK_TYPES,
  DNS_RECORD_TYPES,
  formatExpectedStatus,
  formatHeaders,
  parseExpectedStatus,
  parseHeaders,
  validateSyntheticInput,
  type SyntheticCheckKind,
  type SyntheticFormErrors,
} from "@/lib/synthetics";

const METHODS = [
  "GET",
  "HEAD",
  "POST",
  "PUT",
  "PATCH",
  "DELETE",
  "OPTIONS",
] as const;
const ASSERTIONS: SyntheticAssertionType[] = [
  "none",
  "contains",
  "not_contains",
  "json_path",
];

/** i18n keys of the client-side checks (the message list stays a literal union for the typed t()). */
const VALIDATION_MESSAGES = {
  required: "synthetics.validation.required",
  url: "synthetics.validation.url",
  expectedStatus: "synthetics.validation.expectedStatus",
  headers: "synthetics.validation.headers",
  timeout: "synthetics.validation.timeout",
  interval: "synthetics.validation.interval",
  assertionValue: "synthetics.validation.assertionValue",
  assertionPath: "synthetics.validation.assertionPath",
  hostPort: "synthetics.validation.hostPort",
  hostname: "synthetics.validation.hostname",
  warningDays: "synthetics.validation.warningDays",
} as const;

/** Editable form state: numbers and the parsed fields stay strings until they are submitted. */
interface Draft {
  name: string;
  type: SyntheticCheckKind;
  target: string;
  dns_record_type: string;
  dns_expected: string;
  tls_warning_days: string;
  url: string;
  method: string;
  enabled: boolean;
  headers: string;
  body: string;
  expected_status: string;
  assertion_type: SyntheticAssertionType;
  assertion_path: string;
  assertion_value: string;
  timeout_ms: string;
  interval_seconds: string;
}

function draftOf(check?: SyntheticCheck): Draft {
  return {
    name: check?.name ?? "",
    type: (CHECK_TYPES as readonly string[]).includes(check?.type ?? "")
      ? (check?.type as SyntheticCheckKind)
      : "http",
    target: check?.target ?? "",
    dns_record_type: check?.dns_record_type || "A",
    dns_expected: (check?.dns_expected ?? []).join(", "),
    tls_warning_days: String(check?.tls_warning_days ?? 14),
    url: check?.url ?? "",
    method: check?.method ?? "GET",
    enabled: check?.enabled ?? true,
    headers: formatHeaders(check?.headers),
    body: check?.body ?? "",
    expected_status: formatExpectedStatus(check?.expected_status) || "200",
    assertion_type: check?.assertion_type ?? "none",
    assertion_path: check?.assertion_path ?? "",
    assertion_value: check?.assertion_value ?? "",
    timeout_ms: String(check?.timeout_ms ?? 10000),
    interval_seconds: String(check?.interval_seconds ?? 300),
  };
}

/** The comma or newline separated expected answers of a dns check. */
function parseExpected(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((v) => v.trim())
    .filter((v) => v !== "");
}

function toInput(d: Draft): SyntheticCheckInput {
  const number = (s: string) => Number(s.trim());
  const sendsBody = d.method !== "GET" && d.method !== "HEAD";
  const common = {
    name: d.name.trim(),
    type: d.type,
    enabled: d.enabled,
    timeout_ms: number(d.timeout_ms),
    interval_seconds: number(d.interval_seconds),
    // The server clears the fields of the other types; the form sends only the ones its type uses, so a
    // draft that was switched from http to dns does not carry the old URL along.
    url: "",
    method: "GET" as SyntheticCheckInput["method"],
    target: "",
    tls_warning_days: 14,
  };
  switch (d.type) {
    case "tcp":
      return { ...common, target: d.target.trim(), tls_warning_days: 0 };
    case "tls":
      return {
        ...common,
        target: d.target.trim(),
        tls_warning_days: number(d.tls_warning_days),
      };
    case "dns":
      return {
        ...common,
        target: d.target.trim(),
        dns_record_type:
          d.dns_record_type as SyntheticCheckInput["dns_record_type"],
        dns_expected: parseExpected(d.dns_expected),
        tls_warning_days: 0,
      };
    default:
      return {
        ...common,
        url: d.url.trim(),
        method: d.method as SyntheticCheckInput["method"],
        headers: parseHeaders(d.headers) ?? {},
        body: sendsBody ? d.body : "",
        expected_status: parseExpectedStatus(d.expected_status) ?? [],
        assertion_type: d.assertion_type,
        assertion_path:
          d.assertion_type === "json_path" ? d.assertion_path.trim() : "",
        assertion_value: d.assertion_type === "none" ? "" : d.assertion_value,
        tls_warning_days: 0,
      };
  }
}

export interface SyntheticFormProps {
  check?: SyntheticCheck;
  onSaved?: (check: SyntheticCheck) => void;
  onCancel?: () => void;
  readOnly?: boolean;
}

export function SyntheticForm({
  check,
  onSaved,
  onCancel,
  readOnly = false,
}: SyntheticFormProps) {
  const { t } = useTranslation();
  const uid = useId();
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<Draft>(() => draftOf(check));
  const [submitted, setSubmitted] = useState(false);
  const errors: SyntheticFormErrors = useMemo(() => {
    const e = validateSyntheticInput(toInput(draft));
    if (draft.type !== "http") return e;
    // The parse failures cannot be expressed on the parsed value, so they are reported on the raw field.
    if (parseHeaders(draft.headers) === null) e.headers = "headers";
    if (parseExpectedStatus(draft.expected_status) === null)
      e.expected_status = "expectedStatus";
    return e;
  }, [draft]);
  const id = (name: string) => `${uid}-${name}`;
  const err = (field: keyof SyntheticCheckInput) =>
    submitted ? errors[field] : undefined;
  const message = (field: keyof SyntheticCheckInput): string | undefined => {
    const key = err(field);
    return key ? t(VALIDATION_MESSAGES[key]) : undefined;
  };
  const update = (patch: Partial<Draft>) =>
    setDraft((d) => ({ ...d, ...patch }));
  const sendsBody = draft.method !== "GET" && draft.method !== "HEAD";
  const isHTTP = draft.type === "http";

  const save = useMutation({
    mutationFn: (input: SyntheticCheckInput) =>
      check
        ? updateSyntheticCheck(check.id, input)
        : createSyntheticCheck(input),
    onSuccess: (saved) => {
      void queryClient.invalidateQueries({ queryKey: ["synthetics"] });
      onSaved?.(saved);
    },
  });

  return (
    <form
      noValidate
      className="flex flex-col gap-4"
      data-testid="synthetic-form"
      onSubmit={(e) => {
        e.preventDefault();
        setSubmitted(true);
        if (readOnly || Object.keys(errors).length > 0) return;
        save.mutate(toInput(draft));
      }}
    >
      <fieldset disabled={readOnly} className="flex min-w-0 flex-col gap-4">
        <Section
          title={
            check
              ? t("synthetics.form.editTitle")
              : t("synthetics.form.createTitle")
          }
        >
          <div className="grid gap-4 sm:grid-cols-2">
            <Field
              id={id("name")}
              label={t("synthetics.form.name")}
              error={message("name")}
            >
              <Input
                id={id("name")}
                value={draft.name}
                onChange={(e) => update({ name: e.target.value })}
                {...describedBy(id("name"), message("name"))}
              />
            </Field>
            <Field
              id={id("type")}
              label={t("synthetics.form.type")}
              hint={t(`synthetics.kindHints.${draft.type}`)}
            >
              <NativeSelect
                id={id("type")}
                value={draft.type}
                onChange={(e) =>
                  update({ type: e.target.value as SyntheticCheckKind })
                }
                {...describedBy(
                  id("type"),
                  undefined,
                  t(`synthetics.kindHints.${draft.type}`),
                )}
              >
                {CHECK_TYPES.map((k) => (
                  <option key={k} value={k}>
                    {t(`synthetics.kinds.${k}`)}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            {isHTTP ? (
              <>
                <Field
                  id={id("url")}
                  label={t("synthetics.form.url")}
                  error={message("url")}
                >
                  <Input
                    id={id("url")}
                    value={draft.url}
                    placeholder="https://shop.example.com/health"
                    onChange={(e) => update({ url: e.target.value })}
                    {...describedBy(id("url"), message("url"))}
                  />
                </Field>
                <Field id={id("method")} label={t("synthetics.form.method")}>
                  <NativeSelect
                    id={id("method")}
                    value={draft.method}
                    onChange={(e) => update({ method: e.target.value })}
                  >
                    {METHODS.map((m) => (
                      <option key={m} value={m}>
                        {m}
                      </option>
                    ))}
                  </NativeSelect>
                </Field>
              </>
            ) : (
              <Field
                id={id("target")}
                label={t(
                  draft.type === "dns"
                    ? "synthetics.form.hostname"
                    : "synthetics.form.hostPort",
                )}
                hint={t(
                  draft.type === "dns"
                    ? "synthetics.form.hostnameHint"
                    : "synthetics.form.hostPortHint",
                )}
                error={message("target")}
              >
                <Input
                  id={id("target")}
                  value={draft.target}
                  placeholder={
                    draft.type === "dns"
                      ? "shop.example.com"
                      : "shop.example.com:443"
                  }
                  onChange={(e) => update({ target: e.target.value })}
                  {...describedBy(
                    id("target"),
                    message("target"),
                    t(
                      draft.type === "dns"
                        ? "synthetics.form.hostnameHint"
                        : "synthetics.form.hostPortHint",
                    ),
                  )}
                />
              </Field>
            )}
            {draft.type === "dns" && (
              <>
                <Field
                  id={id("record")}
                  label={t("synthetics.form.recordType")}
                >
                  <NativeSelect
                    id={id("record")}
                    value={draft.dns_record_type}
                    onChange={(e) =>
                      update({ dns_record_type: e.target.value })
                    }
                  >
                    {DNS_RECORD_TYPES.map((r) => (
                      <option key={r} value={r}>
                        {r}
                      </option>
                    ))}
                  </NativeSelect>
                </Field>
                <Field
                  id={id("expected")}
                  label={t("synthetics.form.dnsExpected")}
                  hint={t("synthetics.form.dnsExpectedHint")}
                >
                  <Input
                    id={id("expected")}
                    value={draft.dns_expected}
                    placeholder="203.0.113.10, 203.0.113.11"
                    onChange={(e) => update({ dns_expected: e.target.value })}
                    {...describedBy(
                      id("expected"),
                      undefined,
                      t("synthetics.form.dnsExpectedHint"),
                    )}
                  />
                </Field>
              </>
            )}
            {draft.type === "tls" && (
              <Field
                id={id("warning")}
                label={t("synthetics.form.warningDays")}
                hint={t("synthetics.form.warningDaysHint")}
                error={message("tls_warning_days")}
              >
                <Input
                  id={id("warning")}
                  inputMode="numeric"
                  value={draft.tls_warning_days}
                  onChange={(e) => update({ tls_warning_days: e.target.value })}
                  {...describedBy(
                    id("warning"),
                    message("tls_warning_days"),
                    t("synthetics.form.warningDaysHint"),
                  )}
                />
              </Field>
            )}
            <Field
              id={id("interval")}
              label={t("synthetics.form.interval")}
              hint={t("synthetics.form.intervalHint")}
              error={message("interval_seconds")}
            >
              <Input
                id={id("interval")}
                inputMode="numeric"
                value={draft.interval_seconds}
                onChange={(e) => update({ interval_seconds: e.target.value })}
                {...describedBy(
                  id("interval"),
                  message("interval_seconds"),
                  t("synthetics.form.intervalHint"),
                )}
              />
            </Field>
            <Field
              id={id("timeout")}
              label={t("synthetics.form.timeout")}
              hint={t("synthetics.form.timeoutHint")}
              error={message("timeout_ms")}
            >
              <Input
                id={id("timeout")}
                inputMode="numeric"
                value={draft.timeout_ms}
                onChange={(e) => update({ timeout_ms: e.target.value })}
                {...describedBy(
                  id("timeout"),
                  message("timeout_ms"),
                  t("synthetics.form.timeoutHint"),
                )}
              />
            </Field>
            {isHTTP && (
              <>
                <Field
                  id={id("status")}
                  label={t("synthetics.form.expectedStatus")}
                  hint={t("synthetics.form.expectedStatusHint")}
                  error={message("expected_status")}
                >
                  <Input
                    id={id("status")}
                    value={draft.expected_status}
                    placeholder="200, 204"
                    onChange={(e) =>
                      update({ expected_status: e.target.value })
                    }
                    {...describedBy(
                      id("status"),
                      message("expected_status"),
                      t("synthetics.form.expectedStatusHint"),
                    )}
                  />
                </Field>
                <Field
                  id={id("headers")}
                  label={t("synthetics.form.headers")}
                  hint={t("synthetics.form.headersHint")}
                  error={message("headers")}
                  className="sm:col-span-2"
                >
                  <textarea
                    id={id("headers")}
                    rows={2}
                    value={draft.headers}
                    placeholder="X-Api-Version: 2"
                    onChange={(e) => update({ headers: e.target.value })}
                    className="rounded-md border border-input bg-background px-3 py-2 font-mono text-sm shadow-xs"
                    {...describedBy(
                      id("headers"),
                      message("headers"),
                      t("synthetics.form.headersHint"),
                    )}
                  />
                </Field>
                {sendsBody && (
                  <Field
                    id={id("body")}
                    label={t("synthetics.form.body")}
                    className="sm:col-span-2"
                  >
                    <textarea
                      id={id("body")}
                      rows={2}
                      value={draft.body}
                      onChange={(e) => update({ body: e.target.value })}
                      className="rounded-md border border-input bg-background px-3 py-2 font-mono text-sm shadow-xs"
                    />
                  </Field>
                )}
                <Field
                  id={id("assertion")}
                  label={t("synthetics.form.assertionType")}
                >
                  <NativeSelect
                    id={id("assertion")}
                    value={draft.assertion_type}
                    onChange={(e) =>
                      update({
                        assertion_type: e.target
                          .value as SyntheticAssertionType,
                      })
                    }
                  >
                    {ASSERTIONS.map((a) => (
                      <option key={a} value={a}>
                        {t(`synthetics.assertionTypes.${a}`)}
                      </option>
                    ))}
                  </NativeSelect>
                </Field>
                {draft.assertion_type === "json_path" ? (
                  <Field
                    id={id("path")}
                    label={t("synthetics.form.assertionPath")}
                    hint={t("synthetics.form.assertionPathHint")}
                    error={message("assertion_path")}
                  >
                    <Input
                      id={id("path")}
                      value={draft.assertion_path}
                      placeholder="data.items.0.status"
                      onChange={(e) =>
                        update({ assertion_path: e.target.value })
                      }
                      {...describedBy(
                        id("path"),
                        message("assertion_path"),
                        t("synthetics.form.assertionPathHint"),
                      )}
                    />
                  </Field>
                ) : (
                  <div aria-hidden="true" />
                )}
                {draft.assertion_type !== "none" && (
                  <Field
                    id={id("value")}
                    label={t("synthetics.form.assertionValue")}
                    hint={t("synthetics.form.assertionValueHint")}
                    error={message("assertion_value")}
                  >
                    <Input
                      id={id("value")}
                      value={draft.assertion_value}
                      onChange={(e) =>
                        update({ assertion_value: e.target.value })
                      }
                      {...describedBy(
                        id("value"),
                        message("assertion_value"),
                        t("synthetics.form.assertionValueHint"),
                      )}
                    />
                  </Field>
                )}
              </>
            )}
            <Field
              id={id("enabled")}
              label={t("synthetics.form.enabled")}
              hint={t("synthetics.form.enabledHint")}
            >
              <input
                id={id("enabled")}
                type="checkbox"
                checked={draft.enabled}
                onChange={(e) => update({ enabled: e.target.checked })}
                className="size-4 accent-primary"
                {...describedBy(
                  id("enabled"),
                  undefined,
                  t("synthetics.form.enabledHint"),
                )}
              />
            </Field>
          </div>
        </Section>
      </fieldset>
      <div className="flex flex-wrap items-center gap-3">
        {!readOnly && (
          <Button type="submit" disabled={save.isPending}>
            {check ? t("synthetics.form.save") : t("synthetics.form.create")}
          </Button>
        )}
        {onCancel && (
          <Button type="button" variant="outline" onClick={onCancel}>
            {t("synthetics.form.cancel")}
          </Button>
        )}
        {readOnly && (
          <p className="text-sm text-muted-foreground">
            {t("synthetics.form.readOnly")}
          </p>
        )}
        <FormError error={save.error} />
      </div>
    </form>
  );
}
