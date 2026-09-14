import { Check, Copy, KeyRound } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { FleetPHPAccess } from "@/api/fleet";
import { Button } from "@/components/ui/button";

const MAX_LISTED = 20;
const RESTART = "sudo systemctl restart openlog-infra-agent";

function unique(values: string[]): string[] {
  return [...new Set(values.filter(Boolean))];
}

/** A shell command with a copy button; long lines scroll inside the block. */
function CopyCommand({ code, label, testId }: { code: string; label: string; testId?: string }) {
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
      <pre className="min-w-0 flex-1 overflow-x-auto p-2 font-mono" data-testid={testId}>
        {code}
      </pre>
      <Button type="button" variant="ghost" size="sm" className="shrink-0" aria-label={t("addData.install.copyLabel", { label })} onClick={() => void copy()}>
        {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
        <span className="hidden sm:inline">{copied ? t("addData.install.copied") : t("addData.install.copy")}</span>
      </Button>
    </div>
  );
}

/**
 * PHP-FPM pools whose users cannot write the infra agent's php.sock (php-agent.md §1, D-103): their spans are dropped.
 * Shows the pools and both fixes — a service restart lets the privileged pre-start step add the users and reload PHP-FPM,
 * or the manual usermod + reload. Renders nothing when every pool has access.
 */
export function PHPAccessNotice({ access }: { access: FleetPHPAccess }) {
  const { t } = useTranslation();
  const blocked = access.pools.filter((p) => p.access === "missing" || p.access === "opted_out");
  if (blocked.length === 0) return null;
  const group = access.group || "openlog-php";
  const optedOut = access.grants === "opted_out";
  const users = unique(blocked.map((p) => p.user));
  const units = unique(blocked.map((p) => p.unit));
  const manual = [...users.map((u) => `sudo usermod -aG ${group} ${u}`), ...(units.length ? [`sudo systemctl reload ${units.join(" ")}`] : [])].join(" && \\\n  ");

  return (
    <div role="status" data-testid="php-access" className="flex flex-col gap-2 rounded-md border border-warning/40 bg-warning/10 p-3 text-xs">
      <p className="flex items-center gap-2 font-semibold">
        <KeyRound className="size-4 shrink-0" aria-hidden="true" />
        {t("services.phpAccessTitle", { count: blocked.length })}
      </p>
      <p>{t("services.phpAccessBody", { group })}</p>
      <ul className="flex flex-col gap-0.5 font-mono">
        {blocked.slice(0, MAX_LISTED).map((p) => (
          <li key={`${p.unit}/${p.pool}`} className="truncate" title={p.unit}>
            {p.pool} · {p.user}
            {p.php_version ? ` · PHP ${p.php_version}` : ""}
          </li>
        ))}
        {blocked.length > MAX_LISTED && <li>{t("services.phpAccessMore", { count: blocked.length - MAX_LISTED })}</li>}
      </ul>
      {optedOut ? (
        <p>{t("services.phpAccessOptedOut")}</p>
      ) : (
        <>
          <p>{t("services.phpAccessRestart")}</p>
          <CopyCommand code={RESTART} label="systemctl restart openlog-infra-agent" testId="php-access-restart" />
        </>
      )}
      <p>{t("services.phpAccessManual")}</p>
      <CopyCommand code={manual} label="usermod" testId="php-access-manual" />
    </div>
  );
}
