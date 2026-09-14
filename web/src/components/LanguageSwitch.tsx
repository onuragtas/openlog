import { useQueryClient } from "@tanstack/react-query";
import { useId } from "react";
import { useTranslation } from "react-i18next";
import { meQuery, setMyLanguage, useMe } from "@/api/account";
import { NativeSelect } from "@/components/ui/native-select";
import { SUPPORTED_LANGUAGES, type Language } from "@/i18n";

export function LanguageSwitch({ onSelect }: { onSelect?: (language: Language) => void } = {}) {
  const { t, i18n } = useTranslation();
  const id = useId();
  return (
    <>
      <label htmlFor={id} className="sr-only">
        {t("language.label")}
      </label>
      <NativeSelect
        id={id}
        value={i18n.resolvedLanguage ?? "en"}
        onChange={(e) => {
          const language = e.target.value as Language;
          void i18n.changeLanguage(language);
          onSelect?.(language);
        }}
        className="h-8 text-xs"
      >
        {SUPPORTED_LANGUAGES.map((l) => (
          <option key={l} value={l}>
            {t(`language.${l}`)}
          </option>
        ))}
      </NativeSelect>
    </>
  );
}

/**
 * Language switch of signed-in pages: a user who chose a language in Settings → Profile keeps that preference in
 * sync with the switch (saved on the server); with "auto" the choice stays in this browser only (D-095).
 */
export function AccountLanguageSwitch() {
  const qc = useQueryClient();
  const user = useMe().data?.user;
  return (
    <LanguageSwitch
      onSelect={(language) => {
        if (!user || user.language === "auto" || user.language === language) return;
        void setMyLanguage(language)
          .then((me) => qc.setQueryData(meQuery().queryKey, me))
          .catch(() => undefined);
      }}
    />
  );
}
