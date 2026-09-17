// One session with the events it produced (rum.md §7). The timeline comes from the stored spans, so it is
// bounded by the 7-day trace retention: an older session still has its summary and its trace link — the
// newest trace id is kept on the session row for exactly that reason — but no timeline.
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { rumSessionQuery, type RumEvent } from "@/api/rum";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatDateTime, formatNumber } from "@/lib/format";
import { apiTimeMs, browserLabel, formatMs } from "@/lib/rum";
import type { RangeSpec } from "@/lib/time";

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className="mt-0.5 text-sm break-all">{children}</p>
    </div>
  );
}

/** An error is the only event that changes the row's tone; the rest are ordinary steps of the visit. */
const eventVariant = (e: RumEvent["event"]) => (e === "error" ? "destructive" : e === "vital" ? "secondary" : "muted");

export function RumSessionDetail({ range, sessionId }: { range: RangeSpec; sessionId: string }) {
  const { t, i18n } = useTranslation();
  const detail = useQuery(rumSessionQuery(range, sessionId));

  if (detail.isPending) return <LoadingState />;
  if (detail.isError) return <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />;

  const { session, events } = detail.data;
  const at = (value: string) => {
    const ms = apiTimeMs(value);
    return ms === null ? "–" : formatDateTime(ms, i18n.language, true);
  };

  return (
    <div className="flex flex-col gap-4">
      <div className="rounded-xl border bg-card p-4">
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Field label={t("rum.session.app")}>
            {session.app}
            {session.environment !== "" && <span className="text-muted-foreground"> · {session.environment}</span>}
          </Field>
          <Field label={t("rum.session.started")}>{at(session.started_at)}</Field>
          <Field label={t("rum.session.duration")}>{formatMs(session.duration_ms, i18n.language)}</Field>
          <Field label={t("rum.session.device")}>
            {browserLabel(session.browser_name, session.browser_version)}
            {session.os_name !== "" && <span className="text-muted-foreground"> · {session.os_name}</span>}
            {session.device_type !== "" && <span className="text-muted-foreground"> · {session.device_type}</span>}
          </Field>
          <Field label={t("rum.session.pageViews")}>{formatNumber(session.page_views, i18n.language)}</Field>
          <Field label={t("rum.session.errors")}>{formatNumber(session.errors, i18n.language)}</Field>
          <Field label={t("rum.session.entry")}>
            <span className="font-mono text-xs">{session.entry_route || "–"}</span>
          </Field>
          <Field label={t("rum.session.exit")}>
            <span className="font-mono text-xs">{session.exit_route || "–"}</span>
          </Field>
        </div>
        {session.trace_id !== "" && (
          <Link to="/traces/$traceId" params={{ traceId: session.trace_id }} className="mt-3 inline-block text-sm text-primary hover:underline">
            {t("rum.session.openNewestTrace")}
          </Link>
        )}
      </div>

      <section className="flex flex-col gap-2">
        <h2 className="text-sm font-medium">{t("rum.session.timeline")}</h2>
        {events.length === 0 ? (
          <p className="rounded-xl border bg-card p-4 text-sm text-muted-foreground">{t("rum.session.timelineEmpty")}</p>
        ) : (
          <Table mobile="stack" data-testid="rum-session-events">
            <TableHeader>
              <TableRow>
                <TableHead>{t("rum.session.columns.time")}</TableHead>
                <TableHead>{t("rum.session.columns.event")}</TableHead>
                <TableHead>{t("rum.session.columns.name")}</TableHead>
                <TableHead className="hidden lg:table-cell">{t("rum.session.columns.route")}</TableHead>
                <TableHead className="text-right">{t("rum.session.columns.duration")}</TableHead>
                <TableHead>
                  <span className="sr-only">{t("rum.session.columns.trace")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {events.map((e, i) => (
                <TableRow key={`${e.span_id}-${i}`} data-testid="rum-session-event">
                  <TableCell className="text-muted-foreground tabular-nums">{at(e.timestamp)}</TableCell>
                  <TableCell label={t("rum.session.columns.event")} className="max-md:w-auto">
                    <Badge variant={eventVariant(e.event)}>{t(`rum.session.events.${e.event}`)}</Badge>
                  </TableCell>
                  <TableCell label={t("rum.session.columns.name")} className="break-all">
                    {e.name}
                    {e.status_code > 0 && <span className="text-muted-foreground"> · {e.status_code}</span>}
                  </TableCell>
                  <TableCell label={t("rum.session.columns.route")} className="hidden lg:table-cell font-mono text-xs break-all">
                    {e.route || "–"}
                  </TableCell>
                  <TableCell label={t("rum.session.columns.duration")} className="text-right tabular-nums">
                    {e.duration_ms > 0 ? formatMs(e.duration_ms, i18n.language) : "–"}
                  </TableCell>
                  <TableCell className="text-right">
                    {e.trace_id !== "" && (
                      <Link
                        to="/traces/$traceId"
                        params={{ traceId: e.trace_id }}
                        search={{ span: e.span_id }}
                        className="font-mono text-xs text-primary hover:underline"
                        aria-label={t("rum.session.openTrace", { id: e.trace_id })}
                      >
                        {e.trace_id.slice(0, 8)}…
                      </Link>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </section>
    </div>
  );
}
