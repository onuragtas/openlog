import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { currentOrgQuery, meQuery, renameOrg, setOrgLanguage, useMe, type OrgLanguage } from "@/api/account";
import { can } from "@/api/roles";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { SUPPORTED_LANGUAGES } from "@/i18n";
import { DateTimeText, FormError, SettingsSection } from "./common";
import { DataExportSection } from "./DataExportSection";
import { DeleteOrganizationSection } from "./DeleteOrganizationSection";
import { SupportAccessSettings } from "./SupportAccessSettings";
import { VersionSettings } from "./VersionSettings";

export function OrganizationSettings() {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const me = useMe().data;
  const org = useQuery(currentOrgQuery());
  const canEdit = can(me?.role, "org.update");
  const [draft, setDraft] = useState<string | null>(null);
  const rename = useMutation({
    mutationFn: (name: string) => renameOrg(name),
    onSuccess: () => {
      setDraft(null);
      void qc.invalidateQueries({ queryKey: currentOrgQuery().queryKey });
      void qc.invalidateQueries({ queryKey: meQuery().queryKey });
    },
  });

  const language = useMutation({
    mutationFn: (l: OrgLanguage) => setOrgLanguage(l),
    onSuccess: (next) => qc.setQueryData(currentOrgQuery().queryKey, next),
  });

  if (org.isPending) return <LoadingState />;
  if (org.isError) return <ErrorState error={org.error} onRetry={() => void org.refetch()} />;
  const o = org.data;

  return (
    <div className="flex flex-col gap-4">
    <SettingsSection title={t("settings.organization.title")} description={t("settings.organization.description")}>
      <dl className="grid grid-cols-1 gap-x-6 gap-y-3 text-sm sm:grid-cols-[12rem_1fr]">
        <dt className="text-muted-foreground">{t("settings.organization.name")}</dt>
        <dd>
          {draft === null ? (
            <span className="inline-flex items-center gap-2">
              <span className="font-medium">{o.name}</span>
              {canEdit && (
                <Button type="button" variant="ghost" size="sm" onClick={() => setDraft(o.name)}>
                  <Pencil aria-hidden="true" />
                  {t("settings.edit")}
                </Button>
              )}
            </span>
          ) : (
            <form
              className="flex flex-wrap items-center gap-2"
              onSubmit={(e) => {
                e.preventDefault();
                if (draft.trim() !== "") rename.mutate(draft.trim());
              }}
            >
              <label htmlFor={`${id}-name`} className="sr-only">
                {t("settings.organization.rename")}
              </label>
              <Input id={`${id}-name`} value={draft} maxLength={200} className="max-w-xs" autoFocus onChange={(e) => setDraft(e.target.value)} />
              <Button type="submit" size="sm" disabled={rename.isPending || draft.trim() === ""}>
                {t("settings.save")}
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => {
                  setDraft(null);
                  rename.reset();
                }}
              >
                {t("common.cancel")}
              </Button>
              <FormError error={rename.error} />
            </form>
          )}
        </dd>
        <dt className="text-muted-foreground">{t("settings.organization.tenantId")}</dt>
        <dd>
          <code className="font-mono text-xs">{o.tenant_id}</code>
          <p className="mt-1 text-xs text-muted-foreground">{t("settings.organization.tenantHint")}</p>
        </dd>
        <dt className="text-muted-foreground">{t("settings.organization.id")}</dt>
        <dd>
          <code className="font-mono text-xs">{o.id}</code>
        </dd>
        <dt className="text-muted-foreground">{t("settings.organization.role")}</dt>
        <dd>
          <Badge variant="secondary">{t(`settings.roles.${o.role}`)}</Badge>
        </dd>
        <dt className="text-muted-foreground">{t("settings.organization.created")}</dt>
        <dd>
          <DateTimeText value={o.created_at} />
        </dd>
        <dt className="text-muted-foreground">
          <label htmlFor={`${id}-language`}>{t("settings.organization.language")}</label>
        </dt>
        <dd className="flex flex-col gap-1">
          <NativeSelect
            id={`${id}-language`}
            className="h-8 max-w-xs text-sm"
            value={o.language}
            disabled={!canEdit || language.isPending}
            onChange={(e) => language.mutate(e.target.value as OrgLanguage)}
          >
            <option value="">{t("settings.organization.languageNone")}</option>
            {SUPPORTED_LANGUAGES.map((l) => (
              <option key={l} value={l}>
                {t(`language.${l}`)}
              </option>
            ))}
          </NativeSelect>
          <p className="text-xs text-muted-foreground">{t("settings.organization.languageHint")}</p>
          {language.isSuccess && (
            <p role="status" className="text-xs text-muted-foreground">
              {t("settings.organization.languageSaved")}
            </p>
          )}
          <FormError error={language.error} />
        </dd>
      </dl>
    </SettingsSection>
    <SupportAccessSettings />
    <DataExportSection />
    <VersionSettings />
    <DeleteOrganizationSection orgName={o.name} />
    </div>
  );
}
