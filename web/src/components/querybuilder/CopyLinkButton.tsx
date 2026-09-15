import { Check, Link2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { copyText } from "@/lib/clipboard";

/** Copies the current page URL (explorer state lives in the URL). */
export function CopyLinkButton() {
  const { t } = useTranslation();
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  useEffect(() => {
    if (state === "idle") return;
    const id = setTimeout(() => setState("idle"), 2000);
    return () => clearTimeout(id);
  }, [state]);
  return (
    <Button type="button" variant="outline" size="sm" onClick={() => void copyText(window.location.href).then((ok) => setState(ok ? "copied" : "failed"))}>
      {state === "copied" ? <Check aria-hidden="true" /> : <Link2 aria-hidden="true" />}
      <span aria-live="polite">{state === "copied" ? t("savedViews.linkCopied") : state === "failed" ? t("savedViews.copyFailed") : t("savedViews.copyLink")}</span>
    </Button>
  );
}
