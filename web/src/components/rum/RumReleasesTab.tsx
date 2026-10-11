// How each version of the application behaved, and when its releases happened (rum.md §2.7).
//
// The crash-free rate leads, because that is the number a release is judged on: "98% of the sessions on
// v1.4.2 saw no error" is the question somebody asks right after a deploy. A session with twelve errors
// counts once, so this is a share of visits, not of errors.
//
// The two halves of the answer see different distances and the screen says so rather than letting the
// reader assume: deployments come from the 30-day version rollup, while sessions are counted from the raw
// spans and stop at the 7-day trace retention. A version older than that is listed with zero sessions —
// which is a gap in what is kept, not a week in which nobody visited.
import { useQuery } from "@tanstack/react-query";
import { Rocket } from "lucide-react";
import { useTranslation } from "react-i18next";
import { rumReleasesQuery, type RumRelease } from "@/api/rum";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { versionLabel } from "@/lib/deployments";
import { formatDateTime, formatNumber } from "@/lib/format";
import { apiTimeMs, formatShare } from "@/lib/rum";
import type { RangeSpec } from "@/lib/time";

/** Below 99% is worth a colour; below 95% is worth the loud one. The bands are this screen's own. */
function CrashFree({ release }: { release: RumRelease }) {
  const { t } = useTranslation();
  if (release.sessions === 0) return <span className="text-muted-foreground">{t("rum.releases.noSessions")}</span>;
  const rate = release.crash_free_rate;
  const variant = rate >= 0.99 ? "success" : rate >= 0.95 ? "warning" : "destructive";
  return <Badge variant={variant}>{formatShare(rate)}</Badge>;
}

export function RumReleasesTab({ range, app, environment }: { range: RangeSpec; app: string; environment?: string }) {
  const { t, i18n } = useTranslation();
  const releases = useQuery(rumReleasesQuery(range, app, environment));

  if (releases.isPending) return <LoadingState />;
  if (releases.isError) return <ErrorState error={releases.error} onRetry={() => void releases.refetch()} />;

  const { releases: versions, deployments } = releases.data;
  if (versions.length === 0 && deployments.length === 0) {
    return <EmptyState icon={<Rocket className="size-5" aria-hidden="true" />}>{t("rum.releases.empty")}</EmptyState>;
  }

  const when = (ts: string) => {
    const ms = apiTimeMs(ts);
    return ms === null ? "–" : formatDateTime(ms, i18n.language);
  };

  return (
    <div className="flex flex-col gap-4">
      <section className="flex flex-col gap-2">
        <h2 className="text-sm font-medium">{t("rum.releases.versions")}</h2>
        <p className="text-xs text-muted-foreground">{t("rum.releases.retentionNote")}</p>
        {versions.length === 0 ? (
          <EmptyState icon={<Rocket className="size-5" aria-hidden="true" />}>{t("rum.releases.noVersions")}</EmptyState>
        ) : (
          <Table mobile="stack" data-testid="rum-releases">
            <TableHeader>
              <TableRow>
                <TableHead>{t("rum.releases.columns.version")}</TableHead>
                <TableHead className="text-right">{t("rum.releases.columns.crashFree")}</TableHead>
                <TableHead className="text-right">{t("rum.releases.columns.sessions")}</TableHead>
                <TableHead className="text-right">{t("rum.releases.columns.errorSessions")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("rum.releases.columns.firstSeen")}</TableHead>
                <TableHead className="hidden lg:table-cell">{t("rum.releases.columns.lastSeen")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {versions.map((r) => (
                <TableRow key={r.version || "(none)"} data-testid="rum-release-row">
                  <TableCell className="font-mono text-xs">{r.version ? versionLabel(r.version) : t("rum.releases.noVersion")}</TableCell>
                  <TableCell label={t("rum.releases.columns.crashFree")} className="text-right">
                    <CrashFree release={r} />
                  </TableCell>
                  <TableCell label={t("rum.releases.columns.sessions")} className="text-right tabular-nums">
                    {formatNumber(r.sessions, i18n.language)}
                  </TableCell>
                  <TableCell label={t("rum.releases.columns.errorSessions")} className="text-right tabular-nums">
                    {formatNumber(r.error_sessions, i18n.language)}
                  </TableCell>
                  <TableCell label={t("rum.releases.columns.firstSeen")} className="hidden md:table-cell text-muted-foreground">
                    {when(r.first_seen)}
                  </TableCell>
                  <TableCell label={t("rum.releases.columns.lastSeen")} className="hidden lg:table-cell text-muted-foreground">
                    {when(r.last_seen)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </section>

      <section className="flex flex-col gap-2">
        <h2 className="text-sm font-medium">{t("rum.releases.deployments")}</h2>
        {deployments.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("rum.releases.noDeployments")}</p>
        ) : (
          <ol className="flex flex-col gap-2" data-testid="rum-deployments">
            {deployments.map((d) => (
              <li key={`${d.t}-${d.version}`} className="flex flex-wrap items-center gap-2 rounded-xl border bg-card p-3 text-sm">
                <span className="font-mono text-xs">{versionLabel(d.version)}</span>
                {d.previous_version !== "" && <span className="text-xs text-muted-foreground">← {versionLabel(d.previous_version)}</span>}
                {d.initial && <Badge variant="muted">{t("rum.releases.initial")}</Badge>}
                {/* A version that came back is a rollback, and that is the first thing to know about it. */}
                {d.rollback && <Badge variant="warning">{t("rum.releases.rollback")}</Badge>}
                <span className="ml-auto text-xs text-muted-foreground">{when(d.timestamp)}</span>
              </li>
            ))}
          </ol>
        )}
      </section>
    </div>
  );
}
