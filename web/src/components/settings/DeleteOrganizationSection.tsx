import { useMutation, useQuery } from "@tanstack/react-query";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { setSelectedOrg } from "@/api/auth";
import { accountPrivacyQuery, scheduleOrgDeletion } from "@/api/privacy";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { FormError } from "./common";

/** Settings → Organization → Delete organization (owners; typed confirmation and re-authentication, D-107). */
export function DeleteOrganizationSection({ orgName }: { orgName: string }) {
  const me = useMe().data;
  if (me?.role !== "owner" || me.auth !== "session") return null;
  return <DeleteOrganizationForm orgName={orgName} />;
}

function DeleteOrganizationForm({ orgName }: { orgName: string }) {
  const { t } = useTranslation();
  const id = useId();
  const privacy = useQuery(accountPrivacyQuery());
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const hasPassword = privacy.data?.has_password ?? true;
  const del = useMutation({
    mutationFn: () => scheduleOrgDeletion(name.trim(), hasPassword ? password : undefined),
    onSuccess: () => {
      // The organization is no longer accessible: continue in the profile, where the deletion can be cancelled.
      setSelectedOrg(null);
      window.location.assign("/settings/profile");
    },
  });
  const days = Math.round((privacy.data?.org_deletion_grace_seconds ?? 7 * 86_400) / 86_400);
  const ready = name.trim() === orgName && (!hasPassword || password !== "");

  return (
    <section className="rounded-xl border border-destructive/40 bg-card p-4">
      <h2 className="text-base font-semibold text-destructive">{t("privacy.deleteOrgTitle")}</h2>
      <p className="mt-1 text-sm text-muted-foreground">{t("privacy.deleteOrgDescription", { count: days })}</p>
      <form
        className="mt-4 flex max-w-md flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (ready) del.mutate();
        }}
      >
        <label htmlFor={`${id}-name`} className="flex flex-col gap-1 text-sm">
          {t("privacy.confirmName", { name: orgName })}
          <Input id={`${id}-name`} value={name} autoComplete="off" onChange={(e) => setName(e.target.value)} />
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
          <Button type="submit" variant="destructive" size="sm" disabled={!ready || del.isPending}>
            {t("privacy.deleteOrg")}
          </Button>
        </div>
        <FormError error={del.error} />
      </form>
    </section>
  );
}
