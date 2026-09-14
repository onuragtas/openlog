import type { UserLanguage } from "@/api/account";
import i18n, { LANGUAGE_STORAGE_KEY, SUPPORTED_LANGUAGES, type Language } from "@/i18n";

export function isLanguage(v: string | undefined): v is Language {
  return (SUPPORTED_LANGUAGES as readonly string[]).includes(v ?? "");
}

/**
 * Switches the UI to the user's language preference (D-095). "auto" forgets the language remembered in this browser
 * and follows the browser's language again.
 */
export function applyUserLanguage(language: UserLanguage | undefined): void {
  if (isLanguage(language)) {
    if (i18n.resolvedLanguage !== language) void i18n.changeLanguage(language);
    return;
  }
  if (language === "auto") {
    try {
      localStorage.removeItem(LANGUAGE_STORAGE_KEY);
    } catch {
      // storage unavailable: the detector falls back to the browser language
    }
    void i18n.changeLanguage(undefined);
  }
}
