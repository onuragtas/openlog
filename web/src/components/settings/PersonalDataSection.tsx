import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { meQuery, useMe } from "@/api/account";
import { clearAuthState } from "@/api/auth";
import { accountPrivacyQuery, cancelOrgDeletion, deleteAccount, personalExportsQuery, requestPersonalExport } from "@/api/privacy";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { formatDateTime } from "@/lib/format";
import { FormError, SettingsSection } from "./common";
import { ExportList } from "./DataExportSection";
import { parseApiTime } from "./time";

/** Settings → Profile: scheduled deletions of owned organizations, personal data export, account deletion (D-107). */
export function PersonalDataSection() {
  const me = useMe().data;
  if (!me?.user || me.auth !== "session") return null;
  return <PersonalData email={me.user.email} />;
}

function PersonalData({ email }: { email: string }) {
  const { t, i18n } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const locale = i18n.resolvedLanguage ?? "en";
  const privacy = useQuery(accountPrivacyQuery());
  const exportsEnabled = privacy.data?.data_export_enabled ?? false;
  const exportsList = useQuery({ ...personalExportsQuery(), enabled: exportsEnabled });
  const request = useMutation({
    mutationFn: requestPersonalExport,
    onSuccess: () => void qc.invalidateQueries({ queryKey: personalExportsQuery().queryKey }),
  });
  const cancel = useMutation({
    mutationFn: (deletionId: string) => cancelOrgDeletion(deletionId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: accountPrivacyQuery().queryKey });
      void qc.invalidateQueries({ queryKey: meQuery().queryKey });
    },
  });
  const [confirmEmail, setConfirmEmail] = useState("");
  const [password, setPassword] = useState("");
  const hasPassword = privacy.data?.has_password ?? true;
  const del = useMutation({
    mutationFn: () => deleteAccount(confirmEmail.trim(), hasPassword ? password : undefined),
    onSuccess: () => {
      clearAuthState();
      window.location.assign("/login");
    },
  });
  const readyToDelete = confirmEmail.trim().toLowerCase() === email.toLowerCase() && (!hasPassword || password !== "");
  const deletions = privacy.data?.org_deletions ?? [];

  return (
    <>
      {deletions.length > 0 && (
        <SettingsSection title={t("privacy.pendingTitle")}>
          <ul className="flex flex-col gap-2">
            {deletions.map((d) => (
              <li key={d.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-destructive/40 px-3 py-2 text-sm">
                <span>{t("privacy.pendingItem", { name: d.organization_name, date: formatDateTime(parseApiTime(d.purge_after), locale) })}</span>
                {d.status === "deleting" && <span className="text-xs text-muted-foreground">{t("privacy.deleting")}</span>}
                {d.initiator === "operator" && <span className="text-xs text-muted-foreground">{t("privacy.operatorScheduled")}</span>}
                {d.cancellable && (
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    className="sm:ml-auto"
                    disabled={cancel.isPending}
                    aria-label={t("privacy.cancelNamed", { name: d.organization_name })}
                    onClick={() => cancel.mutate(d.id)}
                  >
                    {t("privacy.cancelDeletion")}
                  </Button>
                )}
              </li>
            ))}
          </ul>
          <FormError error={cancel.error} />
        </SettingsSection>
      )}
      {exportsEnabled && (
        <SettingsSection title={t("privacy.personalTitle")} description={t("privacy.personalDescription")}>
          <div className="flex flex-wrap items-center gap-2">
            <Button type="button" size="sm" disabled={request.isPending} onClick={() => request.mutate()}>
              {t("privacy.personalRequest")}
            </Button>
            {request.isSuccess && (
              <p role="status" className="text-xs text-muted-foreground">
                {t("privacy.requested")}
              </p>
            )}
          </div>
          <FormError error={request.error} />
          {exportsList.data && <ExportList exports={exportsList.data} />}
        </SettingsSection>
      )}
      <section className="rounded-xl border border-destructive/40 bg-card p-4">
        <h2 className="text-base font-semibold text-destructive">{t("privacy.deleteAccountTitle")}</h2>
        <p className="mt-1 text-sm text-muted-foreground">{t("privacy.deleteAccountDescription")}</p>
        <form
          className="mt-4 flex max-w-md flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (readyToDelete) del.mutate();
          }}
        >
          <label htmlFor={`${id}-email`} className="flex flex-col gap-1 text-sm">
            {t("privacy.confirmEmail", { email })}
            <Input id={`${id}-email`} type="email" autoComplete="off" value={confirmEmail} onChange={(e) => setConfirmEmail(e.target.value)} />
          </label>
          {hasPassword ? (
            <label htmlFor={`${id}-password`} className="flex flex-col gap-1 text-sm">
              {t("privacy.password")}
              <Input id={`${id}-password`} type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </label>
          ) : (
            <p className="text-xs text-muted-foreground">{t("privacy.ssoReauth", { minutes: Math.round((privacy.data?.reauth_max_age_seconds ?? 600) / 60) })}</p>
          )}
          <div>
            <Button type="submit" variant="destructive" size="sm" disabled={!readyToDelete || del.isPending}>
              {t("privacy.deleteAccount")}
            </Button>
          </div>
          <FormError error={del.error} />
        </form>
      </section>
    </>
  );
}
