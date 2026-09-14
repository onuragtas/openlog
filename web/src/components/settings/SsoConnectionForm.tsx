import { useMutation, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, ExternalLink, XCircle } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  deleteSsoConnection,
  saveSsoConnection,
  ssoStateQuery,
  startSsoTest,
  testSsoConnection,
  type SsoConnection,
  type SsoConnectionInput,
  type SsoState,
} from "@/api/sso";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";
import { ConfirmButton } from "./ConfirmButton";
import { DateTimeText, FormError, SettingsSection } from "./common";
import { SsoCopyField } from "./SsoCopyField";

type Protocol = "oidc" | "saml";
type AssignableRole = "admin" | "member" | "viewer";

interface Draft {
  protocol: Protocol;
  name: string;
  enabled: boolean;
  issuer: string;
  clientId: string;
  clientSecret: string;
  removeSecret: boolean;
  scopes: string;
  requireEmailVerified: boolean;
  metadataUrl: string;
  metadataXml: string;
  allowIdpInitiated: boolean;
  relayStates: string;
  signRequests: boolean;
  emailAttribute: string;
  nameAttribute: string;
  groupsAttribute: string;
  jit: boolean;
  defaultRole: AssignableRole;
  maxAge: number;
}

const MAX_AGES = [
  { seconds: 0, label: "sso.connection.maxAgeNone" },
  { seconds: 3600, label: "sso.connection.maxAge1h" },
  { seconds: 8 * 3600, label: "sso.connection.maxAge8h" },
  { seconds: 24 * 3600, label: "sso.connection.maxAge24h" },
  { seconds: 7 * 24 * 3600, label: "sso.connection.maxAge7d" },
] as const;

function draftFrom(c: SsoConnection | null): Draft {
  return {
    protocol: c?.protocol ?? "oidc",
    name: c?.name ?? "",
    enabled: c?.enabled ?? true,
    issuer: c?.oidc?.issuer ?? "",
    clientId: c?.oidc?.client_id ?? "",
    clientSecret: "",
    removeSecret: false,
    scopes: (c?.oidc?.scopes ?? []).join(" "),
    requireEmailVerified: c?.oidc?.require_email_verified ?? true,
    metadataUrl: c?.saml?.idp_metadata_url ?? "",
    metadataXml: "",
    allowIdpInitiated: c?.saml?.allow_idp_initiated ?? false,
    relayStates: (c?.saml?.relay_state_allowlist ?? []).join("\n"),
    signRequests: c?.saml?.sign_authn_requests ?? false,
    emailAttribute: c?.email_attribute ?? "",
    nameAttribute: c?.name_attribute ?? "",
    groupsAttribute: c?.groups_attribute ?? "",
    jit: c?.jit_enabled ?? true,
    defaultRole: c?.default_role ?? "viewer",
    maxAge: c?.session_max_age_seconds ?? 0,
  };
}

function toInput(d: Draft, stored: SsoConnection | null): SsoConnectionInput {
  const common = {
    protocol: d.protocol,
    name: d.name,
    enabled: d.enabled,
    email_attribute: d.emailAttribute,
    name_attribute: d.nameAttribute,
    groups_attribute: d.groupsAttribute,
    jit_enabled: d.jit,
    default_role: d.defaultRole,
    session_max_age_seconds: d.maxAge,
  };
  if (d.protocol === "oidc") {
    // Omitted secret = keep the stored one; "" = remove it.
    const secret = d.removeSecret ? "" : d.clientSecret !== "" ? d.clientSecret : stored?.protocol === "oidc" ? undefined : "";
    return {
      ...common,
      oidc: {
        issuer: d.issuer.trim(),
        client_id: d.clientId.trim(),
        ...(secret === undefined ? {} : { client_secret: secret }),
        scopes: d.scopes.split(/\s+/).filter(Boolean),
        require_email_verified: d.requireEmailVerified,
      },
    };
  }
  return {
    ...common,
    saml: {
      idp_metadata_url: d.metadataUrl.trim(),
      idp_metadata_xml: d.metadataXml.trim(),
      allow_idp_initiated: d.allowIdpInitiated,
      relay_state_allowlist: d.allowIdpInitiated ? d.relayStates.split("\n").map((s) => s.trim()).filter(Boolean) : [],
      sign_authn_requests: d.signRequests,
    },
  };
}

