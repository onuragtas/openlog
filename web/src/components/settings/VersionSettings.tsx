import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Loader2, RefreshCw } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { followingRequest, requestUpdateApply, requestUpdateCheck, updateInProgress, versionQuery, type UpdateRequest, type VersionInfo } from "@/api/version";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DateTimeText, FormError, SettingsSection } from "./common";

type Updater = NonNullable<VersionInfo["updater"]>;

const CHECK_VARIANT = { enabled: "success", disabled: "muted", failed: "destructive" } as const;

const REQUEST_VARIANT: Record<UpdateRequest["state"], "success" | "warning" | "destructive" | "muted"> = {
  pending: "warning",
  running: "warning",
  done: "success",
  failed: "destructive",
  expired: "destructive",
};

function updaterVariant(state: Updater["state"]) {
  switch (state) {
    case "up_to_date":
    case "succeeded":
      return "success" as const;
    case "available":
    case "updating":
    case "waiting_for_maintenance_window":
      return "warning" as const;
    case "error":
    case "failed":
    case "rolled_back":
    case "rollback_failed":
      return "destructive" as const;
    default:
      return "muted" as const;
  }
}

/** The release the updater would install: its own selection, else the api's newest release. */
function updateTarget(v: VersionInfo): string | null {
  const u = v.updater;
  if (u?.target_version && u.target_version !== v.version && u.state !== "up_to_date" && u.state !== "succeeded") return u.target_version;
  return v.latest_available?.version ?? null;
}

/**
 * The backend version, the release check and openlog-updater (GET /api/v1/version), with "Check now"
 * and "Update now" for admins (POST /api/v1/version/check, /update). The banner in AppShell only
 * appears for admins when something newer exists; this section always shows the state. While an
 * update it started runs, the page keeps polling through the restart of the server.
 */
