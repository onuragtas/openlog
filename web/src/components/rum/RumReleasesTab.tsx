// How each version of the application behaved, and when its releases happened (rum.md §2.7).
//
// The crash-free rate leads, because that is the number a release is judged on. It is a share of visits and not of
// crashes, and a version that cannot report a crash shows a dash: 100% would claim something nobody measured. The
// error-free rate beside it counts errors the application handled too (mobile-crash.md §5).
import { useQuery } from "@tanstack/react-query";
import { Rocket } from "lucide-react";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { rumReleasesQuery, type RumRelease, type RumReleaseSort } from "@/api/rum";
import { ListPager } from "@/components/ListPager";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { SortableHead, Table, TableBody, TableCell, TableHeader, TableRow } from "@/components/ui/table";
import { versionLabel } from "@/lib/deployments";
import { formatDateTime, formatNumber } from "@/lib/format";
import { useListState } from "@/lib/list";
import { apiTimeMs } from "@/lib/rum";
import type { RangeSpec } from "@/lib/time";

/** Below 99% is worth a colour; below 95% is worth the loud one. The bands are this screen's own. */
function Rate({ rate }: { rate: number }) {
  const variant = rate >= 0.99 ? "success" : rate >= 0.95 ? "warning" : "destructive";
  // One decimal, because 99.6% rounded to a whole number would read as a release with no problems at all.
  return <Badge variant={variant}>{(rate * 100).toFixed(1)}%</Badge>;
}

function CrashFree({ release }: { release: RumRelease }) {
  const { t } = useTranslation();
  if (release.sessions === 0) return <span className="text-muted-foreground">{t("rum.releases.noSessions")}</span>;
  const rate = release.session_crash_free_rate;
  if (rate === null || rate === undefined) return <span className="text-muted-foreground" title={t("rum.releases.noCrashCapture")}>—</span>;
  return <Rate rate={rate} />;
}

export function RumReleasesTab({ range, app, environment }: { range: RangeSpec; app: string; environment?: string }) {
  const { t, i18n } = useTranslation();
  const list = useListState<RumReleaseSort>("last_seen");
  const releases = useQuery(rumReleasesQuery(range, app, list.state, environment));
  const { toFirstPage } = list;
  useEffect(toFirstPage, [app, environment, range.range, range.from, range.to, toFirstPage]);

  if (releases.isPending) return <LoadingState />;
  if (releases.isError) return <ErrorState error={releases.error} onRetry={() => void releases.refetch()} />;

  const { releases: versions, deployments } = releases.data;
  if (versions.length === 0 && deployments.length === 0 && list.state.offset === 0) {
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
        {versions.length === 0 && list.state.offset === 0 ? (
          <EmptyState icon={<Rocket className="size-5" aria-hidden="true" />}>{t("rum.releases.noVersions")}</EmptyState>
        ) : (
          <div className="flex flex-col">
          <Table mobile="stack" data-testid="rum-releases">
            <TableHeader>
              <TableRow>
                <SortableHead column="version" {...list.head}>{t("rum.releases.columns.version")}</SortableHead>
                <SortableHead column="crash_free" className="text-right" {...list.head}>{t("rum.releases.columns.crashFree")}</SortableHead>
                <SortableHead column="error_free" className="text-right" {...list.head}>{t("rum.releases.columns.errorFree")}</SortableHead>
                <SortableHead column="sessions" className="text-right" {...list.head}>{t("rum.releases.columns.sessions")}</SortableHead>
                <SortableHead column="error_sessions" className="text-right" {...list.head}>{t("rum.releases.columns.errorSessions")}</SortableHead>
                <SortableHead column="first_seen" className="hidden md:table-cell" {...list.head}>{t("rum.releases.columns.firstSeen")}</SortableHead>
                <SortableHead column="last_seen" className="hidden lg:table-cell" {...list.head}>{t("rum.releases.columns.lastSeen")}</SortableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {versions.map((r) => (
                <TableRow key={r.version || "(none)"} data-testid="rum-release-row">
                  <TableCell className="font-mono text-xs">{r.version ? versionLabel(r.version) : t("rum.releases.noVersion")}</TableCell>
                  <TableCell label={t("rum.releases.columns.crashFree")} className="text-right">
                    <CrashFree release={r} />
                  </TableCell>
                  <TableCell label={t("rum.releases.columns.errorFree")} className="text-right">
                    {r.sessions === 0 ? "" : <Rate rate={r.error_free_rate ?? r.crash_free_rate} />}
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
          <ListPager list={list} count={versions.length} hasMore={releases.data.has_more ?? false} />
          </div>
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