function Checkbox({ id, checked, onChange, label, hint }: { id: string; checked: boolean; onChange: (v: boolean) => void; label: string; hint?: string }) {
  return (
    <div className="flex items-start gap-2">
      <input id={id} type="checkbox" className="mt-0.5 size-4 accent-primary" checked={checked} aria-describedby={hint ? `${id}-hint` : undefined} onChange={(e) => onChange(e.target.checked)} />
      <div className="flex flex-col">
        <Label htmlFor={id} className="font-normal">
          {label}
        </Label>
        {hint && (
          <p id={`${id}-hint`} className="text-xs text-muted-foreground">
            {hint}
          </p>
        )}
      </div>
    </div>
  );
}

function Field({ id, label, hint, children }: { id: string; label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </div>
  );
}

const textareaClass = "min-h-24 w-full resize-y rounded-md border border-input bg-background px-2 py-1 font-mono text-xs";

/** Connection setup wizard (protocol → service provider values → identity provider → users), tests and deletion. */
export function SsoConnectionForm({ state }: { state: SsoState }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const id = useId();
  const stored = state.connection;
  const [draft, setDraft] = useState<Draft>(() => draftFrom(stored));
  const [step, setStep] = useState(stored ? 2 : 0);
  const set = <K extends keyof Draft>(key: K, value: Draft[K]) => setDraft((d) => ({ ...d, [key]: value }));

  const save = useMutation({
    mutationFn: () => saveSsoConnection(toInput(draft, stored)),
    onSuccess: (res) => {
      qc.setQueryData(ssoStateQuery().queryKey, res);
      setDraft(draftFrom(res.connection));
      if (draft.protocol === "saml" && stored?.protocol !== "saml") setStep(1); // show the generated SP values
    },
  });
  const checks = useMutation({ mutationFn: testSsoConnection });
  const startTest = useMutation({ mutationFn: startSsoTest, onSuccess: (url) => window.location.assign(url) });
  const remove = useMutation({
    mutationFn: deleteSsoConnection,
    onSuccess: () => {
      setDraft(draftFrom(null));
      setStep(0);
      checks.reset();
    },
    onSettled: () => void qc.invalidateQueries({ queryKey: ["settings", "sso"] }),
  });

  const steps = [t("sso.connection.stepProtocol"), t("sso.connection.stepServiceProvider"), t("sso.connection.stepIdentityProvider"), t("sso.connection.stepUsers")];
  const sp = state.service_provider;
  const samlSaved = stored?.protocol === "saml" && sp.saml_entity_id && sp.saml_acs_url && sp.saml_metadata_url;

  return (
    <SettingsSection title={t("sso.connection.title")} description={t("sso.connection.description")}>
      {stored && (
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <Badge variant={stored.enabled ? "success" : "outline"}>{stored.protocol === "oidc" ? t("sso.connection.oidc") : t("sso.connection.saml")}</Badge>
          {stored.name && <span className="font-medium">{stored.name}</span>}
          {stored.enforce && <Badge variant="warning">{t("sso.enforcement.active")}</Badge>}
        </div>
      )}
      <ol aria-label={t("sso.connection.stepsLabel")} className="flex flex-wrap gap-1">
        {steps.map((label, i) => (
          <li key={label}>
            <button
              type="button"
              aria-current={step === i ? "step" : undefined}
              className={cn(
                "rounded-full border px-3 py-1 text-xs pointer-coarse:py-2",
                step === i ? "border-primary bg-primary text-primary-foreground" : "text-muted-foreground hover:text-foreground",
              )}
              onClick={() => setStep(i)}
            >
              {i + 1}. {label}
            </button>
          </li>
        ))}
      </ol>

      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate();
        }}
      >
        {step === 0 && (
          <div className="flex flex-col gap-3">
            <fieldset className="grid gap-2 sm:grid-cols-2">
              <legend className="mb-1.5 text-sm font-medium">{t("sso.connection.protocolLabel")}</legend>
              {(["oidc", "saml"] as const).map((p) => (
                <label key={p} className={cn("flex cursor-pointer items-start gap-2 rounded-lg border p-3", draft.protocol === p && "border-primary")}>
                  <input type="radio" name={`${id}-protocol`} value={p} className="mt-1 accent-primary" checked={draft.protocol === p} onChange={() => set("protocol", p)} />
                  <span className="flex flex-col">
                    <span className="text-sm font-medium">{p === "oidc" ? t("sso.connection.oidc") : t("sso.connection.saml")}</span>
                    <span className="text-xs text-muted-foreground">{p === "oidc" ? t("sso.connection.oidcHelp") : t("sso.connection.samlHelp")}</span>
                  </span>
                </label>
              ))}
            </fieldset>
            <Field id={`${id}-name`} label={t("sso.connection.name")}>
              <Input id={`${id}-name`} value={draft.name} maxLength={200} onChange={(e) => set("name", e.target.value)} className="max-w-md" />
            </Field>
            <Checkbox id={`${id}-enabled`} checked={draft.enabled} onChange={(v) => set("enabled", v)} label={t("sso.connection.enabled")} />
          </div>
        )}

        {step === 1 && (
          <div className="flex flex-col gap-3">
            <p className="text-sm text-muted-foreground">{t("sso.connection.spIntro")}</p>
            {draft.protocol === "oidc" ? (
              <SsoCopyField label={t("sso.connection.redirectUri")} value={sp.oidc_redirect_uri} />
            ) : samlSaved ? (
              <>
                <SsoCopyField label={t("sso.connection.entityId")} value={sp.saml_entity_id!} />
                <SsoCopyField label={t("sso.connection.acsUrl")} value={sp.saml_acs_url!} />
                <SsoCopyField label={t("sso.connection.metadataUrl")} value={sp.saml_metadata_url!} />
                {sp.saml_certificate_pem && <SsoCopyField label={t("sso.connection.certificate")} value={sp.saml_certificate_pem} multiline />}
              </>
            ) : (
              <p className="rounded-lg border border-dashed p-3 text-sm text-muted-foreground">{t("sso.connection.samlSaveFirst")}</p>
            )}
          </div>
        )}

        {step === 2 && draft.protocol === "oidc" && (
          <div className="grid gap-3 md:grid-cols-2">
            <Field id={`${id}-issuer`} label={t("sso.connection.issuer")} hint={t("sso.connection.issuerHint")}>
              <Input id={`${id}-issuer`} type="url" value={draft.issuer} autoComplete="off" onChange={(e) => set("issuer", e.target.value)} />
            </Field>
            <Field id={`${id}-client`} label={t("sso.connection.clientId")}>
              <Input id={`${id}-client`} value={draft.clientId} autoComplete="off" onChange={(e) => set("clientId", e.target.value)} />
            </Field>
            <Field id={`${id}-secret`} label={t("sso.connection.clientSecret")} hint={stored?.oidc?.client_secret_set ? t("sso.connection.clientSecretKeep") : undefined}>
              <Input
                id={`${id}-secret`}
                type="password"
                autoComplete="new-password"
                value={draft.clientSecret}
                disabled={draft.removeSecret}
                onChange={(e) => set("clientSecret", e.target.value)}
              />
            </Field>
            <Field id={`${id}-scopes`} label={t("sso.connection.scopes")} hint={t("sso.connection.scopesHint")}>
              <Input id={`${id}-scopes`} value={draft.scopes} autoComplete="off" onChange={(e) => set("scopes", e.target.value)} />
            </Field>
            {stored?.oidc?.client_secret_set && (
              <Checkbox id={`${id}-rmsecret`} checked={draft.removeSecret} onChange={(v) => set("removeSecret", v)} label={t("sso.connection.clientSecretRemove")} />
            )}
          </div>
        )}

        {step === 2 && draft.protocol === "saml" && (
          <div className="flex flex-col gap-3">
            <p className="text-sm text-muted-foreground">{stored?.saml ? t("sso.connection.metadataStored") : t("sso.connection.metadataHint")}</p>
            <Field id={`${id}-mdurl`} label={t("sso.connection.metadataUrlLabel")}>
              <Input id={`${id}-mdurl`} type="url" value={draft.metadataUrl} autoComplete="off" onChange={(e) => set("metadataUrl", e.target.value)} />
            </Field>
            <Field id={`${id}-mdxml`} label={t("sso.connection.metadataXml")}>
              <textarea id={`${id}-mdxml`} className={textareaClass} value={draft.metadataXml} spellCheck={false} onChange={(e) => set("metadataXml", e.target.value)} />
            </Field>
            {stored?.saml && (
              <dl className="grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[max-content_1fr]">
                <dt className="text-muted-foreground">{t("sso.connection.idpEntityId")}</dt>
                <dd className="font-mono text-xs break-all">{stored.saml.idp_entity_id}</dd>
                <dt className="text-muted-foreground">{t("sso.connection.idpSsoUrl")}</dt>
                <dd className="font-mono text-xs break-all">{stored.saml.idp_sso_url}</dd>
                <dt className="text-muted-foreground">{t("sso.connection.idpCert")}</dt>
                <dd className="font-mono text-xs break-all">
                  {stored.saml.idp_certificates.join(", ")}
                  {stored.saml.idp_cert_not_after && (
                    <span className="block font-sans text-muted-foreground">
                      {t("sso.connection.idpCertExpires", { date: stored.saml.idp_cert_not_after.slice(0, 10) })}
                    </span>
                  )}
                </dd>
              </dl>
            )}
            <Checkbox id={`${id}-signreq`} checked={draft.signRequests} onChange={(v) => set("signRequests", v)} label={t("sso.connection.signRequests")} />
            <Checkbox
              id={`${id}-idpinit`}
              checked={draft.allowIdpInitiated}
              onChange={(v) => set("allowIdpInitiated", v)}
              label={t("sso.connection.allowIdpInitiated")}
              hint={t("sso.connection.allowIdpInitiatedHint")}
            />
            {draft.allowIdpInitiated && (
              <Field id={`${id}-relay`} label={t("sso.connection.relayStates")} hint={t("sso.connection.relayStatesHint")}>
                <textarea id={`${id}-relay`} className={textareaClass} value={draft.relayStates} onChange={(e) => set("relayStates", e.target.value)} />
              </Field>
            )}
          </div>
        )}

        {step === 3 && (
          <div className="flex flex-col gap-3">
            <div className="grid gap-3 md:grid-cols-3">
              <Field id={`${id}-attr-email`} label={t("sso.connection.emailAttribute")}>
                <Input id={`${id}-attr-email`} value={draft.emailAttribute} placeholder={t("sso.connection.attributeDefault", { value: "email" })} onChange={(e) => set("emailAttribute", e.target.value)} />
              </Field>
              <Field id={`${id}-attr-name`} label={t("sso.connection.nameAttribute")}>
                <Input id={`${id}-attr-name`} value={draft.nameAttribute} placeholder={t("sso.connection.attributeDefault", { value: "name" })} onChange={(e) => set("nameAttribute", e.target.value)} />
              </Field>
              <Field id={`${id}-attr-groups`} label={t("sso.connection.groupsAttribute")}>
                <Input id={`${id}-attr-groups`} value={draft.groupsAttribute} placeholder={t("sso.connection.attributeDefault", { value: "groups" })} onChange={(e) => set("groupsAttribute", e.target.value)} />
              </Field>
            </div>
            <div className="grid gap-3 md:grid-cols-2">
              <Field id={`${id}-role`} label={t("sso.connection.defaultRole")} hint={t("sso.connection.defaultRoleHint")}>
                <NativeSelect id={`${id}-role`} value={draft.defaultRole} onChange={(e) => set("defaultRole", e.target.value as AssignableRole)}>
                  {(["viewer", "member", "admin"] as const).map((r) => (
                    <option key={r} value={r}>
                      {t(`settings.roles.${r}`)}
                    </option>
                  ))}
                </NativeSelect>
              </Field>
              <Field id={`${id}-maxage`} label={t("sso.connection.sessionMaxAge")} hint={t("sso.connection.sessionMaxAgeHint")}>
                <NativeSelect id={`${id}-maxage`} value={String(draft.maxAge)} onChange={(e) => set("maxAge", Number(e.target.value))}>
                  {MAX_AGES.map((m) => (
                    <option key={m.seconds} value={m.seconds}>
                      {t(m.label)}
                    </option>
                  ))}
                  {!MAX_AGES.some((m) => m.seconds === draft.maxAge) && <option value={draft.maxAge}>{Math.round(draft.maxAge / 60)} min</option>}
                </NativeSelect>
              </Field>
            </div>
            <Checkbox id={`${id}-jit`} checked={draft.jit} onChange={(v) => set("jit", v)} label={t("sso.connection.jit")} />
            {draft.protocol === "oidc" && (
              <Checkbox id={`${id}-emailverified`} checked={draft.requireEmailVerified} onChange={(v) => set("requireEmailVerified", v)} label={t("sso.connection.requireEmailVerified")} />
            )}
          </div>
        )}

        <FormError error={save.error} />
        {save.isSuccess && (
          <p role="status" className="text-sm text-success">
            {t("sso.connection.saved")}
          </p>
        )}
        <div className="flex flex-wrap gap-2">
          <Button type="button" variant="outline" disabled={step === 0} onClick={() => setStep((s) => Math.max(0, s - 1))}>
            {t("sso.connection.back")}
          </Button>
          {step < steps.length - 1 && (
            <Button type="button" variant="outline" onClick={() => setStep((s) => Math.min(steps.length - 1, s + 1))}>
              {t("sso.connection.next")}
            </Button>
          )}
          <Button type="submit" disabled={save.isPending || !state.available}>
            {t("sso.connection.save")}
          </Button>
        </div>
      </form>

      {stored && (
        <div className="flex flex-col gap-3 border-t pt-4">
          <h3 className="text-sm font-semibold">{t("sso.connection.testTitle")}</h3>
          <div className="flex flex-wrap gap-2">
            <Button type="button" variant="outline" disabled={checks.isPending} onClick={() => checks.mutate()}>
              {t("sso.connection.testChecks")}
            </Button>
            <Button type="button" disabled={startTest.isPending} onClick={() => startTest.mutate()}>
              <ExternalLink aria-hidden="true" />
              {t("sso.connection.testSignIn")}
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">{t("sso.connection.testSignInHint")}</p>
          <FormError error={checks.error ?? startTest.error} />
          {checks.data && (
            <ul aria-label={t("sso.connection.testChecks")} className="flex flex-col gap-1 text-sm">
              {checks.data.checks.map((c) => (
                <li key={c.name} className="flex items-start gap-2">
                  {c.ok ? <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-success" aria-label={t("sso.connection.checkOk")} /> : <XCircle className="mt-0.5 size-4 shrink-0 text-destructive" aria-label={t("sso.connection.checkFailed")} />}
                  <span className="font-mono text-xs">{c.name}</span>
                  <span className="min-w-0 break-all text-muted-foreground">{c.message}</span>
                </li>
              ))}
            </ul>
          )}
          {stored.last_test && (
            <div className="rounded-lg border p-3 text-sm" data-testid="sso-last-test">
              <p className="flex flex-wrap items-center gap-2">
                <span className="font-medium">{t("sso.connection.lastTest")}:</span>
                {stored.last_test.ok ? <Badge variant="success">{t("sso.connection.lastTestOk")}</Badge> : <Badge variant="destructive">{t("sso.connection.lastTestFailed")}</Badge>}
                <DateTimeText value={stored.last_test.at} relative />
              </p>
              {stored.last_test.ok && !stored.last_test.current && <p className="text-warning">{t("sso.connection.lastTestOutdated")}</p>}
              {stored.last_test.error && <p className="break-all text-destructive">{stored.last_test.error}</p>}
              {stored.last_test.ok && typeof stored.last_test.details.email === "string" && (
                <p>
                  {t("sso.connection.testedAs", {
                    email: stored.last_test.details.email,
                    role: t(`settings.roles.${(stored.last_test.details.role as AssignableRole) ?? "viewer"}`),
                  })}{" "}
                  {Array.isArray(stored.last_test.details.groups) && stored.last_test.details.groups.length > 0
                    ? t("sso.connection.testedGroups", { groups: (stored.last_test.details.groups as string[]).join(", ") })
                    : t("sso.connection.testedNoGroups")}
                </p>
              )}
            </div>
          )}
          <div>
            <ConfirmButton label={t("sso.connection.delete")} confirmLabel={t("sso.connection.confirmDelete")} pending={remove.isPending} onConfirm={() => remove.mutate()} />
          </div>
          <FormError error={remove.error} />
        </div>
      )}
    </SettingsSection>
  );
}
