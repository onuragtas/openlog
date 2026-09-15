// Logs Explorer table: log-specific cells (severity badge, body with trace link, cards) on the shared explorer table
// (components/explorer/DataTable: dynamic columns, reordering, resizing, virtualized rows, per-cell value menu).
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import type { LogQueryRow } from "@/api/explorer";
import { DataTable, type CellActions } from "@/components/explorer/DataTable";
import { Badge } from "@/components/ui/badge";
import { cellValue, severityBadgeVariant, type TablePrefs } from "@/lib/logs-explorer";
import { severityLabel } from "@/lib/severity";
import { cn } from "@/lib/utils";

const HEADER_LABEL = { timestamp: "logs.columns.time", severity_text: "logs.columns.severity", "service.name": "logs.columns.service", body: "logs.columns.body", trace_id: "logs.trace" } as const;
const DEFAULT_WIDTH: Record<string, number> = { timestamp: 200, severity_text: 104, "service.name": 160, body: 480, trace_id: 120 };
const CARD_FIELDS = new Set(["timestamp", "severity_text", "service.name", "body", "trace_id"]);

export interface ExplorerTableProps extends TablePrefs {
  rows: LogQueryRow[];
  columns: string[];
  onColumnsChange: (columns: string[]) => void;
  onOpen: (index: number) => void;
  selectedIndex?: number | null;
  /** Row order: "load more" continues with older (desc) or newer (asc) records. */
  order: "asc" | "desc";
  hasMore?: boolean;
  loadingMore?: boolean;
  onLoadMore?: () => void;
  maxHeight?: string;
  /** Per-cell value menu (filter in / out, column, copy). */
  cellActions?: CellActions;
}

function SeverityBadge({ row }: { row: LogQueryRow }) {
  const { t } = useTranslation();
  const label = severityLabel(row.severity_text, row.severity_number);
  return label ? (
    <Badge variant={severityBadgeVariant(row.severity_number)}>{label}</Badge>
  ) : (
    <span className="text-muted-foreground" title={t("logs.severityUnspecified")}>
      —
    </span>
  );
}

function TraceLink({ row }: { row: LogQueryRow }) {
  const { t } = useTranslation();
  return (
    <Link
      to="/traces/$traceId"
      params={{ traceId: row.trace_id }}
      search={row.span_id ? { span: row.span_id } : {}}
      onClick={(e) => e.stopPropagation()}
      className="shrink-0 font-mono text-xs text-primary underline-offset-2 hover:underline pointer-coarse:py-1"
      aria-label={row.span_id ? t("logs.viewSpan", { id: row.span_id }) : t("logs.viewTrace", { id: row.trace_id })}
    >
      {row.trace_id.slice(0, 8)}
    </Link>
  );
}

export function ExplorerTable({ rows, columns, order, wrap, cellActions, ...props }: ExplorerTableProps) {
  const { t } = useTranslation();
  const labelOf = (c: string) => (c in HEADER_LABEL ? t(HEADER_LABEL[c as keyof typeof HEADER_LABEL]) : c);
  return (
    <DataTable
      {...props}
      rows={rows}
      columns={columns}
      order={order}
      wrap={wrap}
      ariaLabel={t("logs.title")}
      headerLabel={labelOf}
      defaultWidths={DEFAULT_WIDTH}
      flexColumn="body"
      openLabel={(time) => t("logsExplorer.openDetails", { time })}
      cellValue={cellValue}
      cellActions={cellActions}
      loadMoreLabels={
        order === "asc"
          ? { more: t("logsExplorer.loadNewer"), loading: t("logsExplorer.loadingNewer"), none: t("logsExplorer.noNewer") }
          : { more: t("logs.loadMore"), loading: t("logs.loadingMore"), none: t("logs.noMore") }
      }
      renderCell={(row, c, _i, h) => {
        switch (c) {
          case "timestamp":
            return h.timeButton();
          case "severity_text":
            return <SeverityBadge row={row} />;
          case "trace_id":
            return row.trace_id ? <TraceLink row={row} /> : null;
          case "body":
            return (
              <span className={cn("flex min-w-0 gap-2", wrap ? "items-start" : "items-center")}>
                {row.trace_id && !columns.includes("trace_id") && <TraceLink row={row} />}
                <span className="min-w-0 flex-1">{h.text(row.body)}</span>
              </span>
            );
          default: {
            const v = cellValue(row, c);
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
              <SeverityBadge row={row} />
              {row.service_name && <span className="max-w-full truncate text-muted-foreground">{row.service_name}</span>}
              {row.trace_id && <TraceLink row={row} />}
            </div>
            <span className={cn("font-mono text-xs break-all", wrap ? "whitespace-pre-wrap" : "line-clamp-3")}>{row.body}</span>
            {extras.length > 0 && (
              <div className="flex min-w-0 flex-wrap gap-x-3 gap-y-0.5 font-mono text-[11px]">
                {extras.map((c) => (
                  <span key={c} className="max-w-full truncate text-muted-foreground">
                    {c}=<span className="text-foreground">{cellValue(row, c) ?? "–"}</span>
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
