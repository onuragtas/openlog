import { AlertTriangle, ExternalLink, ShieldCheck } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { FleetSummary } from "@/api/fleet";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { translateOptional } from "@/i18n/dynamic";
import { formatNumber } from "@/lib/format";

/** Overview cards: agents, version distribution, update status, releases and catalog. */
export function FleetSummaryCards({ summary }: { summary: FleetSummary }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const n = (v: number) => formatNumber(v, locale);
  const maxHosts = Math.max(1, ...summary.versions.map((v) => v.hosts));
  const catalogTone = summary.catalog.status === "ok" ? "success" : summary.catalog.status === "pending" ? "muted" : "destructive";

  return (
    <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
      <Card>
        <CardHeader>
          <CardTitle>
            <h2>{t("fleet.summary.agents")}</h2>
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-2 text-sm">
          <p className="text-3xl font-semibold tabular-nums" data-testid="fleet-active-hosts">
            {n(summary.active_hosts)}
          </p>
          <p className="text-muted-foreground">{t("fleet.summary.active", { active: n(summary.active_hosts), total: n(summary.total_hosts) })}</p>
          <p>{t("fleet.summary.capable", { count: summary.update_capable })}</p>
          <div>
            <h3 className="text-xs font-medium text-muted-foreground">{t("fleet.summary.notCapable")}</h3>
            {summary.not_update_capable.length === 0 ? (
              <p className="text-xs text-muted-foreground">{t("fleet.summary.notCapableNone")}</p>
            ) : (
              <ul className="mt-1 flex flex-wrap gap-1">
                {summary.not_update_capable.map((r) => (
                  <li key={r.reason}>
                    <Badge variant="warning">
                      {translateOptional(`fleet.installMethods.${r.reason}`, r.reason)}: {n(r.hosts)}
                    </Badge>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </CardContent>
      </Card>

      <Card className="xl:col-span-2">
        <CardHeader>
          <CardTitle>
            <h2>{t("fleet.summary.versions")}</h2>
          </CardTitle>
        </CardHeader>
        <CardContent>
          {summary.versions.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("fleet.hosts.empty")}</p>
          ) : (
            <ul className="flex flex-col gap-2" aria-label={t("fleet.summary.versions")}>
              {summary.versions.map((v) => (
                <li key={v.version} className="grid grid-cols-[minmax(6rem,9rem)_1fr_auto] items-center gap-3 text-sm">
                  <span className="flex min-w-0 flex-wrap items-center gap-1">
                    <span className="truncate font-mono text-xs" title={v.version}>
                      {v.version || t("common.unknown")}
                    </span>
                    {v.latest && <Badge variant="success">{t("fleet.summary.latest")}</Badge>}
                    {!v.supported && <Badge variant="destructive">{t("fleet.summary.unsupported")}</Badge>}
                    {v.supported && v.outdated && <Badge variant="outline">{t("fleet.summary.outdated")}</Badge>}
                  </span>
                  <span className="h-2 overflow-hidden rounded-full bg-muted" aria-hidden="true">
                    <span
                      className={v.latest ? "block h-full rounded-full bg-success" : v.supported ? "block h-full rounded-full bg-primary/70" : "block h-full rounded-full bg-destructive"}
                      style={{ width: `${(v.hosts / maxHosts) * 100}%` }}
                    />
                  </span>
                  <span className="text-right text-xs tabular-nums text-muted-foreground">{t("fleet.summary.hostsCount", { count: v.hosts })}</span>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>
            <h2>{t("fleet.summary.releases")}</h2>
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-2 text-sm">
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
            {(["stable", "beta"] as const).map((ch) => {
              const rel = summary.latest[ch];
              return (
                <div key={ch} className="contents">
                  <dt className="text-muted-foreground">{t(`fleet.summary.${ch}`)}</dt>
                  <dd className="flex items-center gap-2">
                    {rel ? (
                      <>
                        <span className="font-mono text-xs">{rel.version}</span>
                        {rel.notes_url && (
                          <a href={rel.notes_url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-xs text-primary hover:underline">
                            {t("fleet.summary.notes")}
                            <ExternalLink className="size-3" aria-hidden="true" />
                          </a>
                        )}
                      </>
                    ) : (
                      <span className="text-muted-foreground">{t("fleet.summary.none")}</span>
                    )}
                  </dd>
                </div>
              );
            })}
            <dt className="text-muted-foreground">{t("fleet.summary.target")}</dt>
            <dd className="font-mono text-xs">{summary.target?.version ?? (summary.policy_mode === "off" ? "–" : t("fleet.summary.targetPatch"))}</dd>
          </dl>
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-xs text-muted-foreground">{t("fleet.summary.catalog")}</span>
            <Badge variant={catalogTone}>
              {summary.catalog.status === "ok" ? <ShieldCheck aria-hidden="true" /> : <AlertTriangle aria-hidden="true" />}
              {translateOptional(`fleet.summary.catalogStatus.${summary.catalog.status}`, summary.catalog.status)}
            </Badge>
          </div>
          {summary.catalog.error && (
            <p className="text-xs break-words text-destructive-text" role="note">
              {summary.catalog.error}
            </p>
          )}
          {summary.oldest_supported_version && <p className="text-xs text-muted-foreground">{t("fleet.summary.oldestSupported", { version: summary.oldest_supported_version })}</p>}
        </CardContent>
      </Card>

      <Card className="md:col-span-2 xl:col-span-4">
        <CardHeader>
          <CardTitle>
            <h2>{t("fleet.summary.status")}</h2>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <dl className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
            {(
              [
                ["outdatedCount", summary.outdated, summary.outdated > 0 ? "text-foreground" : ""],
                ["unsupportedCount", summary.unsupported, summary.unsupported > 0 ? "text-destructive-text" : ""],
                ["inProgress", summary.in_progress, ""],
                ["failed", summary.failed, summary.failed > 0 ? "text-destructive-text" : ""],
                ["held", summary.held, ""],
                ["pinned", summary.pinned, ""],
              ] as const
            ).map(([key, value, cls]) => (
              <div key={key} className="rounded-lg border px-3 py-2">
                <dt className="text-xs text-muted-foreground">{t(`fleet.summary.${key}`)}</dt>
                <dd className={`text-xl font-semibold tabular-nums ${cls}`}>{n(value)}</dd>
              </div>
            ))}
          </dl>
        </CardContent>
      </Card>
    </div>
  );
}
