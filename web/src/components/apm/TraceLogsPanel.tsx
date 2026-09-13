import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { logsQuery } from "@/api/queries";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { formatDateTime } from "@/lib/format";
import { parseTimeParam } from "@/lib/time";

const MARGIN_MS = 60_000;

/** Log records correlated with a trace (GET /logs?trace_id=), shown under the waterfall. */
export function TraceLogsPanel({ traceId, startMs, endMs, onSelectSpan }: { traceId: string; startMs: number; endMs: number; onSelectSpan?: (spanId: string) => void }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const from = String(Math.floor(startMs - MARGIN_MS));
  const to = String(Math.ceil(endMs + MARGIN_MS));
  const q = useQuery(logsQuery({ range: { from, to }, traceId, limit: 200 }));
  return (
    <Card data-testid="trace-logs">
      <CardHeader className="flex flex-row items-center justify-between gap-2">
        <CardTitle>
          <h2>{t("trace.logs.title")}</h2>
        </CardTitle>
        <Link to="/logs" search={{ trace: traceId, from, to }} className="text-xs text-primary hover:underline">
          {t("trace.logs.openAll")}
        </Link>
      </CardHeader>
      <CardContent>
        {q.isPending ? (
          <LoadingState />
        ) : q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : q.data.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("trace.logs.empty")}</p>
        ) : (
          <ol className="flex flex-col divide-y font-mono text-xs">
            {[...q.data].reverse().map((l, i) => {
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
                    <button type="button" className="text-primary hover:underline" onClick={() => onSelectSpan(l.span_id)}>
                      {t("trace.logs.span", { id: l.span_id.slice(0, 8) })}
                    </button>
                  ) : null}
                  <span className="basis-full font-sans text-sm break-words whitespace-pre-wrap">{l.body}</span>
                </li>
              );
            })}
          </ol>
        )}
      </CardContent>
    </Card>
  );
}
