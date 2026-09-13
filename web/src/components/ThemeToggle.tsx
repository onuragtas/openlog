import { Monitor, Moon, Sun } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { useTheme, type ThemePreference } from "@/lib/theme";

const NEXT: Record<ThemePreference, ThemePreference> = { system: "light", light: "dark", dark: "system" };

export function ThemeToggle() {
  const { t } = useTranslation();
  const { preference, setPreference } = useTheme();
  const Icon = preference === "light" ? Sun : preference === "dark" ? Moon : Monitor;
  const label = t("theme.switchTo", { current: t(`theme.${preference}`) });
  return (
    <Button variant="ghost" size="icon" aria-label={label} title={label} onClick={() => setPreference(NEXT[preference])}>
      <Icon aria-hidden="true" />
    </Button>
  );
}
