// Sessions of the application, newest first (rum.md §7). A session is a visit, not a person: the id is
// random, per tab and expires with the tab (§1.1), so nothing here identifies a visitor.
import { useQuery } from "@tanstack/react-query";
import { Users } from "lucide-react";
import { useTranslation } from "react-i18next";
import { rumSessionsQuery, type RumSession } from "@/api/rum";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatDateTime, formatNumber } from "@/lib/format";
import { apiTimeMs, browserLabel, formatMs } from "@/lib/rum";
import type { RangeSpec } from "@/lib/time";

export function RumSessionsTab({
  range,
  app,
  environment,
  onOpen,
}: {
  range: RangeSpec;
  app: string;
  environment?: string;
  onOpen: (session: RumSession) => void;
}) {
  const { t, i18n } = useTranslation();
  const sessions = useQuery(rumSessionsQuery(range, app, environment));

  if (sessions.isPending) return <LoadingState />;
  if (sessions.isError) return <ErrorState error={sessions.error} onRetry={() => void sessions.refetch()} />;
  if (sessions.data.length === 0) return <EmptyState icon={<Users className="size-5" aria-hidden="true" />}>{t("rum.sessions.empty")}</EmptyState>;

  const started = (s: RumSession) => {
    const ms = apiTimeMs(s.started_at);
    return ms === null ? "–" : formatDateTime(ms, i18n.language);
  };

  return (
    <Table mobile="stack" data-testid="rum-sessions">
      <TableHeader>
        <TableRow>
          <TableHead>{t("rum.sessions.columns.session")}</TableHead>
          <TableHead className="hidden md:table-cell">{t("rum.sessions.columns.started")}</TableHead>
          <TableHead className="text-right">{t("rum.sessions.columns.duration")}</TableHead>
          <TableHead className="text-right">{t("rum.sessions.columns.pageViews")}</TableHead>
          <TableHead className="text-right">{t("rum.sessions.columns.errors")}</TableHead>
          <TableHead className="hidden lg:table-cell">{t("rum.sessions.columns.entry")}</TableHead>
          <TableHead className="hidden sm:table-cell">{t("rum.sessions.columns.browser")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {sessions.data.map((s) => (
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
  );
}
