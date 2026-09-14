import { Check, Copy } from "lucide-react";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { maskKey, type CommandBlock } from "@/lib/install-commands";

/**
 * One copyable command, config file or code snippet. A block that contains the license key shows it masked until
 * `revealed`; the copy button always copies the real text. Wide lines scroll inside the block, never the page.
 */
export function CommandBlockView({ block, title, licenseKey, revealed }: { block: CommandBlock; title: string; licenseKey: string; revealed: boolean }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const codeRef = useRef<HTMLElement>(null);
  const shown = block.containsKey && !revealed ? maskKey(block.code, licenseKey) : block.code;

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(block.code);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      // No clipboard permission: select the text so it can be copied by hand (masked text is revealed first).
      const el = codeRef.current;
      const sel = window.getSelection();
      if (el && sel && !(block.containsKey && !revealed)) {
        const range = document.createRange();
        range.selectNodeContents(el);
        sel.removeAllRanges();
        sel.addRange(range);
      }
    }
  };

  return (
    <figure data-testid="command-block" data-block={block.id} className="min-w-0 overflow-hidden rounded-lg border bg-muted/40">
      <figcaption className="flex min-h-10 items-center justify-between gap-2 border-b bg-muted/60 px-3 py-1 text-xs font-medium">
        <span className="min-w-0 truncate">{title}</span>
        <Button type="button" variant="ghost" size="sm" aria-label={t("addData.install.copyLabel", { label: title })} onClick={() => void copy()}>
          {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
          <span className="hidden sm:inline">{copied ? t("addData.install.copied") : t("addData.install.copy")}</span>
        </Button>
      </figcaption>
      <div className="max-w-full overflow-x-auto overscroll-x-contain">
        <pre className="w-max min-w-full p-3 font-mono text-xs leading-relaxed">
          <code ref={codeRef} data-lang={block.lang}>
            {shown}
          </code>
        </pre>
      </div>
    </figure>
  );
}
