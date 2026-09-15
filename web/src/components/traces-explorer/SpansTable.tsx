// Traces Explorer table: span cells (duration, status, trace link, cards) on the shared explorer table
// (components/explorer/DataTable: dynamic columns, reordering, resizing, virtualized rows, per-cell value menu).
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import type { SpanQueryRow } from "@/api/explorer";
import { DataTable, type CellActions } from "@/components/explorer/DataTable";
import { Badge } from "@/components/ui/badge";
import type { TablePrefs } from "@/lib/logs-explorer";
import { formatSpanDuration, SPAN_STORAGE, spanCellValue } from "@/lib/traces-explorer";
import { cn } from "@/lib/utils";

const HEADER_LABEL = {
  timestamp: "tracesExplorer.columns.time",
  "service.name": "tracesExplorer.columns.service",
  name: "tracesExplorer.columns.name",
  duration_ms: "tracesExplorer.columns.duration",
  status_code: "tracesExplorer.columns.status",
  trace_id: "tracesExplorer.columns.trace",
} as const;
const DEFAULT_WIDTH: Record<string, number> = { timestamp: 200, "service.name": 150, name: 320, duration_ms: 110, status_code: 96, trace_id: 150 };
const CARD_FIELDS = new Set(["timestamp", "service.name", "name", "duration_ms", "status_code", "trace_id"]);

export interface SpansTableProps extends TablePrefs {
  rows: SpanQueryRow[];
  columns: string[];
  onColumnsChange: (columns: string[]) => void;
  onOpen: (index: number) => void;
  selectedIndex?: number | null;
  order: "asc" | "desc";
  /** slowest first: one page, no "load more" */
  byDuration: boolean;
  hasMore?: boolean;
  loadingMore?: boolean;
  onLoadMore?: () => void;
  cellActions?: CellActions;
}

export function SpanStatusBadge({ status }: { status: string }) {
  const { t } = useTranslation();
  const variant = status === "error" ? "destructive" : status === "ok" ? "secondary" : "muted";
  const label = status === "error" || status === "ok" || status === "unset" ? t(`tracesExplorer.status.${status}`) : status;
  return <Badge variant={variant}>{label}</Badge>;
}

function TraceLink({ row }: { row: SpanQueryRow }) {
  const { t } = useTranslation();
  return (
    <Link
      to="/traces/$traceId"
      params={{ traceId: row.trace_id }}
      search={{ span: row.span_id }}
      onClick={(e) => e.stopPropagation()}
      className="font-mono text-xs text-primary underline-offset-2 hover:underline pointer-coarse:py-1"
      aria-label={t("tracesExplorer.viewTrace", { id: row.trace_id })}
    >
      {row.trace_id.slice(0, 16)}
    </Link>
  );
}

export function SpansTable({ rows, columns, order, byDuration, wrap, cellActions, onLoadMore, ...props }: SpansTableProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const labelOf = (c: string) => (c in HEADER_LABEL ? t(HEADER_LABEL[c as keyof typeof HEADER_LABEL]) : c);
  return (
    <DataTable
      {...props}
      rows={rows}
      columns={columns}
      order={order}
      wrap={wrap}
      onLoadMore={byDuration ? undefined : onLoadMore}
      ariaLabel={t("tracesExplorer.title")}
      headerLabel={labelOf}
      defaultWidths={DEFAULT_WIDTH}
      flexColumn="name"
      widthsKey={SPAN_STORAGE.widths}
      openLabel={(time) => t("tracesExplorer.openDetails", { time })}
      cellValue={spanCellValue}
      cellActions={cellActions}
      loadMoreLabels={
        order === "asc"
          ? { more: t("tracesExplorer.loadNewer"), loading: t("tracesExplorer.loadingNewer"), none: t("tracesExplorer.noNewer") }
          : { more: t("tracesExplorer.loadMore"), loading: t("tracesExplorer.loadingMore"), none: t("tracesExplorer.noMore") }
      }
      renderCell={(row, c, _i, h) => {
        switch (c) {
          case "timestamp":
            return h.timeButton();
          case "duration_ms":
            return <span className="block text-right font-mono text-xs tabular-nums">{formatSpanDuration(row.duration_ms, locale)}</span>;
          case "status_code":
            return <SpanStatusBadge status={row.status_code} />;
          case "trace_id":
            return <TraceLink row={row} />;
          case "name":
            return (
              <span className={cn("flex min-w-0 gap-2", wrap ? "items-start" : "items-center")}>
                <span className="min-w-0 flex-1">{h.text(row.name, false)}</span>
                <span className="shrink-0 text-[11px] text-muted-foreground">{row.kind}</span>
              </span>
            );
          default: {
            const v = spanCellValue(row, c);
            return v === undefined ? <span className="text-xs text-muted-foreground">–</span> : h.text(v, c !== "service.name");
          }
        }
      }}
      renderCard={(row, _i, h) => {
        const extras = columns.filter((c) => !CARD_FIELDS.has(c));
        return (
          <>
            <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-xs">
              {h.timeButton()}
              <SpanStatusBadge status={row.status_code} />
              {row.service_name && <span className="max-w-full truncate text-muted-foreground">{row.service_name}</span>}
              <span className="font-mono tabular-nums">{formatSpanDuration(row.duration_ms, locale)}</span>
              <TraceLink row={row} />
            </div>
            <span className={cn("text-sm break-all", wrap ? "whitespace-pre-wrap" : "line-clamp-2")}>{row.name}</span>
            {extras.length > 0 && (
              <div className="flex min-w-0 flex-wrap gap-x-3 gap-y-0.5 font-mono text-[11px]">
                {extras.map((c) => (
                  <span key={c} className="max-w-full truncate text-muted-foreground">
                    {c}=<span className="text-foreground">{spanCellValue(row, c) ?? "–"}</span>
                  </span>
                ))}
              </div>
            )}
          </>
        );
      }}
    />
  );
}
