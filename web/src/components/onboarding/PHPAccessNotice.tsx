import { KeyRound } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { FleetPHPAccess } from "@/api/fleet";
import { agentRestart, hostHasPhpForwarder, phpAccessManualCommand, type HostOs } from "@/lib/host-os";
import { CopyCommand } from "./CopyCommand";

const MAX_LISTED = 20;

function unique(values: string[]): string[] {
  return [...new Set(values.filter(Boolean))];
}

/**
 * PHP-FPM pools whose users cannot write the infra agent's php.sock (php-agent.md §1, D-103): their spans are dropped.
 * Shows the pools and both fixes — a service restart lets the privileged start step add the users and reload PHP-FPM,
 * or the manual group grant + reload, for the host's OS (D-112). Renders nothing when every pool has access or on
 * Windows, where the PHP forwarder does not exist.
 */
export function PHPAccessNotice({ access, os = "linux" }: { access: FleetPHPAccess; os?: HostOs }) {
  const { t } = useTranslation();
  const blocked = access.pools.filter((p) => p.access === "missing" || p.access === "opted_out");
  if (blocked.length === 0 || !hostHasPhpForwarder(os)) return null;
  const group = access.group || "openlog-php";
  const optedOut = access.grants === "opted_out";
  const users = unique(blocked.map((p) => p.user));
  const units = unique(blocked.map((p) => p.unit));
  const manual = phpAccessManualCommand(os, group, users, units);
  const restart = agentRestart(os);

  return (
    <div role="status" data-testid="php-access" data-os={os} className="flex flex-col gap-2 rounded-md border border-warning/40 bg-warning/10 p-3 text-xs">
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
          <CopyCommand code={restart.code} label={t("services.phpAccessRestartLabel")} lang={restart.lang} testId="php-access-restart" />
        </>
      )}
      <p>{t("services.phpAccessManual")}</p>
      <CopyCommand code={manual} label={os === "darwin" ? "dseditgroup" : "usermod"} testId="php-access-manual" />
    </div>
  );
}
