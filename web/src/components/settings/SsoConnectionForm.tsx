import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, ExternalLink, X, XCircle } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  acceptSsoMetadata,
  createSsoConnection,
  SSO_TEST_CONNECTION_KEY,
  ssoConnectionLabel,
  ssoConnectionQuery,
  startSsoTestById,
  storeSsoState,
  testSsoConnectionById,
  updateSsoConnection,
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
  metadataSigningCert: string;
  allowUnsignedMetadata: boolean;
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
  logoutRedirects: string;
  allowExternalInvitations: boolean;
}

const MAX_AGES = [
  { seconds: 0, label: "sso.connection.maxAgeNone" },
  { seconds: 3600, label: "sso.connection.maxAge1h" },
  { seconds: 8 * 3600, label: "sso.connection.maxAge8h" },
  { seconds: 24 * 3600, label: "sso.connection.maxAge24h" },
  { seconds: 7 * 24 * 3600, label: "sso.connection.maxAge7d" },
] as const;

const lines = (s: string) => s.split("\n").map((x) => x.trim()).filter(Boolean);

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
    // Empty keeps the pinned certificate (D-098).
    metadataSigningCert: "",
    allowUnsignedMetadata: c?.saml?.allow_unsigned_metadata ?? false,
    allowIdpInitiated: c?.saml?.allow_idp_initiated ?? false,
    relayStates: (c?.saml?.relay_state_allowlist ?? []).join("\n"),
    signRequests: c?.saml?.sign_authn_requests ?? false,
    emailAttribute: c?.email_attribute ?? "",
    nameAttribute: c?.name_attribute ?? "",
    groupsAttribute: c?.groups_attribute ?? "",
    jit: c?.jit_enabled ?? true,
    defaultRole: c?.default_role ?? "viewer",
    maxAge: c?.session_max_age_seconds ?? 0,
    logoutRedirects: (c?.logout_redirect_allowlist ?? []).join("\n"),
    allowExternalInvitations: c?.allow_external_invitations ?? true,
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
    logout_redirect_allowlist: lines(d.logoutRedirects),
    allow_external_invitations: d.allowExternalInvitations,
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
      ...(d.metadataSigningCert.trim() !== "" ? { metadata_signing_certificate_pem: d.metadataSigningCert.trim() } : {}),
      allow_unsigned_metadata: d.allowUnsignedMetadata,
      allow_idp_initiated: d.allowIdpInitiated,
      relay_state_allowlist: d.allowIdpInitiated ? lines(d.relayStates) : [],
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

/**
 * Connection setup wizard (protocol → service provider values → identity provider → users) and tests of one
 * connection. `connectionId` null creates a new connection; `onSaved` receives the saved connection.
 */
