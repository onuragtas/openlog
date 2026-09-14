import { Check, Copy } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

/** A read-only value to register at the identity provider (redirect URI, entity ID, certificate …) with a copy button. */
export function SsoCopyField({ label, value, multiline = false }: { label: string; value: string; multiline?: boolean }) {
  const { t } = useTranslation();
  const id = useId();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      (document.getElementById(id) as HTMLInputElement | HTMLTextAreaElement | null)?.select();
    }
  };
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <div className="flex min-w-0 items-start gap-2">
        {multiline ? (
          <textarea
            id={id}
            readOnly
            value={value}
            rows={4}
            className="min-w-0 flex-1 resize-y rounded-md border border-input bg-muted/40 px-2 py-1 font-mono text-xs"
            onFocus={(e) => e.currentTarget.select()}
          />
        ) : (
          <Input id={id} readOnly value={value} className="min-w-0 flex-1 font-mono text-xs" onFocus={(e) => e.currentTarget.select()} />
        )}
        <Button type="button" variant="outline" size="sm" aria-label={`${t("common.copy")}: ${label}`} onClick={() => void copy()}>
          {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
          <span className="hidden sm:inline">{copied ? t("settings.copied") : t("common.copy")}</span>
        </Button>
      </div>
    </div>
  );
}
