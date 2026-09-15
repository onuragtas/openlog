// Java agent section of a host (java-agent.md §2, D-123): the managed jar, the JVMs that load the openlog Java agent with
// the version each one actually runs, "restart pending" badges and the per-host mode.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId } from "react";
import { useTranslation } from "react-i18next";
import { clearHostJavaAgentMode, setHostJavaAgentMode, type FleetHost, type FleetJavaAgentMode } from "@/api/fleet";
import { FormError } from "@/components/settings/common";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { translateOptional } from "@/i18n/dynamic";
import { hasJavaAgent, javaStateTone, javaStatusTone } from "@/lib/java-agent";

export function JavaAgentPanel({ host, canManage }: { host: FleetHost; canManage: boolean }) {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const p = host.java_agent;
  const mode = useMutation({
    mutationFn: async (m: FleetJavaAgentMode | "") => {
      if (m === "") await clearHostJavaAgentMode(host.host_id);
      else await setHostJavaAgentMode(host.host_id, m);
    },
    onSettled: () => void qc.invalidateQueries({ queryKey: ["fleet"] }),
  });
  if (!hasJavaAgent(p)) return null;
  const pending = p.jvms.filter((j) => j.restart_pending).length;

  return (
    <Card data-testid="java-agent-panel">
      <CardHeader>
        <CardTitle className="text-base">{t("fleet.javaPanel.title")}</CardTitle>
        <p className="text-xs text-muted-foreground">{t("fleet.javaPanel.description")}</p>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-sm">
        <div className="flex flex-wrap items-center gap-2">
          {p.state && <Badge variant={javaStateTone(p.state)}>{translateOptional(`fleet.javaState.${p.state}`, p.state)}</Badge>}
          {p.version && <span className="font-mono text-xs">{t("fleet.hosts.javaInstalled", { version: p.version })}</span>}
          <Badge variant={javaStatusTone(p.status)}>
            {p.status === "offer" ? t("fleet.javaStatus.offer", { version: p.status_target ?? "" }) : translateOptional(`fleet.javaStatus.${p.status}`, p.status)}
          </Badge>
          {p.update && p.update.state !== "applied" && (
            <Badge variant={p.update.state === "failed" || p.update.state === "rolled_back" ? "destructive" : "secondary"} title={p.update.error || undefined}>
              {translateOptional(`fleet.javaUpdate.${p.update.state}`, p.update.state)} {p.update.version}
            </Badge>
          )}
          {p.link_path && <span className="max-w-full truncate font-mono text-[11px] text-muted-foreground">{t("fleet.javaPanel.link", { path: p.link_path })}</span>}
        </div>
        {p.detail && (
          <p className={p.state === "error" ? "text-xs text-destructive-text" : "text-xs text-muted-foreground"} data-testid="java-agent-detail">
            {p.detail}
          </p>
        )}
        {pending > 0 && p.version && (
          <p role="status" className="rounded-md border border-warning/40 bg-warning/10 p-2 text-xs" data-testid="java-restart-hint">
            {t("fleet.javaPanel.restartHint", { version: p.version })}
          </p>
        )}
        {p.jvms.length === 0 ? (
          <p className="text-xs text-muted-foreground">{t("fleet.javaPanel.empty")}</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[36rem] text-left text-xs">
              <thead className="text-muted-foreground">
                <tr className="border-b">
                  <th scope="col" className="px-2 py-1.5 font-medium">{t("fleet.javaPanel.columns.pid")}</th>
                  <th scope="col" className="px-2 py-1.5 font-medium">{t("fleet.javaPanel.columns.process")}</th>
                  <th scope="col" className="px-2 py-1.5 font-medium">{t("fleet.javaPanel.columns.agentPath")}</th>
                  <th scope="col" className="px-2 py-1.5 font-medium">{t("fleet.javaPanel.columns.loaded")}</th>
                  <th scope="col" className="px-2 py-1.5 font-medium">{t("fleet.javaPanel.columns.status")}</th>
                </tr>
              </thead>
              <tbody>
                {p.jvms.map((j) => (
                  <tr key={j.pid} className="border-b border-border/60 last:border-0" data-testid="java-jvm-row">
                    <td className="px-2 py-1.5 font-mono">{j.pid}</td>
                    <td className="max-w-[16rem] px-2 py-1.5">
                      <span className="block truncate font-medium">{j.name}</span>
                      <span className="block truncate font-mono text-[11px] text-muted-foreground" title={j.command}>
                        {j.command}
                      </span>
                    </td>
                    <td className="max-w-[16rem] truncate px-2 py-1.5 font-mono text-[11px]" title={j.agent_path}>
                      {j.agent_path}
                    </td>
                    <td className="px-2 py-1.5 font-mono">{j.loaded_version || t("fleet.javaPanel.unknown")}</td>
                    <td className="px-2 py-1.5">
                      {j.container ? (
                        <Badge variant="outline">{t("fleet.javaPanel.container")}</Badge>
                      ) : !j.managed ? (
                        <Badge variant="outline">{t("fleet.javaPanel.notManaged")}</Badge>
                      ) : j.restart_pending ? (
                        <Badge variant="warning">{t("fleet.javaPanel.restartPending")}</Badge>
                      ) : (
                        <Badge variant="default">{t("fleet.javaPanel.current")}</Badge>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <FormError error={mode.error} />
        {canManage && (
          <div className="flex min-w-0 flex-col gap-1 sm:max-w-xs">
            <Label htmlFor={`${id}-mode`} className="text-xs">
              {t("fleet.javaPanel.mode")}
            </Label>
            <NativeSelect
              id={`${id}-mode`}
              className="w-full min-w-0"
              value={p.override?.mode ?? ""}
              disabled={mode.isPending}
              onChange={(e) => mode.mutate(e.target.value as FleetJavaAgentMode | "")}
            >
              <option value="">{t("fleet.hosts.javaFollowPolicy")}</option>
              <option value="auto">{t("fleet.hosts.javaForce.auto")}</option>
              <option value="manual">{t("fleet.hosts.javaForce.manual")}</option>
              <option value="off">{t("fleet.hosts.javaForce.off")}</option>
            </NativeSelect>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
