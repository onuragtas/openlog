import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { versionQuery, type VersionInfo } from "@/api/version";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { DateTimeText, SettingsSection } from "./common";

type Updater = NonNullable<VersionInfo["updater"]>;

const CHECK_VARIANT = { enabled: "success", disabled: "muted", failed: "destructive" } as const;

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

/**
 * The backend version, the release check and openlog-updater (GET /api/v1/version). The banner in
 * AppShell only appears for admins when something newer exists; this section always shows the state.
 */
export function VersionSettings() {
  const { t } = useTranslation();
  const q = useQuery(versionQuery());
  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const v = q.data;
  const u = v.updater;
  const steps = u?.steps ?? [];

  return (
    <SettingsSection title={t("update.info.title")} description={t("update.info.description")}>
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
      </dl>
    </SettingsSection>
  );
}
