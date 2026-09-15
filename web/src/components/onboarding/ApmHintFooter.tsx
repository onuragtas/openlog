import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Activity, CheckCircle2, Code2, Loader2, Rocket } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { apmHostServicesQuery } from "@/api/apm";
import { fleetHostQuery, setHostPHPAgentMode } from "@/api/fleet";
import { onboardingQuery } from "@/api/onboarding";
import { hostQuery } from "@/api/queries";
import { can } from "@/api/roles";
import { WriteGuard } from "@/components/ReadOnly";
import { FormError } from "@/components/settings/common";
import { hostHasPhpForwarder, hostHasPhpInstall, hostOsOf } from "@/lib/host-os";
import { Button, buttonVariants } from "@/components/ui/button";
import { CardFooter } from "@/components/ui/card";
import { AGENT_PRODUCTS, apmTargetForLanguage } from "@/lib/install-commands";
import { PHPAccessNotice } from "./PHPAccessNotice";

export interface ApmHint {
  language?: string;
  /** Discovery identifier (e.g. openlog-agent-php, semantic-conventions §3.4), not an install name. */
  agent?: string;
  status?: "active" | "not_installed" | string;
}

/**
 * APM part of a discovered service card (host → Services). `status: active` links to APM; otherwise it names the real
 * agent product for the language and deep-links to its Add data card, prefilled with this host and service. PHP can
 * also be installed by the fleet on this host (admins).
 */
export function ApmHintFooter({ hint, hostId, serviceName, language }: { hint: ApmHint; hostId: string; serviceName: string; language: string }) {
  const { t } = useTranslation();
  const host = useQuery(hostQuery(hostId)).data;
  const hostName = host?.host_name ?? "";
  const os = hostOsOf(host);
  const id = useId();
  const [open, setOpen] = useState(false);
  const target = apmTargetForLanguage(hint.language);
  const product = (target && AGENT_PRODUCTS[target]) || language;
  const active = hint.status === "active";
  const me = useMe().data;
  const onboarding = useQuery({ ...onboardingQuery(), enabled: open && target === "apm/php" });
  const services = useQuery({ ...apmHostServicesQuery(hostId), enabled: active });
  const fleet = useMutation({ mutationFn: () => setHostPHPAgentMode(hostId, "auto") });
  // The fleet installs the PHP agent on Linux hosts only (D-104).
  const canFleet =
    target === "apm/php" && hostHasPhpInstall(os) && !!onboarding.data?.features.fleet_php_install && can(me?.role, "fleet.manage") && me?.auth === "session";
  // PHP-FPM pools whose users cannot write php.sock (reported by the infra agent in sync). Every role may read the fleet;
  // without the fleet API (other auth modes) the query fails and nothing is shown. Windows has no PHP forwarder.
  const fleetHost = useQuery({ ...fleetHostQuery(hostId), enabled: target === "apm/php" && !!me && hostHasPhpForwarder(os) });
  const phpAccess = fleetHost.data?.php_access;
  const accessNotice = phpAccess ? <PHPAccessNotice access={phpAccess} os={os} /> : null;

  if (active) {
    const first = services.data?.[0]?.service_name;
    return (
      <CardFooter className="flex-col items-stretch gap-2 border-t pt-3" data-testid="apm-hint" data-status="active">
        <p className="flex items-center gap-2 text-sm font-medium">
          <CheckCircle2 className="size-4 shrink-0 text-success-text" aria-hidden="true" />
          {t("services.apmActive")}
        </p>
        <p className="text-xs text-muted-foreground">{t("services.apmActiveBody", { name: serviceName, product })}</p>
        {accessNotice}
        {first ? (
          <Link to="/apm/services/$service" params={{ service: first }} className={buttonVariants({ variant: "outline", size: "sm", className: "min-h-10" })}>
            <Activity aria-hidden="true" />
            {t("services.apmOpen")}
          </Link>
        ) : (
          <Link to="/apm" className={buttonVariants({ variant: "outline", size: "sm", className: "min-h-10" })}>
            <Activity aria-hidden="true" />
            {t("services.apmOpen")}
          </Link>
        )}
      </CardFooter>
    );
  }

  return (
    <CardFooter className="flex-col items-stretch gap-2 border-t pt-3" data-testid="apm-hint" data-status={hint.status ?? "unknown"}>
      {accessNotice}
      <Button variant="outline" size="sm" aria-expanded={open} aria-controls={`${id}-apm`} onClick={() => setOpen((o) => !o)}>
        <Code2 aria-hidden="true" />
        {t("services.apmInstall", { product })}
      </Button>
      {open && (
        <div id={`${id}-apm`} className="flex flex-col gap-2 rounded-md bg-accent p-3 text-xs text-accent-foreground">
          <p className="font-semibold">{t("services.apmTitle")}</p>
          <p>{target ? t("services.apmBody", { name: serviceName, language, product }) : t("services.apmUnknown", { language })}</p>
          <Link
            to="/add-data/$"
            params={{ _splat: target ?? "otel/sdk" }}
            search={{ host: hostId, hostName, service: serviceName } as never}
            className={buttonVariants({ size: "sm", className: "min-h-10" })}
            data-testid="apm-hint-setup"
          >
            {t("services.apmSetup")}
          </Link>
          {canFleet &&
            (fleet.isSuccess ? (
              <p role="status" className="flex items-center gap-1.5">
                <CheckCircle2 className="size-4 shrink-0 text-success-text" aria-hidden="true" />
                {t("services.apmFleetDone")}
              </p>
            ) : (
              <>
                {/* Changes the host's fleet settings: disabled with the reason in a read-only organization. */}
                <WriteGuard block>
                  <Button type="button" variant="outline" size="sm" className="min-h-10 w-full" disabled={fleet.isPending} onClick={() => fleet.mutate()}>
                    {fleet.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Rocket aria-hidden="true" />}
                    {t("services.apmFleet")}
                  </Button>
                </WriteGuard>
                <p className="text-muted-foreground">{t("services.apmFleetHelp")}</p>
              </>
            ))}
          <FormError error={fleet.error} />
        </div>
      )}
    </CardFooter>
  );
}
