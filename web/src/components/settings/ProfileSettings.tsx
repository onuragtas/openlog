import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { meQuery, setMyLanguage, useMe, type UserLanguage } from "@/api/account";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { NativeSelect } from "@/components/ui/native-select";
import { SUPPORTED_LANGUAGES } from "@/i18n";
import { applyUserLanguage } from "@/lib/user-language";
import { FormError, SettingsSection } from "./common";
import { PersonalDataSection } from "./PersonalDataSection";

/** Settings → Profile: the signed-in user's own preferences (language, D-095). */
export function ProfileSettings() {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const me = useMe();
  const [saved, setSaved] = useState(false);
  const save = useMutation({
    mutationFn: (language: UserLanguage) => setMyLanguage(language),
    onSuccess: (next) => {
      qc.setQueryData(meQuery().queryKey, next);
      applyUserLanguage(next.user?.language ?? "auto");
      setSaved(true);
    },
  });

  if (me.isPending) return <LoadingState />;
  if (me.isError) return <ErrorState error={me.error} onRetry={() => void me.refetch()} />;
  const user = me.data.user;
  if (!user) return null;

  return (
    <div className="flex flex-col gap-4">
    <SettingsSection title={t("settings.profile.title")} description={t("settings.profile.description")}>
      <dl className="grid grid-cols-1 gap-x-6 gap-y-3 text-sm sm:grid-cols-[12rem_1fr]">
        <dt className="text-muted-foreground">{t("settings.profile.name")}</dt>
        <dd className="font-medium">{user.name || "—"}</dd>
        <dt className="text-muted-foreground">{t("settings.profile.email")}</dt>
        <dd>{user.email}</dd>
        <dt className="text-muted-foreground">
          <label htmlFor={`${id}-language`}>{t("settings.profile.language")}</label>
        </dt>
        <dd className="flex flex-col gap-1">
          <NativeSelect
            id={`${id}-language`}
            className="h-8 max-w-xs text-sm"
            value={user.language}
            disabled={save.isPending}
            onChange={(e) => {
              setSaved(false);
              save.mutate(e.target.value as UserLanguage);
            }}
          >
            <option value="auto">{t("settings.profile.languageAuto")}</option>
            {SUPPORTED_LANGUAGES.map((l) => (
              <option key={l} value={l}>
                {t(`language.${l}`)}
              </option>
            ))}
          </NativeSelect>
          <p className="text-xs text-muted-foreground">{t("settings.profile.languageHint")}</p>
          {saved && (
            <p role="status" className="text-xs text-muted-foreground">
              {t("settings.profile.saved")}
            </p>
          )}
          <FormError error={save.error} />
        </dd>
      </dl>
    </SettingsSection>
    <PersonalDataSection />
    </div>
  );
}
