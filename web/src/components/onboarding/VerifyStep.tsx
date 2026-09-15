import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { CheckCircle2, Loader2 } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
  baselineQuery,
  verifyApmServicesQuery,
  verifyHostsQuery,
  verifyKubernetesQuery,
  verifyLogsQuery,
  VERIFY_POLL_MS,
  VERIFY_TIMEOUT_MS,
  type VerifyLogsFilter,
} from "@/api/onboarding";
import { buttonVariants } from "@/components/ui/button";
import { useNow } from "@/lib/hooks";
import { agentLog, agentSelfTest, SYSTEM_LOG_INPUT, type HostOs } from "@/lib/host-os";
import { cleanName, expectedServiceName, type InstallOptions, type InstallTarget } from "@/lib/install-commands";
import { tDynamic } from "@/lib/onboarding-key";
import { cn } from "@/lib/utils";

type TipKey = "firewall" | "endpoint" | "key" | "journal" | "dockerLogs" | "kubectlLogs" | "serviceName" | "appLogs" | "cors" | "agentConfig" | "phpForwarder";

function tipsFor(target: InstallTarget): TipKey[] {
  switch (target.id) {
    case "linux":
    case "macos":
    case "windows":
      return ["firewall", "endpoint", "key", "journal"];
    case "docker":
      return ["firewall", "endpoint", "key", "dockerLogs"];
    case "kubernetes":
      return ["firewall", "endpoint", "key", "kubectlLogs"];
    case "apm/php":
      return ["phpForwarder", "serviceName", "journal"];
    case "logs/host":
    case "logs/containers":
      return ["agentConfig", "journal"];
    case "logs/browser":
      return ["cors", "endpoint", "key"];
    default:
      return ["firewall", "endpoint", "key", "serviceName", "appLogs"];
  }
}

/** OS of the agent the tips talk about: the host card itself, or the host OS option of host-scoped cards; PHP is Linux. */
function tipOs(target: InstallTarget, options: InstallOptions): HostOs {
  if (target.id === "macos") return "darwin";
  if (target.id === "windows" || target.id === "integrations/iis") return "windows";
  if (target.options.includes("hostOs")) return options.hostOs;
  return "linux";
}

export interface VerifyStepProps {
  target: InstallTarget;
  options: InstallOptions;
  /** When the flow started (unix ms): entities that reported before are the baseline. */
  startedAt: number;
  /** OTLP/HTTP endpoint shown in the tips. */
  endpoint: string;
  intervalMs?: number;
  timeoutMs?: number;
}

type Common = Required<VerifyStepProps>;

/** The service name APM verification waits for ("" = any new service). */
function apmServiceName(target: InstallTarget, options: InstallOptions): string {
  if (target.id === "otel/collector") return "";
  if (target.id === "apm/php" && !cleanName(options.serviceName)) return "php-app";
  return expectedServiceName(options);
}

