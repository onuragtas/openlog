import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Globe } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { addSsoDomain, deleteSsoDomain, ssoDomainsQuery, verifySsoDomain, type SsoDomain, type SsoState } from "@/api/sso";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { ConfirmButton } from "./ConfirmButton";
import { DateTimeText, FormError, SettingsSection } from "./common";
import { SsoCopyField } from "./SsoCopyField";

const SSO_KEY = ["settings", "sso"];

function DomainRow({ d, state }: { d: SsoDomain; state: SsoState }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const id = useId();
  const [localPart, setLocalPart] = useState(state.domain_email_local_parts[0] ?? "admin");
  const invalidate = () => void qc.invalidateQueries({ queryKey: SSO_KEY });
  const verify = useMutation({
    mutationFn: (method: "dns_txt" | "email") => verifySsoDomain(d.id, method, localPart),
    onSettled: invalidate,
  });
  const remove = useMutation({ mutationFn: () => deleteSsoDomain(d.id), onSettled: invalidate });

  return (
    <li className="flex flex-col gap-3 rounded-lg border p-3" data-testid={`sso-domain-${d.domain}`}>
      <div className="flex flex-wrap items-center gap-2">
        <Globe className="size-4 text-muted-foreground" aria-hidden="true" />
        <span className="font-mono text-sm font-medium break-all">{d.domain}</span>
        {d.verified ? <Badge variant="success">{t("sso.domains.verified")}</Badge> : <Badge variant="outline">{t("sso.domains.pending")}</Badge>}
        {d.verified && d.verification_method && (
          <span className="text-xs text-muted-foreground">
            {d.verification_method === "email" ? t("sso.domains.viaEmail") : t("sso.domains.viaDns")} · <DateTimeText value={d.verified_at} />
          </span>
        )}
        <span className="ml-auto">
          <ConfirmButton label={t("sso.domains.remove")} confirmLabel={t("sso.domains.confirmRemove")} pending={remove.isPending} onConfirm={() => remove.mutate()} />
        </span>
      </div>
      {!d.verified && (
        <div className="flex flex-col gap-3">
          <p className="text-sm text-muted-foreground">{t("sso.domains.dnsHint")}</p>
          <div className="grid gap-3 md:grid-cols-2">
            <SsoCopyField label={t("sso.domains.recordName")} value={d.dns_record.name} />
            <SsoCopyField label={t("sso.domains.recordValue")} value={d.dns_record.value} />
          </div>
          <div className="flex flex-wrap items-end gap-2">
            <Button type="button" size="sm" disabled={verify.isPending} onClick={() => verify.mutate("dns_txt")}>
              {t("sso.domains.checkDns")}
            </Button>
            {state.email_verification_available && (
              <>
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor={`${id}-lp`} className="text-xs">
                    {t("sso.domains.emailTo")}
                  </Label>
                  <div className="flex items-center gap-1">
                    <NativeSelect id={`${id}-lp`} value={localPart} onChange={(e) => setLocalPart(e.target.value)}>
                      {state.domain_email_local_parts.map((lp) => (
                        <option key={lp} value={lp}>
                          {lp}
                        </option>
                      ))}
                    </NativeSelect>
                    <span className="font-mono text-xs">@{d.domain}</span>
                  </div>
                </div>
                <Button type="button" size="sm" variant="outline" disabled={verify.isPending} onClick={() => verify.mutate("email")}>
                  {t("sso.domains.sendEmail")}
                </Button>
              </>
            )}
          </div>
          {verify.isSuccess && verify.variables === "email" && verify.data.email_address && (
            <p role="status" className="text-sm text-success">
              {t("sso.domains.emailSent", { address: verify.data.email_address })}
            </p>
          )}
          <FormError error={verify.error} />
        </div>
      )}
      <FormError error={remove.error} />
    </li>
  );
}

/** Claimed e-mail domains and their verification (DNS TXT or e-mail). */
export function SsoDomains({ state }: { state: SsoState }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const id = useId();
  const domains = useQuery(ssoDomainsQuery());
  const [domain, setDomain] = useState("");
  const add = useMutation({
    mutationFn: () => addSsoDomain(domain.trim()),
    onSuccess: () => setDomain(""),
    onSettled: () => void qc.invalidateQueries({ queryKey: SSO_KEY }),
  });

  return (
    <SettingsSection title={t("sso.domains.title")} description={t("sso.domains.description")}>
      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (domain.trim()) add.mutate();
        }}
      >
        <div className="flex min-w-0 flex-1 flex-col gap-1.5 sm:max-w-xs">
          <Label htmlFor={`${id}-domain`}>{t("sso.domains.label")}</Label>
          <Input id={`${id}-domain`} value={domain} placeholder={t("sso.domains.placeholder")} autoComplete="off" onChange={(e) => setDomain(e.target.value)} />
        </div>
        <Button type="submit" disabled={add.isPending || domain.trim() === ""}>
          {t("sso.domains.add")}
        </Button>
      </form>
      <FormError error={add.error} />
      {domains.isPending ? (
        <LoadingState />
      ) : domains.isError ? (
        <ErrorState error={domains.error} onRetry={() => void domains.refetch()} />
      ) : domains.data.length === 0 ? (
        <EmptyState>{t("sso.domains.empty")}</EmptyState>
      ) : (
        <ul className="flex flex-col gap-2">
          {domains.data.map((d) => (
            <DomainRow key={d.id} d={d} state={state} />
          ))}
        </ul>
      )}
    </SettingsSection>
  );
}
