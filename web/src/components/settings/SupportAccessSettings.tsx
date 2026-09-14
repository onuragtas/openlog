import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { grantSupportAccess, orgSaaSStateQuery, revokeSupportAccess } from "@/api/operator";
import { Button } from "@/components/ui/button";
import { formatDateTime } from "@/lib/format";
import { FormError, SettingsSection } from "./common";
import { ConfirmButton } from "./ConfirmButton";

/** Owner toggle "allow openlog support for 24h / 7d" (D-105). Hidden where the API has no SaaS state (static mode). */
export function SupportAccessSettings() {
  const { t, i18n } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery(orgSaaSStateQuery());
  const onSuccess = (next: Awaited<ReturnType<typeof grantSupportAccess>>) => qc.setQueryData(orgSaaSStateQuery().queryKey, next);
  const grant = useMutation({ mutationFn: grantSupportAccess, onSuccess });
  const revoke = useMutation({ mutationFn: revokeSupportAccess, onSuccess });
  const s = q.data;
  if (!s || s.support_session) return null;
  const pending = grant.isPending || revoke.isPending;
  return (
    <SettingsSection title={t("saas.access.title")} description={t("saas.access.description")}>
      <p className="text-sm" role="status">
        {s.support_access
          ? t("saas.access.active", { date: formatDateTime(Date.parse(s.support_access.until), i18n.resolvedLanguage ?? "en") })
          : t("saas.access.inactive")}
      </p>
      {s.can_manage_support_access ? (
        <div className="flex flex-wrap gap-2">
          <Button type="button" size="sm" variant="outline" disabled={pending} onClick={() => grant.mutate("24h")}>
            {t("saas.access.grant24h")}
          </Button>
          <Button type="button" size="sm" variant="outline" disabled={pending} onClick={() => grant.mutate("7d")}>
            {t("saas.access.grant7d")}
          </Button>
          {s.support_access && (
            <ConfirmButton label={t("saas.access.revoke")} confirmLabel={t("saas.access.confirmRevoke")} pending={pending} onConfirm={() => revoke.mutate()} />
          )}
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">{t("saas.access.ownersOnly")}</p>
      )}
      <FormError error={grant.error ?? revoke.error} />
    </SettingsSection>
  );
}