function Panel({
  common,
  found,
  waiting,
  success,
  action,
  service,
}: {
  common: Common;
  found: boolean;
  waiting: string;
  success: string;
  action?: ReactNode;
  service?: string;
}) {
  const { t } = useTranslation();
  const now = useNow(1_000);
  const timedOut = !found && now - common.startedAt > common.timeoutMs;
  const os = tipOs(common.target, common.options);
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <div
        data-testid="verify-status"
        data-state={found ? "success" : "waiting"}
        role="status"
        aria-live="polite"
        className={cn("flex items-start gap-3 rounded-lg border p-4", found ? "border-success/50 bg-success/10" : "bg-muted/40")}
      >
        {found ? (
          <CheckCircle2 className="mt-0.5 size-5 shrink-0 text-success-text" aria-hidden="true" />
        ) : (
          <Loader2 className="mt-0.5 size-5 shrink-0 animate-spin text-muted-foreground" aria-hidden="true" />
        )}
        <div className="min-w-0 flex-1">
          <p className="font-medium break-words">{found ? t("addData.verify.success") : waiting}</p>
          <p className="text-sm break-words text-muted-foreground">{found ? success : t("addData.verify.checking", { seconds: Math.max(1, Math.round(common.intervalMs / 1000)) })}</p>
          {found && action && <div className="mt-3 flex flex-wrap gap-2">{action}</div>}
        </div>
      </div>
      {timedOut && (
        <p role="alert" data-testid="verify-timeout" className="rounded-lg border border-warning/60 bg-warning/10 p-3 text-sm">
          {t("addData.verify.timeout", { minutes: Math.max(1, Math.round(common.timeoutMs / 60_000)) })}
        </p>
      )}
      {!found && (
        <details open={timedOut || undefined} className="rounded-lg border p-3 text-sm" data-testid="verify-tips">
          <summary className="cursor-pointer font-medium">{t("addData.verify.tipsTitle")}</summary>
          <ul className="mt-2 flex list-disc flex-col gap-1 pl-5 break-words text-muted-foreground">
            {tipsFor(common.target).map((k) => (
              <li key={k}>
                {/* No expected service name (OpenTelemetry Collector: any new service): a tip without a quoted name. */}
                {tDynamic(t, `addData.verify.tips.${k === "serviceName" && !service ? "serviceNameAny" : k}`, {
                  endpoint: common.endpoint,
                  name: service ?? "",
                  command: k === "agentConfig" ? agentSelfTest(os).code : agentLog(os).code,
                })}
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  );
}

function HostVerify({ common }: { common: Common }) {
  const { t } = useTranslation();
  const base = useQuery(baselineQuery("host", common.startedAt));
  const live = useQuery({ ...verifyHostsQuery(common.intervalMs), enabled: base.isSuccess });
  const name = common.options.hostName.trim();
  const known = new Set(base.data ?? []);
  const found = base.isSuccess
    ? live.data?.find(
        (h) => (name !== "" && h.host_name.toLowerCase() === name.toLowerCase() && Date.parse(h.last_seen) >= common.startedAt - 120_000) || !known.has(h.host_id),
      )
    : undefined;
  return (
    <Panel
      common={common}
      found={!!found}
      waiting={name ? t("addData.verify.hostNamed", { name }) : t("addData.verify.host")}
      success={found ? t("addData.verify.successHost", { name: found.host_name || found.host_id }) : ""}
      action={
        found && (
          <Link to="/hosts/$hostId" params={{ hostId: found.host_id }} className={buttonVariants({ size: "sm" })} data-testid="verify-open">
            {t("addData.verify.openHost")}
          </Link>
        )
      }
    />
  );
}

function KubernetesVerify({ common }: { common: Common }) {
  const { t } = useTranslation();
  const base = useQuery(baselineQuery("kubernetes", common.startedAt));
  const live = useQuery({ ...verifyKubernetesQuery(common.intervalMs), enabled: base.isSuccess });
  const name = cleanName(common.options.clusterName);
  const known = new Set(base.data ?? []);
  const found = base.isSuccess ? live.data?.find((c) => (name !== "" && c.cluster_name === name && c.reporting) || !known.has(c.cluster_uid)) : undefined;
  return (
    <Panel
      common={common}
      found={!!found}
      waiting={name ? t("addData.verify.kubernetesNamed", { name }) : t("addData.verify.kubernetes")}
      success={found ? t("addData.verify.successCluster", { name: found.cluster_name || found.cluster_uid }) : ""}
      action={
        found && (
          <Link
            to="/kubernetes"
            search={(prev: Record<string, unknown>) => ({ range: prev.range, from: prev.from, to: prev.to, cluster: found.cluster_uid }) as never}
            className={buttonVariants({ size: "sm" })}
            data-testid="verify-open"
          >
            {t("addData.verify.openCluster")}
          </Link>
        )
      }
    />
  );
}

function ApmVerify({ common }: { common: Common }) {
  const { t } = useTranslation();
  const base = useQuery(baselineQuery("apm", common.startedAt));
  const live = useQuery({ ...verifyApmServicesQuery(common.intervalMs), enabled: base.isSuccess });
  const expected = apmServiceName(common.target, common.options);
  const known = new Set(base.data ?? []);
  const found = base.isSuccess ? live.data?.find((s) => (expected ? s.service_name === expected : !known.has(s.service_name))) : undefined;
  return (
    <Panel
      common={common}
      found={!!found}
      service={expected}
      waiting={expected ? t("addData.verify.apm", { name: expected }) : t("addData.verify.apmAny")}
      success={found ? t("addData.verify.successService", { name: found.service_name }) : ""}
      action={
        found && (
          <Link to="/apm/services/$service" params={{ service: found.service_name }} className={buttonVariants({ size: "sm" })} data-testid="verify-open">
            {t("addData.verify.openService")}
          </Link>
        )
      }
    />
  );
}

function LogsVerify({ common }: { common: Common }) {
  const { t } = useTranslation();
  const since = common.startedAt - 60_000;
  const service = common.target.id === "logs/browser" || common.target.id === "logs/otel" ? expectedServiceName(common.options) : "";
  const filters: VerifyLogsFilter[] =
    common.target.id === "logs/host"
      ? common.options.journald
        ? [{ source: "file" }, { source: SYSTEM_LOG_INPUT[common.options.hostOs] ?? "journald" }]
        : [{ source: "file" }]
      : common.target.id === "logs/containers"
        ? [{ source: "container" }]
        : [{ service }];
  const first = useQuery(verifyLogsQuery(filters[0]!, since, common.intervalMs));
  const second = useQuery({ ...verifyLogsQuery(filters[1] ?? filters[0]!, since, common.intervalMs), enabled: filters.length > 1 });
  const found = (first.data?.length ?? 0) > 0 || (filters.length > 1 && (second.data?.length ?? 0) > 0);
  return (
    <Panel
      common={common}
      found={found}
      service={service}
      waiting={service ? t("addData.verify.logsService", { name: service }) : t("addData.verify.logs")}
      success={t("addData.verify.successLogs")}
      action={
        <Link
          to="/logs"
          search={(prev: Record<string, unknown>) => ({ range: prev.range, from: prev.from, to: prev.to, service: service || undefined }) as never}
          className={buttonVariants({ size: "sm" })}
          data-testid="verify-open"
        >
          {t("addData.verify.openLogs")}
        </Link>
      }
    />
  );
}

function IntegrationVerify({ common }: { common: Common }) {
  const { t } = useTranslation();
  return (
    <div data-testid="verify-status" data-state="integration" className="flex flex-col gap-3 rounded-lg border bg-muted/40 p-4 text-sm">
      <p>{t("addData.verify.integration")}</p>
      <div className="flex flex-wrap gap-2">
        <Link
          to="/integrations"
          search={(prev: Record<string, unknown>) => ({ range: prev.range, from: prev.from, to: prev.to, integration: common.target.integration }) as never}
          className={buttonVariants({ size: "sm" })}
        >
          {t("addData.verify.openIntegrations")}
        </Link>
        <Link to="/hosts" className={buttonVariants({ size: "sm", variant: "outline" })}>
          {t("addData.verify.openHosts")}
        </Link>
      </div>
    </div>
  );
}

/** Step 4: "Waiting for data…" until the new host, service, cluster or log records show up. */
export function VerifyStep({ intervalMs = VERIFY_POLL_MS, timeoutMs = VERIFY_TIMEOUT_MS, ...props }: VerifyStepProps) {
  const common: Common = { ...props, intervalMs, timeoutMs };
  switch (props.target.verify) {
    case "host":
      return <HostVerify common={common} />;
    case "kubernetes":
      return <KubernetesVerify common={common} />;
    case "apm":
      return <ApmVerify common={common} />;
    case "logs":
      return <LogsVerify common={common} />;
    default:
      return <IntegrationVerify common={common} />;
  }
}
