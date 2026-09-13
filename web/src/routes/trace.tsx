import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { ApiError } from "@/api/client";
import { traceQuery } from "@/api/queries";
import type { Span } from "@/api/types";
import { TraceLogsPanel } from "@/components/apm/TraceLogsPanel";
import { AttributeTable } from "@/components/JsonView";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Waterfall } from "@/components/trace/Waterfall";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { formatDateTime, formatDurationNs } from "@/lib/format";
import { parseTimeParam, parseTimestampNs } from "@/lib/time";
import { layoutWaterfall } from "@/lib/waterfall";

const route = getRouteApi("/app/traces/$traceId");

function SpanDetails({ span, traceStartNs }: { span: Span; traceStartNs: bigint }) {
  const { t, i18n } = useTranslation();
  const startNs = parseTimestampNs(span.start);
  const offset = startNs !== null ? Number(startNs - traceStartNs) : 0;
  const locale = i18n.resolvedLanguage ?? "en";
  return (
    <div className="flex flex-col gap-4 text-xs">
      <div>
        <p className="text-sm font-semibold break-all">{span.name}</p>
        {span.status_code === "error" && (
          <Badge variant="destructive" className="mt-1">
            {span.status_message || "error"}
          </Badge>
        )}
      </div>
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
        <dt className="text-muted-foreground">{t("trace.fields.service")}</dt>
        <dd>{span.service_name}</dd>
        <dt className="text-muted-foreground">{t("trace.fields.kind")}</dt>
        <dd>{span.kind}</dd>
        <dt className="text-muted-foreground">{t("trace.fields.status")}</dt>
        <dd>{span.status_code}</dd>
        <dt className="text-muted-foreground">{t("trace.fields.duration")}</dt>
        <dd className="font-mono">{formatDurationNs(span.duration_ns)}</dd>
        <dt className="text-muted-foreground">{t("trace.fields.start")}</dt>
        <dd className="font-mono">+{formatDurationNs(offset)}</dd>
        <dt className="text-muted-foreground">{t("trace.fields.spanId")}</dt>
        <dd className="font-mono">{span.span_id}</dd>
        <dt className="text-muted-foreground">{t("trace.fields.parent")}</dt>
        <dd className="font-mono">{span.parent_span_id || "–"}</dd>
      </dl>
      <AttributeTable caption={t("trace.attributes")} attributes={span.attributes} />
      <AttributeTable caption={t("trace.resource")} attributes={span.resource_attributes} />
      {span.events.length > 0 && (
        <div>
          <p className="mb-1 font-semibold text-muted-foreground">{t("trace.events")}</p>
          <ul className="flex flex-col gap-2">
            {span.events.map((ev, i) => (
              <li key={i} className="rounded-md border p-2">
                <p className="font-medium">{ev.name}</p>
                {ev.timestamp && <p className="font-mono text-muted-foreground">{formatDateTime(parseTimeParam(ev.timestamp) ?? 0, locale, true)}</p>}
                <AttributeTable caption="" attributes={ev.attributes} />
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

export function TracePage() {
  const { t, i18n } = useTranslation();
  const { traceId } = route.useParams();
  const search = route.useSearch();
  const navigate = useNavigate({ from: "/traces/$traceId" });
  const valid = /^[0-9a-fA-F]{32}$/.test(traceId);
  const query = useQuery({ ...traceQuery(traceId.toLowerCase()), enabled: valid });
  const spans = useMemo(() => query.data?.spans ?? [], [query.data]);
  const layout = useMemo(() => layoutWaterfall(spans), [spans]);
  const selected = useMemo(() => spans.find((s) => s.span_id === search.span), [spans, search.span]);
  const root = layout.rows[0]?.span;
  const locale = i18n.resolvedLanguage ?? "en";

  if (!valid) return <EmptyState>{t("trace.invalid")}</EmptyState>;
  if (query.isPending) return <LoadingState />;
  if (query.isError) {
    if (query.error instanceof ApiError && query.error.status === 404) return <EmptyState>{t("trace.notFound")}</EmptyState>;
    return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  }

  const startMs = Number(layout.traceStartNs / 1_000_000n);

  return (
    <div className="flex flex-col gap-4">
      <div>
        <p className="text-xs text-muted-foreground">
          {t("trace.title")} <span className="font-mono">{query.data.trace_id}</span>
        </p>
        <h1 className="text-xl font-semibold tracking-tight break-all">{root ? `${root.service_name}: ${root.name}` : query.data.trace_id}</h1>
        <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
          <span>
            {t("trace.duration")}: <span className="font-mono text-foreground">{formatDurationNs(layout.totalNs)}</span>
          </span>
          <span>
            {t("trace.started")}: <span className="font-mono text-foreground">{formatDateTime(startMs, locale, true)}</span>
          </span>
          <span>{t("trace.spans", { count: spans.length })}</span>
          <span>{t("trace.services", { count: layout.services.length })}</span>
          <Link
            to="/logs"
            search={{ trace: query.data.trace_id, from: String(startMs - 60_000), to: String(startMs + layout.totalNs / 1e6 + 60_000) }}
            className="text-primary hover:underline"
          >
            {t("trace.relatedLogs")}
          </Link>
        </div>
      </div>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1fr)_22rem]">
        <Card className="min-w-0">
          <CardHeader>
            <CardTitle>
              <h2>{t("trace.waterfall")}</h2>
            </CardTitle>
          </CardHeader>
          <CardContent className="overflow-x-auto">
            <div className="min-w-[36rem]">
              <Waterfall spans={spans} layout={layout} selectedSpanId={search.span} onSelect={(id) => void navigate({ search: (prev) => ({ ...prev, span: id }), replace: true })} />
            </div>
          </CardContent>
        </Card>
        <Card className="min-w-0 lg:sticky lg:top-0 lg:max-h-[calc(100vh-8rem)] lg:overflow-auto">
          <CardHeader>
            <CardTitle>
              <h2>{t("trace.details")}</h2>
            </CardTitle>
          </CardHeader>
          <CardContent aria-live="polite">
            {selected ? <SpanDetails span={selected} traceStartNs={layout.traceStartNs} /> : <p className="text-sm text-muted-foreground">{t("trace.selectSpan")}</p>}
          </CardContent>
        </Card>
      </div>
      <TraceLogsPanel
        traceId={query.data.trace_id}
        startMs={startMs}
        endMs={startMs + layout.totalNs / 1e6}
        onSelectSpan={(id) => void navigate({ search: (prev) => ({ ...prev, span: id }), replace: true })}
      />
    </div>
  );
}
