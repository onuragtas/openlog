import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, Circle, ShieldCheck } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { membersQuery, useMe } from "@/api/account";
import { ssoConnectionLabel, ssoDomainsQuery, storeSsoState, updateSsoConnectionEnforcement, type SsoConnection, type SsoDomain, type SsoState } from "@/api/sso";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { FormError, SettingsSection } from "./common";

/** Whether a domain's sign-ins use connection id (domains without connection_id use the default, first connection). */
function routesTo(d: SsoDomain, connections: SsoConnection[], id: string): boolean {
  return d.connection_id ? d.connection_id === id : connections[0]?.id === id;
}

function EnforcementPanel({ c, state }: { c: SsoConnection; state: SsoState }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const me = useMe().data;
  const isOwner = me?.role === "owner";
  const members = useQuery(membersQuery());
  const domains = useQuery(ssoDomainsQuery());
  const [breakGlass, setBreakGlass] = useState<string[] | null>(null);
  const selected = breakGlass ?? c.break_glass_user_ids;
  const [confirming, setConfirming] = useState(false);
  const update = useMutation({
    mutationFn: (enforce: boolean) => updateSsoConnectionEnforcement(c.id, enforce, selected),
    onSuccess: (res) => {
      storeSsoState(qc, res);
      setBreakGlass(null);
      setConfirming(false);
    },
  });

  const owners = (members.data ?? []).filter((m) => m.role === "owner");
  const requirements = [
    { ok: c.enabled, label: t("sso.enforcement.reqEnabled") },
    { ok: c.tested, label: t("sso.enforcement.reqTested") },
    { ok: (domains.data ?? []).some((d) => d.verified && routesTo(d, state.connections, c.id)), label: t("sso.enforcement.reqDomain") },
    { ok: selected.length > 0, label: t("sso.enforcement.reqBreakGlass") },
  ];
  const ready = requirements.every((r) => r.ok);

  return (
    <>
      <p role="status" className="flex items-center gap-2 text-sm font-medium">
        <ShieldCheck className={c.enforce ? "size-4 text-success" : "size-4 text-muted-foreground"} aria-hidden="true" />
        {c.enforce ? t("sso.enforcement.active") : t("sso.enforcement.inactive")}
      </p>
      {!isOwner && <p className="text-sm text-muted-foreground">{t("sso.enforcement.ownerOnly")}</p>}
      {!c.enforce && (
        <div>
          <h3 className="mb-1 text-sm font-medium">{t("sso.enforcement.prerequisites")}</h3>
          <ul className="flex flex-col gap-1 text-sm" aria-label={t("sso.enforcement.prerequisites")}>
            {requirements.map((r) => (
              <li key={r.label} className="flex items-center gap-2" data-ok={r.ok}>
                {r.ok ? <CheckCircle2 className="size-4 shrink-0 text-success" aria-hidden="true" /> : <Circle className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />}
                <span className={r.ok ? undefined : "text-muted-foreground"}>{r.label}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
      <fieldset className="flex flex-col gap-1.5" disabled={!isOwner}>
        <legend className="text-sm font-medium">{t("sso.enforcement.breakGlass")}</legend>
        <p className="text-xs text-muted-foreground">{t("sso.enforcement.breakGlassHint")}</p>
        {owners.map((o) => (
          <label key={o.user_id} className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              className="size-4 accent-primary"
              checked={selected.includes(o.user_id)}
              onChange={(e) => setBreakGlass(e.target.checked ? [...selected, o.user_id] : selected.filter((id) => id !== o.user_id))}
            />
            <span className="break-all">{o.email}</span>
            {o.user_id === me?.user?.id && <span className="text-muted-foreground">{t("sso.enforcement.you")}</span>}
          </label>
        ))}
      </fieldset>
      <FormError error={update.error} />
      {isOwner && (
        <div className="flex flex-wrap items-center gap-2">
          {c.enforce ? (
            <>
              <Button type="button" variant="outline" disabled={update.isPending} onClick={() => update.mutate(false)}>
                {t("sso.enforcement.disable")}
              </Button>
              <Button type="button" disabled={update.isPending || breakGlass === null || selected.length === 0} onClick={() => update.mutate(true)}>
                {t("sso.enforcement.saveBreakGlass")}
              </Button>
            </>
          ) : confirming ? (
            <>
              <p role="alert" className="w-full text-sm text-warning">
                {t("sso.enforcement.confirm")}
              </p>
              <Button type="button" variant="destructive" disabled={update.isPending} onClick={() => update.mutate(true)}>
                {t("sso.enforcement.confirmButton")}
              </Button>
              <Button type="button" variant="ghost" onClick={() => setConfirming(false)}>
                {t("common.cancel")}
              </Button>
            </>
          ) : (
            <Button type="button" disabled={!ready} onClick={() => setConfirming(true)}>
              {t("sso.enforcement.enforce")}
            </Button>
          )}
        </div>
      )}
    </>
  );
}

/** Enforce SSO per connection with lockout safeguards and break-glass owners (owner only). */
export function SsoEnforcement({ state, preferredId }: { state: SsoState; preferredId?: string | null }) {
  const { t } = useTranslation();
  const id = useId();
  const [chosen, setChosen] = useState<string | null>(null);
  const exists = (x: string | null | undefined): x is string => !!x && state.connections.some((c) => c.id === x);
  const currentId = exists(chosen) ? chosen : exists(preferredId) ? preferredId : state.connections[0]!.id;
  const c = state.connections.find((x) => x.id === currentId)!;

  return (
    <SettingsSection title={t("sso.enforcement.title")} description={t("sso.enforcement.description")}>
      {state.connections.length > 1 && (
        <div className="flex max-w-sm flex-col gap-1.5">
          <Label htmlFor={`${id}-connection`}>{t("sso.enforcement.connection")}</Label>
          <NativeSelect id={`${id}-connection`} value={c.id} onChange={(e) => setChosen(e.target.value)}>
            {state.connections.map((x) => (
              <option key={x.id} value={x.id}>
                {ssoConnectionLabel(x)}
              </option>
            ))}
          </NativeSelect>
        </div>
      )}
      <EnforcementPanel key={c.id} c={c} state={state} />
    </SettingsSection>
  );
}