export function SsoConnectionForm({
  state,
  connectionId,
  onSaved,
  onClose,
}: {
  state: SsoState;
  connectionId: string | null;
  onSaved?: (c: SsoConnection) => void;
  onClose?: () => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const id = useId();
  const stored = connectionId ? (state.connections.find((c) => c.id === connectionId) ?? null) : null;
  // The addressed connection's service provider values (SAML URLs are per connection).
  const detail = useQuery({ ...ssoConnectionQuery(connectionId ?? ""), enabled: connectionId !== null });
  const [draft, setDraft] = useState<Draft>(() => draftFrom(stored));
  const [step, setStep] = useState(stored ? 2 : 0);
  const set = <K extends keyof Draft>(key: K, value: Draft[K]) => setDraft((d) => ({ ...d, [key]: value }));

  const save = useMutation({
    mutationFn: () => (stored ? updateSsoConnection(stored.id, toInput(draft, stored)) : createSsoConnection(toInput(draft, null))),
    onSuccess: (res) => {
      storeSsoState(qc, res);
      setDraft(draftFrom(res.connection));
      if (draft.protocol === "saml" && stored?.protocol !== "saml") setStep(1); // show the generated SP values
      if (res.connection) onSaved?.(res.connection);
    },
  });
  const checks = useMutation({ mutationFn: () => testSsoConnectionById(stored!.id) });
  const startTest = useMutation({
    mutationFn: () => startSsoTestById(stored!.id),
    onSuccess: (url) => {
      try {
        sessionStorage.setItem(SSO_TEST_CONNECTION_KEY, stored!.id);
      } catch {
        // the settings page then opens without the tested connection
      }
      window.location.assign(url);
    },
  });
  const acceptMetadata = useMutation({
    mutationFn: (digest: string) => acceptSsoMetadata(stored!.id, digest),
    onSuccess: (res) => storeSsoState(qc, res),
  });

  const steps = [t("sso.connection.stepProtocol"), t("sso.connection.stepServiceProvider"), t("sso.connection.stepIdentityProvider"), t("sso.connection.stepUsers")];
  const sp = (stored && detail.data?.connection?.id === stored.id ? detail.data : state).service_provider;
  const samlSaved = stored?.protocol === "saml" && sp.saml_entity_id && sp.saml_acs_url && sp.saml_metadata_url;
  const listMode = state.connections.length > 0;
  const title = !listMode ? t("sso.connection.title") : stored ? t("sso.connection.editTitle", { name: ssoConnectionLabel(stored) }) : t("sso.connection.newTitle");

  return (
    <SettingsSection title={title} description={t("sso.connection.description")}>
      {onClose && (
        <div className="-mt-2 flex justify-end">
          <Button type="button" variant="ghost" size="sm" onClick={onClose}>
            <X aria-hidden="true" />
            {t("sso.connection.close")}
          </Button>
        </div>
      )}
      {stored && (
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <Badge variant={stored.enabled ? "success" : "outline"}>{stored.protocol === "oidc" ? t("sso.connection.oidc") : t("sso.connection.saml")}</Badge>
          {stored.name && <span className="font-medium break-all">{stored.name}</span>}
          {stored.enforce && <Badge variant="warning">{t("sso.connections.enforced")}</Badge>}
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
              <>
                <SsoCopyField label={t("sso.connection.redirectUri")} value={sp.oidc_redirect_uri} />
                <SsoCopyField label={t("sso.connection.postLogoutRedirectUri")} value={sp.oidc_post_logout_redirect_uri} />
                {stored?.protocol === "oidc" && sp.oidc_backchannel_logout_uri && sp.oidc_frontchannel_logout_uri ? (
                  <>
                    <SsoCopyField label={t("sso.connection.backchannelLogoutUri")} value={sp.oidc_backchannel_logout_uri} />
                    <SsoCopyField label={t("sso.connection.frontchannelLogoutUri")} value={sp.oidc_frontchannel_logout_uri} />
                    <p className="text-xs text-muted-foreground">{t("sso.connection.logoutUrisHint")}</p>
                  </>
                ) : (
                  <p className="text-xs text-muted-foreground">{t("sso.connection.logoutUrisSaveFirst")}</p>
                )}
              </>
            ) : samlSaved ? (
              <>
                <SsoCopyField label={t("sso.connection.entityId")} value={sp.saml_entity_id!} />
                <SsoCopyField label={t("sso.connection.acsUrl")} value={sp.saml_acs_url!} />
                {sp.saml_slo_url && <SsoCopyField label={t("sso.connection.sloUrl")} value={sp.saml_slo_url} />}
                {sp.saml_slo_soap_url && <SsoCopyField label={t("sso.connection.sloSoapUrl")} value={sp.saml_slo_soap_url} />}
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
            {draft.metadataUrl.trim() !== "" && (
              <>
                <Field id={`${id}-mdcert`} label={t("sso.connection.metadataSigningCert")} hint={t("sso.connection.metadataSigningCertHint")}>
                  <textarea
                    id={`${id}-mdcert`}
                    className={textareaClass}
                    value={draft.metadataSigningCert}
                    spellCheck={false}
                    placeholder="-----BEGIN CERTIFICATE-----"
                    onChange={(e) => set("metadataSigningCert", e.target.value)}
                  />
                </Field>
                <Checkbox
                  id={`${id}-unsigned`}
                  checked={draft.allowUnsignedMetadata}
                  onChange={(v) => set("allowUnsignedMetadata", v)}
                  label={t("sso.connection.allowUnsignedMetadata")}
                  hint={t("sso.connection.allowUnsignedMetadataHint")}
                />
              </>
            )}
            <Field id={`${id}-mdxml`} label={t("sso.connection.metadataXml")}>
              <textarea id={`${id}-mdxml`} className={textareaClass} value={draft.metadataXml} spellCheck={false} onChange={(e) => set("metadataXml", e.target.value)} />
            </Field>
            {stored?.saml && (
              <>
                <dl className="grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[max-content_1fr]">
                  <dt className="text-muted-foreground">{t("sso.connection.idpEntityId")}</dt>
                  <dd className="font-mono text-xs break-all">{stored.saml.idp_entity_id}</dd>
                  <dt className="text-muted-foreground">{t("sso.connection.idpSsoUrl")}</dt>
                  <dd className="font-mono text-xs break-all">{stored.saml.idp_sso_url}</dd>
                  {stored.saml.idp_slo_url && (
                    <>
                      <dt className="text-muted-foreground">{t("sso.connection.idpSloUrl")}</dt>
                      <dd className="font-mono text-xs break-all">{stored.saml.idp_slo_url}</dd>
                    </>
                  )}
                  <dt className="text-muted-foreground">{t("sso.connection.idpCert")}</dt>
                  <dd className="font-mono text-xs break-all">
                    {stored.saml.idp_certificates.join(", ")}
                    {stored.saml.idp_cert_not_after && (
                      <span className="block font-sans text-muted-foreground">
                        {t("sso.connection.idpCertExpires", { date: stored.saml.idp_cert_not_after.slice(0, 10) })}
                      </span>
                    )}
                  </dd>
                  {stored.saml.metadata_signing_certificates.length > 0 && (
                    <>
                      <dt className="text-muted-foreground">{t("sso.connection.metadataSigningPinned")}</dt>
                      <dd className="font-mono text-xs break-all">{stored.saml.metadata_signing_certificates.join(", ")}</dd>
                    </>
                  )}
                </dl>
                {!stored.saml.idp_slo_url && <p className="text-xs text-muted-foreground">{t("sso.connection.idpNoSlo")}</p>}
                {stored.saml.pending_metadata && (
                  <div role="alert" className="flex flex-col gap-2 rounded-lg border border-warning p-3 text-sm" data-testid="sso-pending-metadata">
                    <p className="font-medium">{t("sso.connection.pendingTitle")}</p>
                    <p className="text-muted-foreground">
                      {stored.saml.pending_metadata.reason === "signer_changed" ? t("sso.connection.pendingSignerChanged") : t("sso.connection.pendingChanged")}
                    </p>
                    <dl className="grid gap-x-4 gap-y-1 sm:grid-cols-[max-content_1fr]">
                      <dt className="text-muted-foreground">{t("sso.connection.idpCert")}</dt>
                      <dd className="font-mono text-xs break-all">{stored.saml.pending_metadata.idp_certificates.join(", ")}</dd>
                      <dt className="text-muted-foreground">{t("sso.connection.idpSsoUrl")}</dt>
                      <dd className="font-mono text-xs break-all">{stored.saml.pending_metadata.idp_sso_url}</dd>
                      {stored.saml.pending_metadata.idp_slo_url && (
                        <>
                          <dt className="text-muted-foreground">{t("sso.connection.idpSloUrl")}</dt>
                          <dd className="font-mono text-xs break-all">{stored.saml.pending_metadata.idp_slo_url}</dd>
                        </>
                      )}
                      {stored.saml.pending_metadata.signer_certificate && (
                        <>
                          <dt className="text-muted-foreground">{t("sso.connection.pendingSigner")}</dt>
                          <dd className="font-mono text-xs break-all">{stored.saml.pending_metadata.signer_certificate}</dd>
                        </>
                      )}
                    </dl>
                    <div>
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        disabled={acceptMetadata.isPending}
                        onClick={() => acceptMetadata.mutate(stored.saml!.pending_metadata!.digest)}
                      >
                        {t("sso.connection.pendingConfirm")}
                      </Button>
                    </div>
                    <FormError error={acceptMetadata.error} />
                  </div>
                )}
                {acceptMetadata.isSuccess && (
                  <p role="status" className="text-sm text-success">
                    {t("sso.connection.pendingConfirmed")}
                  </p>
                )}
              </>
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
            <Field id={`${id}-logout`} label={t("sso.connection.logoutRedirects")} hint={t("sso.connection.logoutRedirectsHint")}>
              <textarea id={`${id}-logout`} className={textareaClass} value={draft.logoutRedirects} spellCheck={false} onChange={(e) => set("logoutRedirects", e.target.value)} />
            </Field>
            <Checkbox
              id={`${id}-extinv`}
              checked={draft.allowExternalInvitations}
              onChange={(v) => set("allowExternalInvitations", v)}
              label={t("sso.connection.allowExternalInvitations")}
              hint={t("sso.connection.allowExternalInvitationsHint")}
            />
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
        </div>
      )}
    </SettingsSection>
  );
}
