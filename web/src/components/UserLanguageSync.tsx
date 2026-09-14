import { useEffect, useRef } from "react";
import { useMe } from "@/api/account";
import { applyUserLanguage, isLanguage } from "@/lib/user-language";

/**
 * Applies the signed-in user's language preference over the browser's once it is loaded and whenever it changes;
 * a language picked afterwards with the language switch stays until the preference changes again (D-095).
 */
export function UserLanguageSync() {
  const language = useMe().data?.user?.language;
  const applied = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (!isLanguage(language) || applied.current === language) return;
    applied.current = language;
    applyUserLanguage(language);
  }, [language]);
  return null;
}
