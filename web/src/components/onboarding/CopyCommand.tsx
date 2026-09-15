import { Check, Copy } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";

/**
 * A command or config snippet with a copy button; long lines scroll inside the block. `lang` (sh, powershell, yaml) is
 * exposed as data-lang and a small caption so PowerShell commands are not mistaken for shell commands.
 */
export function CopyCommand({ code, label, lang = "sh", testId }: { code: string; label: string; lang?: string; testId?: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(code);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      // No clipboard permission: the text stays selectable in the block.
    }
  };
  return (
    <div className="flex min-w-0 items-start gap-1 rounded bg-muted/60">
      <div className="min-w-0 flex-1">
        <span className="block px-2 pt-1.5 text-[0.65rem] tracking-wide text-muted-foreground uppercase" aria-hidden="true">
          {lang === "powershell" ? "PowerShell" : lang}
        </span>
        <pre className="overflow-x-auto p-2 pt-1 font-mono" data-testid={testId} data-lang={lang}>
          {code}
        </pre>
      </div>
      <Button type="button" variant="ghost" size="sm" className="shrink-0" aria-label={t("addData.install.copyLabel", { label })} onClick={() => void copy()}>
        {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
        <span className="hidden sm:inline">{copied ? t("addData.install.copied") : t("addData.install.copy")}</span>
      </Button>
    </div>
  );
}
