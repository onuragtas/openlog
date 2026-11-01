// Sessions of the application, newest first (rum.md §7). A session is a visit, not a person: the id is
// random, per tab and expires with the tab (§1.1), so nothing here identifies a visitor.
import { useQuery } from "@tanstack/react-query";
import { Users } from "lucide-react";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { rumSessionsQuery, type RumFacets, type RumSession, type RumSessionSort } from "@/api/rum";
import { ListPager } from "@/components/ListPager";
import { RumFacetPicker } from "@/components/rum/RumFacetPicker";
import { Button } from "@/components/ui/button";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { SortableHead, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatDateTime, formatNumber } from "@/lib/format";
import { useListState } from "@/lib/list";
import { apiTimeMs, browserLabel, formatMs } from "@/lib/rum";
import type { RangeSpec } from "@/lib/time";

export function RumSessionsTab({
  range,
  app,
  environment,
  onOpen,
  facets,
  onFacets,
  userId,
  onUser,
}: {
  range: RangeSpec;
  app: string;
  environment?: string;
  onOpen: (session: RumSession) => void;
  facets: RumFacets;
  onFacets: (facets: RumFacets) => void;
  /** One identity's visits, as the application declared it through identify() (rum.md §3.7). */
  userId?: string;
  onUser: (userId?: string) => void;
}) {
  const { t, i18n } = useTranslation();
  const list = useListState<RumSessionSort>("started");
  const sessions = useQuery(rumSessionsQuery(range, app, list.state, environment, facets, userId));
  const { toFirstPage } = list;
  // Another filter or window, or one person's visits instead of everyone's, is another list.
  useEffect(toFirstPage, [app, environment, facets.device_type, facets.browser, userId, range.range, range.from, range.to, toFirstPage]);

  const controls = (
    <div className="flex flex-wrap items-end gap-3">
      <RumFacetPicker id="rum-sessions" facets={facets} onChange={onFacets} />
      {/* Not a search box: an identity is something you arrive at from a session, not something you
          remember how to spell. */}
      {userId && (
        <Button type="button" variant="outline" size="sm" onClick={() => onUser(undefined)}>
          {t("rum.user.back")}
        </Button>
      )}
    </div>
  );

  if (sessions.isPending) return <>{controls}<LoadingState /></>;
  if (sessions.isError) return <>{controls}<ErrorState error={sessions.error} onRetry={() => void sessions.refetch()} /></>;
  if (sessions.data.sessions.length === 0 && list.state.offset === 0) {
    return (
      <>
        {controls}
        <EmptyState icon={<Users className="size-5" aria-hidden="true" />}>{userId ? t("rum.user.none") : t("rum.sessions.empty")}</EmptyState>
      </>
    );
  }

  const started = (s: RumSession) => {
    const ms = apiTimeMs(s.started_at);
    return ms === null ? "–" : formatDateTime(ms, i18n.language);
  };

  return (
    <div className="flex flex-col gap-2">
      {controls}
      {/* Said on the screen, not only in this file's header: the table looks like a list of people and is
          not one -- the same person in two tabs is two rows. */}
      <p className="text-xs text-muted-foreground">{userId ? t("rum.user.filtered", { id: userId }) : t("rum.sessions.note")}</p>
      <Table mobile="stack" data-testid="rum-sessions">
        <TableHeader>
          <TableRow>
            <TableHead>{t("rum.sessions.columns.session")}</TableHead>
            <SortableHead column="started" className="hidden md:table-cell" {...list.head}>{t("rum.sessions.columns.started")}</SortableHead>
            <SortableHead column="duration" className="text-right" {...list.head}>{t("rum.sessions.columns.duration")}</SortableHead>
            <SortableHead column="page_views" className="text-right" {...list.head}>{t("rum.sessions.columns.pageViews")}</SortableHead>
            <SortableHead column="errors" className="text-right" {...list.head}>{t("rum.sessions.columns.errors")}</SortableHead>
            <SortableHead column="entry" defaultDir="asc" className="hidden lg:table-cell" {...list.head}>{t("rum.sessions.columns.entry")}</SortableHead>
            <SortableHead column="browser" defaultDir="asc" className="hidden sm:table-cell" {...list.head}>{t("rum.sessions.columns.browser")}</SortableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {sessions.data.sessions.map((s) => (
            <TableRow key={s.session_id} data-testid="rum-session-row">
              <TableCell>
                <button
                  type="button"
                  className="font-mono text-xs text-primary hover:underline"
                  onClick={() => onOpen(s)}
                  aria-label={t("rum.sessions.open", { id: s.session_id })}
                >
                  {s.session_id.slice(0, 12)}…
                </button>
              </TableCell>
              <TableCell label={t("rum.sessions.columns.started")} className="hidden md:table-cell text-muted-foreground">
                {started(s)}
              </TableCell>
              <TableCell label={t("rum.sessions.columns.duration")} className="text-right tabular-nums">
                {formatMs(s.duration_ms, i18n.language)}
              </TableCell>
              <TableCell label={t("rum.sessions.columns.pageViews")} className="text-right tabular-nums">
                {formatNumber(s.page_views, i18n.language)}
              </TableCell>
              <TableCell label={t("rum.sessions.columns.errors")} className="text-right tabular-nums">
                {formatNumber(s.errors, i18n.language)}
              </TableCell>
              <TableCell label={t("rum.sessions.columns.entry")} className="hidden lg:table-cell font-mono text-xs break-all">
                {s.entry_route || "–"}
              </TableCell>
              <TableCell label={t("rum.sessions.columns.browser")} className="hidden sm:table-cell text-muted-foreground">
                {browserLabel(s.browser_name, s.browser_version)}
                {s.device_type !== "" && <span className="text-xs"> · {s.device_type}</span>}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <ListPager list={list} count={sessions.data.sessions.length} hasMore={sessions.data.has_more ?? false} />
    </div>
  );
}
