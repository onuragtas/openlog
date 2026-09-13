import { useId } from "react";
import { useTranslation } from "react-i18next";
import { NativeSelect } from "@/components/ui/native-select";
import { SUPPORTED_LANGUAGES } from "@/i18n";

export function LanguageSwitch() {
  const { t, i18n } = useTranslation();
  const id = useId();
  return (
    <>
      <label htmlFor={id} className="sr-only">
        {t("language.label")}
      </label>
      <NativeSelect id={id} value={i18n.resolvedLanguage ?? "en"} onChange={(e) => void i18n.changeLanguage(e.target.value)} className="h-8 text-xs">
        {SUPPORTED_LANGUAGES.map((l) => (
          <option key={l} value={l}>
            {t(`language.${l}`)}
          </option>
        ))}
      </NativeSelect>
    </>
  );
}
