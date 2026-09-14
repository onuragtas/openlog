import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Loader2 } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { logsInfiniteQuery } from "@/api/queries";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { formatDateTime } from "@/lib/format";
import { concatLogPages } from "@/lib/logs";
import { parseTimeParam } from "@/lib/time";
import { cn } from "@/lib/utils";

const MARGIN_MS = 60_000;
const PAGE_SIZE = 100;

export interface TraceLogsPanelProps {
  traceId: string;
  startMs: number;
  endMs: number;
  /** span selected on the trace page (enables the "Selected span" filter) */
  selectedSpanId?: string;
  onSelectSpan?: (spanId: string) => void;
}

/** Log records correlated with a trace (GET /logs?trace_id=, optionally span_id=), shown under the waterfall. */
export function TraceLogsPanel({ traceId, startMs, endMs, selectedSpanId, onSelectSpan }: TraceLogsPanelProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const [scope, setScope] = useState<"all" | "span">("all");
  const spanId = scope === "span" && selectedSpanId ? selectedSpanId : undefined;
  const from = String(Math.floor(startMs - MARGIN_MS));
  const to = String(Math.ceil(endMs + MARGIN_MS));
  const q = useInfiniteQuery(logsInfiniteQuery({ range: { from, to }, traceId, spanId, limit: PAGE_SIZE }));
  const logs = useMemo(() => (q.data ? concatLogPages(q.data.pages) : []), [q.data]);

  return (
    <Card data-testid="trace-logs">
      <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2">
        <CardTitle>
          <h2>{t("trace.logs.title")}</h2>
        </CardTitle>
        <div className="flex flex-wrap items-center gap-2">
          <div role="group" aria-label={t("trace.logs.scope")} className="inline-flex rounded-md border p-0.5">
            {(["all", "span"] as const).map((s) => (
              <button
                key={s}
                type="button"
                aria-pressed={scope === s}
                disabled={s === "span" && !selectedSpanId}
                title={s === "span" && !selectedSpanId ? t("trace.logs.selectSpanHint") : undefined}
                onClick={() => setScope(s)}
                className={cn(
                  "rounded px-2 py-1 text-xs font-medium disabled:cursor-not-allowed disabled:opacity-50 pointer-coarse:py-2",
                  scope === s ? "bg-secondary text-secondary-foreground" : "text-muted-foreground hover:text-foreground",
                )}
              >
                {s === "all" ? t("trace.logs.allSpans") : t("trace.logs.selectedSpan")}
              </button>
            ))}
          </div>
          <Link to="/logs" search={{ trace: traceId, span: spanId, from, to }} className="text-xs text-primary hover:underline">
            {t("trace.logs.openAll")}
          </Link>
        </div>
      </CardHeader>
      <CardContent>
        {q.isPending ? (
          <LoadingState />
        ) : q.isError && logs.length === 0 ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : logs.length === 0 ? (
          <p className="text-sm text-muted-foreground">{spanId ? t("trace.logs.emptySpan") : t("trace.logs.empty")}</p>
        ) : (
          <>
            <ol className="flex flex-col divide-y font-mono text-xs" data-testid="trace-log-rows">
              {logs.map((l, i) => {
                const sev = l.severity_number;
                const variant = sev >= 17 ? "destructive" : sev >= 13 ? "warning" : "muted";
                return (
                  <li key={i} className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 py-1.5">
                    <time dateTime={l.timestamp} className="text-muted-foreground">
                      {formatDateTime(parseTimeParam(l.timestamp) ?? 0, locale, true)}
                    </time>
                    <Badge variant={variant}>{l.severity_text || "–"}</Badge>
                    <span className="text-muted-foreground">{l.service_name}</span>
                    {l.span_id && onSelectSpan ? (
                      <button type="button" className="text-primary hover:underline" aria-pressed={l.span_id === selectedSpanId} onClick={() => onSelectSpan(l.span_id)}>
                        {t("trace.logs.span", { id: l.span_id.slice(0, 8) })}
                      </button>
                    ) : null}
                    <span className="basis-full font-sans text-sm break-words whitespace-pre-wrap">{l.body}</span>
                  </li>
                );
              })}
            </ol>
            {q.hasNextPage && (
              <div className="flex justify-center pt-3">
                <Button variant="outline" size="sm" onClick={() => void q.fetchNextPage()} disabled={q.isFetchingNextPage} aria-live="polite">
                  {q.isFetchingNextPage && <Loader2 className="animate-spin" aria-hidden="true" />}
                  {q.isFetchingNextPage ? t("trace.logs.loadingMore") : t("trace.logs.loadMore")}
                </Button>
              </div>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}
