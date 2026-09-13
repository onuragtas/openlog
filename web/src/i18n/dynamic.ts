import i18n from "./index";

/**
 * Translates a key computed at runtime (e.g. a category coming from the API),
 * falling back to the raw value when no translation exists. Static keys should
 * use the typed `t()` from useTranslation instead.
 */
export function translateOptional(key: string, fallback: string): string {
  if (!i18n.exists(key)) return fallback;
  return (i18n.t as unknown as (k: string) => string)(key);
}
