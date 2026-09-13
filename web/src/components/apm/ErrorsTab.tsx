import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { apmErrorGroupQuery, apmErrorsQuery } from "@/api/apm";
import { Sparkline } from "@/components/apm/Charts";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatMs, stackLines, type ServiceScope } from "@/lib/apm";
import { formatDateTime, formatNumber, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { parseTimeParam, type RangeSpec } from "@/lib/time";
import { cn } from "@/lib/utils";

export function ErrorsTab({ scope, range, selected, onSelect }: { scope: ServiceScope; range: RangeSpec; selected?: string; onSelect: (groupId: string | undefined) => void }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const now = useNow();
  const q = useQuery(apmErrorsQuery(scope, range));
  return (
    <div className="flex flex-col gap-4">
      {selected && <ErrorGroupDetail scope={scope} range={range} groupId={selected} onClose={() => onSelect(undefined)} />}
      <Card>
        <CardHeader>
          <CardTitle>
            <h2>{t("apm.service.tabs.errors")}</h2>
          </CardTitle>
        </CardHeader>
        <CardContent className="px-0">
          {q.isPending ? (
            <LoadingState />
          ) : q.isError ? (
            <ErrorState error={q.error} onRetry={() => void q.refetch()} />
          ) : q.data.groups.length === 0 ? (
            <EmptyState>{t("apm.errors.empty")}</EmptyState>
          ) : (
            <Table data-testid="error-groups">
              <TableHeader>
                <TableRow>
                  <TableHead>{t("apm.errors.error")}</TableHead>
                  <TableHead>{t("apm.errors.trend")}</TableHead>
                  <TableHead className="text-right">{t("apm.errors.count")}</TableHead>
                  <TableHead className="text-right">{t("apm.errors.total")}</TableHead>
                  <TableHead>{t("apm.errors.firstSeen")}</TableHead>
                  <TableHead>{t("apm.errors.lastSeen")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {q.data.groups.map((g) => {
                  const first = parseTimeParam(g.first_seen) ?? 0;
                  const last = parseTimeParam(g.last_seen) ?? 0;
                  return (
                    <TableRow key={g.group_id} data-state={g.group_id === selected ? "selected" : undefined}>
                      <TableCell className="max-w-[32rem]">
                        <button
                          type="button"
                          className="flex max-w-full flex-col items-start text-left"
                          aria-label={t("apm.errors.open", { type: g.error_type })}
                          aria-pressed={g.group_id === selected}
                          onClick={() => onSelect(g.group_id)}
                        >
                          <span className="font-mono text-sm font-semibold text-destructive-text hover:underline">{g.error_type}</span>
                          <span className="max-w-full truncate text-sm" title={g.message}>
                            {g.message || "–"}
                          </span>
                          {g.last_span_name && <span className="text-xs text-muted-foreground">{g.last_span_name}</span>}
                        </button>
                      </TableCell>
                      <TableCell>
                        <Sparkline points={g.sparkline} label={t("apm.errors.trend")} />
                      </TableCell>
                      <TableCell className="text-right font-mono tabular-nums">{formatNumber(g.count, locale)}</TableCell>
                      <TableCell className="text-right font-mono tabular-nums text-muted-foreground">{formatNumber(g.total_count, locale)}</TableCell>
                      <TableCell className="whitespace-nowrap text-xs">
                        <time dateTime={g.first_seen ?? undefined} title={formatDateTime(first, locale)}>
                          {formatRelative(first, now, locale)}
                        </time>
                      </TableCell>
                      <TableCell className="whitespace-nowrap text-xs">
                        <time dateTime={g.last_seen ?? undefined} title={formatDateTime(last, locale)}>
                          {formatRelative(last, now, locale)}
                        </time>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function ErrorGroupDetail({ scope, range, groupId, onClose }: { scope: ServiceScope; range: RangeSpec; groupId: string; onClose: () => void }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const q = useQuery(apmErrorGroupQuery(scope, range, groupId));
  return (
    <Card data-testid="error-group-detail" className="border-destructive/40">
      <CardHeader className="flex flex-row items-start justify-between gap-2">
        <div className="min-w-0">
          <CardTitle>
            <h2 className="font-mono break-all text-destructive-text">{q.data?.error_type ?? groupId}</h2>
          </CardTitle>
          {q.data && <p className="mt-1 text-sm break-words">{q.data.message}</p>}
        </div>
        <Button variant="ghost" size="icon" aria-label={t("apm.errors.close")} onClick={onClose}>
          <X className="size-4" aria-hidden="true" />
        </Button>
      </CardHeader>
      <CardContent>
        {q.isPending ? (
          <LoadingState />
        ) : q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : (
          <div className="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
            <div className="flex min-w-0 flex-col gap-3">
              <div className="flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
                <Badge variant="destructive">
                  {formatNumber(q.data.count, locale)} {t("apm.errors.count").toLowerCase()}
                </Badge>
                <span>
                  {t("apm.errors.total")}: {formatNumber(q.data.total_count, locale)}
                </span>
                <span>
                  {t("apm.errors.firstSeen")}: {formatDateTime(parseTimeParam(q.data.first_seen) ?? 0, locale)}
                </span>
                <Sparkline points={q.data.series} label={t("apm.errors.trend")} width={160} />
              </div>
              {q.data.last_message && (
                <div>
                  <p className="text-xs font-semibold text-muted-foreground">{t("apm.errors.lastMessage")}</p>
                  <p className="font-mono text-sm break-words">{q.data.last_message}</p>
                </div>
              )}
              <div>
                <p className="mb-1 text-xs font-semibold text-muted-foreground">{t("apm.errors.stacktrace")}</p>
                {q.data.stacktrace ? (
                  <pre className="max-h-96 overflow-auto rounded-md border bg-muted/40 p-3 text-xs leading-relaxed" data-testid="stacktrace" tabIndex={0}>
                    {stackLines(q.data.stacktrace).map((l, i) => (
                      <span key={i} className={cn("block whitespace-pre", l.frame && !l.inApp && "text-muted-foreground", l.inApp && "font-semibold text-foreground")}>
                        {l.text || " "}
                      </span>
                    ))}
                  </pre>
                ) : (
                  <p className="text-sm text-muted-foreground">{t("apm.errors.noStack")}</p>
                )}
              </div>
            </div>
            <div className="min-w-0">
              <p className="mb-1 text-xs font-semibold text-muted-foreground">{t("apm.errors.samples")}</p>
              {q.data.samples.length === 0 ? (
                <p className="text-sm text-muted-foreground">{t("apm.errors.noSamples")}</p>
              ) : (
                <ul className="flex flex-col divide-y text-sm" data-testid="error-samples">
                  {q.data.samples.map((s) => (
                    <li key={s.span_id} className="flex flex-col gap-0.5 py-2">
                      <div className="flex items-center justify-between gap-2">
                        <Link to="/traces/$traceId" params={{ traceId: s.trace_id }} search={{ span: s.span_id }} className="font-mono text-xs text-primary hover:underline" aria-label={t("apm.traces.openTrace", { id: s.trace_id })}>
                          {s.trace_id.slice(0, 16)}…
                        </Link>
                        <span className="font-mono text-xs tabular-nums">{formatMs(s.duration_ms, locale)}</span>
                      </div>
                      <span className="text-xs text-muted-foreground">
                        {formatDateTime(parseTimeParam(s.timestamp) ?? 0, locale)} · {s.transaction_name || s.span_name}
                      </span>
                      {s.message && <span className="truncate text-xs" title={s.message}>{s.message}</span>}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