export function VersionSettings() {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const [followId, setFollowId] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [ignoreWindow, setIgnoreWindow] = useState(false);
  const q = useQuery(versionQuery({ followId }));

  const check = useMutation({
    mutationFn: requestUpdateCheck,
    onSuccess: (data) => qc.setQueryData(versionQuery().queryKey, data),
  });
  const apply = useMutation({
    mutationFn: (target: string) => requestUpdateApply(target, ignoreWindow),
    onSuccess: (req) => {
      setFollowId(req.id);
      setConfirming(false);
      void qc.invalidateQueries({ queryKey: ["version"] });
    },
  });

  const v = q.data;
  const latest = v?.update_requests?.latest ?? null;
  // Following stops once the request finished and the server answers again.
  const following = followingRequest(v, followId, !q.isError);

  if (q.isPending) return <LoadingState />;
  if (!v) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;

  const u = v.updater;
  const steps = u?.steps ?? [];
  const requests = v.update_requests;
  const busy = following || updateInProgress(v);
  const reconnecting = q.isError && busy;
  const target = updateTarget(v);
  const updaterUsable = !!u && u.mode !== "off";
  const listening = requests?.updater_listening ?? false;
  const canApply = updaterUsable && (u.engine === "kubernetes" || listening);
  let hint: string | null = null;
  if (u?.mode === "off") hint = t("update.info.modeOff");
  else if (u?.engine === "kubernetes") hint = t("update.info.kubernetes");
  else if (u && !listening) hint = t("update.info.notListening");

  return (
    <SettingsSection title={t("update.info.title")} description={t("update.info.description")}>
      {reconnecting && (
        <p role="status" className="mb-4 flex items-center gap-2 rounded-md border border-primary/40 bg-primary/10 px-3 py-2 text-sm">
          <Loader2 className="size-4 animate-spin" aria-hidden="true" />
          {t("update.info.reconnecting")}
        </p>
      )}
      <dl className="grid grid-cols-1 gap-x-6 gap-y-3 text-sm sm:grid-cols-[12rem_1fr]">
        <dt className="text-muted-foreground">{t("update.info.version")}</dt>
        <dd className="flex flex-wrap items-baseline gap-2">
          <span className="font-mono font-medium" data-testid="server-version">
            {v.version}
          </span>
          {v.commit && v.commit !== "unknown" && <span className="font-mono text-xs text-muted-foreground">{v.commit}</span>}
          {v.date && (
            <span className="text-xs text-muted-foreground">
              {t("update.info.built")} <DateTimeText value={v.date} />
            </span>
          )}
        </dd>

        <dt className="text-muted-foreground">{t("update.info.check")}</dt>
        <dd>
          <Badge variant={CHECK_VARIANT[v.update_check]}>{t(`update.info.checkStates.${v.update_check}`)}</Badge>
        </dd>

        <dt className="text-muted-foreground">{t("update.info.latest")}</dt>
        <dd data-testid="latest-release">
          {v.latest_available ? (
            <span className="flex flex-wrap items-baseline gap-2">
              <span className="font-mono font-medium">{v.latest_available.version}</span>
              {v.latest_available.notes_url && (
                <a href={v.latest_available.notes_url} target="_blank" rel="noopener noreferrer" className="underline underline-offset-2">
                  {t("update.releaseNotes")}
                </a>
              )}
              <span className="text-xs text-muted-foreground">
                {t("update.info.checked")} <DateTimeText value={v.latest_available.checked_at} relative />
              </span>
            </span>
          ) : (
            <span className="text-muted-foreground">{v.update_check === "enabled" ? t("update.info.upToDate") : "—"}</span>
          )}
        </dd>

        <dt className="text-muted-foreground">{t("update.info.updater")}</dt>
        <dd data-testid="updater-status">
          {u ? (
            <div className="flex flex-col gap-1.5">
              <span className="flex flex-wrap items-center gap-2">
                <Badge variant={updaterVariant(u.state)}>{u.state}</Badge>
                <span className="text-xs text-muted-foreground">
                  {u.engine} · {t("update.info.mode")}: <span className="font-mono">{u.mode}</span>
                </span>
                {u.target_version && (
                  <span className="text-xs">
                    {t("update.info.target")}: <span className="font-mono">{u.target_version}</span>
                  </span>
                )}
              </span>
              {(u.error || u.message) && <span className={u.error ? "text-destructive-text" : "text-muted-foreground"}>{u.error || u.message}</span>}
              {steps.length > 0 && (
                <ol className="flex flex-wrap gap-1" aria-label={t("update.info.steps")}>
                  {steps.map((s, i) => (
                    <li key={`${s.name}-${i}`}>
                      <Badge variant={s.status === "ok" ? "success" : s.status === "failed" ? "destructive" : "warning"} title={s.detail}>
                        {s.name}
                      </Badge>
                    </li>
                  ))}
                </ol>
              )}
              {u.finished_at && (
                <span className="text-xs text-muted-foreground">
                  {t("update.info.finished")} <DateTimeText value={u.finished_at} relative />
                  {u.previous_version && ` · ${t("update.info.from", { version: u.previous_version })}`}
                </span>
              )}
            </div>
          ) : (
            <span className="text-muted-foreground">{t("update.info.noUpdater")}</span>
          )}
        </dd>

        {latest && (
          <>
            <dt className="text-muted-foreground">{t("update.info.request")}</dt>
            <dd data-testid="update-request" className="flex flex-col gap-1">
              <span className="flex flex-wrap items-center gap-2">
                <Badge variant={REQUEST_VARIANT[latest.state]}>{t(`update.info.requestStates.${latest.state}`)}</Badge>
                <span>
                  {latest.action === "apply" ? t("update.info.requestApply", { version: latest.target_version }) : t("update.info.requestCheck")}
                </span>
                <span className="text-xs text-muted-foreground">
                  <DateTimeText value={latest.requested_at} relative />
                  {latest.requested_by_email && ` · ${t("update.info.requestedBy", { email: latest.requested_by_email })}`}
                </span>
              </span>
              {latest.message && (
                <span className={latest.state === "failed" || latest.state === "expired" ? "text-destructive-text" : "text-muted-foreground"}>{latest.message}</span>
              )}
            </dd>
          </>
        )}
      </dl>

      {requests?.can_request && (
        <div className="mt-4 flex flex-col gap-3 border-t pt-4">
          <div className="flex flex-wrap items-center gap-2">
            <Button type="button" variant="outline" size="sm" disabled={check.isPending || reconnecting} onClick={() => check.mutate()}>
              <RefreshCw className={check.isPending ? "size-4 animate-spin" : "size-4"} aria-hidden="true" />
              {check.isPending ? t("update.info.checking") : t("update.info.checkNow")}
            </Button>
            {target && updaterUsable && !confirming && (
              <Button type="button" size="sm" disabled={!canApply || busy || apply.isPending} onClick={() => setConfirming(true)}>
                {t("update.info.updateNow")}
              </Button>
            )}
          </div>
          {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
          {confirming && target && (
            <div role="group" aria-labelledby={`${id}-confirm`} className="flex flex-col gap-3 rounded-lg border p-3">
              <p id={`${id}-confirm`} className="text-sm font-medium">
                {t("update.info.confirmTitle", { version: target })}
              </p>
              <p className="text-sm text-muted-foreground">{t("update.info.confirmBody")}</p>
              <label className="flex items-center gap-2 text-sm">
                <input type="checkbox" className="size-4 accent-primary" checked={ignoreWindow} onChange={(e) => setIgnoreWindow(e.target.checked)} />
                {t("update.info.ignoreWindow")}
              </label>
              <span className="flex flex-wrap gap-2">
                <Button type="button" size="sm" disabled={apply.isPending} autoFocus onClick={() => apply.mutate(target)}>
                  {t("update.info.confirm")}
                </Button>
                <Button type="button" variant="ghost" size="sm" onClick={() => setConfirming(false)}>
                  {t("common.cancel")}
                </Button>
              </span>
            </div>
          )}
          <FormError error={check.error ?? apply.error} />
        </div>
      )}
    </SettingsSection>
  );
}
