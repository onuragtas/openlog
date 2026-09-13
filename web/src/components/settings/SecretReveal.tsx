import { Check, Copy } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

/**
 * Shows a secret returned once by the API (license key, API key, invitation
 * link). The parent drops the value on "Done"; it is never stored or refetched.
 */
export function SecretReveal({ label, secret, note, onDone }: { label: string; secret: string; note?: string; onDone: () => void }) {
  const { t } = useTranslation();
  const id = useId();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(secret);
      setCopied(true);
    } catch {
      (document.getElementById(`${id}-secret`) as HTMLInputElement | null)?.select();
    }
  };
  return (
    <div data-testid="secret-reveal" className="flex flex-col gap-2 rounded-lg border border-warning/60 bg-warning/10 p-3">
      <label htmlFor={`${id}-secret`} className="text-sm font-medium">
        {label}
      </label>
      <div className="flex flex-wrap gap-2">
        <Input id={`${id}-secret`} readOnly value={secret} className="min-w-0 flex-1 font-mono text-xs" onFocus={(e) => e.currentTarget.select()} />
        <Button type="button" variant="outline" onClick={() => void copy()}>
          {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
          {copied ? t("settings.copied") : t("common.copy")}
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">{note ?? t("settings.shownOnce")}</p>
      <div>
        <Button type="button" size="sm" onClick={onDone}>
          {t("settings.done")}
        </Button>
      </div>
    </div>
  );
}
